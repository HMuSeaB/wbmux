//go:build !windows

package migrate

import (
	"os/exec"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// hideWindow 在非 Windows 平台无需处理。
func hideWindow(cmd *exec.Cmd) {}

// IsRunning 在非 Windows 平台一律返回 false，即"没检测"。
//
// 这里刻意不留一个 pgrep 之类的启发式：各发行版与 macOS 上客户端进程名
// 并不统一，猜错会给出错误的警告。说"没检测"，调用方就不出这条警告，
// 漏一条提示比给一条假提示好。
func IsRunning(id variant.ID) bool { return false }
