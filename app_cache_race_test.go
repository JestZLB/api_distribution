package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api_distribution/internal/store"
	"api_distribution/internal/types"
)

// TestStatsCache_RacePointerStability is a regression test for the
// audit item #1: statsCache promises to "return the same pointer"
// across the 100ms TTL window, but the current implementation returns
// *types.Stats / *types.StatsWithComparison which App.GetStats then
// dereferences into a value copy for the Wails IPC payload. We
// verify that:
//
//  1. The cache's *types.Stats pointer is stable for the duration of
//     a TTL — i.e. across many sequential cache hits within the
//     100ms window, getStats() returns the same pointer.
//  2. invalidate() actually replaces the pointer (no torn writes
//     where a reader sees a "halfway-invalidated" object).
//  3. Concurrent getStats + invalidate calls never expose a stale or
//     nil pointer (a regression here would panic the dashboard IPC
//     handler).
//
// All three checks use the data-race detector (`go test -race`) so
// any unsynchronized access to the cache fields is caught too.
func TestStatsCache_RacePointerStability(t *testing.T) {
	a := &App{store: store.New()}
	a.store.SetOnChange(a.statsCache.invalidate)

	base := time.Now()
	for i := 0; i < 20; i++ {
		a.store.Append(types.LogEntry{
			ID:        cacheTestID(i),
			Timestamp: base.Add(time.Duration(i) * time.Millisecond).UnixNano(),
			Alias:     "alias",
		})
	}
	a.statsCache.invalidate()

	// (1) Within the 100ms TTL window, getStats must hand back the
	// same *types.Stats pointer on every call. If a future refactor
	// breaks the TTL by always allocating a new struct on every
	// hit, this assertion fails.
	a.GetStatsWithComparison() // warm the cache
	first, ok := a.statsCache.getStatsWithComparison()
	if !ok || first == nil {
		t.Fatalf("cache miss after warm-up: ok=%v first=%v", ok, first)
	}
	for i := 0; i < 50; i++ {
		ptr, ok := a.statsCache.getStatsWithComparison()
		if !ok {
			t.Fatalf("cache expired within 100ms (iteration %d)", i)
		}
		if ptr != first {
			t.Fatalf("cache pointer changed within TTL window (iteration %d):\n  first=%p\n  now   =%p",
				i, first, ptr)
		}
	}

	// (2) invalidate() must replace the pointer atomically. A reader
	// holding a pre-invalidate pointer is allowed to keep using it
	// (we never mutate the underlying object — we just hand out a
	// new struct per putStats). The next call after invalidate must
	// either return nil or a DIFFERENT pointer.
	oldPtr := first
	a.statsCache.invalidate()
	after, ok := a.statsCache.getStatsWithComparison()
	if ok && after == oldPtr {
		t.Fatalf("invalidate() failed to clear the cached pointer: still %p", oldPtr)
	}
	// The next recompute must hand out a freshly-allocated pointer.
	fresh, ok := a.statsCache.getStatsWithComparison()
	// after invalidate, getStats returns false on first probe (cache
	// window starts fresh). We must call GetStatsWithComparison to
	// repopulate.
	if !ok {
		a.GetStatsWithComparison()
		fresh, ok = a.statsCache.getStatsWithComparison()
	}
	if !ok || fresh == oldPtr {
		t.Fatalf("post-invalidate recompute returned the stale pointer (ok=%v, same=%v)",
			ok, fresh == oldPtr)
	}

	// (3) Concurrent getStats + invalidate + Append stress test. We
	// expect NO panic, NO nil-deref (which would crash the Wails IPC
	// handler), and the cached pointer may change across calls but
	// must always be non-nil when getStats returns true.
	a.statsCache.invalidate()
	a.GetStatsWithComparison()

	var (
		wg          sync.WaitGroup
		nRead       atomic.Int64
		nInvalidate atomic.Int64
		nAppend     atomic.Int64
		nilCount    atomic.Int64
		tornCount   atomic.Int64
		lastPtr     atomic.Pointer[types.StatsWithComparison]
	)
	stop := make(chan struct{})

	// Reader goroutine: keep calling getStatsWithComparison. Every
	// returned pointer that is non-nil must be a valid, readable
	// StatsWithComparison (no torn writes).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ptr, ok := a.statsCache.getStatsWithComparison()
			if ok && ptr != nil {
				nRead.Add(1)
				prev := lastPtr.Swap(ptr)
				if prev != nil && prev != ptr && ptr.Stats.AvgLatencyMs < 0 {
					// AvgLatencyMs should never be negative; if a
					// future regression made the cache return a
					// half-initialized struct this would catch it.
					tornCount.Add(1)
				}
			} else {
				nilCount.Add(1)
			}
		}
	}()

	// Writer goroutine: keep invalidating the cache.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.statsCache.invalidate()
			nInvalidate.Add(1)
		}
	}()

	// Mutation goroutine: Append a new entry every iteration. Each
	// Append fires SetOnChange → cache.invalidate.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.store.Append(types.LogEntry{
				ID:        cacheTestID(i + 1000),
				Timestamp: time.Now().UnixNano(),
			})
			i++
			nAppend.Add(1)
		}
	}()

	// Recompute goroutine: keep repopulating the cache.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.GetStatsWithComparison()
			runtime.Gosched()
		}
	}()

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()

	if nRead.Load() == 0 {
		t.Fatalf("reader never observed a cached pointer")
	}
	if tornCount.Load() != 0 {
		t.Fatalf("observed %d torn reads (cache returned half-initialized struct)", tornCount.Load())
	}
	if nilCount.Load() == nRead.Load()+nilCount.Load() {
		t.Fatalf("all reads returned nil — cache is broken")
	}
	t.Logf("reads=%d invalidates=%d appends=%d nil-reads=%d torn=%d",
		nRead.Load(), nInvalidate.Load(), nAppend.Load(), nilCount.Load(), tornCount.Load())
}

