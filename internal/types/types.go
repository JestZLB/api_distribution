// Package types contains the shared data models used between the
// Go backend and the Wails frontend bindings.
package types

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Provider kind identifiers. The frontend currently exposes the first
// four ("openai", "anthropic", "azure", "custom") in its type picker,
// but the backend understands a few additional well-known kinds that
// can be entered via JSON or via the type catalog API exposed by App.
// All kinds speak either OpenAI-compatible JSON or Anthropic-style
// messages and are supported without any third-party SDK dependencies.
const (
	// ProviderOpenAI covers OpenAI's public API and any vendor that
	// speaks the OpenAI /v1/chat/completions wire format (Bearer auth).
	ProviderOpenAI = "openai"
	// ProviderAnthropic covers Anthropic Claude (x-api-key auth).
	ProviderAnthropic = "anthropic"
	// ProviderAzure covers Azure OpenAI Service. Auth uses the api-key
	// header; the BaseURL should point at the deployment host.
	ProviderAzure = "azure"
	// ProviderGemini covers Google Gemini's OpenAI-compatible endpoint
	// at generativelanguage.googleapis.com.
	ProviderGemini = "gemini"
	// ProviderOllama covers a local Ollama server's OpenAI-compatible
	// endpoint at /v1.
	ProviderOllama = "ollama"
	// ProviderCustom covers any other OpenAI-compatible gateway (vLLM,
	// LM Studio, llama.cpp's OpenAI shim, etc.).
	ProviderCustom = "custom"
)

// KnownProviderTypes returns the catalog of provider kinds the
// backend understands, in a stable order suitable for UI rendering.
func KnownProviderTypes() []string {
	return []string{
		ProviderOpenAI,
		ProviderAnthropic,
		ProviderAzure,
		ProviderGemini,
		ProviderOllama,
		ProviderCustom,
	}
}

// IsKnownProviderType reports whether t is one of the recognised
// provider kinds.
func IsKnownProviderType(t string) bool {
	for _, k := range KnownProviderTypes() {
		if strings.EqualFold(t, k) {
			return true
		}
	}
	return false
}

// DefaultBaseURL returns a sensible default base URL for the given
// provider kind. It returns an empty string for kinds that require a
// user-supplied endpoint (azure, custom).
func DefaultBaseURL(t string) string {
	switch strings.ToLower(t) {
	case ProviderOpenAI:
		return "https://api.openai.com/v1"
	case ProviderAnthropic:
		return "https://api.anthropic.com/v1"
	case ProviderGemini:
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case ProviderOllama:
		return "http://127.0.0.1:11434/v1"
	default:
		// azure and custom require user-supplied BaseURLs.
		return ""
	}
}

// Provider represents an upstream LLM provider (e.g. OpenAI, Anthropic).
type Provider struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	BaseURL  string   `json:"baseUrl"`
	APIKey   string   `json:"apiKey"`
	Type     string   `json:"type"` // one of KnownProviderTypes()
	Enabled  bool     `json:"enabled"`
	Priority int      `json:"priority"`
	Weight   int      `json:"weight"`
	Models   []string `json:"models"`
	Notes    string   `json:"notes"`
}

// Validate reports whether the provider carries the minimum fields
// required to be useful. ID and Name are recommended but not required
// for routing so we only surface them as warnings (returned in errs).
func (p Provider) Validate() error {
	if !IsKnownProviderType(p.Type) {
		return fmt.Errorf("provider type %q is not supported (allowed: %s)",
			p.Type, strings.Join(KnownProviderTypes(), ", "))
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("provider %q: baseUrl is required", p.Name)
	}
	parsed, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("provider %q: baseUrl must be an absolute http(s) URL", p.Name)
	}
	if p.Weight < 0 {
		return fmt.Errorf("provider %q: weight must be >= 0", p.Name)
	}
	if p.Priority < 0 {
		return fmt.Errorf("provider %q: priority must be >= 0", p.Name)
	}
	return nil
}

// ModelAlias maps a public alias (used by API consumers) to a
// concrete provider + provider-side model ID. This is the core of
// the distribution system: it lets you unify models from different
// vendors under a single name and route the traffic to the right
// upstream.
type ModelAlias struct {
	ID            string   `json:"id"`
	Alias         string   `json:"alias"`
	ProviderID    string   `json:"providerId"`
	ProviderModel string   `json:"providerModel"`
	Tags          []string `json:"tags"`
	Enabled       bool     `json:"enabled"`
	Description   string   `json:"description"`
}

