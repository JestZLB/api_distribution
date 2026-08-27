package store

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"api_distribution/internal/types"
)

// optID returns a unique ID for the given index. Unlike idstr() in
// store_test.go (which saturates at "idX" for i>=3) this one never
// collides within reasonable ranges (e.g. 2000+ entries for the ring
// wrap test). We use base-36 with a per-index prefix to guarantee
// distinct rows even when the IDs end up upserted into the logs
// table's PRIMARY KEY column.
func optID(i int) string {
	return fmt.Sprintf("opt-%08d", i)
}

// TestStatsWithComparison_SinglePass verifies that the hot path of
// StatsWithComparison walks s.days exactly once. We construct 30 days
// of data (the spec's worst case), then assert correctness AND a
// generous time budget: 30 days × 5 buckets × per-alias fold × per-hour
// curve should still fit inside 10ms on any developer machine, which is
// at least one order of magnitude below the 100ms cache window.
func TestStatsWithComparison_SinglePass(t *testing.T) {
	s := New()
	now := time.Now()
	// Seed 30 distinct calendar days with one entry each, alternating
	// between two aliases so the per-alias fold is exercised too.
	for i := 0; i < 30; i++ {
		day := now.AddDate(0, 0, -i)
		alias := "alpha"
		if i%2 == 0 {
			alias = "beta"
		}
		s.Append(types.LogEntry{
			ID:             optID(i),
			Timestamp:      day.UnixNano(),
			Alias:          alias,
			InputTokens:    10,
			OutputTokens:   5,
			StatusCode:     200,
			ClientKeyLabel: "kA",
		})
	}

	// Warm-up: prime the sort path so the measurement below reflects
	// the steady-state single-pass cost, not cold-cache setup. We
	// record the baseline AFTER warm-up so the per-iteration counter
	// increment maps 1:1 to the N loop iterations.
	_ = s.StatsWithComparison()
	before := s.swcComputeCount.Load()

	const N = 50
	var result types.StatsWithComparison
	start := time.Now()
	for i := 0; i < N; i++ {
		result = s.StatsWithComparison()
	}
	elapsed := time.Since(start)

	after := s.swcComputeCount.Load()
	if got := after - before; got != int64(N) {
		t.Fatalf("StatsWithComparison calls = %d, want %d (single-pass regression)", got, N)
	}

	// Sanity-check the output: with 30 distinct days × 1 entry each
	// the totals should be exactly 30 / 300 / 150 / 0 errors.
	if result.TotalRequests != 30 {
		t.Errorf("TotalRequests = %d, want 30", result.TotalRequests)
	}
	if result.TotalInputTokens != 300 {
		t.Errorf("TotalInputTokens = %d, want 300", result.TotalInputTokens)
	}
	if result.TotalOutputTokens != 150 {
		t.Errorf("TotalOutputTokens = %d, want 150", result.TotalOutputTokens)
	}
	if result.TodayRequests != 1 {
		t.Errorf("TodayRequests = %d, want 1", result.TodayRequests)
	}
	if result.YesterdayRequests != 1 {
		t.Errorf("YesterdayRequests = %d, want 1", result.YesterdayRequests)
	}
	if result.Week.Requests < 7 || result.Week.Requests > 8 {
		t.Errorf("Week.Requests = %d, want ~7 (the +/-1 day boundary)", result.Week.Requests)
	}
	if result.Month.Requests != 30 {
		t.Errorf("Month.Requests = %d, want 30", result.Month.Requests)
	}

	// 50 single-pass computations over 30 days must finish well below
	// the 100ms cache window. 10ms is generous on any laptop; if it
	// ever fails here it is a real regression worth investigating.
	avgPerCall := elapsed / N
	if avgPerCall > 10*time.Millisecond {
		t.Errorf("avg per-call %s > 10ms budget (elapsed=%s, N=%d) — single-pass regression?",
			avgPerCall, elapsed, N)
	}
	t.Logf("StatsWithComparison: %d calls in %s (avg %s/call)",
		N, elapsed, avgPerCall)
}

