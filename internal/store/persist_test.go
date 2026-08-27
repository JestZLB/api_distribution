package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api_distribution/internal/types"
)

// TestSaveLogs_DoesNotBlockAppend verifies that a long-running SaveLogs
// (SQL upserts happen outside the store mutex) does not starve
// concurrent Append calls. We run the saver on its own goroutine for a
// fixed window while a second goroutine hammers Append; we then assert
// that Append made meaningful progress — it would have made zero
// progress under the old "lock the whole function" design.
func TestSaveLogs_DoesNotBlockAppend(t *testing.T) {
	s := New()

	const window = 200 * time.Millisecond

	// Pre-seed so SaveLogs has real rows to write.
	base := time.Now()
	for i := 0; i < 500; i++ {
		s.Append(types.LogEntry{
			ID:        "seed",
			Timestamp: base.UnixNano(),
			Alias:     "seed-alias",
		})
	}

	var (
		appendCount int64
		saveCount   int64
		saveErr     atomic.Value
		wg          sync.WaitGroup
		stop        = make(chan struct{})
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := s.SaveLogs(); err != nil {
				saveErr.Store(err)
			}
			atomic.AddInt64(&saveCount, 1)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		now := time.Now()
		for {
			select {
			case <-stop:
				return
			default:
			}
			s.Append(types.LogEntry{
				ID:        "live",
				Timestamp: now.UnixNano(),
				Alias:     "live-alias",
			})
			atomic.AddInt64(&appendCount, 1)
		}
	}()

	time.Sleep(window)
	close(stop)
	wg.Wait()

	if err, ok := saveErr.Load().(error); ok && err != nil {
		t.Fatalf("SaveLogs returned error: %v", err)
	}

	t.Logf("saveCount=%d appendCount=%d", saveCount, appendCount)

	if saveCount < 1 {
		t.Fatalf("SaveLogs was never invoked (saveCount=%d)", saveCount)
	}
	if appendCount < 100 {
		t.Errorf("Append made only %d calls during %s window of SaveLogs — "+
			"the save appears to be blocking Append", appendCount, window)
	}

	recent := s.Recent(MaxLogEntries)
	if len(recent) != MaxLogEntries {
		t.Errorf("Recent = %d, want %d (buffer should be full)", len(recent), MaxLogEntries)
	}
	for _, e := range recent {
		if e.ID != "live" {
			t.Errorf("found stale entry %q in saturated buffer", e.ID)
			break
		}
	}
}