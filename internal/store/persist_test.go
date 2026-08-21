package store

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api_distribution/internal/types"
)

// TestSaveToJSON_DoesNotBlockAppend verifies that a long-running
// SaveToJSON (disk I/O happens outside the store mutex) does not
// starve concurrent Append calls. We run the saver on its own
// goroutine for a fixed window while a second goroutine hammers
// Append; we then assert that Append made meaningful progress —
// it would have made zero progress under the old "lock the whole
// function" design.
func TestSaveToJSON_DoesNotBlockAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.json")

	s := New()

	const window = 200 * time.Millisecond

	// Pre-seed so exportLogs / MarshalIndent have real work to do;
	// without this the JSON payload is empty and the save finishes
	// before the Append goroutine has any chance to race.
	base := time.Now()
	for i := 0; i < 500; i++ {
		s.Append(types.LogEntry{
			ID:        "seed",
			Timestamp: base.UnixNano(),
			Alias:     "seed-alias",
		})
	}

	var (
		appendCount int64
		saveCount   int64
		saveErr     atomic.Value // last error from the saver
		wg          sync.WaitGroup
		stop        = make(chan struct{})
	)

	// Saver goroutine: keep calling SaveToJSON for the whole window.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.SaveToJSON(path); err != nil {
				saveErr.Store(err)
			}
			atomic.AddInt64(&saveCount, 1)
		}
	}()

	// Appender goroutine: hammer it for the same window.
	wg.Add(1)
	go func() {
		defer wg.Done()
		now := time.Now()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.Append(types.LogEntry{
				ID:        "live",
				Timestamp: now.UnixNano(),
				Alias:     "live-alias",
			})
			atomic.AddInt64(&appendCount, 1)
		}
	}()

	time.Sleep(window)
	close(stop)
	wg.Wait()

	if err, ok := saveErr.Load().(error); ok && err != nil {
		t.Fatalf("SaveToJSON returned error: %v", err)
	}

	t.Logf("saveCount=%d appendCount=%d", saveCount, appendCount)

	if saveCount < 1 {
		t.Fatalf("SaveToJSON was never invoked (saveCount=%d)", saveCount)
	}
	// The real assertion: Append must have made *meaningful* progress
	// while the saver was running. A few saves + a tight Append loop
	// easily reach thousands of appends in 200ms; if we see a tiny
	// number (single digits) something is wrong and the saver is
	// probably blocking Append again.
	if appendCount < 100 {
		t.Errorf("Append made only %d calls during %s window of SaveToJSON — "+
			"the save appears to be blocking Append", appendCount, window)
	}

	// Sanity: the ring buffer should be saturated with live entries
	// (we appended far more than MaxLogEntries).  We can't verify the
	// exact count because the buffer caps at MaxLogEntries and evicts
	// older entries — but every slot must now hold a "live" entry.
	recent := s.Recent(MaxLogEntries)
	if len(recent) != MaxLogEntries {
		t.Errorf("Recent = %d, want %d (buffer should be full)", len(recent), MaxLogEntries)
	}
	for _, e := range recent {
		if e.ID != "live" {
			t.Errorf("found stale entry %q in saturated buffer", e.ID)
			break
		}
	}
}