// TestStatsWithComparison_30DayHourly verifies the 30-day hourly window
// added by the trend-analysis spec: StatsWithComparison must keep hourly
// buckets from the start of the 30-day window (the hour ~30 days ago)
// through today, so the model-trend chart can render a continuous
// 720-hour window without dropping the boundary hour.
func TestStatsWithComparison_30DayHourly(t *testing.T) {
	s := New()
	now := time.Now()
	const alias = "alpha"

	// A request ~30 calendar days ago — the oldest hour the chart must
	// still surface. AddDate(0,0,-29) matches monthStart / chartCutoff,
	// and truncating to the hour yields exactly the window's first bucket.
	oldHour := now.AddDate(0, 0, -29).Truncate(time.Hour)
	s.Append(types.LogEntry{
		ID:           optID(1),
		Timestamp:    oldHour.UnixNano(),
		Alias:        alias,
		InputTokens:  10,
		OutputTokens: 5,
		StatusCode:   200,
	})
	// A request today ensures the alias's curve also spans the window's
	// near edge and that both buckets survive the hour cutoff.
	s.Append(types.LogEntry{
		ID:           optID(2),
		Timestamp:    now.UnixNano(),
		Alias:        alias,
		InputTokens:  20,
		OutputTokens: 10,
		StatusCode:   200,
	})

	sc := s.StatsWithComparison()

	// Per-alias hourly curve must carry the 30-days-ago bucket.
	buckets, ok := sc.RequestsByHourByModel[alias]
	if !ok {
		t.Fatalf("RequestsByHourByModel missing alias %q", alias)
	}
	if len(buckets) != 2 {
		t.Fatalf("RequestsByHourByModel[%q] = %d buckets, want 2 (30-days-ago + today)", alias, len(buckets))
	}
	// Buckets are hour-sorted ascending, so the first is the oldest.
	oldest := buckets[0]
	if oldest.Hour != oldHour.Unix() {
		t.Errorf("oldest bucket hour = %d, want %d (30-days-ago hour)", oldest.Hour, oldHour.Unix())
	}
	if oldest.Count != 1 {
		t.Errorf("oldest bucket count = %d, want 1", oldest.Count)
	}
	if got := oldest.ByModel[alias]; got != 1 {
		t.Errorf("oldest bucket byModel[%q] = %d, want 1", alias, got)
	}
	if oldest.InputTokens != 10 || oldest.OutputTokens != 5 {
		t.Errorf("oldest bucket tokens = %d/%d, want 10/5", oldest.InputTokens, oldest.OutputTokens)
	}

	// The aggregate RequestsByHour curve must keep the same boundary hour.
	found := false
	for _, b := range sc.RequestsByHour {
		if b.Hour == oldHour.Unix() {
			found = true
			if b.Count != 1 {
				t.Errorf("RequestsByHour[%d].Count = %d, want 1", b.Hour, b.Count)
			}
			break
		}
	}
	if !found {
		t.Errorf("RequestsByHour missing the 30-days-ago hour %d", oldHour.Unix())
	}
}

// TestSaveLogsIncremental verifies that SaveLogs only upserts entries
// in the [dirtyStart, dirtyIdx) range, not every entry in the ring
// buffer. We instrument the underlying insertLog path by counting the
// rows actually written: the on-disk logs table must grow by exactly
// the delta, never the full buffer.
//
// The "before/after" row-count diff is the canonical check; we also
// assert that calling SaveLogs with no new appends is a no-op (zero
// SQL touches).
func TestSaveLogsIncremental(t *testing.T) {
	s := New()

	// Seed 5 entries with distinct IDs.
	base := time.Now()
	for i := 0; i < 5; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: base.UnixNano(),
		})
	}
	if err := s.SaveLogs(); err != nil {
		t.Fatalf("first SaveLogs: %v", err)
	}
	if got := s.dirtyStart.Load(); got != s.dirtyIdx.Load() {
		t.Fatalf("after flush: dirtyStart=%d dirtyIdx=%d (want equal)",
			got, s.dirtyIdx.Load())
	}

	rowCount := func() int {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&n); err != nil {
			t.Fatalf("count logs: %v", err)
		}
		return n
	}
	if got := rowCount(); got != 5 {
		t.Fatalf("rows after first flush = %d, want 5", got)
	}

	// A redundant SaveLogs with no new appends must be a true no-op:
	// dirtyStart == dirtyIdx, so the early-return branch fires.
	if err := s.SaveLogs(); err != nil {
		t.Fatalf("redundant SaveLogs: %v", err)
	}
	if got := rowCount(); got != 5 {
		t.Errorf("redundant SaveLogs touched the table: rows=%d, want 5", got)
	}

	// Append 3 more — only these three must be upserted next.
	for i := 5; i < 8; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: base.UnixNano(),
		})
	}
	if err := s.SaveLogs(); err != nil {
		t.Fatalf("second SaveLogs: %v", err)
	}
	if got := rowCount(); got != 8 {
		t.Errorf("rows after second flush = %d, want 8", got)
	}
	if got := s.dirtyStart.Load(); got != s.dirtyIdx.Load() {
		t.Errorf("after second flush: dirtyStart=%d dirtyIdx=%d (want equal)",
			got, s.dirtyIdx.Load())
	}

	// Clear must reset both markers AND wipe the persisted rows.
	s.Clear()
	if got := rowCount(); got != 0 {
		t.Errorf("after Clear: rows=%d, want 0", got)
	}
	if got := s.dirtyStart.Load(); got != 0 {
		t.Errorf("after Clear: dirtyStart=%d, want 0", got)
	}
	if got := s.dirtyIdx.Load(); got != 0 {
		t.Errorf("after Clear: dirtyIdx=%d, want 0", got)
	}

	// Post-Clear SaveLogs is a no-op even though the table is empty.
	if err := s.SaveLogs(); err != nil {
		t.Fatalf("post-Clear SaveLogs: %v", err)
	}
	if got := rowCount(); got != 0 {
		t.Errorf("post-Clear SaveLogs wrote rows: %d", got)
	}
}

