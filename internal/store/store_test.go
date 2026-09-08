package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

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

	if err := s.SaveDaily(0); err != nil {
		t.Fatalf("SaveDaily: %v", err)
	}

	// A fresh store sharing the same SQLite file must recover the same
	// totals from the daily_stats table.
	dbFile := filepath.Join(t.TempDir(), "store.db")
	if err := copyDB(s, dbFile); err != nil {
		t.Fatalf("copy db: %v", err)
	}
	s2, err := Open(dbFile)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer s2.db.Close()
	if err := s2.LoadAllDaily(); err != nil {
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

	if err := s.SaveDaily(7); err != nil {
		t.Fatalf("SaveDaily: %v", err)
	}

	if st := s.Stats(); st.TotalRequests != 1 {
		t.Errorf("TotalRequests after pruning = %d, want 1", st.TotalRequests)
	}
	// The old day must no longer be in s.days and in in the table.
	if _, ok := s.days[dateKey(old.UnixNano())]; ok {
		t.Errorf("old day was not pruned from in-memory days map")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM daily_stats WHERE date = ?`, dateKey(old.UnixNano())).Scan(&n); err != nil {
		t.Fatalf("count old day: %v", err)
	}
	if n != 0 {
		t.Errorf("old day row was not pruned (count=%d)", n)
	}
}

func TestClearDailyFiles_RemovesOnDiskAggregates(t *testing.T) {
	s := New()
	now := time.Now()
	s.Append(types.LogEntry{ID: "x", Timestamp: now.UnixNano(), ClientKeyLabel: "k"})
	if err := s.SaveDaily(0); err != nil {
		t.Fatalf("SaveDaily: %v", err)
	}
	s.ClearDaily()

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM daily_stats`).Scan(&n); err != nil {
		t.Fatalf("count daily_stats: %v", err)
	}
	if n != 0 {
		t.Errorf("expected daily_stats empty after ClearDaily, got %d rows", n)
	}
}

