//go:build windows

// Windows tray implementation built on shell32!Shell_NotifyIcon.
//
// Threading model: every Win32 window has its messages delivered to
// the OS thread that created it. This implementation does everything
// (window registration, window creation, shell icon registration,
// and the message pump) on a single goroutine pinned to its OS
// thread via runtime.LockOSThread. The tray window is a hidden
// top-level window (WS_EX_TOOLWINDOW, never shown) — a real HWND is
// required for Shell_NotifyIcon to register and route callbacks.
// Stop simply posts WM_QUIT to that thread.
//
// Limitations: We load IDI_APPLICATION as the icon because shipping
// a per-app .ico requires an extra build step. Real apps should load
// their own brand icon here.

package system

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Lazy handles for the Win32 procedures we use.
var (
	shell32                 = windows.NewLazySystemDLL("Shell32.dll")
	procShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	user32                  = windows.NewLazySystemDLL("User32.dll")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procLoadCursor          = user32.NewProc("LoadCursorW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	kernel32                = windows.NewLazySystemDLL("Kernel32.dll")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
)

// trayRegistry maps each tray's hidden window HWND to its *winTray
// owner. The wndproc is a static (Go-exported) function whose only
// arguments are (hwnd, msg, wParam, lParam) — there is no way to pass
// a *winTray to it via a closure, so we look the owner up from the
// HWND here. Previously the pointer was stashed via GWLP_USERDATA and
// read back with GetWindowLongPtrW, but that round-trip forces a
// uintptr→unsafe.Pointer conversion outside a syscall expression and
// trips `go vet`'s unsafeptr check. A sync.Map keyed by HWND has the
// same one-entry-per-tray lifetime and stays clear of that warning.
var trayRegistry sync.Map

// Win32 constants.
const (
	wmUser         = 0x0400
	wmQuit         = 0x0012
	wmCommand      = 0x0111
	wmContextMenu  = 0x007B
	wmDestroy      = 0x0002
	wmLButtonDbClk = 0x0203
	// wmRButtonUp is the legacy (pre-v4) right-click notification the
	// shell reports when the icon is NOT running under
	// NOTIFYICON_VERSION_4. v4 uses WM_CONTEXTMENU instead.
	wmRButtonUp    = 0x0205
	wmAppNotify    = wmUser + 20
	wsExToolwindow = 0x00000080
	idcArrow       = 32512
	idiApp         = 32512
	idMenuShow     = 1001
	idMenuQuit     = 1002
	nifMessage     = 0x00000001
	nifIcon        = 0x00000002
	nifTip         = 0x00000004
	nimAdd         = 0x00000000
	nimSetVersion  = 0x00000004
	nimDelete      = 0x00000002
	// notifyIconVersion4 enables the waiter/Shell_NotifyIcon
	// callback behaviour where the shell reports button presses as
	// WM_CONTEXTMENU (right-click) / WM_LBUTTONDBLCLK (double-click)
	// in lParam instead of the legacy per-button-up messages.
	notifyIconVersion4 = 4
	tpmRightButton     = 0x0002
	tpmReturnCmd       = 0x0100
	tpmNonNotify       = 0x0080
	mfString           = 0x0000
	mfSeparator        = 0x0800
	pmNoYield          = 0x0002
)

// trayClassName is the unique window class for our hidden window.
const trayClassName = "APIDistributionTrayWnd_3"

// POINT for cursor positions.
type point struct{ X, Y int32 }

// NOTIFYICONDATAW mirrors shellapi.h's struct. Fields must stay in
// the same order as the SDK definition because we hand the pointer
// straight to Shell_NotifyIconW.
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         uint32
	HBalloonIcon     uintptr
}

// WNDCLASSEXW mirrors the SDK struct layout.
type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     windows.Handle
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

// MSG is the subset of Win32 MSG we read.
type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
}

// winTray is the Windows Tray implementation. Public methods are
// safe to call from any goroutine. All Win32 work runs on a single
// dedicated goroutine (`run`), which locks its OS thread so the
// window's message queue and the pump are pinned to the same
// thread. `ready` signals Start when the icon is registered (or
// unblocks with an error), and `done` signals Stop when the pump
// has exited.
type winTray struct {
	mu      sync.Mutex
	running bool
	cb      TrayCallbacks
	tip     string
	locale  string // BCP-47 tag; "" → en-US fallback for menu labels
	iconPng []byte // appicon.png bytes; used to build the HICON
	stopCh  chan struct{}
	done    chan struct{}
	ready   chan error
}

func newPlatformTray(iconPng []byte) *winTray {
	return &winTray{iconPng: iconPng}
}

// SetLocale stores the active language tag for localized menu labels.
// Menus are rebuilt on every right-click, so the next menu reflects
// the new locale immediately. Accepts any string; unknown tags fall
// back to en-US when the menu is built.
func (t *winTray) SetLocale(locale string) {
	t.mu.Lock()
	t.locale = locale
	t.mu.Unlock()
}

