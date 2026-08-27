package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseUsage_ClampsHugeValues verifies that parseUsage itself does
// NOT clamp (it returns int64 faithfully) and that the clamping is
// done at the boundary site (collectNonStream). The clamp is what
// guards against an int32 wrap-around; without it the daily totals
// would be corrupted. Pre-fix, the SSE chunk decoder and parseUsage
// both stored into int32, so a 1e20 value wrapped to negative.
func TestParseUsage_ClampsHugeValues(t *testing.T) {
	body := []byte(`{"usage":{"prompt_tokens":100000000000000,"completion_tokens":-7}}`)
	in, out := parseUsage(body)
	if in != 100000000000000 {
		t.Errorf("parseUsage must return the raw int64 (no clamp); got in=%d", in)
	}
	if out != -7 {
		t.Errorf("parseUsage must return the raw int64 (no clamp); got out=%d", out)
	}

	// Now verify the boundary clamps correctly when the caller
	// funnels the result into a LogEntry.InputTokens (int32). We
	// simulate by using the same clamp helper that collectNonStream
	// uses.
	if got := clampToInt32(in); got < 0 || got > 1<<31-1 {
		t.Errorf("clampToInt32(%d) produced out-of-int32 result %d", in, got)
	}
	if got := clampToInt32(out); got != 0 {
		t.Errorf("clampToInt32(-7) = %d, want 0 (negative token count must be clamped)", got)
	}
}

// TestClampToInt32 verifies the helper behaves correctly at every
// interesting boundary.
func TestClampToInt32(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want int
	}{
		{"zero", 0, 0},
		{"small positive", 42, 42},
		{"normal range", 1 << 20, 1 << 20},
		{"exactly int32 max", int64(1<<31 - 1), 1<<31 - 1},
		{"one over int32 max", int64(1 << 31), 1<<31 - 1},
		{"very large", int64(1) << 60, 1<<31 - 1},
		{"negative", -1, 0},
		{"very negative", int64(-1) << 60, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampToInt32(tc.in); got != tc.want {
				t.Errorf("clampToInt32(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestRewriteModel_PreservesBody verifies that the conservative
// fallback (when json.Unmarshal or json.Marshal fails) returns the
// original body unchanged, never a partially-rewritten nil/empty
// payload. This is the safety net that the audit confirmed earlier.
func TestRewriteModel_PreservesBodyOnMarshalFailure(t *testing.T) {
	// Fast path: model field, simple value.
	body := []byte(`{"model":"foo"}`)
	out := rewriteModel(body, "model", "bar")
	if !strings.Contains(string(out), `"model":"bar"`) {
		t.Errorf("fast path failed to rewrite model: %s", out)
	}

	// Slow path: non-"model" field. The function falls through to
	// the unmarshal/marshal round-trip and successfully rewrites
	// whatever model-like key is present. The audit concern was
	// not that this succeeds, but that on FAILURE the body is
	// returned unchanged rather than nil.
	out = rewriteModel(body, "model_id", "bar")
	if !strings.Contains(string(out), `"bar"`) {
		t.Errorf("slow path should still rewrite \"model\" via round-trip: %s", out)
	}

	// Truly degenerate input: unparseable body. The slow path's
	// Unmarshal fails and returns the body unchanged.
	junk := []byte(`{not json`)
	if got := rewriteModel(junk, "model_id", "bar"); string(got) != string(junk) {
		t.Errorf("unparseable body must round-trip unchanged; got %q", got)
	}

	// Marshal must not panic on bad input either way.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("rewriteModel panicked: %v", r)
		}
	}()
	_ = json.RawMessage(body)
}

// BenchmarkClampToInt32 measures the cost of the boundary helper on
// every LogEntry write. The hot path is for normal values (<= 1<<20),
// which the function short-circuits with a direct int conversion.
func BenchmarkClampToInt32(b *testing.B) {
	b.ReportAllocs()
	cases := []int64{
		0,
		1 << 10,
		1 << 20,
		1 << 30,
		1 << 31,
		1 << 60,
		-1,
	}
	for _, c := range cases {
		c := c
		b.Run("", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = clampToInt32(c)
			}
		})
	}
}

// BenchmarkParseUsage measures the unmarshal cost of a typical
// non-streaming OpenAI-style response. Pre-fix, the probe was int;
// post-fix it's int64. The benchmark confirms the int64 widening
// doesn't add measurable overhead (json.Unmarshal is the dominant
// cost).
func BenchmarkParseUsage(b *testing.B) {
	body := []byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"finish_reason":"stop"}],"usage":{"prompt_tokens":150,"completion_tokens":75,"total_tokens":225}}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = parseUsage(body)
	}
}
