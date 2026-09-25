//go:build !windows

// Package console 处理终端输出编码。
package console

// EnableUTF8 在非 Windows 平台上是空操作，终端默认即为 UTF-8。
func EnableUTF8() {}
