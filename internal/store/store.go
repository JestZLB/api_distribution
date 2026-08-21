// Package store maintains an in-memory ring buffer of recent
// log entries plus aggregated request statistics.
package store

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"api_distribution/internal/types"
)

const (
	// MaxLogEntries is the cap on in-memory log entries. Older
	// entries are evicted automatically.
	MaxLogEntries = 2000
)

// Store is the central log + stats store used by the proxy server.
type Store struct {
	mu   sync.RWMutex
	logs []types.LogEntry
	idx  int

	// days accumulates per-calendar-day statistics. It is the source of
	// truth for the dashboard totals: every proxied request is folded
	// into today's DailyAgg, then persisted to stats/YYYY-MM-DD.json and
	// reloaded at startup so historical volume survives restarts.
	days map[string]*DailyAgg

	// dirty is set whenever a mutation (Append, Clear, PurgeOlderThan)
	// alters in-memory state that has not yet been flushed to disk.
	// persistLogsLoop reads + clears it on each tick so an idle
	// gateway does zero JSON marshalling on its 30-second heartbeat.
	dirty atomic.Bool
}

// New creates an empty Store.
func New() *Store {
	return &Store{
		days: make(map[string]*DailyAgg),
	}
}

// Append adds a new log entry to the ring buffer and folds it into the
// current calendar day's aggregated statistics.
func (s *Store) Append(e types.LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.logs) < MaxLogEntries {
		s.logs = append(s.logs, e)
	} else {
		s.logs[s.idx] = e
	}
	s.idx = (s.idx + 1) % MaxLogEntries

	s.getOrCreateDay(dateKey(e.Timestamp)).add(e)
	s.dirty.Store(true)
}

// getOrCreateDay returns (creating if missing) the daily aggregate for
// the given calendar date. Caller must hold s.mu.
func (s *Store) getOrCreateDay(date string) *DailyAgg {
	if d, ok := s.days[date]; ok {
		return d
	}
	d := newDaily(date)
	s.days[date] = d
	return d
}

// Recent returns the last n log entries, newest first.
func (s *Store) Recent(n int) []types.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if n <= 0 || n > len(s.logs) {
		n = len(s.logs)
	}
	out := make([]types.LogEntry, 0, n)
	if len(s.logs) == 0 {
		return out
	}

	for i := 0; i < n; i++ {
		idx := (s.idx - 1 - i + MaxLogEntries) % MaxLogEntries
		if idx < 0 || idx >= len(s.logs) {
			continue
		}
		entry := s.logs[idx]
		if entry.ID == "" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Clear empties the log buffer and all accumulated statistics.
// Hourly buckets are preserved.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = s.logs[:0]
	s.idx = 0
	s.days = make(map[string]*DailyAgg)
	s.dirty.Store(true)
}

// IsDirty reports whether in-memory state has unflushed changes.
// Callers (typically persistLogsLoop) consume this together with
// MarkClean to skip disk I/O on idle ticks.
func (s *Store) IsDirty() bool {
	return s.dirty.Load()
}

// MarkClean records that the current in-memory state matches disk.
// Safe to call without holding s.mu; uses atomic semantics.
func (s *Store) MarkClean() {
	s.dirty.Store(false)
}

// Stats returns aggregated statistics for the UI dashboard. Totals are
// summed across every loaded/current calendar day (all history), the
// hourly curve is reconstructed for the last 7 days, and the
// per-client-key "last 24h" counts are derived from the live log buffer.
func (s *Store) Stats() types.Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := s.sumDays()
	recentCutoff := time.Now().Add(-24 * time.Hour).UnixNano()
	for _, e := range s.logs {
		if e.ID == "" || e.ClientKeyLabel == "" {
			continue
		}
		if e.Timestamp >= recentCutoff {
			stats.RequestsByClientKeyRecent[e.ClientKeyLabel]++
		}
	}
	return stats
}