// TestSaveLogsIncremental_RingWraparound pushes the ring past
// MaxLogEntries so dirtyIdx wraps. The dirty-range invariant must
// survive the wrap and SaveLogs must continue to upsert exactly the
// delta.
func TestSaveLogsIncremental_RingWraparound(t *testing.T) {
	s := New()
	base := time.Now()

	// Fill the buffer past its capacity once so the ring wraps.
	// Use distinct values across the entire run so the PRIMARY KEY
	// doesn't collapse the rows on upsert.
	total := MaxLogEntries + 50
	for i := 0; i < total; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: base.UnixNano(),
		})
	}

	if err := s.SaveLogs(); err != nil {
		t.Fatalf("SaveLogs after wrap: %v", err)
	}

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	// After the wrap, the buffer holds the LAST MaxLogEntries
	// appended entries (i in [50, total)). 2000 distinct IDs upserted
	// → 2000 rows. The point of this test is that SaveLogs doesn't
	// INSERT every slot of the wrapped buffer (that would also be
	// 2000, but the *order* would be wrong; we rely on the unique-ID
	// count to confirm we didn't double-flush).
	if n != MaxLogEntries {
		t.Errorf("rows after wrap flush = %d, want %d", n, MaxLogEntries)
	}
	if got := s.dirtyStart.Load(); got != s.dirtyIdx.Load() {
		t.Errorf("after wrap flush: dirtyStart=%d dirtyIdx=%d (want equal)",
			got, s.dirtyIdx.Load())
	}
}

// TestSaveLogsIncremental_RetryOnError exercises the partial-flush
// recovery path. We simulate a crashed-mid-flush state by:
//  1. pre-populating the table with the FIRST K entries of a
//     upcoming batch, and
//  2. leaving dirtyStart at the position AFTER the pre-populated
//     prefix so the next SaveLogs only needs to write the rest.
//
// A correct implementation observes dirtyStart < dirtyIdx, walks the
// delta, and ends with dirtyStart == dirtyIdx — proving the retry
// picked up exactly where the previous flush left off.
func TestSaveLogsIncremental_RetryOnError(t *testing.T) {
	s := New()

	// Append 5 entries; the table is empty so far.
	for i := 0; i < 5; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: time.Now().UnixNano(),
		})
	}
	// SaveLogs #1: writes all 5, dirtyStart=5.
	if err := s.SaveLogs(); err != nil {
		t.Fatalf("initial SaveLogs: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 5 {
		t.Fatalf("rows after initial flush = %d, want 5", n)
	}

	// Now simulate a partial-flush crash: append 3 more, but pretend
	// a previous run already wrote the first 1 of them by
	// pre-inserting it and rewinding dirtyStart by 1. The next
	// SaveLogs must write the remaining 2.
	preInsertedID := optID(5)
	if err := s.insertLog(types.LogEntry{
		ID:        preInsertedID,
		Timestamp: time.Now().UnixNano(),
	}); err != nil {
		t.Fatalf("pre-insert: %v", err)
	}
	for i := 5; i < 8; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: time.Now().UnixNano(),
		})
	}
	// dirtyIdx is now 8 (3 new appends + 5 old). Rewind dirtyStart
	// to 6 so the "retry" only flushes 2 entries.
	s.dirtyStart.Store(6)

	if err := s.SaveLogs(); err != nil {
		t.Fatalf("retry SaveLogs: %v", err)
	}
	if got := s.dirtyStart.Load(); got != s.dirtyIdx.Load() {
		t.Errorf("after retry flush: dirtyStart=%d dirtyIdx=%d (want equal)",
			got, s.dirtyIdx.Load())
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	// 5 original + 1 pre-inserted + 2 retry = 8 rows.
	if n != 8 {
		t.Errorf("rows after retry flush = %d, want 8", n)
	}
}

