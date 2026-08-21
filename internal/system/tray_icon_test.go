//go:build windows

package system

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCreateIconFromPNG verifies the pure-Go PNG→HICON path used for
// the tray icon works and doesn't return 0 (which would fall back to
// the stock icon).
func TestCreateIconFromPNG(t *testing.T) {
	png, err := os.ReadFile(filepath.Join("..", "..", "build", "appicon.png"))
	if err != nil {
		t.Skipf("appicon.png not available: %v", err)
	}
	if len(png) == 0 {
		t.Fatal("appicon.png is empty")
	}
	if h := createIconFromPNG(png); h == 0 {
		t.Fatal("createIconFromPNG returned 0 — icon build failed")
	} else {
		t.Logf("createIconFromPNG produced HICON=%x", h)
	}
	if h := trayIcon(nil); h == 0 {
		t.Fatal("trayIcon(nil) fallback returned 0")
	}
}