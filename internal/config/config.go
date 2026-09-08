// Package config handles persistence of the application configuration
// to a JSON file inside the user's config directory.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"api_distribution/internal/types"
)

// configEncodeBuf is a package-level scratch buffer reused across
// every Save/Apply/Load default-write so repeated JSON encoding does
// not allocate a fresh ~50 KiB each call. The buffer is only used
// inside writeLocked, which is itself serialised by m.writeMu so the
// reuse is safe.
var configEncodeBuf bytes.Buffer

// Manager is responsible for loading and saving Config atomically.
//
// Concurrency model:
//   - m.mu (RWMutex) guards the in-memory m.cfg.
//   - m.writeMu (Mutex) serialises disk writes so two concurrent
//     saves can't trample each other's .config-*.json tmp files
//     before the atomic rename. It is independent of m.mu so the
//     in-memory state remains readable while a slow disk write is
//     in flight.
type Manager struct {
	mu      sync.RWMutex
	writeMu sync.Mutex
	path    string
	cfg     types.Config
}

// New creates a new Manager for the given config directory.
func New(dir string) *Manager {
	return &Manager{
		path: filepath.Join(dir, "config.json"),
		cfg:  types.DefaultConfig(),
	}
}

// Load reads the config file from disk. If the file does not exist,
// a default config is created and persisted.
//
// Disk writes are serialised via m.writeMu (same lock used by Save /
// Apply) so a concurrent Save started after New() but before the
// first user action can't race with the initial default-file write.
func (m *Manager) Load() (types.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, err := os.ReadFile(m.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Create default file on first run.
			def := types.DefaultConfig()
			m.mu.Unlock()
			m.writeMu.Lock()
			err := m.writeLocked(def)
			m.writeMu.Unlock()
			m.mu.Lock()
			if err != nil {
				return types.Config{}, fmt.Errorf("create default config: %w", err)
			}
			m.cfg = def
			return m.cfg, nil
		}
		return types.Config{}, fmt.Errorf("read config: %w", err)
	}

	// First decode: typed config (silently drops unknown fields).
	var cfg types.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return types.Config{}, fmt.Errorf("parse config: %w", err)
	}
	// Second decode: raw key map, used to recover fields the typed
	// Config no longer knows about (legacy single gatewayKey).
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	types.MigrateFromLegacy(&cfg, raw)

	// Backfill / normalise fields so older configs stay loadable.
	cfg.Normalize()

	m.cfg = cfg
	return m.cfg, nil
}

// Get returns a copy of the current config.
// NOTE: The returned Config is a shallow copy; slices (Providers,
// ModelAliases) share their backing arrays with the in-memory
// config. Use Snapshot() when the caller might hold onto the result
// while another goroutine mutates the config.
func (m *Manager) Get() types.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Snapshot returns a deep copy of the current config. The returned
// value is fully independent of the Manager's internal state, so
// callers can hold it without keeping the read-lock and without
// seeing mutations from concurrent writers. This is the preferred
// read path for read-heavy call sites (e.g. request routing) that
// do not need to write back.
func (m *Manager) Snapshot() types.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cfg := m.cfg
	// Deep-copy slices so the caller cannot alias the backing arrays.
	if m.cfg.Providers != nil {
		cp := make([]types.Provider, len(m.cfg.Providers))
		copy(cp, m.cfg.Providers)
		cfg.Providers = cp
	}
	if m.cfg.ModelAliases != nil {
		cp := make([]types.ModelAlias, len(m.cfg.ModelAliases))
		copy(cp, m.cfg.ModelAliases)
		cfg.ModelAliases = cp
	}
	if m.cfg.ClientKeys != nil {
		cp := make([]types.ClientKey, len(m.cfg.ClientKeys))
		copy(cp, m.cfg.ClientKeys)
		cfg.ClientKeys = cp
	}
	return cfg
}

