package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"api_distribution/internal/types"
)

// persistedData is the JSON schema for the on-disk log snapshot.
// Aggregated statistics are no longer stored here — they live in
// per-date files under the stats directory (see SaveDaily).
type persistedData struct {
	Logs  []types.LogEntry `json:"logs"`
	Saved int64            `json:"savedAt"`
}

// diskWriteMu serializes the on-disk portion of file writes so that
// concurrent callers don't race on the temp-file creation inside the
// same directory. The store mutex (s.mu) is *not* held while this is
// acquired, so it does not block Append / Recent / Stats.
var diskWriteMu sync.Mutex

// SaveToJSON writes the current ring buffer to path, creating
// intermediate directories as needed. The snapshot is built under a
// brief RLock; the (potentially slow) JSON encoding and disk I/O happen
// without holding s.mu so concurrent Append calls are not blocked by an
// in-progress save.
func (s *Store) SaveToJSON(path string) error {
	// --- Critical section under s.mu.RLock ---------------------------
	s.mu.RLock()
	snap := persistedData{
		Logs:  s.exportLogs(),
		Saved: time.Now().UnixNano(),
	}
	s.mu.RUnlock()
	// --- End critical section ---------------------------------------

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal logs: %w", err)
	}
	return atomicWrite(path, data)
}

// LoadFromJSON reads the persisted log snapshot from path and replaces
// the in-memory ring buffer. Missing file is treated as empty (no error).
func (s *Store) LoadFromJSON(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // first run — no logs yet
		}
		return fmt.Errorf("read logs: %w", err)
	}

	var snap persistedData
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("parse logs: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Replace ring buffer contents.
	s.logs = make([]types.LogEntry, MaxLogEntries)
	s.idx = 0
	n := len(snap.Logs)
	if n > MaxLogEntries {
		n = MaxLogEntries
	}
	for i := 0; i < n; i++ {
		s.logs[i] = snap.Logs[len(snap.Logs)-n+i]
		s.idx = (s.idx + 1) % MaxLogEntries
	}

	return nil
}

// PurgeOlderThan removes log entries older than the given duration.
// Statistical aggregates are pruned separately via SaveDaily's retention.
func (s *Store) PurgeOlderThan(d time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-d).UnixNano()
	purged := 0

	for i, e := range s.logs {
		if e.ID == "" {
			continue
		}
		if e.Timestamp < cutoff {
			s.logs[i] = types.LogEntry{}
			purged++
		}
	}

	if purged > 0 {
		compact := make([]types.LogEntry, 0, MaxLogEntries)
		for _, e := range s.logs {
			if e.ID != "" {
				compact = append(compact, e)
			}
		}
		s.logs = make([]types.LogEntry, MaxLogEntries)
		s.idx = 0
		for _, e := range compact {
			s.logs[s.idx] = e
			s.idx = (s.idx + 1) % MaxLogEntries
		}
		s.dirty.Store(true)
	}

	return purged
}

// exportLogs returns a sorted (oldest-first) slice of all non-empty
// log entries from the ring buffer. Caller must hold s.mu.RLock.
func (s *Store) exportLogs() []types.LogEntry {
	out := make([]types.LogEntry, 0, len(s.logs))
	for _, e := range s.logs {
		if e.ID != "" {
			out = append(out, e)
		}
	}
	return out
}

// LogPath returns the default path for the persisted logs file
// inside the given config directory.
func LogPath(dir string) string {
	return filepath.Join(dir, "logs.json")
}

// ---------------------------------------------------------------------
// Per-date aggregate statistics
// ---------------------------------------------------------------------

// StatsDir returns the directory holding per-date stats JSON files.
func StatsDir(cfgDir string) string {
	return filepath.Join(cfgDir, "stats")
}

// DailyPath returns the per-date stats file for a calendar day.
func DailyPath(dir, date string) string {
	return filepath.Join(dir, date+".json")
}

// SaveDaily persists every in-memory daily aggregate to its per-date
// JSON file, deleting files older than `retentionDays` (when > 0; 0 or
// negative keeps all history) and dropping those days from memory.
func (s *Store) SaveDaily(dir string, retentionDays int) error {
	s.mu.RLock()
	kept := make([]*DailyAgg, 0, len(s.days))
	for _, d := range s.days {
		kept = append(kept, d)
	}
	s.mu.RUnlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure stats dir: %w", err)
	}

	var prune []string
	for _, d := range kept {
		if retentionDays > 0 && d.Date < cutoffDate(retentionDays) {
			prune = append(prune, d.Date)
			continue
		}
		data, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", d.Date, err)
		}
		if err := atomicWrite(DailyPath(dir, d.Date), data); err != nil {
			return fmt.Errorf("write %s: %w", d.Date, err)
		}
	}

	if len(prune) > 0 {
		s.mu.Lock()
		for _, dt := range prune {
			delete(s.days, dt)
		}
		s.mu.Unlock()
		for _, dt := range prune {
			_ = os.Remove(DailyPath(dir, dt))
		}
	}
	return nil
}

// cutoffDate returns the DayLayout date marking the retention boundary:
// days strictly older than this are pruned.
func cutoffDate(retentionDays int) string {
	return time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Format(DayLayout)
}

// LoadAllDaily scans the stats directory and replaces the in-memory day
// aggregates with those read from disk. Missing directory is treated as
// empty (no error). Corrupt/unparseable files are skipped.
func (s *Store) LoadAllDaily(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read stats dir: %w", err)
	}

	loaded := make(map[string]*DailyAgg)
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, ent.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var d DailyAgg
		if err := json.Unmarshal(data, &d); err != nil || d.Date == "" {
			continue
		}
		if d.ByModel == nil {
			d.ByModel = map[string]int64{}
		}
		if d.ByProvider == nil {
			d.ByProvider = map[string]int64{}
		}
		if d.ByClientKey == nil {
			d.ByClientKey = map[string]int64{}
		}
		if d.ByHour == nil {
			d.ByHour = map[int64]types.HourBucket{}
		}
		// Daily JSON written before HourBucket grew a ByModel field
		// unmarshals to nil for every bucket's inner map. Make those
		// explicit empty maps so the next add() write and any
		// subsequent iteration in sumDays() see a non-nil map and can
		// safely do `b.ByModel[alias]++` / `range b.ByModel`.
		for h, b := range d.ByHour {
			if b.ByModel == nil {
				b.ByModel = map[string]int64{}
				d.ByHour[h] = b
			}
		}
		loaded[d.Date] = &d
	}

	s.mu.Lock()
	s.days = loaded
	s.mu.Unlock()
	return nil
}

// atomicWrite writes data to path atomically (temp file in the same
// directory, then rename) so a crash mid-write never leaves a torn file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure dir: %w", err)
	}

	// Serialize disk writes across goroutines so we don't race on
	// CreateTemp / Rename inside the same directory.
	diskWriteMu.Lock()
	defer diskWriteMu.Unlock()

	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	return os.Rename(tmpPath, path)
}

// ClearDailyFiles removes every per-date stats JSON file under dir. Used
// by ClearLogs so a full reset also drops persisted aggregates.
func ClearDailyFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		_ = os.Remove(filepath.Join(dir, ent.Name()))
	}
}
