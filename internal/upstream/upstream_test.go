package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api_distribution/internal/types"
)

// TestApplyAuth verifies that the right authentication header is
// attached for each provider kind.
func TestApplyAuth(t *testing.T) {
	cases := []struct {
		name        string
		kind        string
		apiKey      string
		wantHeaders map[string]string
	}{
		{
			name:   "openai uses bearer",
			kind:   types.ProviderOpenAI,
			apiKey: "sk-test",
			wantHeaders: map[string]string{
				"Authorization": "Bearer sk-test",
			},
		},
		{
			name:   "anthropic uses x-api-key",
			kind:   types.ProviderAnthropic,
			apiKey: "sk-ant-test",
			wantHeaders: map[string]string{
				"x-api-key":         "sk-ant-test",
				"anthropic-version": "2023-06-01",
			},
		},
		{
			name:   "azure uses api-key header",
			kind:   types.ProviderAzure,
			apiKey: "azurekey-123",
			wantHeaders: map[string]string{
				"api-key": "azurekey-123",
			},
		},
		{
			name:   "gemini uses bearer",
			kind:   types.ProviderGemini,
			apiKey: "goog-test",
			wantHeaders: map[string]string{
				"Authorization": "Bearer goog-test",
			},
		},
		{
			name:        "ollama with empty api key sends no auth",
			kind:        types.ProviderOllama,
			apiKey:      "",
			wantHeaders: map[string]string{},
		},
		{
			name:   "ollama with api key uses bearer",
			kind:   types.ProviderOllama,
			apiKey: "ollama-key",
			wantHeaders: map[string]string{
				"Authorization": "Bearer ollama-key",
			},
		},
		{
			name:   "custom uses bearer",
			kind:   types.ProviderCustom,
			apiKey: "ckey",
			wantHeaders: map[string]string{
				"Authorization": "Bearer ckey",
			},
		},
		{
			name:   "type is case-insensitive",
			kind:   "OpenAI",
			apiKey: "sk-test",
			wantHeaders: map[string]string{
				"Authorization": "Bearer sk-test",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cli := newClient(types.Provider{Type: c.kind, APIKey: c.apiKey, Name: "p"})
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.com/x", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			cli.applyAuth(req)

			for k, want := range c.wantHeaders {
				if got := req.Header.Get(k); got != want {
					t.Errorf("header %q = %q, want %q", k, got, want)
				}
			}
			// And no stray Authorization header for non-bearer cases.
			if c.kind == types.ProviderAnthropic || c.kind == types.ProviderAzure {
				if h := req.Header.Get("Authorization"); h != "" {
					t.Errorf("unexpected Authorization header for %s: %q", c.kind, h)
				}
			}
		})
	}
}

// TestDo_DispatchesRequest spins up an httptest server, points the
// client at it, and verifies that the request reaches the right
// endpoint with the expected headers and body. This guards the URL
// join path too.
func TestDo_DispatchesRequest(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotCT string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cli := newClient(types.Provider{
		Type:    types.ProviderOpenAI,
		Name:    "openai",
		BaseURL: srv.URL + "/v1",
		APIKey:  "sk-test",
	})

	resp, err := cli.Do(context.Background(), http.MethodPost, "/chat/completions",
		[]byte(`{"model":"x"}`), false)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer sk-test")
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("Path = %q, want %q", gotPath, "/v1/chat/completions")
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", gotCT)
	}
}

func TestDo_MissingBaseURL(t *testing.T) {
	cli := newClient(types.Provider{Name: "x", Type: types.ProviderOpenAI})
	_, err := cli.Do(context.Background(), http.MethodPost, "/chat/completions",
		[]byte(`{}`), false)
	if err == nil || !strings.Contains(err.Error(), "no base URL") {
		t.Fatalf("expected base URL error, got %v", err)
	}
}

// TestGetOrCreate_CachesInstance verifies that repeated calls with the
// same provider configuration return the same *Client instance, while
// changes to identity or secrets create new clients.
func TestGetOrCreate_CachesInstance(t *testing.T) {
	provider := types.Provider{
		ID:      "p1",
		Name:    "openai",
		Type:    types.ProviderOpenAI,
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-abc",
	}

	first := GetOrCreate(provider)
	second := GetOrCreate(provider)
	if first != second {
		t.Fatalf("expected cached client instance, got different pointers")
	}

	changed := provider
	changed.ID = "p2"
	third := GetOrCreate(changed)
	if third == first {
		t.Fatalf("expected new client when provider ID changes")
	}
}

// resetClients clears the global client cache so tests don't leak
// entries into one another.
func resetClients(t *testing.T) {
	t.Helper()
	clientsMu.Lock()
	clients = map[cacheKey]*Client{}
	clientsMu.Unlock()
}

// TestReapIdle_SkipsActiveClient verifies that a client with an
// in-flight request is never evicted, even when its lastUsed timestamp
// is far past the idle threshold (e.g. a long-running stream).
func TestReapIdle_SkipsActiveClient(t *testing.T) {
	resetClients(t)
	defer resetClients(t)

	p := types.Provider{ID: "p-active", Name: "openai", Type: types.ProviderOpenAI, BaseURL: "https://example.com/v1", APIKey: "sk"}
	c := GetOrCreate(p)
	c.acquire() // active request in progress
	// Simulate a stale lastUsed (1 hour ago) while still in-flight.
	c.lastUsed.Store(time.Now().Add(-time.Hour).UnixNano())

	if removed := ReapIdle(time.Minute); removed != 0 {
		t.Fatalf("expected 0 removed, got %d", removed)
	}
	if _, ok := clients[buildCacheKey(p)]; !ok {
		t.Fatalf("active client was evicted despite in-flight request")
	}

	c.release()
}

// TestReapIdle_EvictsIdleClient verifies that a client with no
// in-flight requests and a stale lastUsed is evicted.
func TestReapIdle_EvictsIdleClient(t *testing.T) {
	resetClients(t)
	defer resetClients(t)

	p := types.Provider{ID: "p-idle", Name: "openai", Type: types.ProviderOpenAI, BaseURL: "https://example.com/v1", APIKey: "sk"}
	c := GetOrCreate(p)
	c.lastUsed.Store(time.Now().Add(-time.Hour).UnixNano())

	if removed := ReapIdle(time.Minute); removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}
	if _, ok := clients[buildCacheKey(p)]; ok {
		t.Fatalf("idle client was not evicted")
	}
}
