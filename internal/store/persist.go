package store

import (
	"encoding/json"
	"fmt"
	"time"

	"api_distribution/internal/types"
)

// persistedData is the legacy JSON schema for the on-disk log snapshot.
// It is only read during MigrateLegacyJSON to import pre-SQLite data.
type persistedData struct {
	Logs  []types.LogEntry `json:"logs"`
	Saved int64            `json:"savedAt"`
}

// SaveLogs upserts every unflushed entry in the ring buffer into the
// logs table. The dirty-range markers (dirtyStart, dirtyIdx) drive the
// incremental flush:
//
//   - We snapshot dirtyIdx / dirtyStart WITHOUT holding s.mu (they
//     are atomics), then take a brief RLock to copy the entries in
//     [dirtyStart, dirtyIdx) into a local slice.
//   - After every successful insert we advance dirtyStart; if any
//     insert fails we return the error and leave dirtyStart where it
//     was, so the next tick retries from the same position.
//   - An empty delta is a no-op (returns nil immediately), so the
//     30-second idle heartbeat performs zero SQL on an idle gateway.
//
// The SQL work happens outside s.mu so concurrent Append calls are not
// blocked by an in-progress save. Retry semantics are preserved: a
// transient SQL error causes the same delta to be retried on the next
// tick, exactly like the previous "INSERT ON CONFLICT" loop did.
func (s *Store) SaveLogs() error {
	// Atomic snapshot first so the slice we copy under the lock has
	// a well-defined upper bound.
	dirtyIdx := s.dirtyIdx.Load()
	dirtyStart := s.dirtyStart.Load()
	if dirtyIdx <= dirtyStart {
		return nil
	}

	delta := dirtyIdx - dirtyStart
	entries := make([]types.LogEntry, 0, delta)

	s.mu.RLock()
	for seq := dirtyStart; seq < dirtyIdx; seq++ {
		// The N-th append (0-indexed) lives at ring slot N %
		// MaxLogEntries. We capture the entry by value so the SQL
		// upsert runs against a stable copy.
		idx := int(seq % MaxLogEntries)
		if idx < 0 || idx >= len(s.logs) {
			continue
		}
		e := s.logs[idx]
		if e.ID == "" {
			continue
		}
		entries = append(entries, e)
	}
	s.mu.RUnlock()

	if len(entries) == 0 {
		// The dirty range contained only empty slots (e.g. after a
		// Compact-and-purge). Still advance dirtyStart so we don't
		// keep scanning the same range on every tick.
		s.dirtyStart.Store(dirtyIdx)
		return nil
	}

	for i := range entries {
		if err := s.insertLog(entries[i]); err != nil {
			// Leave dirtyStart at the failing offset so the retry
			// picks up exactly here. We re-derive the failure point
			// from the (still-incrementing) dirtyIdx and the number
			// of successfully inserted entries this round.
			failedStart := dirtyStart + int64(i)
			s.dirtyStart.Store(failedStart)
			return err
		}
	}
	s.dirtyStart.Store(dirtyIdx)
	return nil
}

// LoadLogs reads the most recent MaxLogEntries rows from the logs
// table and replaces the in-memory ring buffer. An empty table is
// treated as a first run (no error). The dirty-range markers are
// aligned with the loaded entries: dirtyIdx == dirtyStart == N means
// "every loaded entry is already on disk; nothing to flush yet".
func (s *Store) LoadLogs() error {
	rows, err := s.db.Query(`SELECT id, ts, method, path, status_code, latency_ms,
		alias, provider_id, provider_name, provider_model,
		input_tokens, output_tokens, client_ip, api_key_suffix,
		client_key_label, error, streaming
		FROM logs ORDER BY ts DESC LIMIT ?`, MaxLogEntries)
	if err != nil {
		return fmt.Errorf("query logs: %w", err)
	}
	defer rows.Close()

	var entries []types.LogEntry
	for rows.Next() {
		var e types.LogEntry
		var streaming int
		if err := rows.Scan(&e.ID, &e.Timestamp, &e.Method, &e.Path, &e.StatusCode,
			&e.LatencyMs, &e.Alias, &e.ProviderID, &e.ProviderName, &e.ProviderModel,
			&e.InputTokens, &e.OutputTokens, &e.ClientIP, &e.APIKeySuffix,
			&e.ClientKeyLabel, &e.Error, &streaming); err != nil {
			return fmt.Errorf("scan log: %w", err)
		}
		e.Streaming = streaming != 0
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate logs: %w", err)
	}

	// rows are newest-first; fill the ring buffer oldest-first so
	// Recent() (which walks backwards from idx-1) returns newest first.
	n := int64(len(entries))
	s.mu.Lock()
	s.logs = make([]types.LogEntry, MaxLogEntries)
	s.idx = 0
	for i := len(entries) - 1; i >= 0; i-- {
		s.logs[s.idx] = entries[i]
		s.idx = (s.idx + 1) % MaxLogEntries
	}
	// Treat all loaded rows as already on disk: nothing to flush yet.
	s.dirtyIdx.Store(n)
	s.dirtyStart.Store(n)
	s.mu.Unlock()

	s.fireOnChange()
	return nil
}

