package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"api_distribution/internal/types"
)

func idstr(i int) string {
	switch i {
	case 0:
		return "id0"
	case 1:
		return "id1"
	case 2:
		return "id2"
	}
	return "idX"
}

func TestAppend_Basic(t *testing.T) {
	s := New()
	now := time.Now()
	for i := 0; i < 5; i++ {
		s.Append(types.LogEntry{
			ID:        idstr(i),
			Timestamp: now.UnixNano(),
			Alias:     "fast",
		})
	}
	if got := len(s.Recent(10)); got != 5 {
		t.Errorf("Recent = %d, want 5", got)
	}
}

func TestAppend_ByDateAggregation(t *testing.T) {
	// Stats are aggregated per calendar day and summed across days, so
	// historical volume (even beyond the ring buffer / 7-day chart) is
	// counted in the totals.
	s := New()
	now := time.Now()
	today := now
	yesterday := now.Add(-25 * time.Hour) // outside the 24h "recent" window
	farPast := now.Add(-10 * 24 * time.Hour)

	s.Append(types.LogEntry{ID: "t1", Timestamp: today.UnixNano(), Alias: "m", ClientKeyLabel: "kA"})
	s.Append(types.LogEntry{ID: "t2", Timestamp: today.UnixNano(), ClientKeyLabel: "kA"})
	s.Append(types.LogEntry{ID: "y1", Timestamp: yesterday.UnixNano(), ClientKeyLabel: "kB"})
	s.Append(types.LogEntry{ID: "fp", Timestamp: farPast.UnixNano(), ClientKeyLabel: "kA"})

	st := s.Stats()
	if st.TotalRequests != 4 {
		t.Errorf("TotalRequests = %d, want 4", st.TotalRequests)
	}
	if st.RequestsByClientKey["kA"] != 3 {
		t.Errorf("RequestsByClientKey[kA] = %d, want 3", st.RequestsByClientKey["kA"])
	}
	if st.RequestsByClientKey["kB"] != 1 {
		t.Errorf("RequestsByClientKey[kB] = %d, want 1", st.RequestsByClientKey["kB"])
	}
	// "Recent" (last 24h) is derived from the live log buffer.
	if st.RequestsByClientKeyRecent["kA"] != 2 {
		t.Errorf("RequestsByClientKeyRecent[kA] = %d, want 2", st.RequestsByClientKeyRecent["kA"])
	}
	if st.RequestsByClientKeyRecent["kB"] != 0 {
		t.Errorf("RequestsByClientKeyRecent[kB] = %d, want 0", st.RequestsByClientKeyRecent["kB"])
	}
}

func TestAppend_HourlyWithinSameHourCollapses(t *testing.T) {
	// 10 appends at the same wall-clock minute must all land in one
	// bucket - the per-minute truncation must not fragment them.
	s := New()
	now := time.Now().Truncate(time.Hour).Add(15 * time.Minute)
	for i := 0; i < 10; i++ {
		s.Append(types.LogEntry{
			ID:        idstr(i),
			Timestamp: now.Add(time.Duration(i) * time.Second).UnixNano(),
		})
	}
	stats := s.Stats()
	if stats.TotalRequests != 10 {
		t.Errorf("TotalRequests = %d, want 10", stats.TotalRequests)
	}
	// All 10 should be in one hourly slot.
	var totalInBuckets int64
	for _, b := range stats.RequestsByHour {
		totalInBuckets += b.Count
	}
	if totalInBuckets != 10 {
		t.Errorf("sum of bucket counts = %d, want 10", totalInBuckets)
	}
}

func TestRecent_Empty(t *testing.T) {
	s := New()
	if got := s.Recent(10); len(got) != 0 {
		t.Errorf("Recent empty store = %d, want 0", len(got))
	}
}

func TestRecent_NewestFirst(t *testing.T) {
	s := New()
	base := time.Now()
	for i := 0; i < 3; i++ {
		s.Append(types.LogEntry{
			ID:        idstr(i),
			Timestamp: base.Add(time.Duration(i) * time.Millisecond).UnixNano(),
		})
	}
	got := s.Recent(3)
	if len(got) != 3 {
		t.Fatalf("Recent = %d, want 3", len(got))
	}
	// Newest first: idx==2 was added last.
	if got[0].ID != "id2" {
		t.Errorf("got[0].ID = %q, want id2", got[0].ID)
	}
}

func TestClear_ResetsLogs(t *testing.T) {
	s := New()
	base := time.Now()
	for i := 0; i < 3; i++ {
		s.Append(types.LogEntry{
			ID:        idstr(i),
			Timestamp: base.Add(time.Duration(i) * time.Millisecond).UnixNano(),
		})
	}
	s.Clear()
	if got := len(s.Recent(10)); got != 0 {
		t.Errorf("after Clear, Recent = %d, want 0", got)
	}
	// TotalRequests must drop after Clear because Stats iterates logs.
	if st := s.Stats(); st.TotalRequests != 0 {
		t.Errorf("after Clear, TotalRequests = %d, want 0", st.TotalRequests)
	}
}

