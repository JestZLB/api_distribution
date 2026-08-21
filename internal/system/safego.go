package system

import (
	"log"
	"runtime/debug"
)

// SafeGo launches fn on a new goroutine with a deferred recover so a
// panic inside fn is logged (with stack) instead of crashing the
// process. Use this for any goroutine whose panic would otherwise
// take down the whole application — background pumps, callback
// dispatchers, etc. The `name` appears in the log line so operators
// can correlate the crash with the originating subsystem.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[safeGo] panic in %s: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
