//go:build windows

package console

import "testing"

// TestProcsResolve 钉住一类很难发现的错误：把某个 API 挂在错的 DLL 上。
//
// 背景：ShowWindow 曾经被写成 kernel32.NewProc，而它在 user32 里。
// syscall 的 LazyProc 找不到函数时**直接 panic**，而这个调用点恰好在
// "收起控制台"之后——panic 信息被写进一个用户看不见的窗口，现象是
// "双击了，什么都没发生"，排查了整整几轮才定位到。
//
// 这个测试把"声明的每个 proc 都必须能解析到"变成硬约束：改错 DLL、
// 拼错函数名，都会在 CI 上立刻红。
func TestProcsResolve(t *testing.T) {
	cases := []struct {
		name string
		proc interface{ Find() error }
	}{
		{"SetConsoleOutputCP", procSetConsoleOutputCP},
		{"SetConsoleCP", procSetConsoleCP},
		{"GetConsoleMode", procGetConsoleMode},
		{"SetConsoleMode", procSetConsoleMode},
		{"GetConsoleProcessList", procGetConsoleProcessList},
		{"GetConsoleWindow", procGetConsoleWindow},
		{"ShowWindow", procShowWindow},
	}
	for _, c := range cases {
		if err := c.proc.Find(); err != nil {
			t.Errorf("%s 解析失败（挂错 DLL 或函数名拼错）: %v", c.name, err)
		}
	}
}
