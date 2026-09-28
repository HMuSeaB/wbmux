// Package shortcut 创建启动 wbmux 的桌面快捷方式。
//
// # 为什么用 PowerShell 而不是手写 .lnk
//
// .lnk 是二进制格式（shell item id list、link flags、字符串编码开关…），
// 手写极易产生"看着能点、双击报错"的残废文件。Windows 自带的 WScript.Shell
// COM 一行就能写标准 .lnk，而且 [Environment]::GetFolderPath('Desktop')
// 能正确处理 OneDrive 重定向的桌面路径——这个细节手写必错。
//
// # 覆盖语义
//
// CreateShortcut 对已存在的同名 .lnk 是覆盖写。这是特性不是副作用：
// exe 挪了位置（比如升级换目录）后重新点一次创建，快捷方式就自动跟上。
package shortcut

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Create 在桌面创建（或覆盖）wbmux.lnk，指向当前正在运行的 exe，
// 返回快捷方式的完整路径。
func Create() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	workdir := filepath.Dir(exe)

	// PowerShell 单引号字符串里唯一的转义是单引号本身（双写），
	// 所以所有插值都用单引号形式，路径里有空格、中文、括号都不怕。
	psQuote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

	// 输出编码显式钉成 UTF-8：桌面路径可能含中文（OneDrive 重定向的
	// 目录名尤其常见），不钉的话 PowerShell 按 OEM 码页（GBK）输出，
	// Go 这边按 UTF-8 读就是乱码。
	script := "[Console]::OutputEncoding = [Text.Encoding]::UTF8; " +
		"$ws = New-Object -ComObject WScript.Shell; " +
		"$d = [Environment]::GetFolderPath('Desktop'); " +
		"$s = $ws.CreateShortcut(\"$d\\wbmux.lnk\"); " +
		"$s.TargetPath = " + psQuote(exe) + "; " +
		"$s.WorkingDirectory = " + psQuote(workdir) + "; " +
		"$s.Description = 'wbmux — 用一份安装连两套后端'; " +
		"$s.Save(); " +
		"Write-Output (Join-Path $d 'wbmux.lnk')"

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	// 图形界面多半是双击启动的，父进程自己已经把控制台藏了；
	// 此时子进程再弹一个黑窗口闪一下，用户会以为程序出错。
	hideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("创建快捷方式失败：%w", err)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", errors.New("PowerShell 没有返回快捷方式路径")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("快捷方式写完却找不到（%s）：%w", path, err)
	}
	return path, nil
}
