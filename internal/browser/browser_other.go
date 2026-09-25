//go:build !windows && !darwin && !linux

package browser

import (
	"fmt"
	"os/exec"
)

// detach 在不支持会话隔离的平台上不做特殊处理。
func detach(cmd *exec.Cmd) {}

// appModeCommands 在这些平台上没有可靠的实现，直接退回默认打开方式。
func appModeCommands(url string) []appModeCmd { return nil }

// openDefault 尝试用 xdg-open，失败则如实报错。
func openDefault(url string) error {
	cmd := exec.Command("xdg-open", url)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("browser: 无法打开浏览器: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