// LogEntry records a single proxied request.
type LogEntry struct {
	ID             string `json:"id"`
	Timestamp      int64  `json:"timestamp"` // unix nano
	Method         string `json:"method"`
	Path           string `json:"path"`
	StatusCode     int    `json:"statusCode"`
	LatencyMs      int64  `json:"latencyMs"`
	Alias          string `json:"alias"`
	ProviderID     string `json:"providerId"`
	ProviderName   string `json:"providerName"`
	ProviderModel  string `json:"providerModel"`
	InputTokens    int    `json:"inputTokens"`
	OutputTokens   int    `json:"outputTokens"`
	ClientIP       string `json:"clientIp"`
	APIKeySuffix   string `json:"apiKeySuffix"`   // last 4 chars
	ClientKeyLabel string `json:"clientKeyLabel"` // matched client key label
	Error          string `json:"error"`
	Streaming      bool   `json:"streaming"`
}

// ServerStatus describes the runtime state of the proxy server.
type ServerStatus struct {
	Running      bool   `json:"running"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	StartedAt    int64  `json:"startedAt"`
	RequestCount int64  `json:"requestCount"`
	ErrorCount   int64  `json:"errorCount"`
	LastError    string `json:"lastError"`
	BaseURL      string `json:"baseUrl"`
}

// Stats provides aggregated usage statistics.
type Stats struct {
	TotalRequests             int64                   `json:"totalRequests"`
	TotalInputTokens          int64                   `json:"totalInputTokens"`
	TotalOutputTokens         int64                   `json:"totalOutputTokens"`
	AvgLatencyMs              int64                   `json:"avgLatencyMs"`
	RequestsByModel           map[string]int64        `json:"requestsByModel"`
	RequestsByProvider        map[string]int64        `json:"requestsByProvider"`
	RequestsByClientKey       map[string]int64        `json:"requestsByClientKey"`       // total requests, by key label
	RequestsByClientKeyRecent map[string]int64        `json:"requestsByClientKeyRecent"` // last 24h requests, by key label
	RequestsByHour            []HourBucket            `json:"requestsByHour"`
	RequestsByHourByModel     map[string][]HourBucket `json:"requestsByHourByModel"`
}

// PeriodStats aggregates usage over a rolling window of calendar days.
// AvgLatency is the mean latency over the window's requests.
type PeriodStats struct {
	Requests     int64 `json:"requests"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	Errors       int64 `json:"errors"`
	AvgLatency   int64 `json:"avgLatency"`
}

// StatsWithComparison extends Stats with day-over-day comparison data
// for delta display. Today* are the current calendar day's aggregates,
// Yesterday* the previous full calendar day's, so the dashboard can
// show "today vs yesterday" with an up/down percentage.
type StatsWithComparison struct {
	Stats
	// Prev* are kept for backward compatibility with the frontend's
	// `isComparison` type probe and now mirror Yesterday* (previously
	// they summed every full day before today).
	PrevRequests     int64 `json:"prevRequests"`
	PrevInputTokens  int64 `json:"prevInputTokens"`
	PrevOutputTokens int64 `json:"prevOutputTokens"`
	PrevAvgLatency   int64 `json:"prevAvgLatency"`

	// Today's aggregates (current calendar day so far).
	TodayRequests     int64 `json:"todayRequests"`
	TodayInputTokens  int64 `json:"todayInputTokens"`
	TodayOutputTokens int64 `json:"todayOutputTokens"`
	TodayErrors       int64 `json:"todayErrors"`
	TodayAvgLatency   int64 `json:"todayAvgLatency"`

	// Yesterday's aggregates (previous full calendar day).
	YesterdayRequests     int64 `json:"yesterdayRequests"`
	YesterdayInputTokens  int64 `json:"yesterdayInputTokens"`
	YesterdayOutputTokens int64 `json:"yesterdayOutputTokens"`
	YesterdayErrors       int64 `json:"yesterdayErrors"`
	YesterdayAvgLatency   int64 `json:"yesterdayAvgLatency"`

	// Rolling-window consumption for the dashboard's top cards:
	// all-time, last 7 days, last 30 days, plus the previous equivalent
	// windows so the UI can show period-over-period deltas.
	AllTime   PeriodStats `json:"allTime"`
	Week      PeriodStats `json:"week"`
	PrevWeek  PeriodStats `json:"prevWeek"`
	Month     PeriodStats `json:"month"`
	PrevMonth PeriodStats `json:"prevMonth"`
}

