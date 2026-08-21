package main

import (
	"log"
	"testing"
)

// TestDeferRecover verifies the `defer func() { recover() }()` idiom
// used by the three background goroutines (emitLogsLoop, persistLogsLoop,
// reapClientsLoop). We don't drive the loops themselves because each one
// runs on its own ticker driven by a.ctx; instead we exercise the
// recover pattern in isolation so the behaviour is deterministic and
// independent of goroutine scheduling.
func TestDeferRecover(t *testing.T) {
	// Case 1: a single defer recover() catches a panic and the
	// surrounding function returns normally afterwards. If the panic
	// had escaped, the test binary would crash instead of reaching
	// the second case.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected recover() to catch panic, got nil")
			}
		}()
		panic("first panic")
	}()

	// Case 2: nested defers — the inner defer (registered last, runs
	// first per Go's LIFO order) consumes the panic; the outer defer
	// then sees a clean return and must NOT observe a panic.
	func() {
		innerCaught := false
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("outer defer unexpectedly got panic: %v", r)
			}
			if !innerCaught {
				t.Fatal("inner defer should have run first and recovered")
			}
		}()
		defer func() {
			if r := recover(); r != nil {
				innerCaught = true
				log.Printf("[test] inner recover caught: %v", r)
				return
			}
			t.Fatal("inner recover expected panic, got nil")
		}()
		panic("nested panic")
	}()

	// Case 3: when there is no panic, recover() returns nil and
	// downstream code keeps running. Guards against a misuse where a
	// recover block accidentally swallows legitimate control flow.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("recover() unexpectedly returned %v without a panic", r)
			}
		}()
		// intentionally no panic here
	}()
}
