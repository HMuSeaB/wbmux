//go:build windows

// Package console 处理终端输出编码与 ANSI 转义支持。
package console

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procSetConsoleOutputCP = kernel32.NewProc("SetConsoleOutputCP")
	procSetConsoleCP       = kernel32.NewProc("SetConsoleCP")
	procGetConsoleMode     = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode     = kernel32.NewProc("SetConsoleMode")
)

// enableVirtualTerminalProcessing 是 SetConsoleMode 的 ANSI 转义开关。
const enableVirtualTerminalProcessing = 0x0004

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
