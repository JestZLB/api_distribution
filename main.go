package main

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

// App icon (and tray icon) source. Wails builds it into the exe for
// the window; we embed it here too so the system tray can build its
// own HICON from the PNG without relying on exe resources at runtime.
//
//go:embed build/appicon.png
var appIconPng []byte

func main() {
	// Determine default config directory
	configDir, err := defaultConfigDir()
	if err != nil {
		println("Warning: could not determine config directory:", err.Error())
	}

	app := NewApp(configDir, "")

	// `HideWindowOnClose` is a boot-time option, so we look at the
	// saved config *before* handing the App to wails.Run. The
	// OnBeforeClose callback then hides the window instead of
	// quitting when the user enabled the feature.
	closeToTray := app.cfgMgr.Get().CloseToTray
	beforeClose := func(_ context.Context) (prevent bool) {
		// A deliberate quit (tray "Quit" / Settings "Quit app") sets
		// forceQuit and MUST exit for real. Only an ordinary window
		// close (X button) should hide to tray.
		if closeToTray && !app.forceQuitting() && app.ctx != nil {
			wruntime.WindowHide(app.ctx)
			return true
		}
		return false
	}

	err = wails.Run(&options.App{
		Title:             "API Distribution",
		Width:             1280,
		Height:            820,
		MinWidth:          960,
		MinHeight:         600,
		AssetServer:       &assetserver.Options{Assets: assets},
		BackgroundColour:  &options.RGBA{R: 250, G: 250, B: 249, A: 1},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		OnBeforeClose:     beforeClose,
		HideWindowOnClose: closeToTray,
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})

	if err != nil {
		println("Error:", err.Error())
		os.Exit(1)
	}
}

func defaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".api_distribution")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// silence unused import linting when fmt is removed
var _ = fmt.Sprintf
