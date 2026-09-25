//go:build !windows

// Package console 处理终端输出编码。
package console

// EnableUTF8 在非 Windows 平台上是空操作，终端默认即为 UTF-8。
func EnableUTF8() {}

// IsExclusiveConsole 在非 Windows 平台上恒为 false。
//
// "双击启动"是 Windows 资源管理器的概念，类 Unix 平台上不存在需要
// 区分的场景，因此一律返回 false，让无参数时走打印帮助的路径。
func IsExclusiveConsole() bool { return false }

// HideConsoleWindow 在非 Windows 平台上是空操作。
func HideConsoleWindow() {}
