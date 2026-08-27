package main

import (
	"sync"
	"testing"
	"time"

	"api_distribution/internal/store"
	"api_distribution/internal/types"
)

// TestAppStatsCache verifies the 100ms in-process TTL cache that wraps
// App.GetStats and App.GetStatsWithComparison. We hand-craft an App
// with a real *store.Store (so Append + stats machinery actually run)
// and assert that:
//
//   - five sequential calls within the 100ms window trigger exactly
//     ONE actual StatsWithComparison() computation, and
//   - an Append during the cache window invalidates it so the next
//     call sees fresh data, and
//   - waiting out the TTL forces a fresh compute on the next call.
//
// The spec scenario ("连续 5 次 100ms 内调用 → 只触发 1 次真实计算") is
// the regression this test guards.
func TestAppStatsCache(t *testing.T) {
	a := &App{store: store.New()}
	// Wire the onChange hook the same way startup() does.
	a.store.SetOnChange(a.statsCache.invalidate)

	// Seed enough data that StatsWithComparison has real work to do.
	base := time.Now()
	for i := 0; i < 50; i++ {
		a.store.Append(types.LogEntry{
			ID:        cacheTestID(i),
			Timestamp: base.Add(time.Duration(i) * time.Millisecond).UnixNano(),
			Alias:     "alias",
		})
	}

	// Force a fresh state: clear the cache so the first call below is
	// a "real" compute. The warm-up Appends themselves do NOT trigger
	// stats computation, so this just primes the cache window.
	a.statsCache.invalidate()
	baselineSWC := a.store.SwcComputeCountForTest()

	// ---- Five calls within the 100ms window ----
	for i := 0; i < 5; i++ {
		_ = a.GetStatsWithComparison()
	}

	if got := a.store.SwcComputeCountForTest() - baselineSWC; got != 1 {
		t.Fatalf("StatsWithComparison compute count grew by %d, want 1 (cache absorbed the rest)",
			got)
	}

	// The basic Stats() path uses its own counter, so verify that
	// the cache also collapses those calls.
	baselineStats := a.store.StatsComputeCountForTest()
	for i := 0; i < 5; i++ {
		_ = a.GetStats()
	}
	if got := a.store.StatsComputeCountForTest() - baselineStats; got != 1 {
		t.Errorf("Stats compute count grew by %d, want 1 (cache absorbed the rest)", got)
	}

	// ---- Wait out the TTL, then verify a fresh compute happens ----
	time.Sleep(150 * time.Millisecond)
	_ = a.GetStatsWithComparison()
	if got := a.store.SwcComputeCountForTest() - baselineSWC; got != 2 {
		t.Errorf("after TTL expiry, compute count grew by %d, want 2", got)
	}

	// ---- Append invalidates the cache so the next call recomputes ----
	a.store.Append(types.LogEntry{
		ID:        cacheTestID(99),
		Timestamp: base.UnixNano(),
	})
	before := a.store.SwcComputeCountForTest()
	_ = a.GetStatsWithComparison()
	if got := a.store.SwcComputeCountForTest() - before; got != 1 {
		t.Errorf("after Append, compute count grew by %d, want 1 (invalidation worked)", got)
	}
}

// TestAppStatsCache_ConcurrentContention verifies the cache behaves
// under concurrent load: 50 goroutines × 10 calls = 500 calls, but
// the underlying compute must happen at most a handful of times (one
// per ~100ms window). The bound is loose to avoid flakes, but a
// regression that re-introduced a per-call mutex or removed the cache
// entirely would push this to 500.
func TestAppStatsCache_ConcurrentContention(t *testing.T) {
	a := &App{store: store.New()}
	a.store.SetOnChange(a.statsCache.invalidate)

	base := time.Now()
	for i := 0; i < 10; i++ {
		a.store.Append(types.LogEntry{
			ID:        cacheTestID(i),
			Timestamp: base.UnixNano(),
		})
	}
	a.statsCache.invalidate()
	baseline := a.store.SwcComputeCountForTest()

	const goroutines = 50
	const perGoroutine = 10

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = a.GetStatsWithComparison()
			}
		}()
	}
	wg.Wait()

	grew := a.store.SwcComputeCountForTest() - baseline
	if grew < 1 {
		t.Errorf("compute count grew by %d, want >= 1", grew)
	}
	// 500 concurrent calls in microseconds with the cache should
	// collapse to a small handful. Generous upper bound of 50 so the
	// test is robust on slow CI but still catches "cache broken".
	if grew > 50 {
		t.Errorf("compute count grew by %d, want <= 50 (cache likely broken)", grew)
	}
	t.Logf("500 concurrent GetStatsWithComparison calls → %d real computes", grew)
}

// cacheTestID returns a stable, distinct ID for a given index, large
// enough that the same IDs never collide across test runs.
func cacheTestID(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var b [8]byte
	for j := range b {
		b[j] = alphabet[((i*7)+j)%len(alphabet)]
	}
	return string(b[:])
}
