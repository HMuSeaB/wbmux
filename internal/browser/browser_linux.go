//go:build linux

package browser

import (
	"fmt"
	"os/exec"
	"syscall"
)

// detach 让浏览器脱离父进程的会话与进程组。
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// appModeCommands 按发行版常见命名依次尝试。
//
// Linux 上浏览器装在 PATH 里而不是固定目录，因此 need 留空，
// 由 resolvable 走 LookPath 判断。用 snap/flatpak 安装的变体命名各异
// （如 microsoft-edge、google-chrome-stable），这里覆盖最常见的几种，
// 命中不了就退回 xdg-open，不影响功能。
func appModeCommands(url string) []appModeCmd {
	names := []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"microsoft-edge",
		"microsoft-edge-stable",
	}
	var out []appModeCmd
	for _, n := range names {
		out = append(out, appModeCmd{argv: []string{n, "--app=" + url}})
	}
	return out
}

// openDefault 交给桌面环境的默认浏览器。
func openDefault(url string) error {
	cmd := exec.Command("xdg-open", url)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("browser: 无法打开浏览器: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
