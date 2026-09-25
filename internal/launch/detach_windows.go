//go:build windows

package launch

import (
	"os/exec"
	"syscall"
)

const (
	// CREATE_NEW_PROCESS_GROUP 让子进程不受父进程控制台 Ctrl+C 影响。
	createNewProcessGroup = 0x00000200
	// DETACHED_PROCESS 让子进程完全脱离父进程的控制台。
	// 这样即使 wbmux 是从终端里启动的，关掉终端也不会带走客户端。
	detachedProcess = 0x00000008
)

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess,
	}
}
