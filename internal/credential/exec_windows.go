//go:build windows

package credential

import (
	"os/exec"
	"syscall"
)

// hideWindow 让 helper 子进程不弹控制台窗口。
//
// 不设它的话，每次解开信封（或诊断时试一次）都会闪一个黑窗口——
// 客户端主程序是 GUI 子系统程序，但 ELECTRON_RUN_AS_NODE 模式下它会
// 把自己当成控制台应用，于是控制台被拉起来。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
