package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api_distribution/internal/types"
)

func TestExtractModel(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4o","messages":[]}`)
		alias, field, ok := extractModel(body)
		if !ok || alias != "gpt-4o" || field != "model" {
			t.Errorf("got alias=%q field=%q ok=%v", alias, field, ok)
		}
	})
	t.Run("missing model field", func(t *testing.T) {
		_, _, ok := extractModel([]byte(`{"messages":[]}`))
		if ok {
			t.Errorf("expected ok=false")
		}
	})
	t.Run("invalid json", func(t *testing.T) {
		_, _, ok := extractModel([]byte(`not json`))
		if ok {
			t.Errorf("expected ok=false")
		}
	})
}

func TestRewriteModel(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		newModel string
		want     string
	}{
		{
			name:     "replaces simple value",
			in:       `{"model":"gpt-4o","messages":[]}`,
			newModel: "gpt-4o-mini",
			want:     `{"model":"gpt-4o-mini","messages":[]}`,
		},
		{
			name:     "handles escaped quote inside value",
			in:       `{"model":"weird\"name","messages":[]}`,
			newModel: "replaced",
			want:     `{"model":"replaced","messages":[]}`,
		},
		{
			name:     "returns body unchanged when key missing",
			in:       `{"messages":[]}`,
			newModel: "x",
			want:     `{"messages":[]}`,
		},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			got := rewriteModel([]byte(c.in), "model", c.newModel)
			if string(got) != c.want {
				t.Errorf("\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}

func TestIsStreaming(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"stream":true}`, true},
		{`{"stream":false}`, false},
		{`{}`, false},
		{`not json`, false},
	}
	for _, c := range cases {
		if got := isStreaming([]byte(c.body)); got != c.want {
			t.Errorf("isStreaming(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

func TestParseUsage(t *testing.T) {
	cases := []struct {
		body    string
		wantIn  int
		wantOut int
	}{
		{`{"usage":{"prompt_tokens":11,"completion_tokens":22}}`, 11, 22},
		{`{}`, 0, 0},
		{`not json`, 0, 0},
	}
	for _, c := range cases {
		in, out := parseUsage([]byte(c.body))
		if in != c.wantIn || out != c.wantOut {
			t.Errorf("parseUsage(%q) = (%d,%d), want (%d,%d)", c.body, in, out, c.wantIn, c.wantOut)
		}
	}
}

func TestResolveAlias(t *testing.T) {
	cfg := types.Config{
		ModelAliases: []types.ModelAlias{
			{ID: "a1", Alias: "fast", ProviderID: "p1", ProviderModel: "gpt-4o-mini", Enabled: true},
			{ID: "a2", Alias: "off", ProviderID: "p2", ProviderModel: "x", Enabled: false},
		},
		Providers: []types.Provider{
			{ID: "p1", Name: "openai", Type: types.ProviderOpenAI, BaseURL: "https://x", Enabled: true},
			{ID: "p2", Name: "anthropic", Type: types.ProviderAnthropic, BaseURL: "https://y", Enabled: true},
			{ID: "p3", Name: "disabled", Type: types.ProviderOpenAI, BaseURL: "https://z", Enabled: false},
		},
	}

	t.Run("resolved happy path", func(t *testing.T) {
		r, err := resolveAlias(cfg, "fast")
		if err != nil {
			t.Fatalf("resolveAlias: %v", err)
		}
		if r.ProviderModel != "gpt-4o-mini" {
			t.Errorf("ProviderModel = %q", r.ProviderModel)
		}
		if r.Provider.ID != "p1" {
			t.Errorf("Provider.ID = %q", r.Provider.ID)
		}
	})

	t.Run("disabled alias", func(t *testing.T) {
		_, err := resolveAlias(cfg, "off")
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("expected unknown alias error, got %v", err)
		}
	})

	t.Run("disabled provider", func(t *testing.T) {
		cfg := cfg
		cfg.ModelAliases = []types.ModelAlias{{ID: "a1", Alias: "off-by-prov", ProviderID: "p3", ProviderModel: "x", Enabled: true}}
		_, err := resolveAlias(cfg, "off-by-prov")
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("expected disabled provider error, got %v", err)
		}
	})

	t.Run("unknown alias", func(t *testing.T) {
		_, err := resolveAlias(cfg, "nope")
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("expected unknown alias error, got %v", err)
		}
	})
}

func TestClientIP(t *testing.T) {
	t.Run("uses X-Forwarded-For", func(t *testing.T) {
		req := mustReq("192.0.2.1:1234", "10.0.0.1, 192.0.2.1")
		if got := clientIP(req); got != "10.0.0.1" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("uses RemoteAddr fallback", func(t *testing.T) {
		req := mustReq("192.0.2.1:1234", "")
		if got := clientIP(req); got != "192.0.2.1" {
			t.Errorf("got %q", got)
		}
	})
}

func TestKeySuffix(t *testing.T) {
	if got := keySuffix("abcd"); got != "abcd" {
		t.Errorf("short key: got %q", got)
	}
	if got := keySuffix("gw-abcdef1234"); got != "1234" {
		t.Errorf("long key: got %q", got)
	}
}

// mustReq builds an *http.Request with a synthetic remote address and
// an optional X-Forwarded-For header. It's a shortcut for exercising
// clientIP without spinning up a real TCP listener.
func mustReq(remote, xff string) *http.Request {
	r := httptest.NewRequest("GET", "/x", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}
