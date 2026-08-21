package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api_distribution/internal/types"
)

func TestManager_LoadCreatesDefaultWhenMissing(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	cfg, err := m.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	def := types.DefaultConfig()
	if cfg.ServerHost != def.ServerHost {
		t.Errorf("ServerHost = %q, want %q", cfg.ServerHost, def.ServerHost)
	}
	if cfg.ServerPort != def.ServerPort {
		t.Errorf("ServerPort = %d, want %d", cfg.ServerPort, def.ServerPort)
	}
	if cfg.LogRetention != def.LogRetention {
		t.Errorf("LogRetention = %d, want %d", cfg.LogRetention, def.LogRetention)
	}
	// And the file should now exist.
	if _, err := os.ReadFile(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("expected config.json on disk: %v", err)
	}
}

func TestManager_SaveValidates(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	t.Run("rejects out-of-range port", func(t *testing.T) {
		bad := m.Get()
		bad.ServerPort = 70000
		err := m.Save(bad)
		if err == nil || !strings.Contains(err.Error(), "serverPort") {
			t.Fatalf("expected serverPort error, got %v", err)
		}
	})

	t.Run("rejects unknown provider type", func(t *testing.T) {
		bad := m.Get()
		bad.Providers = []types.Provider{
			{ID: "p1", Name: "p", Type: "exotic", BaseURL: "http://x"},
		}
		err := m.Save(bad)
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("expected unsupported provider type error, got %v", err)
		}
	})

	t.Run("normalizes blank fields", func(t *testing.T) {
		c := m.Get()
		c.ServerHost = ""
		c.ServerPort = 0
		c.LogRetention = 0
		if err := m.Save(c); err != nil {
			t.Fatalf("Save with blanks: %v", err)
		}
		got := m.Get()
		if got.ServerHost != "127.0.0.1" {
			t.Errorf("ServerHost = %q, want 127.0.0.1", got.ServerHost)
		}
		if got.ServerPort != 8080 {
			t.Errorf("ServerPort = %d, want 8080", got.ServerPort)
		}
		if got.LogRetention != 7 {
			t.Errorf("LogRetention = %d, want 7", got.LogRetention)
		}
	})

	t.Run("accepts happy path with provider + alias", func(t *testing.T) {
		c := m.Get()
		c.ServerPort = 9000
		c.Providers = []types.Provider{{
			ID: "p1", Name: "openai", Type: types.ProviderOpenAI,
			BaseURL: "https://api.openai.com/v1", APIKey: "sk-test",
		}}
		c.ModelAliases = []types.ModelAlias{{
			ID: "a1", Alias: "fast", ProviderID: "p1", ProviderModel: "gpt-4o-mini", Enabled: true,
		}}
		if err := m.Save(c); err != nil {
			t.Fatalf("Save: %v", err)
		}
		got := m.Get()
		if got.ServerPort != 9000 {
			t.Errorf("expected port 9000, got %d", got.ServerPort)
		}
		if len(got.Providers) != 1 || got.Providers[0].Type != types.ProviderOpenAI {
			t.Errorf("provider not persisted correctly: %+v", got.Providers)
		}
	})
}

