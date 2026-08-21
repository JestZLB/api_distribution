//go:build windows

package system

import "testing"

// TestTrayMenuStrings verifies the localized tray menu labels for the
// supported locales and the en-US fallback for unknown/empty input.
func TestTrayMenuStrings(t *testing.T) {
	tests := []struct {
		name      string
		locale    string
		wantShow  string
		wantQuit  string
	}{
		{name: "en-US", locale: "en-US", wantShow: "Show main window", wantQuit: "Quit"},
		{name: "zh-CN", locale: "zh-CN", wantShow: "显示主窗口", wantQuit: "退出"},
		{name: "ja-JP", locale: "ja-JP", wantShow: "メインウィンドウを表示", wantQuit: "終了"},
		{name: "ko-KR", locale: "ko-KR", wantShow: "메인 창 표시", wantQuit: "종료"},
		{name: "unknown falls back to en-US", locale: "xx-XX", wantShow: "Show main window", wantQuit: "Quit"},
		{name: "empty falls back to en-US", locale: "", wantShow: "Show main window", wantQuit: "Quit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			show, quit := trayMenuStrings(tt.locale)
			if show != tt.wantShow {
				t.Errorf("show = %q, want %q", show, tt.wantShow)
			}
			if quit != tt.wantQuit {
				t.Errorf("quit = %q, want %q", quit, tt.wantQuit)
			}
		})
	}
}