// PurgeOlderThan removes log entries older than the given duration from
// both the ring buffer and the logs table. Statistical aggregates are
// pruned separately via SaveDaily's retention.
//
// The dirty-range markers are invalidated on purge: the ring slot
// reshuffle can leave dirtyIdx pointing at a slot that has been
// rewritten with an older entry. Setting dirtyIdx == dirtyStart forces
// the next SaveLogs to be a no-op until a fresh Append advances
// dirtyIdx again. LoadLogs + the next Append will re-establish the
// correct invariant (see LoadLogs comment).
func (s *Store) PurgeOlderThan(d time.Duration) int {
	cutoff := time.Now().Add(-d).UnixNano()

	s.mu.Lock()
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
		// Ring was reshuffled: the slot dirtyIdx pointed at may no
		// longer contain what we think it does. Collapse the dirty
		// range so SaveLogs does not insert stale IDs, then let the
		// next Append re-open it.
		n := int64(len(compact))
		s.dirtyIdx.Store(n)
		s.dirtyStart.Store(n)
	}
	s.mu.Unlock()

	// Best-effort: drop the same rows from the logs table. The ring
	// buffer stays authoritative for what the UI surfaces this session.
	_, _ = s.db.Exec(`DELETE FROM logs WHERE ts < ?`, cutoff)
	if purged > 0 {
		// Both the in-memory ring buffer and the persisted logs table
		// lost entries; notify subscribers so cached stats (totals,
		// hourly buckets that overlap the purge window, etc.) get
		// invalidated. fireOnChange must run without s.mu held — the
		// unlock above already guarantees that.
		s.fireOnChange()
	}
	return purged
}

// ---------------------------------------------------------------------
// Per-date aggregate statistics
// ---------------------------------------------------------------------

// SaveDaily upserts every in-memory daily aggregate into the
// daily_stats table and prunes rows older than `retentionDays` (when >
// 0; 0 or negative keeps all history), dropping those days from memory
// as well.
func (s *Store) SaveDaily(retentionDays int) error {
	s.mu.RLock()
	kept := make([]*DailyAgg, 0, len(s.days))
	for _, d := range s.days {
		kept = append(kept, d)
	}
	s.mu.RUnlock()

	for _, d := range kept {
		if err := s.upsertDaily(d); err != nil {
			return err
		}
	}

	if retentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour).Format(DayLayout)
		if _, err := s.db.Exec(`DELETE FROM daily_stats WHERE date < ?`, cutoff); err != nil {
			return fmt.Errorf("prune daily_stats: %w", err)
		}
		s.mu.Lock()
		pruned := false
		for _, d := range kept {
			if d.Date < cutoff {
				delete(s.days, d.Date)
				pruned = true
			}
		}
		s.mu.Unlock()
		if pruned {
			// Pruning dropped at least one in-memory day aggregate;
			// surface the change so cached stats derived from
			// `days` (e.g. the dashboard totals) get invalidated.
			s.fireOnChange()
		}
	}
	return nil
}

// LoadAllDaily reads every row from the daily_stats table and replaces
// the in-memory day aggregates. An empty table is treated as a first
// run (no error).
func (s *Store) LoadAllDaily() error {
	rows, err := s.db.Query(`SELECT date, requests, input_tokens, output_tokens,
		errors, latency_sum, by_model, by_provider, by_client_key, by_hour
		FROM daily_stats`)
	if err != nil {
		return fmt.Errorf("query daily_stats: %w", err)
	}
	defer rows.Close()

	loaded := make(map[string]*DailyAgg)
	for rows.Next() {
		var d DailyAgg
		var byModel, byProvider, byClientKey, byHour string
		if err := rows.Scan(&d.Date, &d.TotalRequests, &d.TotalInputTokens,
			&d.TotalOutputTokens, &d.Errors, &d.LatencySumMs,
			&byModel, &byProvider, &byClientKey, &byHour); err != nil {
			return fmt.Errorf("scan daily_stats: %w", err)
		}
		// JSON columns: unmarshal errors are non-fatal (documents a
		// missing/legacy map as absent, which normalize() then fixes).
		_ = jsonUnmarshalMap(byModel, &d.ByModel)
		_ = jsonUnmarshalMap(byProvider, &d.ByProvider)
		_ = jsonUnmarshalMap(byClientKey, &d.ByClientKey)
		_ = jsonUnmarshalHourBuckets(byHour, &d.ByHour)
		d.normalize()
		loaded[d.Date] = &d
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate daily_stats: %w", err)
	}

	s.mu.Lock()
	s.days = loaded
	s.mu.Unlock()
	return nil
}

// jsonUnmarshalMap unmarshals a JSON object column into an int64 map,
// leaving it nil when the payload is empty/unparseable.
func jsonUnmarshalMap(raw string, into *map[string]int64) error {
	if raw == "" || raw == "{}" {
		return nil
	}
	return json.Unmarshal([]byte(raw), into)
}

// jsonUnmarshalHourBuckets unmarshals the by_hour JSON column (whose
// keys are unix-second timestamps as strings) into an int64-keyed map.
func jsonUnmarshalHourBuckets(raw string, into *map[int64]types.HourBucket) error {
	if raw == "" || raw == "{}" {
		return nil
	}
	tmp := map[string]types.HourBucket{}
	if err := json.Unmarshal([]byte(raw), &tmp); err != nil {
		return err
	}
	out := make(map[int64]types.HourBucket, len(tmp))
	for k, v := range tmp {
		var h int64
		if _, err := fmt.Sscan(k, &h); err != nil {
			continue
		}
		out[h] = v
	}
	*into = out
	return nil
}

// ClearDaily removes every row from the daily_stats table. Used by
// ClearLogs so a full reset also drops persisted aggregates.
func (s *Store) ClearDaily() {
	_, _ = s.db.Exec(`DELETE FROM daily_stats`)
}