func (t *winTray) Active() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// Start registers the icon and starts the message pump. The call
// blocks until either the icon is registered (ready receives nil)
// or 2 seconds elapse (timeout), so callers can rely on the
// shell-icon being live by the time Start returns.
func (t *winTray) Start(tooltip string, cb TrayCallbacks) error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return nil
	}
	t.cb = cb
	t.tip = tooltip
	t.stopCh = make(chan struct{})
	t.done = make(chan struct{})
	t.ready = make(chan error, 1)
	t.mu.Unlock()

	SafeGo("tray.run", t.run)

	select {
	case err := <-t.ready:
		if err != nil {
			return err
		}
	case <-time.After(2 * time.Second):
		return fmt.Errorf("tray start timed out")
	}
	return nil
}

// Stop removes the icon and signals the pump to exit. The pump
// goroutine is responsible for destroying the window and the
// shell-icon entry; we just ask it to quit and wait for the done
// channel so the caller (shutdown path) can sequence cleanup
// deterministically.
func (t *winTray) Stop() {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return
	}
	close(t.stopCh)
	done := t.done
	t.running = false
	t.mu.Unlock()

	// SnapshotGrace: PostQuitMessage to the pump thread. Since run()
	// owns the same OS thread that created the window, this reaches
	// the pump directly.
	procPostQuitMessage.Call(0)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
}

// run is the tray's only worker goroutine. It owns the OS thread
// for its entire lifetime: registers the window class, creates the
// hidden message-only window, registers the shell-icon, and then
// runs the message pump until WM_QUIT. Everything happens on this
// thread so the window and the pump see the same message queue.
func (t *winTray) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)

	// Window class registration.
	className, err := windows.UTF16FromString(trayClassName)
	if err != nil {
		t.signalReady(fmt.Errorf("encode class name: %w", err))
		return
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	cursor, _, _ := procLoadCursor.Call(0, uintptr(idcArrow))
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   windows.NewCallback(trayWndProc),
		HInstance:     windows.Handle(hInstance),
		HCursor:       cursor,
		LpszClassName: &className[0],
	}
	// RegisterClassExW reports failure by returning ATOM == 0. Its
	// last-error is UNRELIABLE here: LazyProc.Call surfaces a stale
	// "operation completed successfully" (Errno 0) even on success,
	// so we MUST key the check off r1, never errno.
	if atom, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)), 0, 0); atom == 0 {
		le := windows.GetLastError()
		leCode := uintptr(0)
		if e, ok := le.(syscall.Errno); ok {
			leCode = uintptr(e)
		}
		t.signalReady(fmt.Errorf("register class failed: lastErr=%d", leCode))
		return
	}

	// Hidden top-level window. We never call ShowWindow and use
	// WS_EX_TOOLWINDOW (no taskbar button), so it stays invisible in
	// the UI — but it IS a real desktop HWND, which is required for
	// Shell_NotifyIcon to register the icon and route its callback
	// messages (right-click / double-click) to this thread's pump.
	// A message-only (HWND_MESSAGE) parent can make Shell_NotifyIcon
	// silently fail on some builds, which is why the icon never
	// appeared.
	hwnd, _, _ := procCreateWindowExW.Call(
		uintptr(wsExToolwindow),
		uintptr(unsafe.Pointer(&className[0])),
		0,
		0,
		0, 0, 0, 0,
		0,
		0,
		hInstance, 0,
	)
	if hwnd == 0 {
		t.signalReady(fmt.Errorf("create tray window: %s", windows.GetLastError()))
		return
	}

	// Bind HWND to *winTray so the static wndproc can dispatch.
	trayRegistry.Store(hwnd, t)

	// Register the shell-icon entry using the app's own icon built
	// from appicon.png (via trayIcon) if available; otherwise the
	// stock icon is used.
	hIcon := trayIcon(t.iconPng)
	tipUTF16, _ := windows.UTF16FromString(truncateTip(t.tip))
	nid := notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmAppNotify,
		HIcon:            hIcon,
	}
	copy(nid.SzTip[:], tipUTF16)
	procShellNotifyIcon.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&nid)))

	// Opt in to the modern callback contract. Without NIM_SETVERSION
	// the shell reports a right-click as the legacy WM_RBUTTONUP in
	// lParam, which onMessage never listens for — so the context
	// menu silently never opened. With NOTIFYICON_VERSION_4 the
	// shell delivers WM_CONTEXTMENU for right-click and
	// WM_LBUTTONDBLCLK for double-click in lParam instead.
	nid.UVersion = notifyIconVersion4
	procShellNotifyIcon.Call(uintptr(nimSetVersion), uintptr(unsafe.Pointer(&nid)))

	t.mu.Lock()
	t.running = true
	t.mu.Unlock()
	t.signalReady(nil)

	// Message pump: GetMessageW blocks until a message arrives or
	// WM_QUIT is posted. We don't poll the stopCh inside the loop
	// because WM_QUIT is the canonical exit signal — Stop posts
	// WM_QUIT and we break out here.
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 {
			// GetMessageW returns 0 / -1 on WM_QUIT or error.
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	// Tear down the icon and window. Best-effort — process is exiting.
	procShellNotifyIcon.Call(uintptr(nimDelete), uintptr(unsafe.Pointer(&nid)))
	trayRegistry.Delete(hwnd)
	procDestroyWindow.Call(hwnd)
}

