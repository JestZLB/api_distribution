//go:build !windows

// Stub Tray for non-Windows builds. The Settings UI surfaces
// ErrUnsupported so users get a clear "not available on this OS"
// message instead of a panic.

package system

type stubTray struct{}

func newPlatformTray(_ []byte) Tray { return stubTray{} }

func (stubTray) Start(_ string, _ TrayCallbacks) error { return ErrUnsupported }
func (stubTray) SetLocale(_ string)                    {}
func (stubTray) Stop()                                 {}
func (stubTray) Active() bool                           { return false }