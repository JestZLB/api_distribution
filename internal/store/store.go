// Package store maintains an in-memory ring buffer of recent
// log entries plus aggregated request statistics.
package store

import (
	"database/sql"
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

	// dirtyIdx / dirtyStart form the incremental-persistence marker
	// for the ring buffer. Both are monotonic seq counters (the N-th
	// append is assigned seq N, with ring slot N % MaxLogEntries).
	// The unflushed delta is [dirtyStart, dirtyIdx). On every
	// successful SaveLogs we advance dirtyStart to dirtyIdx; on
	// Append we advance dirtyIdx. Atomic semantics let SaveLogs
	// snapshot the range without holding s.mu.
	dirtyIdx   atomic.Int64
	dirtyStart atomic.Int64

	// onChange is invoked (without holding s.mu) after every mutation
	// that changes the externally observable state (Append, Clear,
	// PurgeOlderThan). It is the hook App uses to invalidate the
	// 100ms stats cache so the next heartbeat sees fresh data.
	// nil-safe — callers without a cache just leave it unset.
	onChange atomic.Pointer[func()]

	// db is the SQLite persistence backend. All persisted state (per-day
	// aggregates + the log ring buffer) lives here, replacing the old
	// stats/*.json + logs.json files. It is nil only if the store was
	// constructed without a database (not the case for New/Open).
	db *sql.DB

	// days accumulates per-calendar-day statistics. It is the source of
	// truth for the dashboard totals: every proxied request is folded
	// into today's DailyAgg, then persisted to SQLite and reloaded at
	// startup so historical volume survives restarts.
	days map[string]*DailyAgg

	// dirty is set whenever a mutation (Append, Clear, PurgeOlderThan)
	// alters in-memory state that has not yet been flushed to disk.
	// persistLogsLoop reads + clears it on each tick so an idle
	// gateway does zero SQL on its 30-second heartbeat.
	dirty atomic.Bool

	// statsComputeCount tracks how many times Stats() and
	// StatsWithComparison() actually entered the heavy path (vs.
	// being served from an in-process TTL cache). It is incremented
	// at the very top of each method, BEFORE any locking, so tests
	// can prove the cache absorbed N concurrent calls. Two
	// atomics per compute is dwarfed by the sort + map traversal
	// the methods do, so this is free in production.
	statsComputeCount atomic.Int64
	swcComputeCount   atomic.Int64
}

// New creates an empty Store backed by an in-memory SQLite database.
// Tests use this; the app uses Open with a file-backed database.
func New() *Store {
	st, err := openDB(":memory:")
	if err != nil {
		// The pure-Go driver backing an in-memory database should never
		// fail to open; a failure here means the build is broken.
		panic("store: open in-memory db: " + err.Error())
	}
	return st
}

// SetOnChange registers a callback invoked after every mutation that
// changes the externally observable state. Pass nil to clear the hook.
// The callback runs WITHOUT s.mu held so it is safe to take other
// locks or perform I/O.
func (s *Store) SetOnChange(fn func()) {
	if fn == nil {
		s.onChange.Store(nil)
		return
	}
	s.onChange.Store(&fn)
}

// fireOnChange invokes the registered onChange hook (if any). Must NOT
// be called while holding s.mu.
func (s *Store) fireOnChange() {
	if fn := s.onChange.Load(); fn != nil {
		(*fn)()
	}
}