// HourBucket is one hour of request counts and token usage. Token
// fields are partitioned by model alias so the dashboard can plot a
// per-alias hourly curve in either request-count or token-volume.
type HourBucket struct {
	Hour    int64            `json:"hour"` // unix seconds, aligned to hour
	Count   int64            `json:"count"`
	Errors  int64            `json:"errors"`
	ByModel map[string]int64 `json:"byModel"`
	// InputTokens / OutputTokens are the hour's totals across every
	// alias. They mirror Stats.TotalInputTokens / TotalOutputTokens but
	// scoped to a single hour and are summed across days in sumDays.
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	// ByModelTokens is the per-alias *total* token count
	// (InputTokens+OutputTokens) for this hour. ByModelInputTokens /
	// ByModelOutputTokens split that total back into the input and
	// output sides per alias so the frontend can render both a combined
	// token trend and the input/output breakdown for any alias without
	// re-aggregating. All three are nil-safe: a legacy daily JSON that
	// predates these fields unmarshals to a zero / empty map.
	ByModelTokens       map[string]int64 `json:"byModelTokens"`
	ByModelInputTokens  map[string]int64 `json:"byModelInputTokens"`
	ByModelOutputTokens map[string]int64 `json:"byModelOutputTokens"`
}

// Config is the persisted application configuration.
type Config struct {
	ServerHost   string       `json:"serverHost"`
	ServerPort   int          `json:"serverPort"`
	ClientKeys   []ClientKey  `json:"clientKeys"`
	LogRetention int          `json:"logRetention"` // days
	Providers    []Provider   `json:"providers"`
	ModelAliases []ModelAlias `json:"modelAliases"`

	// System preferences persisted with the rest of the config so
	// they survive across launches. Defaults (false) match the
	// pre-feature behaviour.
	CloseToTray bool `json:"closeToTray"` // hide window on close instead of quitting
	AutoStart   bool `json:"autoStart"`   // launch app at login
	TrayEnabled bool `json:"trayEnabled"` // keep the tray icon visible at all times
	// Locale is the user's selected UI language tag (e.g. en-US,
	// zh-CN, ja-JP, ko-KR). It is mirrored to the OS tray menu so the
	// tray items are localized as well.
	Locale string `json:"locale"`

	// ShutdownTimeoutSec controls how long server.Stop waits for an
	// in-flight handler (long SSE streams in particular) to drain
	// before forcibly terminating connections. 0 falls back to the
	// built-in default (5s); values >60 are rejected by Validate.
	ShutdownTimeoutSec int `json:"shutdownTimeoutSec"`

	// MaxRequestBodyMB caps the request body (and the buffered
	// non-streaming response body) accepted by the forwarding proxy,
	// in MiB. The gateway intentionally does NOT enforce IDE-level
	// context limits — that is the client's job — so the default is a
	// generous memory-safety net (256 MiB) rather than a context
	// cutoff. 0 falls back to the default; range 1-1024 enforced by
	// Validate.
	MaxRequestBodyMB int `json:"maxRequestBodyMB"`
}

// Normalize fills in zero-value fields with defaults so the on-disk
// representation is always self-consistent. It is idempotent and never
// returns an error. Callers that want strict checks should call
// Validate afterwards.
func (c *Config) Normalize() {
	def := DefaultConfig()
	if c.ServerHost == "" {
		c.ServerHost = def.ServerHost
	}
	if c.ServerPort == 0 {
		c.ServerPort = def.ServerPort
	}
	if c.LogRetention == 0 {
		c.LogRetention = def.LogRetention
	}
	if c.Providers == nil {
		c.Providers = []Provider{}
	}
	if c.ModelAliases == nil {
		c.ModelAliases = []ModelAlias{}
	}
	if c.ClientKeys == nil {
		c.ClientKeys = []ClientKey{}
	}
	if c.ShutdownTimeoutSec == 0 {
		c.ShutdownTimeoutSec = def.ShutdownTimeoutSec
	}
	if c.MaxRequestBodyMB == 0 {
		c.MaxRequestBodyMB = def.MaxRequestBodyMB
	}
	if c.Locale == "" {
		c.Locale = def.Locale
	}
}