// copyDB streams every row of s.db (which is :memory: and dies when s
// goes out of scope) into a fresh file-backed database so a second
// Store can re-open and read it. We use SQLite's VACUUM INTO — the
// simplest portable way and it avoids plumbing the modernc driver's
// backup API into tests.
func copyDB(s *Store, dst string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, dst)
	return err
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
// cleanly via the legacy-JSON migration: the legacy file omits
// byModel on each hour bucket, but the loader must not error and the
// per-day totals must survive.
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
	if err := s.MigrateLegacyJSON(dir); err != nil {
		t.Fatalf("MigrateLegacyJSON: %v", err)
	}
	if err := s.LoadAllDaily(); err != nil {
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

// TestStats_RequestsByHourByModel_TokenAggregation verifies that
// RequestsByHourByModel carries a per-alias, per-hour token trend:
// ByModelTokens[alias] equals that alias's input+output tokens for the
// hour, and the inner InputTokens/OutputTokens mirror the alias's own
// input/output (not the hour total). The emitted slices must be sorted
// by hour ascending.
func TestStats_RequestsByHourByModel_TokenAggregation(t *testing.T) {
	s := New()
	base := time.Now().Truncate(time.Hour)
	h0 := base.Unix()
	h1 := base.Add(time.Hour).Unix()

	// alias-a spans two hours; alias-b only shares h0 with alias-a.
	s.Append(types.LogEntry{ID: "a0", Timestamp: base.UnixNano(), Alias: "alias-a", InputTokens: 10, OutputTokens: 5})
	s.Append(types.LogEntry{ID: "a1", Timestamp: base.Add(time.Hour).UnixNano(), Alias: "alias-a", InputTokens: 20, OutputTokens: 8})
	s.Append(types.LogEntry{ID: "b0", Timestamp: base.UnixNano(), Alias: "alias-b", InputTokens: 100, OutputTokens: 50})

	stats := s.Stats()
	if stats.RequestsByHourByModel == nil {
		t.Fatal("RequestsByHourByModel is nil")
	}
	if got := len(stats.RequestsByHourByModel); got != 2 {
		t.Fatalf("len(RequestsByHourByModel) = %d, want 2", got)
	}

	ab, ok := stats.RequestsByHourByModel["alias-a"]
	if !ok {
		t.Fatal(`missing "alias-a" key in RequestsByHourByModel`)
	}
	if len(ab) != 2 {
		t.Fatalf("alias-a buckets = %d, want 2", len(ab))
	}
	// Sorted by hour ascending.
	if ab[0].Hour != h0 || ab[1].Hour != h1 {
		t.Fatalf("alias-a hours = [%d,%d], want [%d,%d]", ab[0].Hour, ab[1].Hour, h0, h1)
	}
	// h0: in 10, out 5 => total 15.
	if got := ab[0].ByModelTokens["alias-a"]; got != 15 {
		t.Errorf(`alias-a h0 byModelTokens["alias-a"] = %d, want 15`, got)
	}
	if ab[0].InputTokens != 10 || ab[0].OutputTokens != 5 {
		t.Errorf("alias-a h0 input/output = %d/%d, want 10/5", ab[0].InputTokens, ab[0].OutputTokens)
	}
	// h1: in 20, out 8 => total 28.
	if got := ab[1].ByModelTokens["alias-a"]; got != 28 {
		t.Errorf(`alias-a h1 byModelTokens["alias-a"] = %d, want 28`, got)
	}
	if ab[1].InputTokens != 20 || ab[1].OutputTokens != 8 {
		t.Errorf("alias-a h1 input/output = %d/%d, want 20/8", ab[1].InputTokens, ab[1].OutputTokens)
	}

	bb, ok := stats.RequestsByHourByModel["alias-b"]
	if !ok {
		t.Fatal(`missing "alias-b" key in RequestsByHourByModel`)
	}
	if len(bb) != 1 {
		t.Fatalf("alias-b buckets = %d, want 1", len(bb))
	}
	// h0: in 100, out 50 => total 150.
	if got := bb[0].ByModelTokens["alias-b"]; got != 150 {
		t.Errorf(`alias-b byModelTokens["alias-b"] = %d, want 150`, got)
	}
	if bb[0].InputTokens != 100 || bb[0].OutputTokens != 50 {
		t.Errorf("alias-b input/output = %d/%d, want 100/50", bb[0].InputTokens, bb[0].OutputTokens)
	}
}

// TestStats_RequestsByHourByModel_CrossDayTokenSum verifies that token
// usage for the same alias across two different calendar days is summed
// into the per-hour buckets without double counting.
func TestStats_RequestsByHourByModel_CrossDayTokenSum(t *testing.T) {
	s := New()
	yesterday := time.Now().Add(-25 * time.Hour).Truncate(time.Hour)
	today := time.Now().Truncate(time.Hour)

	s.Append(types.LogEntry{ID: "y", Timestamp: yesterday.UnixNano(), Alias: "alias-a", InputTokens: 1, OutputTokens: 2})
	s.Append(types.LogEntry{ID: "t", Timestamp: today.UnixNano(), Alias: "alias-a", InputTokens: 3, OutputTokens: 4})

	stats := s.Stats()
	ab, ok := stats.RequestsByHourByModel["alias-a"]
	if !ok {
		t.Fatal(`missing "alias-a" key in RequestsByHourByModel`)
	}
	if len(ab) != 2 {
		t.Fatalf("alias-a buckets = %d, want 2 (one per day)", len(ab))
	}
	if ab[0].Hour >= ab[1].Hour {
		t.Fatalf("alias-a buckets not ascending: [%d,%d]", ab[0].Hour, ab[1].Hour)
	}
	var total int64
	for _, b := range ab {
		total += b.ByModelTokens["alias-a"]
	}
	if total != 10 {
		t.Errorf("cross-day total byModelTokens = %d, want 10", total)
	}
	// Each hour independently: (1+2)=3 and (3+4)=7.
	if ab[0].ByModelTokens["alias-a"] != 3 || ab[1].ByModelTokens["alias-a"] != 7 {
		t.Errorf("per-hour byModelTokens = [%d,%d], want [3,7]",
			ab[0].ByModelTokens["alias-a"], ab[1].ByModelTokens["alias-a"])
	}
}

// TestLoadAllDaily_LegacyJSONNoTokenFields verifies that a daily JSON
// file written before the HourBucket token fields existed still loads
// cleanly via the legacy-JSON migration: the legacy file omits
// inputTokens/outputTokens/byModelTokens on each hour bucket, but the
// loader must not error and token fields must default to 0 / a non-nil
// empty map.
func TestLoadAllDaily_LegacyJSONNoTokenFields(t *testing.T) {
	dir := t.TempDir()
	statsDir := filepath.Join(dir, "stats")
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Legacy schema: hour bucket carries byModel but no token fields.
	base := time.Now().Truncate(time.Hour)
	dateStr := base.Format("2006-01-02")
	hour := base.Unix()
	body := []byte(fmt.Sprintf(`{"date":%q,"totalRequests":3,"totalInputTokens":0,"totalOutputTokens":0,"errors":0,"latencySumMs":0,"byModel":{"foo":3},"byProvider":{},"byClientKey":{},"byHour":{"%d":{"hour":%d,"count":3,"errors":0,"byModel":{"foo":3}}}}`, dateStr, hour, hour))
	if err := os.WriteFile(filepath.Join(statsDir, "2026-08-02.json"), body, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s := New()
	if err := s.MigrateLegacyJSON(dir); err != nil {
		t.Fatalf("MigrateLegacyJSON: %v", err)
	}
	if err := s.LoadAllDaily(); err != nil {
		t.Fatalf("LoadAllDaily: %v", err)
	}

	stats := s.Stats()
	if stats.TotalRequests != 3 {
		t.Errorf("TotalRequests = %d, want 3", stats.TotalRequests)
	}
	buckets, ok := stats.RequestsByHourByModel["foo"]
	if !ok {
		t.Fatal(`missing "foo" key in RequestsByHourByModel`)
	}
	if len(buckets) != 1 {
		t.Fatalf("foo buckets = %d, want 1", len(buckets))
	}
	b := buckets[0]
	if b.Count != 3 {
		t.Errorf("foo[0].Count = %d, want 3", b.Count)
	}
	if b.InputTokens != 0 || b.OutputTokens != 0 {
		t.Errorf("foo[0] input/output = %d/%d, want 0/0", b.InputTokens, b.OutputTokens)
	}
	if b.ByModelTokens == nil {
		t.Error("foo[0].ByModelTokens is nil")
	}
	if got := b.ByModelTokens["foo"]; got != 0 {
		t.Errorf(`foo[0].byModelTokens["foo"] = %d, want 0`, got)
	}
}

// TestStatsWithComparison_TodayVsYesterday verifies that
// StatsWithComparison splits the per-date aggregates into today vs the
// previous full calendar day, and that Prev* mirror Yesterday* for
// backward compatibility.
func TestStatsWithComparison_TodayVsYesterday(t *testing.T) {
	s := New()
	now := time.Now()
	yesterday := now.Add(-24 * time.Hour)
	s.Append(types.LogEntry{ID: "t1", Timestamp: now.UnixNano(), Alias: "a", InputTokens: 100, OutputTokens: 50, StatusCode: 200})
	s.Append(types.LogEntry{ID: "t2", Timestamp: now.UnixNano(), Alias: "a", InputTokens: 200, OutputTokens: 100, StatusCode: 500})
	s.Append(types.LogEntry{ID: "y1", Timestamp: yesterday.UnixNano(), Alias: "a", InputTokens: 300, OutputTokens: 150, StatusCode: 200})

	sc := s.StatsWithComparison()
	if sc.TodayRequests != 2 {
		t.Errorf("TodayRequests = %d, want 2", sc.TodayRequests)
	}
	if sc.TodayInputTokens != 300 {
		t.Errorf("TodayInputTokens = %d, want 300", sc.TodayInputTokens)
	}
	if sc.TodayOutputTokens != 150 {
		t.Errorf("TodayOutputTokens = %d, want 150", sc.TodayOutputTokens)
	}
	if sc.TodayErrors != 1 {
		t.Errorf("TodayErrors = %d, want 1", sc.TodayErrors)
	}
	if sc.YesterdayRequests != 1 {
		t.Errorf("YesterdayRequests = %d, want 1", sc.YesterdayRequests)
	}
	if sc.YesterdayInputTokens != 300 {
		t.Errorf("YesterdayInputTokens = %d, want 300", sc.YesterdayInputTokens)
	}
	if sc.YesterdayOutputTokens != 150 {
		t.Errorf("YesterdayOutputTokens = %d, want 150", sc.YesterdayOutputTokens)
	}
	if sc.YesterdayErrors != 0 {
		t.Errorf("YesterdayErrors = %d, want 0", sc.YesterdayErrors)
	}
	// Prev* mirror yesterday for backward compatibility.
	if sc.PrevRequests != sc.YesterdayRequests {
		t.Errorf("PrevRequests = %d, want %d", sc.PrevRequests, sc.YesterdayRequests)
	}
	if sc.PrevInputTokens != sc.YesterdayInputTokens {
		t.Errorf("PrevInputTokens = %d, want %d", sc.PrevInputTokens, sc.YesterdayInputTokens)
	}
	if sc.PrevOutputTokens != sc.YesterdayOutputTokens {
		t.Errorf("PrevOutputTokens = %d, want %d", sc.PrevOutputTokens, sc.YesterdayOutputTokens)
	}
}

// TestDailyPersistence_TokenRoundTrip verifies that the SQLite store
// round-trips the token aggregates: after SaveDaily into a fresh file
// + LoadAllDaily, the day-level input/output totals and the
// per-alias hourly byModelTokens survive a restart.
func TestDailyPersistence_TokenRoundTrip(t *testing.T) {
	s := New()
	now := time.Now()
	// gpt totals 300 tokens (in 200 / out 100); claude totals 450.
	s.Append(types.LogEntry{ID: "a1", Timestamp: now.UnixNano(), Alias: "gpt", InputTokens: 100, OutputTokens: 50, StatusCode: 200})
	s.Append(types.LogEntry{ID: "a2", Timestamp: now.UnixNano(), Alias: "gpt", InputTokens: 100, OutputTokens: 50, StatusCode: 200})
	s.Append(types.LogEntry{ID: "b1", Timestamp: now.UnixNano(), Alias: "claude", InputTokens: 300, OutputTokens: 150, StatusCode: 200})

	if err := s.SaveDaily(0); err != nil {
		t.Fatalf("SaveDaily: %v", err)
	}

	// A fresh store sharing the same SQLite file must recover the same
	// token totals from the daily_stats table.
	dbFile := filepath.Join(t.TempDir(), "store.db")
	if err := copyDB(s, dbFile); err != nil {
		t.Fatalf("copy db: %v", err)
	}
	s2, err := Open(dbFile)
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer s2.db.Close()
	if err := s2.LoadAllDaily(); err != nil {
		t.Fatalf("LoadAllDaily: %v", err)
	}
	st := s2.Stats()
	if st.TotalInputTokens != 500 {
		t.Errorf("TotalInputTokens after reload = %d, want 500", st.TotalInputTokens)
	}
	if st.TotalOutputTokens != 250 {
		t.Errorf("TotalOutputTokens after reload = %d, want 250", st.TotalOutputTokens)
	}
	gpt := st.RequestsByHourByModel["gpt"]
	if len(gpt) != 1 {
		t.Fatalf("gpt buckets = %d, want 1", len(gpt))
	}
	if got := gpt[0].ByModelTokens["gpt"]; got != 300 {
		t.Errorf(`gpt[0].byModelTokens["gpt"] = %d, want 300`, got)
	}
	claude := st.RequestsByHourByModel["claude"]
	if len(claude) != 1 {
		t.Fatalf("claude buckets = %d, want 1", len(claude))
	}
	if got := claude[0].ByModelTokens["claude"]; got != 450 {
		t.Errorf(`claude[0].byModelTokens["claude"] = %d, want 450`, got)
	}
}

// TestStatsWithComparison_RollingWindows verifies the all-time / 7-day /
// 30-day consumption windows and their previous-period counterparts.
func TestStatsWithComparison_RollingWindows(t *testing.T) {
	s := New()
	now := time.Now()
	// Today: inside the 7-day and 30-day windows.
	s.Append(types.LogEntry{ID: "d0", Timestamp: now.UnixNano(), Alias: "a", InputTokens: 100, OutputTokens: 50, StatusCode: 200})
	// 3 days ago: inside the 7-day and 30-day windows.
	s.Append(types.LogEntry{ID: "d3", Timestamp: now.AddDate(0, 0, -3).UnixNano(), Alias: "a", InputTokens: 200, OutputTokens: 100, StatusCode: 200})
	// 10 days ago: in prev 7-day window and the 30-day window.
	s.Append(types.LogEntry{ID: "d10", Timestamp: now.AddDate(0, 0, -10).UnixNano(), Alias: "a", InputTokens: 300, OutputTokens: 150, StatusCode: 200})
	// 40 days ago: outside the 30-day window, inside prev 30-day window.
	s.Append(types.LogEntry{ID: "d40", Timestamp: now.AddDate(0, 0, -40).UnixNano(), Alias: "a", InputTokens: 400, OutputTokens: 200, StatusCode: 200})

	sc := s.StatsWithComparison()
	if sc.AllTime.Requests != 4 {
		t.Errorf("AllTime.Requests = %d, want 4", sc.AllTime.Requests)
	}
	if sc.Week.Requests != 2 {
		t.Errorf("Week.Requests = %d, want 2", sc.Week.Requests)
	}
	if sc.Week.InputTokens != 300 || sc.Week.OutputTokens != 150 {
		t.Errorf("Week tokens = %d/%d, want 300/150", sc.Week.InputTokens, sc.Week.OutputTokens)
	}
	if sc.PrevWeek.Requests != 1 {
		t.Errorf("PrevWeek.Requests = %d, want 1", sc.PrevWeek.Requests)
	}
	if sc.PrevWeek.InputTokens != 300 || sc.PrevWeek.OutputTokens != 150 {
		t.Errorf("PrevWeek tokens = %d/%d, want 300/150", sc.PrevWeek.InputTokens, sc.PrevWeek.OutputTokens)
	}
	if sc.Month.Requests != 3 {
		t.Errorf("Month.Requests = %d, want 3", sc.Month.Requests)
	}
	if sc.Month.InputTokens != 600 || sc.Month.OutputTokens != 300 {
		t.Errorf("Month tokens = %d/%d, want 600/300", sc.Month.InputTokens, sc.Month.OutputTokens)
	}
	if sc.PrevMonth.Requests != 1 {
		t.Errorf("PrevMonth.Requests = %d, want 1", sc.PrevMonth.Requests)
	}
	if sc.PrevMonth.InputTokens != 400 || sc.PrevMonth.OutputTokens != 200 {
		t.Errorf("PrevMonth tokens = %d/%d, want 400/200", sc.PrevMonth.InputTokens, sc.PrevMonth.OutputTokens)
	}
}

// TestStats_ConcurrentAppendAndStats is the G-006 race-test
// canary: 50 goroutines hammer Append with a single client-key
// label while a coordinator goroutine continuously reads Stats /
// StatsWithComparison. After 1s of pressure we assert that no
// goroutine panicked, no race detector complaint fired, and the
// per-key totals reported by Stats reflect the exact count of
// appends the test executed (every Append is captured via an atomic
// counter so the assertion is exact, not a probabilistic range).
//
// Run with `go test -race` to exercise the data-race detector;
// this test deliberately uses overlapping RLock + Lock paths so any
// future regression that removes the lock around the G-006
// incrementals surfaces as a race report.
func TestStats_ConcurrentAppendAndStats(t *testing.T) {
	s := New()
	now := time.Now()
	const label = "race-key"

	stop := make(chan struct{})
	done := make(chan struct{})
	var appendCount int64

	go func() {
		defer close(done)
		const goroutines = 50
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for g := 0; g < goroutines; g++ {
			go func() {
				defer wg.Done()
				i := 0
				for {
					select {
					case <-stop:
						return
					default:
					}
					s.Append(types.LogEntry{
						ID:             optID(i),
						Timestamp:      now.UnixNano(),
						Alias:          "race-alias",
						ClientKeyLabel: label,
						StatusCode:     200,
					})
					atomic.AddInt64(&appendCount, 1)
					i++
				}
			}()
		}
		wg.Wait()
	}()

	// Main goroutine reads Stats / StatsWithComparison concurrently
	// for a 1s window to exercise the concurrent-read + write path.
	// The final snapshot is taken AFTER all appenders have stopped
	// (post close(stop) + <-done) so we can compare against the
	// exact final appendCount without a race against the writers.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		_ = s.Stats()
		_ = s.StatsWithComparison()
	}

	close(stop)
	<-done

	// Final read, AFTER the appenders have stopped. This is the
	// snapshot we compare to the final appendCount.
	finalStats := s.Stats()
	finalSwc := s.StatsWithComparison()

	// Sanity-check final stats after the dust settles. With every
	// Append using the same ClientKeyLabel, RequestsByClientKeyRecent
	// must equal the total append count (all entries are in-window).
	if got := finalStats.RequestsByClientKeyRecent[label]; got != appendCount {
		t.Errorf("RequestsByClientKeyRecent[%q] = %d, want %d", label, got, appendCount)
	}
	if got := finalSwc.RequestsByClientKeyRecent[label]; got != appendCount {
		t.Errorf("swc.RequestsByClientKeyRecent[%q] = %d, want %d", label, got, appendCount)
	}
	// TotalRequests (sumDays) must match too, otherwise something
	// in the G-006 incrementals path silently dropped entries.
	if got := finalStats.TotalRequests; got != appendCount {
		t.Errorf("Stats.TotalRequests = %d, want %d", got, appendCount)
	}
	if got := finalSwc.TotalRequests; got != appendCount {
		t.Errorf("swc.TotalRequests = %d, want %d", got, appendCount)
	}
}

// TestStatsWithComparison_AfterAppend verifies that after a single
// Append, a subsequent StatsWithComparison call observes the
// incremented G-006 fields exactly once — no double counting and no
// drops. The test uses deterministic, time-aligned entries so the
// 24h window filter does not exclude the appended entry.
func TestStatsWithComparison_AfterAppend(t *testing.T) {
	s := New()
	now := time.Now()
	// Initial two entries: one in the last 24h, one outside.
	s.Append(types.LogEntry{
		ID:             "first-recent",
		Timestamp:      now.UnixNano(),
		Alias:          "alpha",
		ClientKeyLabel: "keyA",
		StatusCode:     200,
	})
	s.Append(types.LogEntry{
		ID:             "first-old",
		Timestamp:      now.Add(-48 * time.Hour).UnixNano(),
		Alias:          "beta",
		ClientKeyLabel: "keyB",
		StatusCode:     200,
	})

	before := s.StatsWithComparison()
	if before.TotalRequests != 2 {
		t.Fatalf("baseline TotalRequests = %d, want 2", before.TotalRequests)
	}
	if before.RequestsByClientKeyRecent["keyA"] != 1 {
		t.Fatalf("baseline RequestsByClientKeyRecent[keyA] = %d, want 1", before.RequestsByClientKeyRecent["keyA"])
	}
	if before.RequestsByClientKeyRecent["keyB"] != 0 {
		t.Fatalf("baseline RequestsByClientKeyRecent[keyB] = %d, want 0", before.RequestsByClientKeyRecent["keyB"])
	}

	// Append a third entry — must increment exactly once.
	s.Append(types.LogEntry{
		ID:             "second-recent",
		Timestamp:      now.UnixNano(),
		Alias:          "alpha",
		ClientKeyLabel: "keyA",
		StatusCode:     200,
	})

	after := s.StatsWithComparison()
	if after.TotalRequests != 3 {
		t.Errorf("after Append TotalRequests = %d, want 3", after.TotalRequests)
	}
	if after.RequestsByClientKeyRecent["keyA"] != 2 {
		t.Errorf("after Append RequestsByClientKeyRecent[keyA] = %d, want 2", after.RequestsByClientKeyRecent["keyA"])
	}
	if after.RequestsByClientKeyRecent["keyB"] != 0 {
		t.Errorf("after Append RequestsByClientKeyRecent[keyB] = %d, want 0", after.RequestsByClientKeyRecent["keyB"])
	}
	// RequestsByModel["alpha"] must include both "first-recent" and
	// "second-recent" (the G-006 todayByModel incrementals and the
	// sumDays DailyAgg should agree).
	if got := after.RequestsByModel["alpha"]; got != 2 {
		t.Errorf("after Append RequestsByModel[alpha] = %d, want 2", got)
	}
}
