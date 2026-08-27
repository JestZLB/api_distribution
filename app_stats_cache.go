package main

import (
	"sync"
	"sync/atomic"
	"time"

	"api_distribution/internal/types"
)

// statsCacheTTL bounds how long a freshly computed Stats /
// StatsWithComparison result can be reused across concurrent calls.
// 100ms is short enough that the dashboard's 2s heartbeat never
// observes a stale view (worst case: one extra snapshot per cache
// window), and long enough to absorb the burst of 4-6 simultaneous
// Wails IPC calls that arrive when the frontend kicks off its
// dashboard render + model-trend + recent-traffic fetches in one go.
const statsCacheTTL = 100 * time.Millisecond

// statsCache is a tiny single-entry TTL cache shared by App.GetStats
// and App.GetStatsWithComparison. It is intentionally NOT safe for high
// concurrency beyond what an `atomic.Int64` + sync.Mutex provides:
// stats computation already holds the store's RWMutex, so the cache's
// own lock is held only long enough to copy two pointers and a
// timestamp.
//
// Invalidations come from two sources:
//
//   - On every Store mutation (Append / Clear / LoadLogs /
//     PurgeOlderThan) the store calls invalidateStatsCache via the
//     SetOnChange hook installed in NewApp.
//   - On TTL expiry a fresh computation is forced; the cache itself
//     does not run a timer, it just compares time.Now() at lookup.
//
// Because both Stats and StatsWithComparison return value types, the
// cache stores pointers to freshly-allocated structs so the cached
// pointer is the SAME object the caller will receive (per spec:
// "其余命中缓存返回相同指针").
type statsCache struct {
	mu      sync.Mutex
	expires atomic.Int64 // unix nano of expiry; 0 means empty

	stats *types.Stats
	swc   *types.StatsWithComparison
}

// fresh reports whether the cache holds a non-expired entry. Holds no
// lock; safe to call on the hot path because the only state read is
// the atomic expires timestamp.
func (c *statsCache) fresh() bool {
	exp := c.expires.Load()
	if exp == 0 {
		return false
	}
	return time.Now().UnixNano() < exp
}

// getStats returns the cached *Stats and true if it is still fresh,
// otherwise nil/false. Holds c.mu only long enough to read the pointer.
func (c *statsCache) getStats() (*types.Stats, bool) {
	if !c.fresh() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check under the lock: a concurrent invalidation might have
	// cleared the pointer between fresh() and Lock().
	if c.stats == nil || !c.fresh() {
		return nil, false
	}
	return c.stats, true
}

// getStatsWithComparison mirrors getStats for the comparison variant.
func (c *statsCache) getStatsWithComparison() (*types.StatsWithComparison, bool) {
	if !c.fresh() {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.swc == nil || !c.fresh() {
		return nil, false
	}
	return c.swc, true
}

// putStats stores a fresh *Stats and extends the TTL window. Caller
// already holds (or is about to publish) the value; we just keep a
// pointer and let the cache window elapse naturally.
func (c *statsCache) putStats(s *types.Stats) {
	c.mu.Lock()
	c.stats = s
	c.mu.Unlock()
	c.expires.Store(time.Now().Add(statsCacheTTL).UnixNano())
}

// putStatsWithComparison mirrors putStats for the comparison variant.
func (c *statsCache) putStatsWithComparison(s *types.StatsWithComparison) {
	c.mu.Lock()
	c.swc = s
	c.mu.Unlock()
	c.expires.Store(time.Now().Add(statsCacheTTL).UnixNano())
}

// invalidate clears both cached pointers and zeroes the expiry so the
// next lookup forces a recompute. Called from Store.SetOnChange after
// every Append / Clear / PurgeOlderThan / LoadLogs.
func (c *statsCache) invalidate() {
	c.mu.Lock()
	c.stats = nil
	c.swc = nil
	c.mu.Unlock()
	c.expires.Store(0)
}