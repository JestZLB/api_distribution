// Package system bundles small OS-level integrations that aren't
// generic Go work: auto-start on login, minimize-to-tray, and the
// tray icon itself. Each feature is split into a small platform-
// neutral interface here plus a *_windows.go / *_other.go pair that
// does the actual work (or returns a clear "not supported" error on
// non-Windows builds).
package system

import "errors"

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