// Validate checks the high-level configuration for safe values and
// validates each provider entry. Normalize should be called first so
// callers see normalised host/port in any error message.
func (c Config) Validate() error {
	if c.ServerHost == "" {
		return fmt.Errorf("serverHost is required")
	}
	if err := ValidatePort(c.ServerPort); err != nil {
		return fmt.Errorf("serverPort: %w", err)
	}
	if c.LogRetention < 0 || c.LogRetention > 365 {
		return fmt.Errorf("logRetention must be between 0 and 365 days, got %d", c.LogRetention)
	}
	if c.ShutdownTimeoutSec < 0 || c.ShutdownTimeoutSec > 60 {
		return fmt.Errorf("shutdownTimeoutSec must be between 0 and 60 seconds, got %d", c.ShutdownTimeoutSec)
	}
	if c.MaxRequestBodyMB < 1 || c.MaxRequestBodyMB > 1024 {
		return fmt.Errorf("maxRequestBodyMB must be between 1 and 1024, got %d", c.MaxRequestBodyMB)
	}
	// Aliases must reference a known provider id.
	providerIDs := make(map[string]struct{}, len(c.Providers))
	for i := range c.Providers {
		if err := c.Providers[i].Validate(); err != nil {
			return fmt.Errorf("providers[%d]: %w", i, err)
		}
		providerIDs[c.Providers[i].ID] = struct{}{}
	}
	seenAliases := make(map[string]struct{}, len(c.ModelAliases))
	for i := range c.ModelAliases {
		a := c.ModelAliases[i]
		if strings.TrimSpace(a.Alias) == "" {
			return fmt.Errorf("modelAliases[%d]: alias is required", i)
		}
		if _, ok := providerIDs[a.ProviderID]; !ok {
			return fmt.Errorf("modelAliases[%d]: providerId %q not found in providers",
				i, a.ProviderID)
		}
		key := strings.ToLower(strings.TrimSpace(a.Alias))
		if _, dup := seenAliases[key]; dup {
			return fmt.Errorf("modelAliases[%d]: duplicate alias %q", i, a.Alias)
		}
		seenAliases[key] = struct{}{}
	}
	// ClientKeys: any non-empty key must be at least 8 chars, and
	// labels must be unique when trimmed and case-folded. An empty
	// list means "open mode" (no auth required), matching the
	// pre-feature behaviour where GatewayKey=="" disabled auth.
	seenLabels := make(map[string]struct{}, len(c.ClientKeys))
	for i := range c.ClientKeys {
		k := c.ClientKeys[i]
		trimmed := strings.TrimSpace(k.Key)
		if trimmed != "" && len(trimmed) < 8 {
			return fmt.Errorf("clientKeys[%d]: key must be at least 8 characters", i)
		}
		label := strings.ToLower(strings.TrimSpace(k.Label))
		if label == "" {
			continue
		}
		if _, dup := seenLabels[label]; dup {
			return fmt.Errorf("clientKeys[%d]: duplicate label %q", i, k.Label)
		}
		seenLabels[label] = struct{}{}
	}
	return nil
}

// ValidatePort enforces a TCP-legal port range. 0 is rejected here
// so callers that want the default must resolve it explicitly via
// Normalize / DefaultConfig — this keeps Save paths strict.
func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d is out of range (1-65535)", port)
	}
	return nil
}

// AppInfo is returned by GetAppInfo and used by the frontend on boot.
type AppInfo struct {
	Version   string `json:"version"`
	BuildTime string `json:"buildTime"`
	ConfigDir string `json:"configDir"`
}

// DefaultConfig returns the default configuration used on first launch.
func DefaultConfig() Config {
	return Config{
		ServerHost:         "127.0.0.1",
		ServerPort:         8080,
		ClientKeys:         []ClientKey{},
		LogRetention:       30,
		Providers:          []Provider{},
		ModelAliases:       []ModelAlias{},
		ShutdownTimeoutSec: 5,
		Locale:             "en-US",
		MaxRequestBodyMB:   256,
	}
}

// ClientKey represents a single client API key. Users can create /
// edit / delete many of these; any enabled entry is accepted by the
// gateway's auth middleware. The CreatedAt timestamp uses unix nano
// for stable ordering across timezones.
type ClientKey struct {
	ID        string `json:"id"`
	Label     string `json:"label"` // user-friendly name
	Key       string `json:"key"`   // actual bearer token
	Enabled   bool   `json:"enabled"`
	CreatedAt int64  `json:"createdAt"` // unix nano
}

// MigrateFromLegacy promotes the single, pre-multi-key `gatewayKey`
// field into a single ClientKey so existing on-disk configs survive
// the upgrade. It is invoked by config.Manager.Load after a fresh
// JSON decode; the raw map is the second-pass decode that captures
// fields the typed Config no longer knows about. If ClientKeys is
// already populated, the migration is a no-op — callers editing the
// list manually are not surprised by a "Imported" entry reappearing.
func MigrateFromLegacy(cfg *Config, raw map[string]json.RawMessage) {
	if cfg == nil {
		return
	}
	if len(cfg.ClientKeys) > 0 {
		return
	}
	if raw == nil {
		return
	}
	gwRaw, ok := raw["gatewayKey"]
	if !ok {
		return
	}
	var legacy string
	if err := json.Unmarshal(gwRaw, &legacy); err != nil || strings.TrimSpace(legacy) == "" {
		return
	}
	now := time.Now().UnixNano()
	var idb [8]byte
	_, _ = rand.Read(idb[:])
	cfg.ClientKeys = []ClientKey{{
		ID:        hex.EncodeToString(idb[:]),
		Label:     "Imported",
		Key:       legacy,
		Enabled:   true,
		CreatedAt: now,
	}}
}
