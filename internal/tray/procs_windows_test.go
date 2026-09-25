//go:build windows

package tray

import "testing"

// TestProcsResolve 钉住"把 API 挂在错的 DLL 上"这类错误。
//
// 见 internal/console 里同名测试的说明：ShowWindow 曾被挂到 kernel32，
// 而 syscall 的 LazyProc 找不到函数时会直接 panic。
// 托盘的调用点同样是"出事就没声音"的场景，所以一并钉住。
func TestProcsResolve(t *testing.T) {
	cases := []struct {
		name string
		proc interface{ Find() error }
	}{
		{"RegisterClassExW", procRegisterClassExW},
		{"CreateWindowExW", procCreateWindowExW},
		{"DefWindowProcW", procDefWindowProcW},
		{"GetMessageW", procGetMessageW},
		{"TranslateMessage", procTranslateMessage},
		{"DispatchMessageW", procDispatchMessageW},
		{"PostQuitMessage", procPostQuitMessage},
		{"CreatePopupMenu", procCreatePopupMenu},
		{"AppendMenuW", procAppendMenuW},
		{"TrackPopupMenu", procTrackPopupMenu},
		{"DestroyMenu", procDestroyMenu},
		{"SetForegroundWindow", procSetForegroundWnd},
		{"LoadIconW", procLoadIconW},
		{"GetCursorPos", procGetCursorPos},
		{"PostMessageW", procPostMessageW},
		{"DestroyWindow", procDestroyWindow},
		{"GetModuleHandleW", procGetModuleHandleW},
		{"Shell_NotifyIconW", procShellNotifyIconW},
	}
	for _, c := range cases {
		if err := c.proc.Find(); err != nil {
			t.Errorf("%s 解析失败（挂错 DLL 或函数名拼错）: %v", c.name, err)
		}
	}
}
