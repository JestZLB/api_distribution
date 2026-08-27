package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"api_distribution/internal/types"
)

// TestAppLifecycle_RestartKeepsData simulates the full Wails app
// lifecycle end to end with a real file-backed SQLite database:
//
//	NewApp → startup (load) → Append → shutdown (save)
//	  → NewApp (same dir) → startup (load) → assert data present
//
// This is the closest approximation to "restart the desktop app" that
// we can run headlessly. If the symptom is real, one of the two
// passes (the first shutdown flush, or the second startup load) will
// drop the data and this test fails.
func TestAppLifecycle_RestartKeepsData(t *testing.T) {
	dir, err := os.MkdirTemp("", "app-lifecycle-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	// Cleanup is best-effort: Windows / the SQLite driver may briefly
	// hold the file handle even after db.Close(), so do NOT fail the
	// test on a cleanup error (we only care about the assertions).
	defer func() {
		time.Sleep(300 * time.Millisecond)
		_ = os.RemoveAll(dir)
	}()

	// ---- First process ----
	app1 := NewApp(dir)
	// startup() needs a context and auto-starts the proxy; we only
	// need the load half, so run the load manually (startup also
	// starts tray/autostart which are side-effectful on Windows CI).
	app1.store.SetOnChange(nil) // decouple stats cache
	app1.cfgMgr.Load()
	if err := app1.store.LoadLogs(); err != nil {
		t.Fatalf("app1 LoadLogs: %v", err)
	}
	if err := app1.store.LoadAllDaily(); err != nil {
		t.Fatalf("app1 LoadAllDaily: %v", err)
	}

	base := time.Now()
	for i := 0; i < 4; i++ {
		app1.store.Append(types.LogEntry{
			ID:             "app-log-" + string(rune('a'+i)),
			Timestamp:      base.Add(-time.Duration(i) * 24 * time.Hour).UnixNano(),
			Alias:          "trend-alias",
			InputTokens:    10,
			OutputTokens:   5,
			StatusCode:     200,
			ClientKeyLabel: "k1",
		})
	}

	// Graceful shutdown — same as shutdown() does at the end.
	if err := app1.store.SaveLogs(); err != nil {
		t.Fatalf("app1 SaveLogs: %v", err)
	}
	if err := app1.store.SaveDaily(app1.cfgMgr.Get().LogRetention); err != nil {
		t.Fatalf("app1 SaveDaily: %v", err)
	}

	// ---- Second process (restart) ----
	app2 := NewApp(dir)
	app2.store.SetOnChange(nil)
	if err := app2.store.LoadLogs(); err != nil {
		t.Fatalf("app2 LoadLogs: %v", err)
	}
	if err := app2.store.LoadAllDaily(); err != nil {
		t.Fatalf("app2 LoadAllDaily: %v", err)
	}

	logs := app2.store.Recent(10)
	if len(logs) != 4 {
		t.Fatalf("APP BUG: after restart got %d logs, want 4 (data wiped across app restart)", len(logs))
	}
	stats := app2.store.Stats()
	if stats.TotalRequests != 4 {
		t.Errorf("APP BUG: after restart TotalRequests = %d, want 4", stats.TotalRequests)
	}
	if stats.TotalInputTokens != 40 || stats.TotalOutputTokens != 20 {
		t.Errorf("after restart tokens = %d/%d, want 40/20", stats.TotalInputTokens, stats.TotalOutputTokens)
	}
	if stats.RequestsByClientKey["k1"] != 4 {
		t.Errorf("RequestsByClientKey[k1] = %d, want 4", stats.RequestsByClientKey["k1"])
	}

	// Per-alias hourly chart must survive too — this feeds the trend
	// charts the user is looking at when they notice the reset.
	if buckets, ok := stats.RequestsByHourByModel["trend-alias"]; !ok || len(buckets) != 4 {
		t.Errorf("RequestsByHourByModel[trend-alias] after restart = %v (want 4 buckets)", stats.RequestsByHourByModel["trend-alias"])
	}
	app2.store.Close()                            // release the handle so TempDir cleanup works
	_ = filepath.Join(dir, "api_distribution.db") // DBPath is exercised by NewApp
}
