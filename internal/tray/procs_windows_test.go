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

// TestTraySafeCallsWhenNotInited 确保托盘未初始化时调用 UpdateTooltip / NotifyInfo 安全不崩溃。
func TestTraySafeCallsWhenNotInited(t *testing.T) {
	// 在未调用 Run 前，状态为未初始化，应静默安全返回，绝不能 panic
	UpdateTooltip("测试提示")
	NotifyInfo("测试标题", "测试内容")
	Notify("警告标题", "警告内容")
}

// TestSetUTF16SafeTruncate 验证字符串超长时截断并严格保留尾部 0 终止符。
func TestSetUTF16SafeTruncate(t *testing.T) {
	buf := make([]uint16, 5)
	setUTF16(buf, "hello world")
	if buf[4] != 0 {
		t.Fatalf("末尾应为 0 终止符，得到 %d", buf[4])
	}
}