func TestStats_ReportsErrors(t *testing.T) {
	s := New()
	now := time.Now()
	s.Append(types.LogEntry{ID: "ok", Timestamp: now.UnixNano(), StatusCode: 200})
	s.Append(types.LogEntry{ID: "fail", Timestamp: now.UnixNano(), StatusCode: 500})

	st := s.Stats()
	if st.TotalRequests != 2 {
		t.Errorf("TotalRequests = %d, want 2", st.TotalRequests)
	}
	// Find the bucket for now's hour and check errors.
	wantHour := now.Truncate(time.Hour).Unix()
	var found bool
	for _, b := range st.RequestsByHour {
		if b.Hour == wantHour {
			found = true
			if b.Errors < 1 {
				t.Errorf("Errors = %d, want >= 1", b.Errors)
			}
			if b.Count != 2 {
				t.Errorf("Count = %d, want 2", b.Count)
			}
		}
	}
	if !found {
		t.Errorf("no bucket recorded for current hour")
	}
}

func TestStats_RequestsByClientKey(t *testing.T) {
	s := New()
	now := time.Now()
	// Two recent entries for keyA, one recent for keyB, one old for keyA.
	s.Append(types.LogEntry{ID: "a1", Timestamp: now.UnixNano(), ClientKeyLabel: "keyA"})
	s.Append(types.LogEntry{ID: "a2", Timestamp: now.UnixNano(), ClientKeyLabel: "keyA"})
	s.Append(types.LogEntry{ID: "b1", Timestamp: now.UnixNano(), ClientKeyLabel: "keyB"})
	s.Append(types.LogEntry{ID: "a3", Timestamp: now.Add(-48 * time.Hour).UnixNano(), ClientKeyLabel: "keyA"})

	st := s.Stats()
	if st.RequestsByClientKey["keyA"] != 3 {
		t.Errorf("RequestsByClientKey[keyA] = %d, want 3", st.RequestsByClientKey["keyA"])
	}
	if st.RequestsByClientKey["keyB"] != 1 {
		t.Errorf("RequestsByClientKey[keyB] = %d, want 1", st.RequestsByClientKey["keyB"])
	}
	// Recent (last 24h): keyA has 2, keyB has 1.
	if st.RequestsByClientKeyRecent["keyA"] != 2 {
		t.Errorf("RequestsByClientKeyRecent[keyA] = %d, want 2", st.RequestsByClientKeyRecent["keyA"])
	}
	if st.RequestsByClientKeyRecent["keyB"] != 1 {
		t.Errorf("RequestsByClientKeyRecent[keyB] = %d, want 1", st.RequestsByClientKeyRecent["keyB"])
	}
}

func TestDailyPersistence_RoundTrip(t *testing.T) {
	s := New()
	now := time.Now()
	for i := 0; i < 3; i++ {
		s.Append(types.LogEntry{
			ID:             idstr(i),
			Timestamp:      now.UnixNano(),
			ClientKeyLabel: "keyA",
			Alias:          "fast",
		})
	}

	dir := t.TempDir()
	if err := s.SaveDaily(dir, 0); err != nil {
		t.Fatalf("SaveDaily: %v", err)
	}

	// A fresh store must recover the same totals from the per-date files.
	s2 := New()
	if err := s2.LoadAllDaily(dir); err != nil {
		t.Fatalf("LoadAllDaily: %v", err)
	}
	st := s2.Stats()
	if st.TotalRequests != 3 {
		t.Errorf("TotalRequests after reload = %d, want 3", st.TotalRequests)
	}
	if st.RequestsByClientKey["keyA"] != 3 {
		t.Errorf("RequestsByClientKey[keyA] after reload = %d, want 3", st.RequestsByClientKey["keyA"])
	}
	if st.RequestsByModel["fast"] != 3 {
		t.Errorf("RequestsByModel[fast] after reload = %d, want 3", st.RequestsByModel["fast"])
	}
}

func TestDailyRetention_PrunesOldDays(t *testing.T) {
	s := New()
	now := time.Now()
	s.Append(types.LogEntry{ID: "new", Timestamp: now.UnixNano()})
	old := now.Add(-30 * 24 * time.Hour)
	s.Append(types.LogEntry{ID: "old", Timestamp: old.UnixNano()})

	dir := t.TempDir()
	if err := s.SaveDaily(dir, 7); err != nil {
		t.Fatalf("SaveDaily: %v", err)
	}

	if st := s.Stats(); st.TotalRequests != 1 {
		t.Errorf("TotalRequests after pruning = %d, want 1", st.TotalRequests)
	}
	if _, err := os.Stat(DailyPath(dir, dateKey(old.UnixNano()))); !os.IsNotExist(err) {
		t.Errorf("old day file was not pruned (err=%v)", err)
	}
}