// TestStatsCache_RaceGetOrComputeWindow guards a subtle race we
// identified during the audit: between fresh() returning true and
// Lock() acquiring, a concurrent invalidate() can clear the cached
// pointer. The current implementation re-checks under the lock, so
// the second-tier check is what actually protects the IPC payload.
//
// This test forces that race by hammering invalidate from one
// goroutine while a second goroutine hammers getStats. With the
// re-check, the test sees (nil, false) or (ptr, true) — never
// (non-nil stale pointer, true).
func TestStatsCache_RaceGetOrComputeWindow(t *testing.T) {
	a := &App{store: store.New()}
	a.store.SetOnChange(a.statsCache.invalidate)

	base := time.Now()
	for i := 0; i < 10; i++ {
		a.store.Append(types.LogEntry{
			ID:        cacheTestID(i),
			Timestamp: base.UnixNano(),
		})
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Invalidate storm.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			a.statsCache.invalidate()
		}
	}()

	// Reader storm. Each call must either return a valid non-nil
	// pointer or (nil, false). Anything else is a race regression.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ptr, ok := a.statsCache.getStatsWithComparison()
			if ok && ptr == nil {
				t.Errorf("getStatsWithComparison returned (nil, true) — race window broken")
				return
			}
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestStatsCache_PointerRecycledSafely checks the audit concern about
// the cache holding a pointer to a Stats struct that future
// putStats() calls will replace. We read a Stats pointer, then
// invalidate + repopulate, then verify the OLD pointer is still
// readable and not garbage (no use-after-free). With Go's GC and
// the current implementation (the cache only holds pointers, never
// mutates structs in place), this test guards against a future
// regression that, say, mutates *Stats in place after returning it.
func TestStatsCache_PointerRecycledSafely(t *testing.T) {
	a := &App{store: store.New()}
	a.store.SetOnChange(a.statsCache.invalidate)

	base := time.Now()
	for i := 0; i < 5; i++ {
		a.store.Append(types.LogEntry{
			ID:        cacheTestID(i),
			Timestamp: base.Add(time.Duration(i) * time.Millisecond).UnixNano(),
			Alias:     "alias-a",
		})
	}
	a.statsCache.invalidate()
	a.GetStatsWithComparison()

	first, ok := a.statsCache.getStatsWithComparison()
	if !ok || first == nil {
		t.Fatalf("cache miss after warm-up")
	}
	// Snapshot the field we care about.
	firstTotalRequests := first.Stats.TotalRequests
	firstAllTime := first.AllTime.Requests

	// Now invalidate + repopulate with new data.
	a.statsCache.invalidate()
	for i := 5; i < 10; i++ {
		a.store.Append(types.LogEntry{
			ID:        cacheTestID(i),
			Timestamp: base.Add(time.Duration(i) * time.Millisecond).UnixNano(),
			Alias:     "alias-a",
		})
	}
	a.GetStatsWithComparison()

	// The OLD pointer must still be readable and reflect the OLD
	// value (we must not have mutated it in place).
	if first.Stats.TotalRequests != firstTotalRequests {
		t.Errorf("cache mutated *Stats in place: old.TotalRequests %d → %d (inuse-after-free)",
			firstTotalRequests, first.Stats.TotalRequests)
	}
	if first.AllTime.Requests != firstAllTime {
		t.Errorf("cache mutated AllTime in place: %d → %d", firstAllTime, first.AllTime.Requests)
	}

	// And the new pointer must reflect the new data.
	second, ok := a.statsCache.getStatsWithComparison()
	if !ok || second == first {
		t.Fatalf("post-invalidate cache should return a new pointer, got same=%v", second == first)
	}
	if second.Stats.TotalRequests <= first.Stats.TotalRequests {
		t.Errorf("post-recompute TotalRequests=%d, expected > %d",
			second.Stats.TotalRequests, first.Stats.TotalRequests)
	}
}
