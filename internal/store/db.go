package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)

	"api_distribution/internal/types"
)

// DefaultDataDir returns the directory that stores the SQLite
// database: a "data" folder next to the executable. Persisting next to
// a portable exe keeps stats/logs with the binary instead of the user
// config directory. The folder itself is created lazily when the
// database is opened (see openDB), keeping this a pure path resolver.
func DefaultDataDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "data"), nil
}

// DBPath returns the SQLite database path inside dataDir. All
// persisted state (per-date aggregates + the log ring buffer) lives in
// this single file, replacing the previous stats/*.json + logs.json.
func DBPath(dataDir string) string {
	return filepath.Join(dataDir, "api_distribution.db")
}

// openDB opens (creating if needed) the SQLite database at path and
// returns a Store backed by it. The connection pool is capped at one
// connection: SQLite is a single-writer engine and this also keeps an
// in-memory database (:memory:) alive across calls.
func openDB(path string) (*Store, error) {
	// SQLite creates the file lazily but not its parent directory;
	// ensure the data folder exists so a fresh install works.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	st := &Store{db: db, days: make(map[string]*DailyAgg)}
	// Pre-allocate the ring buffer at full capacity so Append does not
	// trigger cap-grow allocations as the buffer fills up to
	// MaxLogEntries (previously grown incrementally, retaining
	// intermediate ~MiB-class backing arrays).
	st.logs = make([]types.LogEntry, 0, MaxLogEntries)
	if err := st.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

// Open opens (creating if needed) the file-backed SQLite database at
// path and returns a Store backed by it. The app uses this; tests use
// New() with an in-memory database.
func Open(path string) (*Store, error) {
	return openDB(path)
}

// Close releases the underlying SQLite database handle. The app calls
// this during shutdown AFTER the final SaveLogs/SaveDaily flush so no
// data is lost — leaving the handle open would hold an exclusive lock
// on the file on Windows and can cause "database is locked" errors for
// a second app instance started while the first is still exiting.
func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// initSchema creates the tables if they do not exist. The complex
// per-day maps (byModel / byProvider / byClientKey / byHour) are stored
// as JSON columns; the scalar aggregates are real columns so retention
// pruning and future SQL queries stay cheap.
func (s *Store) initSchema() error {
	const schema = `
CREATE TABLE IF NOT EXISTS daily_stats (
	date TEXT PRIMARY KEY,
	requests INTEGER NOT NULL DEFAULT 0,
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	errors INTEGER NOT NULL DEFAULT 0,
	latency_sum INTEGER NOT NULL DEFAULT 0,
	by_model TEXT NOT NULL DEFAULT '{}',
	by_provider TEXT NOT NULL DEFAULT '{}',
	by_client_key TEXT NOT NULL DEFAULT '{}',
	by_hour TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS logs (
	id TEXT PRIMARY KEY,
	ts INTEGER NOT NULL,
	method TEXT NOT NULL DEFAULT '',
	path TEXT NOT NULL DEFAULT '',
	status_code INTEGER NOT NULL DEFAULT 0,
	latency_ms INTEGER NOT NULL DEFAULT 0,
	alias TEXT NOT NULL DEFAULT '',
	provider_id TEXT NOT NULL DEFAULT '',
	provider_name TEXT NOT NULL DEFAULT '',
	provider_model TEXT NOT NULL DEFAULT '',
	input_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	client_ip TEXT NOT NULL DEFAULT '',
	api_key_suffix TEXT NOT NULL DEFAULT '',
	client_key_label TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	streaming INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_logs_ts ON logs(ts);
`
	_, err := s.db.Exec(schema)
	if err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	return nil
}

// MigrateLegacyJSON imports the legacy JSON persistence (stats/*.json
// and logs.json) into SQLite. It is idempotent: when the target tables
// already contain rows it is a no-op, so it only runs once after an
// upgrade. The legacy files are left in place.
func (s *Store) MigrateLegacyJSON(cfgDir string) error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM daily_stats`).Scan(&count); err != nil {
		return fmt.Errorf("count daily_stats: %w", err)
	}
	if count == 0 {
		if err := s.importDailyJSON(filepath.Join(cfgDir, "stats")); err != nil {
			return err
		}
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&count); err != nil {
		return fmt.Errorf("count logs: %w", err)
	}
	if count == 0 {
		if err := s.importLogsJSON(filepath.Join(cfgDir, "logs.json")); err != nil {
			return err
		}
	}
	return nil
}

// importDailyJSON reads every stats/YYYY-MM-DD.json file and upserts it
// into daily_stats. Missing directory is treated as empty.
func (s *Store) importDailyJSON(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read stats dir: %w", err)
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		var d DailyAgg
		if err := json.Unmarshal(data, &d); err != nil || d.Date == "" {
			continue
		}
		if err := s.upsertDaily(&d); err != nil {
			return fmt.Errorf("import %s: %w", ent.Name(), err)
		}
	}
	return nil
}

// importLogsJSON reads the legacy logs.json snapshot and inserts every
// entry into the logs table. Missing file is treated as empty.
func (s *Store) importLogsJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read logs: %w", err)
	}
	var snap persistedData
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("parse logs: %w", err)
	}
	for _, e := range snap.Logs {
		if err := s.insertLog(e); err != nil {
			return fmt.Errorf("import log %s: %w", e.ID, err)
		}
	}
	return nil
}

// upsertDaily writes one day's aggregate into daily_stats, replacing
// any existing row for the same date.
//
// JSON encoding errors are returned explicitly (not silently swallowed):
// the previous implementation ignored json.Marshal errors, which meant
// a corrupted DailyAgg (e.g. a future unencodable field) would silently
// overwrite the row with empty objects and lose data with no log.
func (s *Store) upsertDaily(d *DailyAgg) error {
	// The four per-day maps are encoded into the package-level
	// upsertDailyBuf (Reset + json.NewEncoder.Encode per map), so
	// we don't allocate a fresh ~MB-class []byte for each column.
	// The string() conversions below copy the bytes so subsequent
	// Resets are safe.
	enc := json.NewEncoder(&upsertDailyBuf)

	upsertDailyBuf.Reset()
	if err := enc.Encode(d.ByModel); err != nil {
		return fmt.Errorf("marshal by_model for %s: %w", d.Date, err)
	}
	byModel := string(upsertDailyBuf.Bytes())

	upsertDailyBuf.Reset()
	if err := enc.Encode(d.ByProvider); err != nil {
		return fmt.Errorf("marshal by_provider for %s: %w", d.Date, err)
	}
	byProvider := string(upsertDailyBuf.Bytes())

	upsertDailyBuf.Reset()
	if err := enc.Encode(d.ByClientKey); err != nil {
		return fmt.Errorf("marshal by_client_key for %s: %w", d.Date, err)
	}
	byClientKey := string(upsertDailyBuf.Bytes())

	upsertDailyBuf.Reset()
	if err := enc.Encode(d.ByHour); err != nil {
		return fmt.Errorf("marshal by_hour for %s: %w", d.Date, err)
	}
	byHour := string(upsertDailyBuf.Bytes())

	_, err := s.db.Exec(`
		INSERT INTO daily_stats
			(date, requests, input_tokens, output_tokens, errors, latency_sum, by_model, by_provider, by_client_key, by_hour)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date) DO UPDATE SET
			requests = excluded.requests,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			errors = excluded.errors,
			latency_sum = excluded.latency_sum,
			by_model = excluded.by_model,
			by_provider = excluded.by_provider,
			by_client_key = excluded.by_client_key,
			by_hour = excluded.by_hour`,
		d.Date, d.TotalRequests, d.TotalInputTokens, d.TotalOutputTokens,
		d.Errors, d.LatencySumMs,
		byModel, byProvider, byClientKey, byHour)
	if err != nil {
		return fmt.Errorf("upsert daily %s: %w", d.Date, err)
	}
	return nil
}

// insertLog writes one log entry into the logs table, replacing any
// existing row with the same ID.
func (s *Store) insertLog(e types.LogEntry) error {
	if e.ID == "" {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO logs
			(id, ts, method, path, status_code, latency_ms, alias, provider_id, provider_name, provider_model,
			 input_tokens, output_tokens, client_ip, api_key_suffix, client_key_label, error, streaming)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			ts = excluded.ts,
			method = excluded.method,
			path = excluded.path,
			status_code = excluded.status_code,
			latency_ms = excluded.latency_ms,
			alias = excluded.alias,
			provider_id = excluded.provider_id,
			provider_name = excluded.provider_name,
			provider_model = excluded.provider_model,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			client_ip = excluded.client_ip,
			api_key_suffix = excluded.api_key_suffix,
			client_key_label = excluded.client_key_label,
			error = excluded.error,
			streaming = excluded.streaming`,
		e.ID, e.Timestamp, e.Method, e.Path, e.StatusCode, e.LatencyMs,
		e.Alias, e.ProviderID, e.ProviderName, e.ProviderModel,
		e.InputTokens, e.OutputTokens, e.ClientIP, e.APIKeySuffix,
		e.ClientKeyLabel, e.Error, boolToInt(e.Streaming))
	if err != nil {
		return fmt.Errorf("insert log %s: %w", e.ID, err)
	}
	return nil
}

// ClearPersisted removes every row from both the logs and daily_stats
// tables. Used by ClearLogs so a full reset also drops persisted data.
func (s *Store) ClearPersisted() {
	_, _ = s.db.Exec(`DELETE FROM logs`)
	_, _ = s.db.Exec(`DELETE FROM daily_stats`)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
