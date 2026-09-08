// Package main wires together the config manager, log store,
// proxy server and Wails bindings.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"api_distribution/internal/config"
	"api_distribution/internal/server"
	"api_distribution/internal/store"
	"api_distribution/internal/system"
	"api_distribution/internal/types"
	"api_distribution/internal/upstream"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is bound to the Wails frontend. Every exported method here is
// available on the JS side via wailsjs/go/main/App.
type App struct {
	ctx     context.Context
	cfgDir  string
	cfgMgr  *config.Manager
	store   *store.Store
	proxy   *server.Server
	version string

	// statsCache is a 100ms process-internal TTL cache shared by
	// GetStats and GetStatsWithComparison. The store's onChange hook
	// invalidates it on every Append / Clear / PurgeOlderThan so the
	// next heartbeat sees fresh data.
	statsCache statsCache

	// flushArmed is set while one 3-second auto-flush goroutine is
	// in flight. It coalesces the many Appends that arrive in a burst
	// into a single disk write a few seconds after the first one, so
	// an abnormally-terminated process (task manager kill, wails dev
	// Ctrl+C, crash) loses at most the trailing ~3s instead of the
	// entire run (the periodic 5s tick is the lower-bound safety net).
	flushArmed atomic.Bool

	// flushMu serializes flushPersisted so the 5s ticker, the 3s
	// auto-flush goroutine and shutdown() never interleave two
	// SaveLogs on the same dirty-range markers (concurrent
	// incremental flushes could otherwise double-advance dirtyStart).
	flushMu sync.Mutex

	mu sync.Mutex
	// forceQuit is set by the explicit Quit paths (tray menu, Settings
	// "Quit app"). When enabled, OnBeforeClose in main.go lets the
	// window close instead of hiding to tray, so a deliberate quit
	// always exits even when close-to-tray is on.
	forceQuit bool

	// System feature state — driven by Settings UI toggles.
	closeToTray bool
	trayStarted bool
	autoStart   system.AutoStart
	tray        system.Tray

	// doneCh is closed by App.shutdown to signal every background
	// goroutine that the process is exiting. Each loop selects on
	// both <-a.ctx.Done() and <-a.doneCh so shutdown can drive a
	// deterministic exit without racing the Wails-provided ctx.
	// Initialized in startup() (we need a.ctx first so the loops can
	// safely read both signals).
	doneCh chan struct{}

	// loopWG tracks the three long-lived loops (emitLogsLoop,
	// persistLogsLoop, reapClientsLoop). shutdown closes doneCh, then
	// waits up to 200ms for them all to return so flushPersisted() and
	// store.Close() can run without a concurrent Append touching the
	// dirty-range markers.
	loopWG sync.WaitGroup
}

const (
	// upstreamReapInterval controls how often the background goroutine
	// scans the upstream client cache for idle entries.
	upstreamReapInterval = 1 * time.Minute
	// upstreamReapIdle is how long an upstream client may sit unused
	// before it is evicted and its idle connections closed.
	upstreamReapIdle = 5 * time.Minute
)

// NewApp creates a new App with persistent config in cfgDir and the
// SQLite database in dataDir. An empty dataDir falls back to the
// executable-adjacent "./data" directory (store.DefaultDataDir).
func NewApp(cfgDir, dataDir string) *App {
	if cfgDir == "" {
		cfgDir = "."
	}
	if dataDir == "" {
		var err error
		dataDir, err = store.DefaultDataDir()
		if err != nil {
			fmt.Println("warning: locate data dir:", err)
		}
	}
	cfgMgr := config.New(cfgDir)
	if _, err := cfgMgr.Load(); err != nil {
		fmt.Println("warning: load config:", err)
	}
	var st *store.Store
	if dataDir == "" {
		// DefaultDataDir failed (e.g. the exe lives somewhere read-only);
		// degrade to an in-memory store rather than writing a stray DB
		// file next to the working directory.
		fmt.Println("warning: sqlite data dir unavailable, using in-memory store")
		st = store.New()
	} else {
		var err error
		st, err = store.Open(store.DBPath(dataDir))
		if err != nil {
			fmt.Println("warning: open sqlite, falling back to in-memory:", err)
			st = store.New()
		}
	}
	srv := server.New(cfgMgr, st)

	return &App{
		cfgDir:    cfgDir,
		cfgMgr:    cfgMgr,
		store:     st,
		proxy:     srv,
		version:   "0.1.0",
		autoStart: system.NewAutoStart(),
		tray:      system.NewTray(appIconPng),
	}
}