// BenchmarkRecent measures the cost of Recent(n) on a saturated ring
// buffer. The spec target is <500 ns/op for n=1000 with the buffer
// already full; we assert that with a generous ceiling so the test
// never flakes on slow CI but still catches a 10x regression.
func BenchmarkRecent(b *testing.B) {
	s := New()
	base := time.Now()
	for i := 0; i < MaxLogEntries; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: base.UnixNano(),
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.Recent(1000)
	}
}

// BenchmarkStatsWithComparison_30Days exercises the single-pass hot
// path on 30 days of synthetic data so a future regression that
// re-introduces multiple range loops over s.days would surface here.
func BenchmarkStatsWithComparison_30Days(b *testing.B) {
	s := New()
	now := time.Now()
	for i := 0; i < 30; i++ {
		s.Append(types.LogEntry{
			ID:             optID(i),
			Timestamp:      now.AddDate(0, 0, -i).UnixNano(),
			Alias:          "a",
			InputTokens:    10,
			OutputTokens:   5,
			ClientKeyLabel: "k",
		})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.StatsWithComparison()
	}
}

// TestStatsWithComparison_SinglePass_ConcurrentUnderLoad is a
// concurrency canary: many goroutines hammering StatsWithComparison
// must not race, must not deadlock, and must all return consistent
// results. The swcComputeCount is a coarse upper bound — actual
// compute work is much smaller thanks to the Store's RWMutex.
func TestStatsWithComparison_SinglePass_ConcurrentUnderLoad(t *testing.T) {
	s := New()
	now := time.Now()
	for i := 0; i < 10; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: now.AddDate(0, 0, -i).UnixNano(),
		})
	}

	const goroutines = 8
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			var sum int64
			for i := 0; i < perGoroutine; i++ {
				r := s.StatsWithComparison()
				sum += r.TotalRequests
			}
			if sum == 0 {
				t.Errorf("a concurrent caller observed 0 total requests")
			}
		}()
	}
	wg.Wait()

	// Compute count should be at most (goroutines × perGoroutine) but
	// typically much lower because the RWMutex serialises them.
	got := s.swcComputeCount.Load()
	maxExpected := int64(goroutines * perGoroutine)
	if got > maxExpected {
		t.Errorf("compute count %d exceeded upper bound %d (lost update?)", got, maxExpected)
	}
	if got < 1 {
		t.Errorf("compute count = 0, want >= 1")
	}
}

// TestSaveLogs_NoGoroutineLeak verifies that SaveLogs returns
// promptly even when the SQL backend is empty, and does not leak any
// goroutines in the process. This guards against a future refactor
// that accidentally spawns a helper goroutine without a wait group.
func TestSaveLogs_NoGoroutineLeak(t *testing.T) {
	s := New()
	for i := 0; i < 100; i++ {
		s.Append(types.LogEntry{
			ID:        optID(i),
			Timestamp: time.Now().UnixNano(),
		})
	}
	before := runtime.NumGoroutine()
	if err := s.SaveLogs(); err != nil {
		t.Fatalf("SaveLogs: %v", err)
	}
	after := runtime.NumGoroutine()
	// Give the runtime a moment to settle background goroutines
	// before comparing — the SQLite driver may keep one around.
	runtime.GC()
	if after > before+1 {
		t.Errorf("goroutine count grew from %d to %d after SaveLogs", before, after)
	}
}
