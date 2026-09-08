// Package system bundles small OS-level integrations that aren't
// generic Go work: auto-start on login, minimize-to-tray, and the
// tray icon itself. Each feature is split into a small platform-
// neutral interface here plus a *_windows.go / *_other.go pair that
// does the actual work (or returns a clear "not supported" error on
// non-Windows builds).
package system

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ErrUnsupported is returned by platform stubs on non-Windows
// builds so the frontend can surface a friendly message instead of
// a panic.
var ErrUnsupported = errors.New("system feature not supported on this platform")

// AutoStart manages the "launch at login" toggle. On Windows it
// edits HKCU\Software\Microsoft\Windows\CurrentVersion\Run; on other
// platforms every method returns ErrUnsupported.
type AutoStart interface {
	// Enabled reports whether the OS is configured to launch the
	// current executable on user login.
	Enabled() (bool, error)
	// Enable registers the current executable under the given
	// display name so it launches at next login.
	Enable(name string) error
	// Disable removes the auto-start entry, if any.
	Disable() error
}

// NewAutoStart returns the AutoStart implementation appropriate for
// the current OS. Callers should treat the returned value as
// best-effort and surface ErrUnsupported to the user.
func NewAutoStart() AutoStart {
	return newPlatformAutoStart()
}

// cachedExePath + cachedExePathOnce hold the process-lifetime result
// of os.Executable(), wrapped in registry-safe double quotes. os.Executable
// reads /proc/self/exe on Linux/macOS and the per-process executable
// path on Windows; it is stable for the lifetime of the process, so
// every toggle of the AutoStart switch re-uses the cached value
// instead of re-querying the OS.
//
// G-019: previously quotedExePath() called os.Executable on every
// invocation. With Settings able to call SetAutoStart repeatedly,
// that was needless syscall + allocation overhead. Now we resolve
// the path ONCE per process via sync.Once.
var (
	cachedExePath     string
	cachedExePathOnce sync.Once
)

// quotedExePath returns the current executable path wrapped in
// double quotes so paths with spaces survive registry parsing.
// The lookup is process-lifetime cached (see cachedExePath /
// cachedExePathOnce) — every caller after the first gets the
// pre-resolved string with zero syscalls.
func quotedExePath() (string, error) {
	cachedExePathOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			cachedExePath = ""
			return
		}
		cachedExePath = `"` + strings.ReplaceAll(exe, `"`, `\"`) + `"`
	})
	if cachedExePath == "" {
		// We can distinguish a real os.Executable failure from a
		// successful empty lookup by checking the cached value:
		// successful lookups always yield a quoted, non-empty string.
		return "", fmt.Errorf("locate executable: os.Executable failed at first call")
	}
	return cachedExePath, nil
}

// resetExePathForTest clears the cached executable path so the next
// call to quotedExePath re-resolves it via os.Executable. Tests use
// this hook to verify the cache contract; production callers must
// not touch the cache (the path is stable for the process lifetime).
func resetExePathForTest() {
	cachedExePath = ""
	cachedExePathOnce = sync.Once{}
}