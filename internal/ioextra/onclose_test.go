package ioextra

import (
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// panicReadCloser is an io.ReadCloser whose Close panics, used to
// verify that onClose still runs via defer when the underlying
// Close panics.
type panicReadCloser struct{}

func (panicReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (panicReadCloser) Close() error             { panic("boom") }

// countingReadCloser counts how many times Close is called, using an
// atomic counter so it is safe under concurrent access.
type countingReadCloser struct {
	closeCount atomic.Int32
}

func (c *countingReadCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (c *countingReadCloser) Close() error {
	c.closeCount.Add(1)
	return nil
}

func TestWrapCloser_RunsOnce(t *testing.T) {
	var calls atomic.Int32
	rc := WrapCloser(io.NopCloser(strings.NewReader("hello")), func() {
		calls.Add(1)
	})

	if err := rc.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("onClose calls = %d, want 1", got)
	}

	// Double close must not trigger onClose again.
	if err := rc.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("onClose calls after double close = %d, want 1", got)
	}
}

// TestWrapCloser_RunsOnPanic verifies that onClose still runs when the
// underlying Close panics.
func TestWrapCloser_RunsOnPanic(t *testing.T) {
	var calls atomic.Int32
	rc := WrapCloser(panicReadCloser{}, func() {
		calls.Add(1)
	})

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("expected a panic from underlying Close")
			}
		}()
		_ = rc.Close()
	}()

	if got := calls.Load(); got != 1 {
		t.Fatalf("onClose calls = %d, want 1", got)
	}
}

// TestWrapCloser_ConcurrentClose verifies that concurrent Close calls
// invoke the underlying Close exactly once and trigger onClose
// exactly once.
func TestWrapCloser_ConcurrentClose(t *testing.T) {
	rc := &countingReadCloser{}
	var calls atomic.Int32
	body := WrapCloser(rc, func() { calls.Add(1) })

	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = body.Close()
		}()
	}
	wg.Wait()

	if got := rc.closeCount.Load(); got != 1 {
		t.Fatalf("underlying Close called %d times, want 1", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("onClose calls = %d, want 1", got)
	}
}