// startup is called when the Wails app is ready.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.doneCh = make(chan struct{})

	// One-time migration from the legacy JSON persistence layout
	// (logs.json + stats/*.json) into the SQLite database. It is
	// idempotent and a no-op after the first run.
	if err := a.store.MigrateLegacyJSON(a.cfgDir); err != nil {
		fmt.Println("warning: migrate legacy json:", err)
	}

	// Wire the store's mutation hook so every Append / Clear /
	// PurgeOlderThan / LoadLogs invalidates the 100ms stats cache.
	// Without this hook a brand-new request would be hidden behind a
	// stale value for up to 100ms, which is fine for the dashboard
	// but surprising on the Logs page where the user just submitted
	// a request and expects to see it.
	a.store.SetOnChange(a.statsCache.invalidate)

	// Load persisted logs and per-date stats from SQLite.
	if err := a.store.LoadLogs(); err != nil {
		fmt.Println("warning: load logs:", err)
	}
	if err := a.store.LoadAllDaily(); err != nil {
		fmt.Println("warning: load stats:", err)
	}

	// Try to start the proxy server automatically if there's a
	// saved configuration.
	cfg := a.cfgMgr.Get()
	if cfg.ServerPort != 0 {
		if err := a.proxy.Start(); err != nil {
			fmt.Println("warning: auto-start proxy:", err)
		}
	}

	// Hydrate system preferences from the persisted config so the
	// Settings UI shows the live state on first paint.
	a.mu.Lock()
	a.closeToTray = cfg.CloseToTray
	a.mu.Unlock()

	// Register the tray icon on launch so it's present from the very
	// first frame. We start it both when the user explicitly kept the
	// tray on (TrayEnabled) AND when close-to-tray is on (otherwise
	// clicking the X would hide the window with no way to restore it).
	// Start is synchronous up to ~200ms, so it's safe to call inline;
	// the Win32 message pump itself runs on a background goroutine.
	if a.tray != nil {
		// Seed the localized menu labels before Start so the very
		// first right-click uses the persisted language.
		a.tray.SetLocale(cfg.Locale)
		if cfg.TrayEnabled || cfg.CloseToTray {
			if err := a.tray.Start("API Distribution", &trayForwarder{a: a}); err != nil {
				fmt.Println("warning: auto-start tray:", err)
			} else {
				a.mu.Lock()
				a.trayStarted = true
				a.mu.Unlock()
			}
		}
	}

	// Enable OS auto-start if the user asked for it.
	if cfg.AutoStart && a.autoStart != nil {
		if err := a.autoStart.Enable("APIDistribution"); err != nil {
			fmt.Println("warning: enable auto-start:", err)
		}
	}

	// Emit log updates on a timer for the UI dashboard.
	go a.emitLogsLoop()

	// Persist logs to disk periodically (every 30 seconds).
	go a.persistLogsLoop()

	// Periodically evict idle upstream clients from the global cache.
	go a.reapClientsLoop()
}

// shutdown is called when Wails is closing. The sequence is
// deterministic and bounded so a Wails-side Quit never blocks the
// event loop for more than ~1.5s:
//
//  1. proxy.Stop() — refuses any new inbound request and waits up to
//     its 5s ShutdownTimeoutSec for in-flight streams to drain;
//  2. close(doneCh) — signals the three background loops to exit;
//  3. loopWG.Wait with a 200ms ceiling — give the loops a chance to
//     finish their final tick (emit, persist, reap). The Wait is
//     non-blocking past 200ms via time.After so shutdown never
//     stalls behind a slow ticker;
//  4. flushPersisted() — single, flushMu-serialized write of the
//     dirty log range and per-day aggregates. We do NOT call
//     store.SaveLogs/SaveDaily directly because those race with
//     persistLogsLoop's own flush on the dirty-range markers; the
//     helper owns the serialization;
//  5. go a.tray.Stop() — fire-and-forget the tray teardown so a
//     slow PostQuitMessage (Windows can take ~500ms) does not
//     inflate the shutdown wall-clock;
//  6. store.Close() — release the SQLite handle last so the flush
//     above has a live connection;
//  7. upstream.ReapIdle(0) — force-close cached upstream idle conns.
func (a *App) shutdown(_ context.Context) {
	// (1) Stop accepting new proxy traffic.
	_ = a.proxy.Stop()

	// (2) Signal all background loops to exit. Safe to call multiple
	// times — close of a nil channel panics, so we only do it once
	// and only after startup has populated doneCh.
	if a.doneCh != nil {
		select {
		case <-a.doneCh:
			// already closed (e.g. unit test calling shutdown twice)
		default:
			close(a.doneCh)
		}
	}

	// (3) Wait up to 200ms for the loops to finish. We do NOT block on
	// loopWG.Wait alone because a stuck loop (e.g. disk full on the
	// flush) would wedge the Wails shutdown indefinitely. A goroutine
	// + time.After gives us a non-blocking ceiling.
	doneWaiting := make(chan struct{})
	go func() {
		a.loopWG.Wait()
		close(doneWaiting)
	}()
	select {
	case <-doneWaiting:
	case <-time.After(200 * time.Millisecond):
		// proceed anyway; flushPersisted + store.Close are still
		// safe to call concurrently with the lingering loops.
	}

	// (4) Serialized final flush. flushMu ensures this and the
	// persistLogsLoop's exit-flush do not interleave two SaveLogs.
	a.flushPersisted()

	// (5) Tray teardown off the critical path. tray.Stop internally
	// waits up to 500ms for the Win32 pump to drain; we do not want
	// that on the shutdown wall-clock. The goroutine outlives this
	// function but the process exits right after, so the leak window
	// is bounded.
	a.mu.Lock()
	ts := a.trayStarted
	a.mu.Unlock()
	if ts && a.tray != nil {
		go func() {
			a.tray.Stop()
		}()
	}

	// (6) Release the SQLite handle AFTER the final flush. Leaving it
	// open holds a file lock on Windows, so a second app instance
	// started while the first is still exiting can hit "database is
	// locked" and silently fail to write — which manifests as "data
	// wiped on restart".
	a.store.Close()

	// (7) Force-evict every cached upstream client so their idle
	// TCP connections close before the process exits. Without this
	// step, idle conns linger until the next reap tick (up to 1
	// minute) and prevent the runtime from releasing socket fds
	// promptly — visible as goroutine / fd growth in a long-running
	// dev session.
	upstream.ReapIdle(0)
}

