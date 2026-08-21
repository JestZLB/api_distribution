//go:build windows

package system

import (
	"log"
	"testing"
	"time"
)

// TestTrayLiveEnable reproduces the "enable tray" path in isolation so a
// crash (access violation / unreadable output) is surfaced as a test
// failure instead of silently killing the whole app.
func TestTrayLiveEnable(t *testing.T) {
	tray := newPlatformTray(nil) // nil png → falls back to the stock icon; exercises start/stop + pump
	if tray == nil {
		t.Fatal("newPlatformTray returned nil")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic during tray enable: %v", r)
		}
	}()

	log.Printf("starting tray...")
	if err := tray.Start("test tip", noopTrayCallbacks{}); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	log.Printf("tray started, Active=%v", tray.Active())

	// Let the pump run a moment, then tear down.
	time.Sleep(500 * time.Millisecond)
	log.Printf("stopping tray...")
	tray.Stop()
	log.Printf("tray stopped")
}

type noopTrayCallbacks struct{}

func (noopTrayCallbacks) OnAction(TrayAction) {}