// Append adds a new log entry to the ring buffer and folds it into the
// current calendar day's aggregated statistics.
//
// dirtyIdx is the monotonic seq counter (1-indexed) for the total
// number of appends performed since startup. After this call returns
// it equals the seq of the just-appended entry plus one; the ring slot
// that entry lives in is (dirtyIdx-1) % MaxLogEntries. SaveLogs reads
// dirtyIdx - dirtyStart to know how many entries are unflushed.
func (s *Store) Append(e types.LogEntry) {
	s.mu.Lock()
	// Increment dirtyIdx FIRST so any concurrent reader sees a high
	// water mark that already includes this append, and we can derive
	// the ring slot as (dirtyIdx-1) % MaxLogEntries without races.
	s.dirtyIdx.Add(1)

	if len(s.logs) < MaxLogEntries {
		s.logs = append(s.logs, e)
	} else {
		s.logs[s.idx] = e
	}
	s.idx = (s.idx + 1) % MaxLogEntries

	s.getOrCreateDay(dateKey(e.Timestamp)).add(e)
	s.dirty.Store(true)
	s.mu.Unlock()

	s.fireOnChange()
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
//
// Performance: the result slice is preallocated to min(n, len(s.logs))
// so it never grows; the loop runs O(n) without touching any allocation
// path. len(s.logs) is captured into a local once so the bound check is
// a register load rather than a per-iteration slice header read.
func (s *Store) Recent(n int) []types.LogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	l := len(s.logs)
	if n <= 0 || n > l {
		n = l
	}
	out := make([]types.LogEntry, 0, n)
	if l == 0 {
		return out
	}

	for i := 0; i < n; i++ {
		idx := (s.idx - 1 - i + MaxLogEntries) % MaxLogEntries
		if idx < 0 || idx >= l {
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
// Hourly buckets are preserved. The dirty-range markers are reset to
// 0 so a subsequent SaveLogs treats the (now-empty) buffer as fully
// flushed, and the persisted logs table is wiped so a previous run's
// rows do not come back on the next LoadLogs.
func (s *Store) Clear() {
	s.mu.Lock()
	s.logs = s.logs[:0]
	s.idx = 0
	s.days = make(map[string]*DailyAgg)
	s.dirty.Store(true)
	s.dirtyIdx.Store(0)
	s.dirtyStart.Store(0)
	s.mu.Unlock()

	// Persist-side wipe happens outside the lock so a slow disk does
	// not block concurrent Append. ClearPersisted is idempotent.
	_, _ = s.db.Exec(`DELETE FROM logs`)

	s.fireOnChange()
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
	s.statsComputeCount.Add(1)
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
//
// Optimisation: snapshot the s.days values, sort once, then walk the
// sorted slice exactly once. The previous implementation re-ranged
// s.days as a map (random order) which is fine semantically but
// defeats CPU prefetch on hot data; the sorted walk also makes the
// caller naturally cache-friendly.
func (s *Store) sumDays() types.Stats {
	stats := types.Stats{
		RequestsByModel:           map[string]int64{},
		RequestsByProvider:        map[string]int64{},
		RequestsByClientKey:       map[string]int64{},
		RequestsByClientKeyRecent: map[string]int64{},
		RequestsByHour:            []types.HourBucket{},
		RequestsByHourByModel:     map[string][]types.HourBucket{},
	}

	dailySnapshot := snapshotDaysSorted(s.days)
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

	for _, d := range dailySnapshot {
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
			b.InputTokens += a.InputTokens
			b.OutputTokens += a.OutputTokens
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
				// Accumulate this alias's token usage for the hour.
				// Reading the per-alias maps is nil-safe: a legacy
				// daily file that predates token fields yields 0.
				ab.InputTokens += a.ByModelInputTokens[alias]
				ab.OutputTokens += a.ByModelOutputTokens[alias]
				if ab.ByModelTokens == nil {
					ab.ByModelTokens = map[string]int64{}
				}
				ab.ByModelTokens[alias] += a.ByModelTokens[alias]
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
	// without extra aggregation. ByModelTokens mirrors that pattern for
	// token volume: `data[alias][i].byModelTokens[alias]` is the alias's
	// total (input+output) tokens for that hour.
	for alias, inner := range byHourByModel {
		buckets := make([]types.HourBucket, 0, len(inner))
		for _, b := range inner {
			b.ByModel = map[string]int64{alias: b.Count}
			b.ByModelTokens = map[string]int64{alias: b.InputTokens + b.OutputTokens}
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Hour < buckets[j].Hour
		})
		stats.RequestsByHourByModel[alias] = buckets
	}
	return stats
}

// snapshotDaysSorted returns the days map's values as a slice sorted
// by date string ascending. The sort is cheap (keyset <= 30) and lets
// downstream traversals stay cache-friendly. Caller does not need to
// hold s.mu; the result is independent of the underlying map once the
// snapshot is captured. We snapshot defensively (map values copied by
// reference are intentional — DailyAgg is only mutated under s.mu).
func snapshotDaysSorted(days map[string]*DailyAgg) []*DailyAgg {
	out := make([]*DailyAgg, 0, len(days))
	for _, d := range days {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Date < out[j].Date
	})
	return out
}

// StatsWithComparison returns the current statistics together with
// day-over-day comparison data for delta display in the UI. Today*
// hold the current calendar day's aggregates, Yesterday* the previous
// full calendar day's, so the dashboard can show "today vs yesterday"
// with an up/down percentage. Prev* mirror Yesterday* for backward
// compatibility.
//
// Performance: this is the single hot path the dashboard hits every
// 2s. Instead of the legacy `sumDays() + separate today/yesterday
// loop + separate rolling-window loop` (three iterations of s.days),
// it does ONE sorted snapshot of s.days and a SINGLE linear pass that
// dispatches each day into every bucket (today/yesterday/allTime/
// week/prevWeek/month/prevMonth) plus the hourly curve that
// sumDays() used to build. The result matches the previous output
// field-for-field (regression-covered by the existing TestStats and
// TestStatsWithComparison_* tests).
func (s *Store) StatsWithComparison() types.StatsWithComparison {
	s.swcComputeCount.Add(1)
	// Take a read lock just like Stats() so a concurrent Append (which
	// grows/reallocates the ring slice under the write lock) cannot race
	// with these unbounded reads. Without it, the every-2s UI refresh
	// could observe a slice mid-reallocation and report wrong/zero counts.
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := types.StatsWithComparison{
		Stats: types.Stats{
			RequestsByModel:           map[string]int64{},
			RequestsByProvider:        map[string]int64{},
			RequestsByClientKey:       map[string]int64{},
			RequestsByClientKeyRecent: map[string]int64{},
			RequestsByHour:            []types.HourBucket{},
			RequestsByHourByModel:     map[string][]types.HourBucket{},
		},
	}

	now := time.Now()
	today := now.Format(DayLayout)
	yesterday := now.Add(-24 * time.Hour).Format(DayLayout)
	// chartCutoff keeps the last 30 calendar days (720 hours) of hourly
	// buckets so the model-trend chart can render a 30-day window. It
	// mirrors monthStart's AddDate(0,0,-29) baseline so the hourly curve
	// and the Month rolling window agree on coverage. Missing hours are
	// zero-filled by the frontend.
	chartCutoff := now.AddDate(0, 0, -29).Truncate(time.Hour).Unix()
	weekStart := now.AddDate(0, 0, -6).Format(DayLayout) // last 7 days incl today
	prevWeekStart := now.AddDate(0, 0, -13).Format(DayLayout)
	monthStart := now.AddDate(0, 0, -29).Format(DayLayout) // last 30 days incl today
	prevMonthStart := now.AddDate(0, 0, -59).Format(DayLayout)

	recentCutoff := now.Add(-24 * time.Hour).UnixNano()

	var (
		totalLatency     int64
		todayLatency     int64
		yesterdayLatency int64
	)
	byHour := make(map[int64]types.HourBucket)
	// Per-alias per-hour accumulation (see sumDays for semantics).
	byHourByModel := make(map[string]map[int64]types.HourBucket)

	dailySnapshot := snapshotDaysSorted(s.days)

	// ----- Single traversal over s.days -----
	// Every DailyAgg is folded into:
	//   - the base Stats totals (sumDays work),
	//   - the today or yesterday bucket (if the date matches),
	//   - the rolling-window PeriodStats (allTime + week/prevWeek
	//     and month/prevMonth based on string comparison),
	//   - the per-hour chart buckets (filtered to the 30-day window),
	//   - the per-alias per-hour buckets.
	for _, d := range dailySnapshot {
		result.TotalRequests += d.TotalRequests
		result.TotalInputTokens += d.TotalInputTokens
		result.TotalOutputTokens += d.TotalOutputTokens
		totalLatency += d.LatencySumMs
		for m, c := range d.ByModel {
			result.RequestsByModel[m] += c
		}
		for p, c := range d.ByProvider {
			result.RequestsByProvider[p] += c
		}
		for k, c := range d.ByClientKey {
			result.RequestsByClientKey[k] += c
		}

		// Today vs yesterday.
		switch d.Date {
		case today:
			result.TodayRequests += d.TotalRequests
			result.TodayInputTokens += d.TotalInputTokens
			result.TodayOutputTokens += d.TotalOutputTokens
			result.TodayErrors += d.Errors
			todayLatency += d.LatencySumMs
		case yesterday:
			result.YesterdayRequests += d.TotalRequests
			result.YesterdayInputTokens += d.TotalInputTokens
			result.YesterdayOutputTokens += d.TotalOutputTokens
			result.YesterdayErrors += d.Errors
			yesterdayLatency += d.LatencySumMs
		}

		// Rolling-window consumption: all-time, last 7 days, last 30
		// days, plus the previous equivalent windows. Date strings are
		// YYYY-MM-DD so lexicographic comparison is chronological.
		addPeriod(&result.AllTime, d)
		if d.Date >= weekStart {
			addPeriod(&result.Week, d)
		} else if d.Date >= prevWeekStart {
			addPeriod(&result.PrevWeek, d)
		}
		if d.Date >= monthStart {
			addPeriod(&result.Month, d)
		} else if d.Date >= prevMonthStart {
			addPeriod(&result.PrevMonth, d)
		}

		// 30-day hourly curve (sumDays still keeps a 7-day curve).
		for h, a := range d.ByHour {
			if h < chartCutoff {
				continue
			}
			b := byHour[h]
			b.Hour = h
			b.Count += a.Count
			b.Errors += a.Errors
			b.InputTokens += a.InputTokens
			b.OutputTokens += a.OutputTokens
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
				ab.InputTokens += a.ByModelInputTokens[alias]
				ab.OutputTokens += a.ByModelOutputTokens[alias]
				if ab.ByModelTokens == nil {
					ab.ByModelTokens = map[string]int64{}
				}
				ab.ByModelTokens[alias] += a.ByModelTokens[alias]
				inner[h] = ab
			}
		}
	}

	if result.TotalRequests > 0 {
		result.AvgLatencyMs = totalLatency / result.TotalRequests
	}
	if result.TodayRequests > 0 {
		result.TodayAvgLatency = todayLatency / result.TodayRequests
	}
	if result.YesterdayRequests > 0 {
		result.YesterdayAvgLatency = yesterdayLatency / result.YesterdayRequests
	}
	result.PrevRequests = result.YesterdayRequests
	result.PrevInputTokens = result.YesterdayInputTokens
	result.PrevOutputTokens = result.YesterdayOutputTokens
	result.PrevAvgLatency = result.YesterdayAvgLatency

	for _, p := range []*types.PeriodStats{
		&result.AllTime, &result.Week, &result.PrevWeek,
		&result.Month, &result.PrevMonth,
	} {
		if p.Requests > 0 {
			p.AvgLatency /= p.Requests
		}
	}

	for _, b := range byHour {
		result.RequestsByHour = append(result.RequestsByHour, b)
	}
	sort.Slice(result.RequestsByHour, func(i, j int) bool {
		return result.RequestsByHour[i].Hour < result.RequestsByHour[j].Hour
	})
	for alias, inner := range byHourByModel {
		buckets := make([]types.HourBucket, 0, len(inner))
		for _, b := range inner {
			b.ByModel = map[string]int64{alias: b.Count}
			b.ByModelTokens = map[string]int64{alias: b.InputTokens + b.OutputTokens}
			buckets = append(buckets, b)
		}
		sort.Slice(buckets, func(i, j int) bool {
			return buckets[i].Hour < buckets[j].Hour
		})
		result.RequestsByHourByModel[alias] = buckets
	}

	// Last-24h per-client-key counts come from the live log buffer
	// (s.logs), not from the per-day aggregates. This is a second pass
	// over a separate structure (the ring buffer) that the previous
	// implementation also did, so we keep it as-is.
	for _, e := range s.logs {
		if e.ID == "" || e.ClientKeyLabel == "" {
			continue
		}
		if e.Timestamp >= recentCutoff {
			result.RequestsByClientKeyRecent[e.ClientKeyLabel]++
		}
	}

	return result
}

// addPeriod folds one day's aggregates into a rolling-window
// PeriodStats. AvgLatency accumulates the latency sum and is divided by
// Requests by the caller after all days are folded.
func addPeriod(p *types.PeriodStats, d *DailyAgg) {
	p.Requests += d.TotalRequests
	p.InputTokens += d.TotalInputTokens
	p.OutputTokens += d.TotalOutputTokens
	p.Errors += d.Errors
	p.AvgLatency += d.LatencySumMs
}

// StatsComputeCountForTest returns the number of times Stats() entered
// its heavy path. It exists for the App-level cache test
// (TestAppStatsCache) which needs to prove the cache absorbed N
// concurrent calls. Production callers should never need this.
func (s *Store) StatsComputeCountForTest() int64 {
	return s.statsComputeCount.Load()
}

// SwcComputeCountForTest returns the number of times
// StatsWithComparison() entered its heavy path. Same rationale as
// StatsComputeCountForTest.
func (s *Store) SwcComputeCountForTest() int64 {
	return s.swcComputeCount.Load()
}