// ---------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------

// GetAppInfo returns metadata about the running app.
func (a *App) GetAppInfo() types.AppInfo {
	return types.AppInfo{
		Version:   a.version,
		BuildTime: "dev",
		ConfigDir: a.cfgDir,
	}
}

// ProviderTypeInfo describes a single provider kind for UI rendering.
type ProviderTypeInfo struct {
	Type        string `json:"type"`
	Label       string `json:"label"`
	DefaultURL  string `json:"defaultUrl"`
	Description string `json:"description"`
}

// ListProviderTypes returns the catalog of provider kinds the
// backend understands. Frontends can call this to keep their type
// picker in sync with what the proxy can route to.
func (a *App) ListProviderTypes() []ProviderTypeInfo {
	return []ProviderTypeInfo{
		{Type: types.ProviderOpenAI, Label: "OpenAI", DefaultURL: types.DefaultBaseURL(types.ProviderOpenAI), Description: "OpenAI public API and compatible vendors."},
		{Type: types.ProviderAnthropic, Label: "Anthropic", DefaultURL: types.DefaultBaseURL(types.ProviderAnthropic), Description: "Anthropic Claude messages API."},
		{Type: types.ProviderAzure, Label: "Azure OpenAI", DefaultURL: "", Description: "Azure OpenAI Service deployment."},
		{Type: types.ProviderGemini, Label: "Google Gemini", DefaultURL: types.DefaultBaseURL(types.ProviderGemini), Description: "Gemini OpenAI-compatible endpoint."},
		{Type: types.ProviderOllama, Label: "Ollama (local)", DefaultURL: types.DefaultBaseURL(types.ProviderOllama), Description: "Local Ollama server, OpenAI-compatible."},
		{Type: types.ProviderCustom, Label: "Custom OpenAI-compatible", DefaultURL: "", Description: "Any other OpenAI-compatible gateway."},
	}
}

// SuggestProviderDefaults fills in sensible defaults (BaseURL etc.)
// for a partially-filled provider based on its Type. It mutates the
// passed provider and returns it so the UI can persist immediately.
func (a *App) SuggestProviderDefaults(p types.Provider) types.Provider {
	if def := types.DefaultBaseURL(p.Type); def != "" && p.BaseURL == "" {
		p.BaseURL = def
	}
	if p.Weight == 0 {
		p.Weight = 1
	}
	return p
}

// GetConfig returns the current configuration.
func (a *App) GetConfig() types.Config {
	return a.cfgMgr.Snapshot()
}

// SaveConfig replaces the in-memory config and persists it. The
// manager normalises and validates the config first, so invalid
// ports, unknown provider types, or dangling aliases never reach
// disk. The proxy is restarted only when the bind address actually
// changed — providers, aliases, and client keys are hot-loaded
// (the request handler reads the latest snapshot on every request),
// so restarting on those edits would unnecessarily interrupt
// in-flight streams.
func (a *App) SaveConfig(cfg types.Config) error {
	current := a.cfgMgr.Snapshot()
	if err := a.cfgMgr.Save(cfg); err != nil {
		return err
	}
	// Restart the proxy only when the bind address actually changed.
	// Providers/aliases/keys edits are hot-loaded (the handler reads
	// the latest snapshot on every request) and must not interrupt
	// in-flight streams.
	if current.ServerHost != cfg.ServerHost || current.ServerPort != cfg.ServerPort {
		status := a.proxy.Status()
		if status.Running {
			if err := a.proxy.Stop(); err != nil {
				return fmt.Errorf("stop server: %w", err)
			}
			if err := a.proxy.Start(); err != nil {
				return fmt.Errorf("start server: %w", err)
			}
		}
	}
	a.emitConfigChanged()
	return nil
}

