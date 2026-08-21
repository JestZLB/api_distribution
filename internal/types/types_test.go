package types

import (
	"strings"
	"testing"
)

func TestValidatePort(t *testing.T) {
	cases := []struct {
		port int
		ok   bool
	}{
		{0, false},
		{1, true},
		{80, true},
		{1024, true},
		{8080, true},
		{65535, true},
		{65536, false},
		{-1, false},
	}
	for _, c := range cases {
		err := ValidatePort(c.port)
		if (err == nil) != c.ok {
			t.Errorf("ValidatePort(%d): expected ok=%v, err=%v", c.port, c.ok, err)
		}
	}
}

func TestKnownProviderTypes(t *testing.T) {
	got := KnownProviderTypes()
	want := []string{ProviderOpenAI, ProviderAnthropic, ProviderAzure, ProviderGemini, ProviderOllama, ProviderCustom}
	if len(got) != len(want) {
		t.Fatalf("KnownProviderTypes() returned %d entries, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("KnownProviderTypes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsKnownProviderType(t *testing.T) {
	if !IsKnownProviderType("openai") {
		t.Error("expected openai to be known")
	}
	if !IsKnownProviderType("OpenAI") {
		t.Error("expected case-insensitive match")
	}
	if IsKnownProviderType("foo") {
		t.Error("expected foo to be unknown")
	}
}

func TestDefaultBaseURL(t *testing.T) {
	cases := map[string]string{
		ProviderOpenAI:    "https://api.openai.com/v1",
		ProviderAnthropic: "https://api.anthropic.com/v1",
		ProviderGemini:    "https://generativelanguage.googleapis.com/v1beta/openai",
		ProviderOllama:    "http://127.0.0.1:11434/v1",
		ProviderAzure:     "",
		ProviderCustom:    "",
	}
	for kind, want := range cases {
		if got := DefaultBaseURL(kind); got != want {
			t.Errorf("DefaultBaseURL(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestProviderValidate(t *testing.T) {
	cases := []struct {
		name     string
		provider Provider
		wantErr  string
	}{
		{
			name:     "valid openai",
			provider: Provider{ID: "p1", Name: "p", Type: ProviderOpenAI, BaseURL: "https://api.openai.com/v1"},
		},
		{
			name:     "valid anthropic",
			provider: Provider{ID: "p2", Name: "p", Type: ProviderAnthropic, BaseURL: "https://api.anthropic.com/v1"},
		},
		{
			name:     "valid azure",
			provider: Provider{ID: "p3", Name: "p", Type: ProviderAzure, BaseURL: "https://x.openai.azure.com/openai/deployments/d"},
		},
		{
			name:     "valid gemini (OpenAI-compatible)",
			provider: Provider{ID: "p4", Name: "p", Type: ProviderGemini, BaseURL: DefaultBaseURL(ProviderGemini)},
		},
		{
			name:     "valid ollama",
			provider: Provider{ID: "p5", Name: "p", Type: ProviderOllama, BaseURL: "http://localhost:11434/v1"},
		},
		{
			name:     "valid custom",
			provider: Provider{ID: "p6", Name: "p", Type: ProviderCustom, BaseURL: "http://localhost:1234/v1"},
		},
		{
			name:     "unknown type",
			provider: Provider{ID: "p7", Name: "p", Type: "fancy", BaseURL: "http://x"},
			wantErr:  "not supported",
		},
		{
			name:     "missing base url",
			provider: Provider{ID: "p8", Name: "p", Type: ProviderOpenAI},
			wantErr:  "baseUrl is required",
		},
		{
			name:     "invalid base url",
			provider: Provider{ID: "p8b", Name: "p", Type: ProviderOpenAI, BaseURL: "ftp://example.com"},
			wantErr:  "http(s) URL",
		},
		{
			name:     "negative weight",
			provider: Provider{ID: "p9", Name: "p", Type: ProviderOpenAI, BaseURL: "http://x", Weight: -5},
			wantErr:  "weight",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.provider.Validate()
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	t.Run("defaults are valid", func(t *testing.T) {
		c := DefaultConfig()
		if err := c.Validate(); err != nil {
			t.Fatalf("default config should validate: %v", err)
		}
	})

	t.Run("invalid port rejected", func(t *testing.T) {
		c := DefaultConfig()
		c.ServerPort = 70000
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "serverPort") {
			t.Fatalf("expected serverPort error, got %v", err)
		}
	})

	t.Run("alias with missing provider rejected", func(t *testing.T) {
		c := DefaultConfig()
		c.ModelAliases = []ModelAlias{{ID: "a1", Alias: "x", ProviderID: "nope"}}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "providerId") {
			t.Fatalf("expected dangling alias error, got %v", err)
		}
	})

	t.Run("duplicate alias rejected", func(t *testing.T) {
		c := DefaultConfig()
		c.Providers = []Provider{{ID: "p1", Name: "p", Type: ProviderOpenAI, BaseURL: "http://x"}}
		c.ModelAliases = []ModelAlias{
			{ID: "a1", Alias: "GPT", ProviderID: "p1"},
			{ID: "a2", Alias: "gpt", ProviderID: "p1"},
		}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("expected duplicate alias error, got %v", err)
		}
	})

	t.Run("happy path with provider and alias", func(t *testing.T) {
		c := DefaultConfig()
		c.Providers = []Provider{{ID: "p1", Name: "p", Type: ProviderOpenAI, BaseURL: "http://x"}}
		c.ModelAliases = []ModelAlias{{ID: "a1", Alias: "my-gpt", ProviderID: "p1", ProviderModel: "gpt-4o"}}
		if err := c.Validate(); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestConfigNormalize(t *testing.T) {
	var c Config
	c.Normalize()
	if c.ServerHost != "127.0.0.1" {
		t.Errorf("expected default host, got %q", c.ServerHost)
	}
	if c.ServerPort != 8080 {
		t.Errorf("expected default port, got %d", c.ServerPort)
	}
	if c.LogRetention != 7 {
		t.Errorf("expected default retention, got %d", c.LogRetention)
	}
	if c.Providers == nil || c.ModelAliases == nil {
		t.Errorf("expected non-nil slices after Normalize")
	}
	// Idempotency: calling again doesn't change anything.
	c.Normalize()
	if c.ServerPort != 8080 {
		t.Errorf("Normalize not idempotent: port became %d", c.ServerPort)
	}
}
