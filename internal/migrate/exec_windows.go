//go:build windows

package migrate

import (
	"os/exec"
	"strings"
	"syscall"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// hideWindow 让子进程不弹控制台窗口。
//
// 图形界面多半是双击启动的，父进程自己已经把控制台藏了；
// 此时子进程再弹一个黑窗口闪一下，用户会以为程序出错。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// IsRunning 报告某档位的客户端是不是正在运行。
//
// 结论只用作警告，不阻断：SQLite 跑在 WAL 模式，多进程写同一个库是安全的，
// 真正的影响只是客户端把会话列表读进了内存，要重启才会刷新。
//
// 因此这里刻意"失败即当作没在运行"——误报"请先关闭客户端"比漏报更烦人，
// 用户看到明明关了还被拦会直接不信任这个功能。
func IsRunning(id variant.ID) bool {
	b, err := variant.Get(id)
	if err != nil {
		return false
	}
	name := b.WinExecutableName + ".exe"

	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq "+name, "/NH")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return false
	}

	// 命中时 tasklist 输出以镜像名开头的行；未命中时输出一句本地化的提示，
	// 那句话里不含镜像名。所以按行首比对，而不是全文 Contains——
	// 否则换个系统语言就可能把提示文案误判成"正在运行"。
	want := strings.ToLower(name)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(line)), want) {
			return true
		}
	}
	return false
}