// UpdateServerSettings updates only the server bind address,
// without touching providers, aliases, or client keys. It is a thin,
// validation-friendly path for the Settings page; the same
// guarantees apply as SaveConfig.
func (a *App) UpdateServerSettings(host string, port int) error {
	cfg := a.cfgMgr.Get()
	cfg.ServerHost = host
	cfg.ServerPort = port
	return a.SaveConfig(cfg)
}

// SetLogRetention updates only the log retention window (in days),
// without touching providers, aliases, client keys, or the proxy
// bind address. The change is persisted via cfgMgr.Save — which
// triggers the config:changed event so the Settings UI reloads — but
// the proxy is intentionally NOT restarted (log retention only
// influences the persistLogsLoop's purge tick, never an in-flight
// request handler). The bound Settings UI calls this when the
// logRetention input changes so providers / aliases / clientKeys
// are not redundantly re-written to disk.
func (a *App) SetLogRetention(n int) error {
	cfg := a.cfgMgr.Get()
	cfg.LogRetention = n
	if err := a.cfgMgr.Save(cfg); err != nil {
		return err
	}
	a.emitConfigChanged()
	return nil
}

// GenerateClientKey returns a fresh, high-entropy API key the user can
// drop into a new ClientKey entry. Format matches the historical
// "gw-<48 hex chars>" shape (24 random bytes hex-encoded) so existing
// operator workflows keep working.
func (a *App) GenerateClientKey() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return "gw-" + hex.EncodeToString(b[:])
}

// AddClientKey creates and persists a new client API key. If value is
// empty a fresh random key is generated automatically; the returned
// ClientKey carries the assigned ID and timestamp. The mutation goes
// through cfgMgr.Apply so a concurrent Wails caller cannot race on
// Get + Save.
func (a *App) AddClientKey(label string, value string) (types.ClientKey, error) {
	if strings.TrimSpace(value) == "" {
		value = a.GenerateClientKey()
	}
	entry := types.ClientKey{
		ID:        newID(),
		Label:     strings.TrimSpace(label),
		Key:       value,
		Enabled:   true,
		CreatedAt: time.Now().UnixNano(),
	}
	err := a.cfgMgr.Apply(func(cfg *types.Config) error {
		cfg.ClientKeys = append(cfg.ClientKeys, entry)
		return nil
	})
	if err != nil {
		return types.ClientKey{}, err
	}
	a.emitConfigChanged()
	return entry, nil
}

// UpdateClientKey mutates an existing entry matched by ID. A blank
// value preserves the current key (callers sometimes want to toggle
// the enabled flag or rename the label without rotating the secret).
// Returns an error if the ID does not exist.
func (a *App) UpdateClientKey(id string, label string, value string, enabled bool) (types.ClientKey, error) {
	if strings.TrimSpace(id) == "" {
		return types.ClientKey{}, fmt.Errorf("id is required")
	}
	var updated types.ClientKey
	err := a.cfgMgr.Apply(func(cfg *types.Config) error {
		for i := range cfg.ClientKeys {
			if cfg.ClientKeys[i].ID == id {
				cfg.ClientKeys[i].Label = strings.TrimSpace(label)
				if strings.TrimSpace(value) != "" {
					cfg.ClientKeys[i].Key = value
				}
				cfg.ClientKeys[i].Enabled = enabled
				updated = cfg.ClientKeys[i]
				return nil
			}
		}
		return fmt.Errorf("client key %q not found", id)
	})
	if err != nil {
		return types.ClientKey{}, err
	}
	a.emitConfigChanged()
	return updated, nil
}

// DeleteClientKey removes a client key by ID. Deleting a non-existent
// ID is a no-op (idempotent) so the UI can blindly call it without
// worrying about stale IDs.
func (a *App) DeleteClientKey(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	err := a.cfgMgr.Apply(func(cfg *types.Config) error {
		out := cfg.ClientKeys[:0]
		for _, k := range cfg.ClientKeys {
			if k.ID != id {
				out = append(out, k)
			}
		}
		cfg.ClientKeys = out
		return nil
	})
	if err != nil {
		return err
	}
	a.emitConfigChanged()
	return nil
}

// ---------------------------------------------------------------------
// Providers
// ---------------------------------------------------------------------

