//go:build unix

package launch

import (
	"os/exec"
	"syscall"
)

// detach 让子进程脱离父进程的会话与进程组，
// 这样父进程退出后子进程继续存活，也不会收到父进程终端发出的信号。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
