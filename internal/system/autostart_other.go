//go:build !windows

// Stub AutoStart for non-Windows builds. The settings UI surfaces
// ErrUnsupported so users get a clear "not available on this OS"
// message instead of a panic.

package system

// stubAutoStart is the no-op implementation used on every OS other
// than Windows. Every methods returns ErrUnsupported so the frontend
// can show an explanatory toast.
type stubAutoStart struct{}

// newPlatformAutoStart returns the stub implementation.
func newPlatformAutoStart() AutoStart { return stubAutoStart{} }

func (stubAutoStart) Enabled() (bool, error)        { return false, ErrUnsupported }
func (stubAutoStart) Enable(_ string) error         { return ErrUnsupported }
func (stubAutoStart) Disable() error                { return ErrUnsupported }