// ListProviders returns all configured providers.
func (a *App) ListProviders() []types.Provider {
	return a.cfgMgr.Snapshot().Providers
}

// UpsertProvider inserts or updates a provider (matched by ID).
// The mutation goes through config.Manager.Apply so a concurrent
// Wails caller cannot lose its edit by racing on Get + Save.
func (a *App) UpsertProvider(p types.Provider) (types.Provider, error) {
	if p.ID == "" {
		p.ID = newID()
	}
	var old *types.Provider
	err := a.cfgMgr.Apply(func(cfg *types.Config) error {
		found := false
		for i := range cfg.Providers {
			if cfg.Providers[i].ID == p.ID {
				prev := cfg.Providers[i]
				old = &prev
				cfg.Providers[i] = p
				found = true
				break
			}
		}
		if !found {
			cfg.Providers = append(cfg.Providers, p)
		}
		return nil
	})
	if err != nil {
		return types.Provider{}, err
	}
	// Evict the cached upstream client for the previous configuration
	// so the global cache does not leak one entry (and its connection
	// pool) per provider edit.
	if old != nil {
		upstream.Evict(*old)
	}
	a.emitConfigChanged()
	return p, nil
}

// DeleteProvider removes a provider by ID. Model aliases pointing at
// it become orphaned and the UI should warn the user.
func (a *App) DeleteProvider(id string) error {
	var removed *types.Provider
	err := a.cfgMgr.Apply(func(cfg *types.Config) error {
		out := cfg.Providers[:0]
		for _, p := range cfg.Providers {
			if p.ID != id {
				out = append(out, p)
			} else {
				cp := p
				removed = &cp
			}
		}
		cfg.Providers = out

		// Disable aliases referencing the removed provider.
		for i := range cfg.ModelAliases {
			if cfg.ModelAliases[i].ProviderID == id {
				cfg.ModelAliases[i].Enabled = false
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Release the cached upstream client (and its idle connections) for
	// the deleted provider.
	if removed != nil {
		upstream.Evict(*removed)
	}
	a.emitConfigChanged()
	return nil
}

// TestProvider performs a lightweight reachability + auth check.
// The probe URL varies by provider type:
//   - OpenAI / Gemini / Custom : GET <baseURL>/models
//   - Anthropic                : HEAD <baseURL>
//   - Azure                    : GET <baseURL>/openai/models?api-version=2024-10-21
//   - Ollama                   : GET http://127.0.0.1:11434/api/tags
//
// For non-Ollama providers both BaseURL and APIKey must be non-empty;
// Ollama only requires BaseURL.
func (a *App) TestProvider(p types.Provider) (string, error) {
	if p.BaseURL == "" {
		return "", fmt.Errorf("base URL is empty")
	}

	isOllama := strings.EqualFold(p.Type, types.ProviderOllama)
	if !isOllama && p.APIKey == "" {
		return "missing API key", nil
	}

	// Determine method and URL based on provider type.
	var method string
	var url string
	base := strings.TrimRight(p.BaseURL, "/")

	switch strings.ToLower(p.Type) {
	case types.ProviderAnthropic:
		method = http.MethodHead
		url = base
	case types.ProviderAzure:
		method = http.MethodGet
		url = base + "/openai/models?api-version=2024-10-21"
	case types.ProviderOllama:
		method = http.MethodGet
		url = "http://127.0.0.1:11434/api/tags"
	default:
		// OpenAI, Gemini, Custom
		method = http.MethodGet
		url = base + "/models"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	if p.APIKey != "" {
		if strings.EqualFold(p.Type, types.ProviderAnthropic) {
			req.Header.Set("x-api-key", p.APIKey)
		} else {
			req.Header.Set("Authorization", "Bearer "+p.APIKey)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("connection failed: %w", err)
	}
	defer resp.Body.Close()

	if p.APIKey != "" {
		return fmt.Sprintf("ok [status %d]", resp.StatusCode), nil
	}
	return fmt.Sprintf("ok [status %d]", resp.StatusCode), nil
}

// ---------------------------------------------------------------------
// Model aliases
// ---------------------------------------------------------------------

// ListModelAliases returns all model alias entries.
func (a *App) ListModelAliases() []types.ModelAlias {
	return a.cfgMgr.Snapshot().ModelAliases
}

// UpsertModelAlias inserts or updates a model alias (matched by ID).
// Mutates the config atomically via config.Manager.Apply so we can't
// race against a concurrent Wails caller on Get + Save.
func (a *App) UpsertModelAlias(m types.ModelAlias) (types.ModelAlias, error) {
	if m.ID == "" {
		m.ID = newID()
	}
	if m.Alias == "" {
		return types.ModelAlias{}, fmt.Errorf("alias is required")
	}
	err := a.cfgMgr.Apply(func(cfg *types.Config) error {
		found := false
		for i := range cfg.ModelAliases {
			if cfg.ModelAliases[i].ID == m.ID {
				cfg.ModelAliases[i] = m
				found = true
				break
			}
		}
		if !found {
			// Reject duplicates on alias name (case-insensitive).
			for _, existing := range cfg.ModelAliases {
				if equalFold(existing.Alias, m.Alias) {
					return fmt.Errorf("alias %q already exists", m.Alias)
				}
			}
			cfg.ModelAliases = append(cfg.ModelAliases, m)
		}
		return nil
	})
	if err != nil {
		return types.ModelAlias{}, err
	}
	a.emitConfigChanged()
	return m, nil
}

// DeleteModelAlias removes an alias by ID.
func (a *App) DeleteModelAlias(id string) error {
	err := a.cfgMgr.Apply(func(cfg *types.Config) error {
		out := cfg.ModelAliases[:0]
		for _, m := range cfg.ModelAliases {
			if m.ID != id {
				out = append(out, m)
			}
		}
		cfg.ModelAliases = out
		return nil
	})
	if err != nil {
		return err
	}
	a.emitConfigChanged()
	return nil
}

// ---------------------------------------------------------------------
// Server control
// ---------------------------------------------------------------------

// GetServerStatus returns the proxy server status.
func (a *App) GetServerStatus() types.ServerStatus {
	return a.proxy.Status()
}

// StartServer starts the proxy server.
func (a *App) StartServer() error {
	return a.proxy.Start()
}

// StopServer stops the proxy server.
func (a *App) StopServer() error {
	return a.proxy.Stop()
}

// RestartServer restarts the proxy server.
func (a *App) RestartServer() error {
	if err := a.proxy.Stop(); err != nil {
		return err
	}
	return a.proxy.Start()
}

// ---------------------------------------------------------------------
// Logs and stats
// ---------------------------------------------------------------------

// GetLogs returns the most recent n log entries.
func (a *App) GetLogs(n int) []types.LogEntry {
	if n <= 0 {
		n = 200
	}
	return a.store.Recent(n)
}

// ClearLogs empties both the live log buffer and the on-disk
// persisted log file. After this returns the in-memory buffer is
// empty AND the JSON file (if any) on disk is removed so the next
// launch starts from a clean state.
func (a *App) ClearLogs() error {
	a.store.Clear()
	a.store.ClearPersisted()
	a.emitLogsChanged()
	return nil
}

// GetStats returns aggregated stats.
//
// The 100ms in-process TTL cache (see statsCache) lets concurrent
// heartbeats from the frontend share one computation. The cache is
// invalidated on every Append / Clear / PurgeOlderThan via the store's
// onChange hook, so a brand-new request never lingers past 100ms.
func (a *App) GetStats() types.Stats {
	if cached, ok := a.statsCache.getStats(); ok {
		return *cached
	}
	fresh := a.store.Stats()
	a.statsCache.putStats(&fresh)
	return fresh
}

// GetStatsWithComparison returns aggregated stats together with
// previous-period comparison data for delta display in the UI.
//
// Shares the same 100ms TTL cache as GetStats: if either call has
// populated the cache within the last 100ms the other returns the
// cached pointer. The comparison variant is the one the dashboard
// actually fetches, so this is the hot-path win.
func (a *App) GetStatsWithComparison() types.StatsWithComparison {
	if cached, ok := a.statsCache.getStatsWithComparison(); ok {
		return *cached
	}
	fresh := a.store.StatsWithComparison()
	a.statsCache.putStatsWithComparison(&fresh)
	return fresh
}

// ---------------------------------------------------------------------
// System settings (auto-start, close-to-tray, tray icon, window)
// ---------------------------------------------------------------------

// GetAutoStart reports whether the OS is currently configured to
// launch the app at login. Returns false on platforms that don't
// support the feature.
func (a *App) GetAutoStart() (bool, error) {
	if a.autoStart == nil {
		return false, nil
	}
	return a.autoStart.Enabled()
}

// SetAutoStart turns the "launch at login" behaviour on or off
// and persists the preference to disk. `enable` controls the
// direction; `name` is the display label for the registry entry
// (only used when enabling).
func (a *App) SetAutoStart(enable bool, name string) error {
	if a.autoStart == nil {
		if enable {
			return system.ErrUnsupported
		}
	} else {
		if enable {
			if err := a.autoStart.Enable(name); err != nil {
				return err
			}
		} else if err := a.autoStart.Disable(); err != nil {
			return err
		}
	}
	cfg := a.cfgMgr.Get()
	cfg.AutoStart = enable
	if err := a.cfgMgr.Save(cfg); err != nil {
		fmt.Println("warning: save auto-start:", err)
	}
	return nil
}

// GetCloseToTray reports the cached "close button minimizes to
// tray" toggle. The value reflects the Settings UI state but does
// NOT take effect until the next launch — HideWindowOnClose is a
// boot-time Wails option.
func (a *App) GetCloseToTray() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closeToTray
}

// SetCloseToTray persists the "close button minimizes to tray"
// preference to disk so it survives across launches, and updates
// the in-memory cache. The change takes effect immediately for the
// tray-forwarder (Show/Quit actions still work) but the actual
// Wails HideWindowOnClose flag is read only at startup.
func (a *App) SetCloseToTray(v bool) {
	a.mu.Lock()
	a.closeToTray = v
	a.mu.Unlock()
	cfg := a.cfgMgr.Get()
	cfg.CloseToTray = v
	if err := a.cfgMgr.Save(cfg); err != nil {
		fmt.Println("warning: save close-to-tray:", err)
	}
}

// GetTrayEnabled reports whether the system tray icon is
// currently registered with the OS.
func (a *App) GetTrayEnabled() bool {
	if a.tray == nil {
		return false
	}
	return a.tray.Active()
}

// SetTrayEnabled toggles the tray icon. On platforms without tray
// support returns system.ErrUnsupported so the UI can surface a
// friendly message. The preference is persisted so the icon is
// restored on the next launch.
func (a *App) SetTrayEnabled(enable bool) error {
	if a.tray == nil {
		return system.ErrUnsupported
	}
	if !enable {
		a.tray.Stop()
		a.mu.Lock()
		a.trayStarted = false
		a.mu.Unlock()
	} else if !a.tray.Active() {
		a.mu.Lock()
		a.trayStarted = true
		a.mu.Unlock()
		// Match the tray menu language to the persisted selection.
		a.tray.SetLocale(a.cfgMgr.Get().Locale)
		if err := a.tray.Start("API Distribution", &trayForwarder{a: a}); err != nil {
			a.mu.Lock()
			a.trayStarted = false
			a.mu.Unlock()
			return err
		}
	}
	// Persist the preference so the icon comes back on next launch.
	cfg := a.cfgMgr.Get()
	cfg.TrayEnabled = enable
	if err := a.cfgMgr.Save(cfg); err != nil {
		fmt.Println("warning: save tray-enabled:", err)
	}
	return nil
}

// trayForwarder adapts the system.TrayCallbacks interface to App.
// The tray runs on its own goroutine; callbacks must be cheap.
type trayForwarder struct{ a *App }

func (f *trayForwarder) OnAction(action system.TrayAction) {
	if f.a.ctx == nil {
		return
	}
	switch action {
	case system.ActionShow:
		wruntime.WindowShow(f.a.ctx)
		wruntime.WindowUnminimise(f.a.ctx)
	case system.ActionQuit:
		f.a.requestExit()
	}
}

// requestExit flags an intentional shutdown and requests Wails to
// quit. Setting forceQuit first ensures OnBeforeClose won't convert
// the quit into a window-hide when close-to-tray is enabled.
func (a *App) requestExit() {
	if a.ctx == nil {
		return
	}
	a.mu.Lock()
	a.forceQuit = true
	a.mu.Unlock()
	wruntime.Quit(a.ctx)
}

// forceQuitting reports whether an explicit quit has been requested.
// Read by main.go's OnBeforeClose decision.
func (a *App) forceQuitting() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.forceQuit
}

// ShowWindow brings the main window to the foreground. Used by the
// Settings UI's "Show window" button when the app is hidden.
func (a *App) ShowWindow() {
	if a.ctx == nil {
		return
	}
	wruntime.WindowShow(a.ctx)
	wruntime.WindowUnminimise(a.ctx)
}

// QuitApp terminates the application cleanly. The OS-level "Quit"
// tray menu item and the Settings page "Quit app" button both
// route through here.
func (a *App) QuitApp() {
	a.requestExit()
}

// SetLocale records the user's selected UI language (a BCP-47 tag
// such as "zh-CN") and propagates it to the OS tray menu so its
// items are localized. The choice is persisted so it survives
// restarts. The frontend calls this whenever the language changes.
func (a *App) SetLocale(locale string) {
	if locale == "" {
		return
	}
	if a.tray != nil {
		a.tray.SetLocale(locale)
	}
	cfg := a.cfgMgr.Get()
	if cfg.Locale == locale {
		return
	}
	cfg.Locale = locale
	if err := a.cfgMgr.Save(cfg); err != nil {
		fmt.Println("warning: save locale:", err)
	}
}

// ---------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------

// emitLogsChanged pushes a "logs:changed" event to the frontend.
func (a *App) emitLogsChanged() {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "logs:changed")
	}
}

// emitStatsChanged pushes a "stats:changed" event to the frontend.
func (a *App) emitStatsChanged() {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "stats:changed")
	}
}