// sumDays computes the aggregate across all daily statistics (total
// requests/tokens, per-model/provider/client-key, and the last-7-day
// hourly curve). Caller must hold s.mu (read or write).
func (s *Store) sumDays() types.Stats {
	stats := types.Stats{
		RequestsByModel:           map[string]int64{},
		RequestsByProvider:        map[string]int64{},
		RequestsByClientKey:       map[string]int64{},
		RequestsByClientKeyRecent: map[string]int64{},
		RequestsByHour:            []types.HourBucket{},
		RequestsByHourByModel:     map[string][]types.HourBucket{},
	}

	now := time.Now()
	chartCutoff := now.Add(-7 * 24 * time.Hour).Truncate(time.Hour).Unix()
	var totalLatency int64
	byHour := make(map[int64]types.HourBucket)
	// Per-alias per-hour accumulation: alias -> hour -> partial bucket.
	// Each inner bucket only tracks the count for its own alias (the
	// inner ByModel from the per-day DailyAgg is the source we sum
	// over); errors are not tracked per-model, so the inner Errors
	// field stays 0 in the output.
	byHourByModel := make(map[string]map[int64]types.HourBucket)

	for _, d := range s.days {
		stats.TotalRequests += d.TotalRequests
		stats.TotalInputTokens += d.TotalInputTokens
		stats.TotalOutputTokens += d.TotalOutputTokens
		totalLatency += d.LatencySumMs
		for m, c := range d.ByModel {
			stats.RequestsByModel[m] += c
		}
		for p, c := range d.ByProvider {
			stats.RequestsByProvider[p] += c
		}
		for k, c := range d.ByClientKey {
			stats.RequestsByClientKey[k] += c
		}
		for h, a := range d.ByHour {
			if h < chartCutoff {
				continue
			}
			b := byHour[h]
			b.Hour = h
			b.Count += a.Count
			b.Errors += a.Errors
			byHour[h] = b
			for alias, count := range a.ByModel {
				inner, ok := byHourByModel[alias]
				if !ok {
					inner = make(map[int64]types.HourBucket)
					byHourByModel[alias] = inner
				}
				ab := inner[h]
				ab.Hour = h
				ab.Count += count
				inner[h] = ab
			}
		}
	}

	if stats.TotalRequests > 0 {
		stats.AvgLatencyMs = totalLatency / stats.TotalRequests
	}

	for _, b := range byHour {
		stats.RequestsByHour = append(stats.RequestsByHour, b)
	}
	sort.Slice(stats.RequestsByHour, func(i, j int) bool {
		return stats.RequestsByHour[i].Hour < stats.RequestsByHour[j].Hour
	})

	// Flatten byHourByModel into per-alias hour-sorted slices. Each
	// emitted bucket's ByModel is a one-entry map keyed by the alias
	// itself so the frontend can do `data[alias][i].byModel[alias]`
	// without extra aggregation.
	for alias, inner := range byHourByModel {
		buckets := make([]types.HourBucket, 0, len(inner))
		for _, b := range inner {
			b.ByModel = map[string]int64{alias: b.Count}
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Hour < buckets[j].Hour
		})
		stats.RequestsByHourByModel[alias] = buckets
	}
	return stats
}

// StatsWithComparison returns the current statistics together with
// previous-period comparison data for delta display in the UI. The
// "previous period" is everything accumulated before today (i.e. the
// most recent full days), so the delta reflects today's activity.
func (s *Store) StatsWithComparison() types.StatsWithComparison {
	// Take a read lock just like Stats() so a concurrent Append (which
	// grows/reallocates the ring slice under the write lock) cannot race
	// with these unbounded reads. Without it, the every-2s UI refresh
	// could observe a slice mid-reallocation and report wrong/zero counts.
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := types.StatsWithComparison{Stats: s.sumDays()}

	recentCutoff := time.Now().Add(-24 * time.Hour).UnixNano()
	for _, e := range s.logs {
		if e.ID == "" || e.ClientKeyLabel == "" {
			continue
		}
		if e.Timestamp >= recentCutoff {
			result.RequestsByClientKeyRecent[e.ClientKeyLabel]++
		}
	}

	// Previous period: all full days (lexical date < today).
	today := time.Now().Format(DayLayout)
	var prevLatency int64
	for _, d := range s.days {
		if d.Date >= today {
			continue
		}
		result.PrevRequests += d.TotalRequests
		result.PrevInputTokens += d.TotalInputTokens
		result.PrevOutputTokens += d.TotalOutputTokens
		prevLatency += d.LatencySumMs
	}
	if result.PrevRequests > 0 {
		result.PrevAvgLatency = prevLatency / result.PrevRequests
	}

	return result
}