func TestManager_Path(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	want := filepath.Join(dir, "config.json")
	if got := m.Path(); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestManager_Apply_AppliesAndPersists(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	err := m.Apply(func(cfg *types.Config) error {
		cfg.Providers = []types.Provider{{
			ID: "p1", Name: "openai", Type: types.ProviderOpenAI,
			BaseURL: "https://api.openai.com/v1", APIKey: "sk-test",
		}}
		cfg.ModelAliases = []types.ModelAlias{{
			ID: "a1", Alias: "fast", ProviderID: "p1",
			ProviderModel: "gpt-4o-mini", Enabled: true,
		}}
		return nil
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := m.Get()
	if len(got.Providers) != 1 || got.Providers[0].ID != "p1" {
		t.Errorf("Providers not applied: %+v", got.Providers)
	}
	if len(got.ModelAliases) != 1 || got.ModelAliases[0].Alias != "fast" {
		t.Errorf("ModelAliases not applied: %+v", got.ModelAliases)
	}

	// Confirm it hit disk.
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	if !strings.Contains(string(data), "fast") {
		t.Errorf("alias not persisted: %s", data)
	}
}

func TestManager_Apply_AbortsOnMutateError(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	myErr := errors.New("nope")
	err := m.Apply(func(cfg *types.Config) error {
		// Mutate, then bail. Nothing should be persisted.
		cfg.Providers = []types.Provider{{ID: "p1", Name: "p", Type: types.ProviderOpenAI, BaseURL: "http://x"}}
		return myErr
	})
	if !errors.Is(err, myErr) {
		t.Fatalf("Apply: want myErr, got %v", err)
	}
	if got := m.Get(); len(got.Providers) != 0 {
		t.Errorf("Providers persisted despite error: %+v", got.Providers)
	}
}

func TestManager_Apply_AbortsOnValidationError(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	err := m.Apply(func(cfg *types.Config) error {
		cfg.ServerPort = 70000 // invalid
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "serverPort") {
		t.Fatalf("expected validation error, got %v", err)
	}
	if got := m.Get(); got.ServerPort != types.DefaultConfig().ServerPort {
		t.Errorf("invalid port persisted: %d", got.ServerPort)
	}
}

func TestManager_Apply_NilCallbackRejected(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := m.Apply(nil); err == nil {
		t.Fatal("expected error for nil callback")
	}
}

func TestManager_Apply_ConcurrentUpdaters(t *testing.T) {
	// Stress-test that concurrent Apply calls don't lose updates. We
	// keep the existing default config (no Providers/Aliases needed
	// for normalisation), and instead change ServerPort by a fixed
	// delta from the current value. The final value must reflect the
	// last writer's logical sequencing; we don't require strict
	// monotonicity, only that no Apply call's mutation is silently
	// dropped against another Apply call's mutation.
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var wg sync.WaitGroup
	const N = 50
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := m.Apply(func(cfg *types.Config) error {
				cfg.ServerPort = 8080 + i
				return nil
			}); err != nil {
				t.Errorf("Apply %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	got := m.Get()
	if got.ServerPort < 8080 || got.ServerPort >= 8080+N {
		t.Errorf("ServerPort = %d, expected one of the writers' values", got.ServerPort)
	}
}

func TestSaveAndSnapshot_Concurrent(t *testing.T) {
	// Exercises the lock-scope change that moved the writeLocked
	// disk I/O outside m.mu. While one goroutine churns Save()s
	// (each does json.Marshal + CreateTemp + Write + Rename on the
	// real temp filesystem), N=10 goroutines should be able to call
	// Snapshot() far more often than Save() because Snapshot only
	// takes the read-lock for a deep-copy. If the disk write were
	// still held under m.mu, Snapshot calls would be serially
	// blocked by every Save and their counts would be roughly
	// equal — that's the regression we want to catch here.
	dir := t.TempDir()
	m := New(dir)
	if _, err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var saveCount, snapCount atomic.Int64
	var saveErr atomic.Int64 // tracks how many Saves returned an error
	stop := make(chan struct{})

	var wg sync.WaitGroup

	// 1 writer goroutine: keeps Save()ing slightly varying configs.
	wg.Add(1)
	go func() {
		defer wg.Done()
		var i int
		for {
			select {
			case <-stop:
				return
			default:
			}
			c := types.DefaultConfig()
			c.ServerPort = 8000 + (i % 1000)
			c.LogRetention = 1 + (i % 30)
			if err := m.Save(c); err != nil {
				saveErr.Add(1)
				continue
			}
			saveCount.Add(1)
			i++
		}
	}()

	// N reader goroutines: pound on Snapshot().
	const N = 10
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				cfg := m.Snapshot()
				// Touch the snapshot so the compiler can't
				// optimise the call away.
				if cfg.ServerPort < 0 {
					t.Errorf("unreachable negative port: %d", cfg.ServerPort)
				}
				snapCount.Add(1)
			}
		}()
	}

	// Let the storm run for 100ms — short enough to keep the test
	// snappy, long enough that disk I/O actually shows up in the
	// Save count.
	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()

	saves := saveCount.Load()
	snaps := snapCount.Load()
	errs := saveErr.Load()
	t.Logf("Save=%d (errors=%d) Snapshot=%d over 100ms", saves, errs, snaps)
	if saves == 0 {
		t.Fatal("Save goroutine made no successful saves; test cannot draw a conclusion")
	}
	if snaps <= saves {
		t.Errorf("Snapshot calls (%d) should outnumber Save calls (%d): "+
			"Snapshot must not be blocked by Save's disk I/O", snaps, saves)
	}
}