// emitConfigChanged pushes a "config:changed" event to the frontend.
func (a *App) emitConfigChanged() {
	if a.ctx != nil {
		wruntime.EventsEmit(a.ctx, "config:changed")
	}
}

// emitLogsLoop periodically emits the logs:changed and stats:changed
// events so the UI dashboard can refresh in real time without
// polling. One ticker is shared between both signals; the 2-second
// cadence is intentionally kept light.
func (a *App) emitLogsLoop() {
	a.loopWG.Add(1)
	defer a.loopWG.Done()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[loop] panic in emitLogsLoop: %v\n%s", r, debug.Stack())
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.doneCh:
			return
		case <-ticker.C:
			a.emitLogsChanged()
			a.emitStatsChanged()
		}
	}
}

// persistLogsLoop periodically writes the log buffer to disk and
// purges entries older than the configured LogRetention days.
//
// It is a "dirty write" loop: when nothing has changed since the last
// flush it returns immediately without touching the disk. The 5-second
// cadence (down from 30s) bounds how much a forcibly-terminated
// process can lose: with the Append-driven scheduleFlush() running in
// parallel, the worst case window is a few seconds of trailing data.
//
// The loop listens on both <-a.ctx.Done() and <-a.doneCh so that
// shutdown can force an exit without waiting on a tick; on either
// signal it performs exactly one final flushPersisted() and returns,
// so the shutdown path's own flushPersisted() never races a duplicate
// ticker-driven flush on the same dirty-range markers.
func (a *App) persistLogsLoop() {
	a.loopWG.Add(1)
	defer a.loopWG.Done()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[loop] panic in persistLogsLoop: %v\n%s", r, debug.Stack())
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	done := false
	for !done {
		select {
		case <-a.ctx.Done():
			done = true
		case <-a.doneCh:
			done = true
		case <-ticker.C:
			cfg := a.cfgMgr.Get()
			if cfg.LogRetention > 0 {
				a.store.PurgeOlderThan(time.Duration(cfg.LogRetention) * 24 * time.Hour)
			}
			a.flushPersisted()
		}
	}
	// Final flush on shutdown — exactly once. The shutdown main path
	// also calls flushPersisted, but flushMu serializes the two so
	// only one SaveLogs runs against the dirty range.
	a.flushPersisted()
}

