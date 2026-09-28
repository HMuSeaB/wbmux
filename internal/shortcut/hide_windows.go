//go:build windows

package shortcut

import (
	"os/exec"
	"syscall"
)

// hideConsole 让子进程不弹控制台窗口。
//
// 图形界面多半是双击启动的，父进程自己已经把控制台藏了；
// 此时子进程再弹一个黑窗口闪一下，用户会以为程序出错。
// （与 migrate 里的 hideWindow 同款；单独复制一份是因为它依赖
// Windows 专属的 SysProcAttr 字段，而本包要在三平台编译。）
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