func (t *winTray) signalReady(err error) {
	select {
	case t.ready <- err:
	default:
	}
}

// trayWndProc looks up the *winTray owner of the message window
// from trayRegistry (keyed by HWND) and forwards the message.
func trayWndProc(hwnd uintptr, m uint32, wParam, lParam uintptr) uintptr {
	if v, ok := trayRegistry.Load(hwnd); ok {
		t := v.(*winTray)
		if handled, ret := t.onMessage(hwnd, m, wParam, lParam); handled {
			return ret
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(m), wParam, lParam)
	return r
}

func (t *winTray) onMessage(hwnd uintptr, m uint32, wParam, lParam uintptr) (bool, uintptr) {
	switch m {
	case wmAppNotify:
		// The mouse event is in the LOWORD of lParam. HIWORD holds
		// the icon ID (1) when running under NOTIFYICON_VERSION_4,
		// so we MUST mask the low word before comparing — otherwise
		// right/double-click never match and the tray appears dead.
		// We also listen for the legacy wmRButtonUp in case the shell
		// didn't honour NIM_SETVERSION.
		switch uintptr(uint32(lParam) & 0xFFFF) {
		// Left/middle button double-click opens the main window.
		// Only a right-click (wmContextMenu / legacy wmRButtonUp)
		// pops the menu — matching common desktop-app behaviour.
		case wmLButtonDbClk:
			t.fire(ActionShow)
		case wmContextMenu, wmRButtonUp:
			t.showMenu(hwnd)
		}
		return true, 0
	case wmCommand:
		switch int(wParam & 0xFFFF) {
		case idMenuShow:
			t.fire(ActionShow)
		case idMenuQuit:
			t.fire(ActionQuit)
		}
		return true, 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return true, 0
	}
	return false, 0
}

// fire reads the current callback under lock and dispatches a tray
// action. Menu commands, menu selections and double-clicks all route
// through here.
func (t *winTray) fire(action TrayAction) {
	t.mu.Lock()
	cb := t.cb
	t.mu.Unlock()
	if cb != nil {
		cb.OnAction(action)
	}
}

// showMenu pops up the right-click menu anchored at the cursor. With
// TPM_RETURNCMD + TPM_NONOTIFY, TrackPopupMenu does NOT post a
// WM_COMMAND message — it returns the selected item's command id
// directly as its return value. We read that and dispatch to the
// callback, so Show/Quit actually fire.
func (t *winTray) showMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	t.mu.Lock()
	locale := t.locale
	t.mu.Unlock()
	showStr, quitStr := trayMenuStrings(locale)
	showText, _ := syscall.UTF16PtrFromString(showStr)
	quitText, _ := syscall.UTF16PtrFromString(quitStr)

	procAppendMenuW.Call(menu, mfString, idMenuShow, uintptr(unsafe.Pointer(showText)))
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	procAppendMenuW.Call(menu, mfString, idMenuQuit, uintptr(unsafe.Pointer(quitText)))

	// SetForegroundWindow BEFORE TrackPopupMenu so the popup opens in
	// the foreground and can receive keyboard focus in a single click.
	procSetForegroundWindow.Call(hwnd)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	cmd, _, _ := procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd|tpmNonNotify,
		uintptr(pt.X), uintptr(pt.Y),
		0, hwnd, 0,
	)

	// The selected menu id comes back in the low word. Dispatch it.
	switch uintptr(cmd) & 0xFFFF {
	case idMenuShow:
		t.fire(ActionShow)
	case idMenuQuit:
		t.fire(ActionQuit)
	}
}

func truncateTip(s string) string {
	const max = 127
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// trayMenuStrings returns the localized labels for the tray's two
// menu items given a BCP-47 language tag. Unknown or empty tags fall
// back to English. The menu is rebuilt per right-click by showMenu,
// so the current locale always wins.
func trayMenuStrings(locale string) (show, quit string) {
	texts := map[string][2]string{
		"en-US": {"Show main window", "Quit"},
		"zh-CN": {"显示主窗口", "退出"},
		"ja-JP": {"メインウィンドウを表示", "終了"},
		"ko-KR": {"메인 창 표시", "종료"},
	}
	if v, ok := texts[locale]; ok {
		return v[0], v[1]
	}
	return texts["en-US"][0], texts["en-US"][1]
}

var (
	_ = unsafe.Sizeof(notifyIconData{})
	_ = unsafe.Sizeof(wndClassEx{})
	_ = syscall.UTF16PtrFromString
)