// scheduleFlush coalesces the burst of Appends that typically arrive
// in a short window into ONE disk flush a few seconds after the first
// one. It is safe to call from the store's onChange hook (which fires
// with no store lock held) and from multiple goroutines: only the
// first call arms the timer, subsequent calls within the 3s window are
// ignored (flushArmed CAS).
func (a *App) scheduleFlush() {
	if !a.flushArmed.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer a.flushArmed.Store(false)
		select {
		case <-time.After(3 * time.Second):
			a.flushPersisted()
		case <-a.ctx.Done():
			// Process exiting — let shutdown()'s explicit flush own
			// the final save to avoid racing it.
		}
	}()
}

// flushPersisted writes the log buffer and per-date stats to disk when
// there is anything dirty. The two saves are independent: a failure to
// write logs.json must not prevent the daily stats (which carry the
// request + token aggregates) from being persisted, and vice versa.
// The dirty flag is only cleared when both succeed so a failed save is
// retried on the next tick.
func (a *App) flushPersisted() {
	// Serialize against the other flush sources (5s ticker, 3s
	// auto-flush goroutine, shutdown). Concurrent SaveLogs calls would
	// race on the dirty-range markers and could skip or duplicate rows.
	a.flushMu.Lock()
	defer a.flushMu.Unlock()

	if !a.store.IsDirty() {
		return
	}
	logsErr := a.store.SaveLogs()
	statsErr := a.store.SaveDaily(a.cfgMgr.Get().LogRetention)
	if logsErr != nil {
		log.Printf("error saving logs: %v", logsErr)
	}
	if statsErr != nil {
		log.Printf("error saving stats: %v", statsErr)
	}
	if logsErr == nil && statsErr == nil {
		a.store.MarkClean()
	}
}

// reapClientsLoop periodically evicts upstream clients that have been
// idle for longer than upstreamReapIdle, so the global client cache in
// internal/upstream does not grow without bound over time.
func (a *App) reapClientsLoop() {
	a.loopWG.Add(1)
	defer a.loopWG.Done()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[loop] panic in reapClientsLoop: %v\n%s", r, debug.Stack())
		}
	}()
	ticker := time.NewTicker(upstreamReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.doneCh:
			return
		case <-ticker.C:
			upstream.ReapIdle(upstreamReapIdle)
		}
	}
}

// reuse helpers
func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca := a[i]
		cb := b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