// Save replaces the in-memory config and persists it atomically. The
// supplied config is normalized so callers don't have to fill in
// blank fields, then validated strictly so obviously bad values
// (out-of-range ports, unknown provider types, dangling aliases)
// never reach disk.
//
// Locking strategy: the critical section (normalize + validate +
// slice clone) runs under m.mu so other readers and writers see a
// consistent snapshot. The disk I/O is performed *outside* m.mu so a
// slow disk doesn't block Snapshot/Get callers, but it is serialised
// by m.writeMu to keep concurrent writers from clobbering each
// other's tmp files. The in-memory m.cfg is only replaced after the
// disk write succeeds; a write failure leaves m.cfg untouched so the
// in-memory state and on-disk file never diverge.
func (m *Manager) Save(cfg types.Config) error {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}

	m.mu.Lock()
	// Deep-copy slices so the commit cannot accidentally alias
	// anything that other goroutines might still be holding via
	// Snapshot()/Get().
	next := cfg
	next.Providers = append([]types.Provider(nil), cfg.Providers...)
	next.ModelAliases = append([]types.ModelAlias(nil), cfg.ModelAliases...)
	next.ClientKeys = append([]types.ClientKey(nil), cfg.ClientKeys...)
	m.mu.Unlock()

	// Disk I/O — outside m.mu so reads (Get / Snapshot) are not
	// blocked by a slow filesystem. Serialised by writeMu so two
	// concurrent Saves don't race on the .config-*.json tmp file.
	m.writeMu.Lock()
	err := m.writeLocked(next)
	m.writeMu.Unlock()
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.cfg = next
	m.mu.Unlock()
	return nil
}

// Apply atomically reads the current config, hands a deep copy to
// mutate, normalises + validates the result, and persists it. Use
// this from call sites that perform read-modify-write sequences (e.g.
// UpsertProvider / DeleteProvider in App) to avoid lost updates when
// two Wails callers race for the same list. The mutate callback may
// return an error to abort the change without writing; that error is
// propagated to the caller.
//
// Locking strategy mirrors Save: the read-modify-write critical
// section (deep-copy + mutate + normalise + validate) is taken under
// m.mu, then m.mu is released before the disk write so concurrent
// Get/Snapshot calls are not blocked by slow I/O. Disk writes are
// serialised through m.writeMu. m.cfg is only swapped in after the
// disk write returns nil, so a failed write leaves both the in-memory
// and on-disk state at the previous consistent value.
func (m *Manager) Apply(mutate func(*types.Config) error) error {
	if mutate == nil {
		return errors.New("config: mutate callback must not be nil")
	}

	m.mu.Lock()
	cfg := m.cfg
	// Deep-copy the slices we expect callers to mutate. Scalar fields
	// are safe because Config is passed by value here.
	cfg.Providers = append([]types.Provider(nil), m.cfg.Providers...)
	cfg.ModelAliases = append([]types.ModelAlias(nil), m.cfg.ModelAliases...)
	cfg.ClientKeys = append([]types.ClientKey(nil), m.cfg.ClientKeys...)

	if err := mutate(&cfg); err != nil {
		m.mu.Unlock()
		return err
	}

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("invalid config: %w", err)
	}
	next := cfg
	m.mu.Unlock()

	m.writeMu.Lock()
	err := m.writeLocked(next)
	m.writeMu.Unlock()
	if err != nil {
		return err
	}

	m.mu.Lock()
	m.cfg = next
	m.mu.Unlock()
	return nil
}

// Path returns the absolute path of the config file.
func (m *Manager) Path() string {
	return m.path
}

// writeLocked persists cfg atomically. The caller must serialise
// disk writes by holding either m.mu (the legacy single-lock path
// still used by Load for the first-run default write) or m.writeMu
// (used by Save / Apply so the tmp-file + rename dance doesn't race
// against another concurrent writer).
func (m *Manager) writeLocked(cfg types.Config) error {
	// Reuse the package-level configEncodeBuf across calls so each
	// Save does not allocate a fresh ~50 KiB marshalled payload.
	// writeLocked is serialised by m.writeMu, so the buffer reuse
	// is race-free.
	configEncodeBuf.Reset()
	enc := json.NewEncoder(&configEncodeBuf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data := configEncodeBuf.Bytes()

	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure config dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op if renamed

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, m.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
