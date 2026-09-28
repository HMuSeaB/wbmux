//go:build !windows

package shortcut

import "os/exec"

// hideConsole 在非 Windows 平台是空操作：那里没有"控制台窗口"可藏，
// PowerShell 也不存在（Create 会在调用层面报错，见其文档）。
func hideConsole(cmd *exec.Cmd) {}
