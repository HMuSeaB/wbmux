//go:build !windows

// Package tray 在非 Windows 平台暂不提供托盘。
//
// 这里刻意不做"空实现假装成功"：Available 返回 false，调用方据此走回
// 原来的等待方式（等 Ctrl+C 或界面请求关闭）。假装有托盘会让
// `wbmux gui` 在 macOS/Linux 上静默失去唯一的退出入口。
package tray

import "errors"

// Available 报告当前平台是否支持托盘。
//
// macOS 与 Linux 的通知区域机制差异很大（前者是 NSStatusBar，后者是
// 各种桌面环境自己的 StatusNotifier / XEmbed 协议），不是加几十行能覆盖的。
// 与其做个半吊子，不如明确不支持，交给后续单独设计。
func Available() bool { return false }

// Options 是托盘的行为回调。
type Options struct {
	Tooltip string
	OnOpen  func()
	OnQuit  func()
	// Ready 在图标进入通知区域后被调用一次，可为 nil。
	Ready func()
}

// Run 在非 Windows 平台直接报错，不应被调用。
func Run(opts Options) error {
	return errors.New("tray: 当前平台暂不支持系统托盘")
}