func TestClearDailyFiles_RemovesOnDiskAggregates(t *testing.T) {
	s := New()
	now := time.Now()
	s.Append(types.LogEntry{ID: "x", Timestamp: now.UnixNano(), ClientKeyLabel: "k"})
	dir := t.TempDir()
	if err := s.SaveDaily(dir, 0); err != nil {
		t.Fatalf("SaveDaily: %v", err)
	}
	ClearDailyFiles(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected stats dir empty after ClearDailyFiles, got %d entries", len(entries))
	}
}

// TestStats_RequestsByHourByModel_SplitByAlias verifies that
// RequestsByHourByModel surfaces a per-alias hourly curve and that two
// distinct aliases produce two top-level entries. Each emitted per-alias
// slice contains exactly one bucket for the current hour, and the inner
// ByModel map is keyed by the alias itself.
func TestStats_RequestsByHourByModel_SplitByAlias(t *testing.T) {
	s := New()
	now := time.Now()
	s.Append(types.LogEntry{ID: "g1", Timestamp: now.UnixNano(), Alias: "gpt-4o", StatusCode: 200})
	s.Append(types.LogEntry{ID: "g2", Timestamp: now.UnixNano(), Alias: "gpt-4o", StatusCode: 200})
	s.Append(types.LogEntry{ID: "c1", Timestamp: now.UnixNano(), Alias: "claude", StatusCode: 200})
	s.Append(types.LogEntry{ID: "c2", Timestamp: now.UnixNano(), Alias: "claude", StatusCode: 500})

	stats := s.Stats()
	if stats.RequestsByHourByModel == nil {
		t.Fatal("RequestsByHourByModel is nil")
	}
	if got := len(stats.RequestsByHourByModel); got != 2 {
		t.Fatalf("len(RequestsByHourByModel) = %d, want 2", got)
	}

	gptBuckets, ok := stats.RequestsByHourByModel["gpt-4o"]
	if !ok {
		t.Fatal(`missing "gpt-4o" key in RequestsByHourByModel`)
	}
	if len(gptBuckets) != 1 {
		t.Fatalf("gpt-4o buckets = %d, want 1", len(gptBuckets))
	}
	if got := gptBuckets[0].ByModel["gpt-4o"]; got != 2 {
		t.Errorf(`gpt-4o[0].ByModel["gpt-4o"] = %d, want 2`, got)
	}
	if got := gptBuckets[0].Errors; got != 0 {
		t.Errorf("gpt-4o[0].Errors = %d, want 0", got)
	}

	claudeBuckets, ok := stats.RequestsByHourByModel["claude"]
	if !ok {
		t.Fatal(`missing "claude" key in RequestsByHourByModel`)
	}
	if len(claudeBuckets) != 1 {
		t.Fatalf("claude buckets = %d, want 1", len(claudeBuckets))
	}
	if got := claudeBuckets[0].ByModel["claude"]; got != 2 {
		t.Errorf(`claude[0].ByModel["claude"] = %d, want 2`, got)
	}
	// Per-model error tracking is not propagated into the per-alias
	// bucket (see comment in sumDays): the inner Errors field stays 0.
	if got := claudeBuckets[0].Errors; got != 0 {
		t.Errorf("claude[0].Errors = %d, want 0", got)
	}
}

// TestLoadAllDaily_LegacyJSONNoByModel verifies that a daily JSON file
// written before the HourBucket.ByModel field existed still loads
// cleanly: the legacy file omits byModel on each hour bucket, but the
// loader must not error and the per-day totals must survive.
func TestLoadAllDaily_LegacyJSONNoByModel(t *testing.T) {
	dir := t.TempDir()
	statsDir := filepath.Join(dir, "stats")
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Legacy schema: no byModel key inside the hour-bucket object.
	body := []byte(`{"date":"2026-08-01","totalRequests":5,"totalInputTokens":0,"totalOutputTokens":0,"errors":0,"latencySumMs":0,"byModel":{"foo":3},"byProvider":{},"byClientKey":{},"byHour":{"1754006400":{"hour":1754006400,"count":3,"errors":0}}}`)
	if err := os.WriteFile(filepath.Join(statsDir, "2026-08-01.json"), body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := New()
	if err := s.LoadAllDaily(statsDir); err != nil {
		t.Fatalf("LoadAllDaily: %v", err)
	}

	stats := s.Stats()
	if stats.TotalRequests != 5 {
		t.Errorf("TotalRequests = %d, want 5", stats.TotalRequests)
	}
	if stats.RequestsByModel["foo"] != 3 {
		t.Errorf(`RequestsByModel["foo"] = %d, want 3`, stats.RequestsByModel["foo"])
	}
	// RequestsByHourByModel is allowed to be empty or all-zero for the
	// missing per-model data — either is acceptable. Just make sure
	// no bucket carries a phantom count.
	for alias, buckets := range stats.RequestsByHourByModel {
		for i, b := range buckets {
			if b.Count != 0 {
				t.Errorf("RequestsByHourByModel[%s][%d].Count = %d, want 0", alias, i, b.Count)
			}
		}
	}
}
