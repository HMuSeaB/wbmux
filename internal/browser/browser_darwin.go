//go:build darwin

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

// appModeCommands 用 open 的 -n 强制新开实例、-a 指定应用包。
//
// 前提检查落在 .app 目录上，而不是 argv[0]（/usr/bin/open 永远存在）：
// 否则没装 Chrome 时也会尝试，且失败发生在 Start 之后，会被当成成功。
func appModeCommands(url string) []appModeCmd {
	apps := []string{
		"/Applications/Google Chrome.app",
		"/Applications/Microsoft Edge.app",
		"/Applications/Chromium.app",
	}
	var out []appModeCmd
	for _, app := range apps {
		out = append(out, appModeCmd{
			argv: []string{"open", "-na", app, "--args", "--app=" + url},
			need: app,
		})
	}
	return out
}

// openDefault 交给系统默认浏览器。
func openDefault(url string) error {
	cmd := exec.Command("open", url)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("browser: 无法打开浏览器: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
