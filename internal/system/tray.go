// Package system — tray.go declares the platform-neutral Tray
// interface and the lifecycle methods the frontend uses to toggle
// tray behaviour. The actual Win32 NotifyIcon plumbing lives in
// tray_windows.go; non-Windows builds get a no-op stub.

package system

// TrayAction describes what the user asked for via the tray menu.
// The frontend-bound App translates these into runtime.WindowShow,
// runtime.WindowHide, runtime.Quit, etc.
type TrayAction string

const (
	// ActionShow brings the main window to the foreground.
	ActionShow TrayAction = "show"
	// ActionQuit terminates the application cleanly.
	ActionQuit TrayAction = "quit"
)

// TrayCallbacks receives the user's selection from the tray menu.
// Channel-based to avoid blocking the message loop in tray_windows.
type TrayCallbacks interface {
	OnAction(TrayAction)
}

// Tray manages the OS-level tray icon. Start registers the icon and
// begins pumping messages; Stop tears it down. Calling Start twice
// without Stop in between is a no-op.
type Tray interface {
	// Start installs the icon with the supplied tooltip and a
	// minimal menu ("Show", "Quit"). Callbacks fire on the
	// supplied implementation when the user picks a menu item or
	// double-clicks the icon.
	Start(tooltip string, cb TrayCallbacks) error
	// SetLocale updates the language used for localized tray menu
	// item labels. Menus are built on demand, so a subsequent
	// right-click reflects the new locale immediately. Safe to call
	// before or after Start.
	SetLocale(locale string)
	// Stop removes the icon and unhooks the message window. Safe
	// to call multiple times.
	Stop()
	// Active reports whether Start has been called and not yet
	// matched by a Stop.
	Active() bool
}

// NewTray returns the Tray implementation appropriate for the
// current OS. `iconPng` is the raw bytes of the application icon
// (build/appicon.png) used for the tray icon; it may be nil on
// platforms without tray support. On unsupported platforms it
// returns nil and the caller should treat the feature as absent.
func NewTray(iconPng []byte) Tray {
	return newPlatformTray(iconPng)
}