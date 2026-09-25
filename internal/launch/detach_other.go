//go:build !windows && !unix

package launch

import "os/exec"

// detach 在缺少会话概念的平台上不做特殊处理，直接继承父进程属性。
func detach(cmd *exec.Cmd) {}
