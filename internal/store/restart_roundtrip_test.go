package store

import (
	"path/filepath"
	"testing"
	"time"

	"api_distribution/internal/types"
)

// TestRestartRoundTrip_RealFileDB reproduces the "every restart wipes
// the data" symptom. It is the ONLY store test that exercises the
// full production persistence chain end-to-end:
//
//	Append -> SaveLogs / SaveDaily -> close -> Open(same file) -> LoadLogs / LoadAllDaily
//
// The existing TestDailyPersistence_* tests use `copyDB` (VACUUM INTO),
// which dumps the whole SQLite file and therefore SKIPS SaveLogs /
// SaveDaily entirely — they cannot catch a broken write path.
//
// If this test fails (logs or daily stats come back empty after the
// reload), it proves the bug is BACKEND-side: the write path
// (SaveLogs incremental ring flush, or SaveDaily -> LoadAllDaily) is
// dropping data on disk.
func TestRestartRoundTrip_RealFileDB(t *testing.T) {
	// -------- First "run" (process A) --------
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	sA, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (A): %v", err)
	}

	now := time.Now()
	// 3 distinct days of traffic to exercise SaveDaily (per-date rows).
	days := []int64{now.UnixNano(), now.AddDate(0, 0, -1).UnixNano(), now.AddDate(0, 0, -2).UnixNano()}
	for i, ts := range days {
		sA.Append(types.LogEntry{
			ID:             optID(i),
			Timestamp:      ts,
			Alias:          "restart-alias",
			InputTokens:    100,
			OutputTokens:   50,
			StatusCode:     200,
			ClientKeyLabel: "kR",
		})
	}

	// Flush exactly like flushPersisted does in app.go.
	if err := sA.SaveLogs(); err != nil {
		t.Fatalf("SaveLogs (A): %v", err)
	}
	if err := sA.SaveDaily(30); err != nil {
		t.Fatalf("SaveDaily (A): %v", err)
	}
	if err := sA.db.Close(); err != nil {
		t.Fatalf("Close (A): %v", err)
	}

	// -------- Second "run" (process B — simulates restart) --------
	sB, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (B): %v", err)
	}
	defer sB.db.Close()

	if err := sB.LoadLogs(); err != nil {
		t.Fatalf("LoadLogs (B): %v", err)
	}
	if err := sB.LoadAllDaily(); err != nil {
		t.Fatalf("LoadAllDaily (B): %v", err)
	}

	// ---- Assertions: if the write path is broken, these fail ----
	logs := sB.Recent(10)
	if len(logs) != 3 {
		t.Fatalf("BUG REPRODUCED: after restart LoadLogs returned %d logs, want 3 (data wiped on the write path)", len(logs))
	}

	stats := sB.Stats()
	if stats.TotalRequests != 3 {
		t.Fatalf("BUG REPRODUCED: after restart Stats().TotalRequests = %d, want 3 (daily stats wiped)", stats.TotalRequests)
	}
	if stats.TotalInputTokens != 300 {
		t.Errorf("TotalInputTokens = %d, want 300", stats.TotalInputTokens)
	}
	if stats.RequestsByClientKey["kR"] != 3 {
		t.Errorf("RequestsByClientKey[kR] = %d, want 3", stats.RequestsByClientKey["kR"])
	}

	// Tuesday's alias must survive the per-alias hourly chart too.
	if buckets, ok := stats.RequestsByHourByModel["restart-alias"]; !ok || len(buckets) == 0 {
		t.Errorf("RequestsByHourByModel[restart-alias] empty after restart (want 1+ buckets)")
	}
}

// TestRestartRoundTrip_NewRequestsAfterRestart verifies that after a
// restart the dirty-range markers still flush NEW appends correctly —
// this is the incremental SaveLogs path working over a loaded ring
// buffer (the previous test proves loads, this one proves writes do
// not break after a load).
func TestRestartRoundTrip_NewRequestsAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "restart2.db")

	// Run A: write 2 entries, close.
	sA, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (A): %v", err)
	}
	now := time.Now()
	sA.Append(types.LogEntry{ID: optID(0), Timestamp: now.UnixNano(), Alias: "a"})
	sA.Append(types.LogEntry{ID: optID(1), Timestamp: now.UnixNano(), Alias: "a"})
	if err := sA.SaveLogs(); err != nil {
		t.Fatalf("SaveLogs (A): %v", err)
	}
	if err := sA.db.Close(); err != nil {
		t.Fatalf("Close (A): %v", err)
	}

	// Run B: load 2, append 1 more, flush, close.
	sB, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (B): %v", err)
	}
	if err := sB.LoadLogs(); err != nil {
		t.Fatalf("LoadLogs (B): %v", err)
	}
	sB.Append(types.LogEntry{ID: "post-restart-1", Timestamp: time.Now().UnixNano(), Alias: "a"})
	if err := sB.SaveLogs(); err != nil {
		t.Fatalf("SaveLogs (B): %v", err)
	}
	if err := sB.db.Close(); err != nil {
		t.Fatalf("Close (B): %v", err)
	}

	// Run C: load — expect 3 rows (2 original + 1 post-restart).
	sC, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open (C): %v", err)
	}
	defer sC.db.Close()
	if err := sC.LoadLogs(); err != nil {
		t.Fatalf("LoadLogs (C): %v", err)
	}
	logs := sC.Recent(10)
	if len(logs) != 3 {
		t.Fatalf("after second restart got %d logs, want 3 — post-restart append was lost", len(logs))
	}
	seen := map[string]bool{}
	for _, l := range logs {
		seen[l.ID] = true
	}
	if !seen["post-restart-1"] {
		t.Fatalf("post-restart append %q missing after reload: %v", "post-restart-1", logs)
	}
	for i := 0; i < 2; i++ {
		if !seen[optID(i)] {
			t.Errorf("original log %q missing after reload", optID(i))
		}
	}
}