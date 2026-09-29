//go:build !windows

package credential

import "os/exec"

// hideWindow 在无窗口概念的平台上不做处理。
func hideWindow(cmd *exec.Cmd) {}
