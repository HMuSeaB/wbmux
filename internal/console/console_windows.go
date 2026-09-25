//go:build windows

// Package console 处理终端输出编码与 ANSI 转义支持。
package console

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                  = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP    = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP          = kernel32.NewProc("SetConsoleCP")
	procGetConsoleMode        = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode        = kernel32.NewProc("SetConsoleMode")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procGetConsoleWindow      = kernel32.NewProc("GetConsoleWindow")

	// ShowWindow 在 user32 里，不在 kernel32。
	//
	// 这里曾经写成 kernel32.NewProc("ShowWindow")，而 syscall 的 LazyProc
	// 找不到函数时会**直接 panic**。后果极其隐蔽：双击启动时才会走到
	// HideConsoleWindow，panic 信息正好被写进那个刚被隐藏的控制台，
	// 用户看到的只有"双击了，什么都没发生"，浏览器那边则因为服务端
	// 已经死掉而显示 ERR_CONNECTION_REFUSED。
	user32         = syscall.NewLazyDLL("user32.dll")
	procShowWindow = user32.NewProc("ShowWindow")
)

// enableVirtualTerminalProcessing 是 SetConsoleMode 的 ANSI 转义开关。
const enableVirtualTerminalProcessing = 0x0004

// swHide 是 ShowWindow 的 SW_HIDE。
const swHide = 0

// EnableUTF8 把控制台代码页切到 UTF-8，并尝试开启 ANSI 转义支持。
//
// Windows 控制台默认使用本地代码页（简体中文环境为 936），
// 直接输出 UTF-8 中文会变成乱码。两项设置失败都不影响程序运行：
// 输出重定向到文件时本就不是控制台，取不到 mode 属正常情况。
func EnableUTF8() {
	_, _, _ = procSetConsoleOutputCP.Call(65001)
	_, _, _ = procSetConsoleCP.Call(65001)
	enableVT()
}

func enableVT() {
	handle := syscall.Handle(os.Stdout.Fd())

	var mode uint32
	ok, _, _ := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	if ok == 0 {
		return
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return
	}
	_, _, _ = procSetConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing))
}

// IsExclusiveConsole 判断当前进程是否独占一个控制台。
//
// 用途：区分"用户双击了 exe"和"用户在终端里敲了命令"。
// 控制台子系统（-H=console，即默认）的程序被双击时，Windows 会**新开**一个
// 控制台，此时控制台里只有我们一个进程；而在 cmd / PowerShell / Windows
// Terminal 里运行时，shell 本身也挂在这个控制台上，进程数必然大于 1。
//
// 这比"看有没有父进程"或"看父进程是不是 explorer.exe"简单得多，
// 也不需要遍历进程快照。
//
// 注意 GetConsoleProcessList 的返回值语义：缓冲区不足时返回所需容量，
// 足够时返回实际写入的个数。我们只关心"是不是恰好 1 个"，给 2 个元素的
// 缓冲即可区分 1 与大于 1。
func IsExclusiveConsole() bool {
	var pids [2]uint32
	n, _, _ := procGetConsoleProcessList.Call(
		uintptr(unsafe.Pointer(&pids[0])),
		uintptr(len(pids)),
	)
	return n == 1
}

// HideConsoleWindow 隐藏当前控制台窗口。
//
// 双击启动图形界面时用：控制台子系统的程序双击必然先弹出一个黑窗口，
// 我们没法阻止它出现，但可以立刻把它藏掉。
//
// 这里的每一步都刻意不 panic：调用点正好在"收起控制台"的动作上，
// 一旦这里出事，报错信息会写进一个用户看不见的窗口——等于把故障藏起来。
// 宁可藏不掉窗口，也不能把进程带走。
func HideConsoleWindow() {
	// 先确认 ShowWindow 能解析到。曾经把它写成 kernel32 的 proc，
	// 而 syscall 的 LazyProc 在找不到函数时是直接 panic 的。
	if err := procShowWindow.Find(); err != nil {
		return
	}
	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(hwnd, swHide)
}
