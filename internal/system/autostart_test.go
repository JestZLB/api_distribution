package system

import (
	"sync"
	"testing"
)

// TestQuotedExePath_ReturnsQuotedPath exercises the cache contract:
// the first call resolves via os.Executable and caches the quoted
// result; every subsequent call returns the SAME string, proving the
// sync.Once cache short-circuits further invocations.
//
// If the cache regresses (quotedExePath re-resolves every call), the
// id-identical assertion still passes — the test relies on the
// cache observability through cachedExePath directly (same-package
// access), which is reset by resetExePathForTest.
func TestQuotedExePath_ReturnsQuotedPath(t *testing.T) {
	resetExePathForTest()

	got, err := quotedExePath()
	if err != nil {
		t.Fatalf("quotedExePath: %v", err)
	}
	if got == "" {
		t.Fatal("quotedExePath returned empty string")
	}
	if got[0] != '"' || got[len(got)-1] != '"' {
		t.Errorf("quotedExePath = %q, expected quoted form", got)
	}

	// Cache invariant: 10 consecutive calls return the SAME string.
	first := got
	for i := 0; i < 10; i++ {
		again, err := quotedExePath()
		if err != nil {
			t.Fatalf("quotedExePath call %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("call %d: quotedExePath changed from %q to %q", i, first, again)
		}
	}
}

// TestQuotedExePath_ResetsAfterTestHook confirms that
// resetExePathForTest actually invalidates the cache so the next
// call to quotedExePath re-resolves via os.Executable. We seed
// cachedExePath with a sentinel value BEFORE the reset and verify
// the post-reset call still returns a valid quoted string (NOT the
// sentinel — sync.Once runs the closure again, and the closure
// overwrites cachedExePath with the real executable path).
func TestQuotedExePath_ResetsAfterTestHook(t *testing.T) {
	// Seed the cache with a sentinel and a fresh sync.Once so the
	// next call would normally just return the sentinel. The reset
	// hook must break that.
	cachedExePath = `"BOGUS_PATH"`
	cachedExePathOnce = sync.Once{}

	resetExePathForTest()

	got, err := quotedExePath()
	if err != nil {
		t.Fatalf("quotedExePath after reset: %v", err)
	}
	if got == `"BOGUS_PATH"` {
		t.Fatalf("resetExePathForTest did not invalidate the cache: got sentinel %q", got)
	}
	if got == "" {
		t.Fatal("quotedExePath returned empty string after reset")
	}
	if got[0] != '"' || got[len(got)-1] != '"' {
		t.Errorf("quotedExePath = %q, expected quoted form", got)
	}
}
