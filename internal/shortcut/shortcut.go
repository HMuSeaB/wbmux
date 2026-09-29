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
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/tray"
)

// ExePath 返回"可以放心写进快捷方式"的程序路径。
//
// # 为什么不能直接用 os.Executable()
//
// `os.Executable()` 给的是**当前进程的** exe 路径，而它可能是编译临时目录
// 里的产物：`go run`、`go build -o %TEMP%\x.exe`、IDE 的调试构建都属于这类。
// 把那种路径写进桌面快捷方式，产物被清理后用户双击就是"没反应"——而且
// 从图标上完全看不出问题（2026-09-28 实测：建出来的 .lnk 指向
// `%LOCALAPPDATA%\go-tmp\go-build*\<hash>\b001\exe\wbmux.exe`，那个目录
// 早被清掉了）。
//
// 判据用"路径里有没有编译器临时目录的痕迹"而不是"在不在 PATH 里"：
// 后者会把用户在 D:\Tools 下手动放的 exe 也误判成不安全。
//
// 返回的第二个值说明是否是退而求其次的结果，供调用方给用户一句提醒。
func ExePath() (string, bool) {
	exe, err := os.Executable()
	if err == nil {
		if abs, aerr := filepath.Abs(exe); aerr == nil {
			exe = abs
		}
		if !looksTemporary(exe) {
			return exe, true
		}
	}

	// 退而求其次：exe 现在待在临时目录里，但**自己**会被复制到
	// 设置目录下的 bin/ 再指过去——否则这条命令等于骗人。
	//
	// 复制目标同样要过 looksTemporary：设置目录也可能落在临时目录里
	// （隔离测试、便携版、把 %USERPROFILE% 指到临时盘的场景）。指向那种
	// 位置和指向编译产物一样不靠谱，所以这时老实报告"做不到"。
	self, err := os.Executable()
	if err != nil {
		return "", false
	}
	dst, err := selfCopy(self)
	if err != nil {
		return self, false // 复制不成就老实指向原位，由调用方提醒
	}
	if looksTemporary(dst) {
		return dst, false
	}
	return dst, true
}

// looksTemporary 判断一个 exe 路径是不是"随时会被清理"的编译器产物。
func looksTemporary(exe string) bool {
	if strings.Contains(exe, `\go-build`) || strings.Contains(exe, "/go-build") {
		return true
	}
	tmp := os.TempDir() // %TEMP%
	if tmp != "" && strings.HasPrefix(strings.ToLower(exe), strings.ToLower(tmp)) {
		return true
	}
	if dir := os.Getenv("GOTMPDIR"); dir != "" &&
		strings.HasPrefix(strings.ToLower(exe), strings.ToLower(dir)) {
		return true
	}
	// 进程自己就在临时目录里跑，基本可以断定是调试/临时构建。
	return false
}

// selfCopy 把自己复制到 <设置目录>/bin/wbmux.exe，返回新路径。
//
// 复制而不是移动：调用方可能正在跑（界面里的按钮），移动正在运行的 exe
// 在 Windows 上会失败；留一份原地副本也无害。
func selfCopy(src string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("创建 %s 失败：%w", binDir, err)
	}
	dst := filepath.Join(binDir, "wbmux.exe")
	config.GuardRealWrite(dst)
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("读取自身失败：%w", err)
	}
	if err := os.WriteFile(dst, raw, 0o755); err != nil {
		return "", fmt.Errorf("写入 %s 失败：%w", dst, err)
	}
	return dst, nil
}

// desktopOverride 让测试把"桌面"指到临时目录。
//
// # 为什么必须有这个接缝（2026-09-29 真事故）
//
// 快捷方式的落点是**固定的**「<桌面>\wbmux.lnk」。测试里调 createFor 时，
// 它建出来的东西与用户真实的快捷方式**同名同路径**——于是测试的清理
// （`t.Cleanup(os.Remove(path))`）删掉的是用户桌面上那一份。
//
// 回收站里躺着 5 个 `C:\Users\36230\Desktop\wbmux.lnk` 就是证据：
// 每次跑 `go test ./internal/shortcut/` 都会把用户的快捷方式删一次，
// 用户看到的是"桌面的快捷方式又不见了"（2026-09-28、09-29 各反馈过一次）。
//
// 桌面路径原本写死在 shell 脚本里的 `GetFolderPath('Desktop')`，测试够不着。
// 这个变量把那一行换成可控值；正常运行时为空，行为与从前完全一致。
var desktopOverride string

// DesktopForTest 把"桌面"临时指到 dir，返回恢复函数。
// 名字里带 ForTest 是想让它在生产代码里显眼——**只有测试该调它**。
func DesktopForTest(dir string) (restore func()) {
	prev := desktopOverride
	desktopOverride = dir
	return func() { desktopOverride = prev }
}

// Create 在桌面创建（或覆盖）wbmux.lnk，返回快捷方式的完整路径。
func Create() (string, error) {
	exe, stable := ExePath()
	if exe == "" {
		return "", errors.New("拿不到当前程序路径")
	}
	if !stable {
		// 不静默降级：指向一个"随时会被清理"的路径，用户下次双击就没反应，
		// 而那时更想不起来是这里的问题。
		return "", fmt.Errorf("当前程序位于临时目录（%s），从这里建快捷方式会指向随时会被清理的副本；"+
			"请改用正式安装/下载的那份 wbmux.exe", exe)
	}
	return createFor(exe)
}

// createFor 按给定的 exe 路径建快捷方式。抽出来是为了让测试能指定目标，
// 不必依赖"测试进程自己恰好待在稳定目录"。
func createFor(exe string) (string, error) {
	workdir := filepath.Dir(exe)

	// PowerShell 单引号字符串里唯一的转义是单引号本身（双写），
	// 所以所有插值都用单引号形式，路径里有空格、中文、括号都不怕。
	psQuote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

	// 图标：把托盘那把"自画 ⇄"落成 .ico 文件，快捷方式 IconLocation 指着它。
	// exe 本体不嵌资源（见 tray/icon.go 的取舍），不指 .ico 的快捷方式
	// 就是用户见到的"白纸"图标（2026-09-28 反馈"没图标啊"）。
	// 写失败不挡路：快捷方式照样创建，只是图标是默认的。
	icoSet := ""
	if dir, derr := config.Dir(); derr == nil {
		icoPath := filepath.Join(dir, "wbmux.ico")
		if werr := os.WriteFile(icoPath, tray.ICOFileBytes(16, 32, 48, 256), 0o644); werr == nil {
			// icoPath 走 PowerShell 单引号；IconLocation 的 ",0"（取第 0 号
			// 图标）要在双引号串里做变量插值。
			icoSet = "$ico = " + psQuote(icoPath) + "; $s.IconLocation = \"$ico,0\"; "
		}
	}

	// 输出编码显式钉成 UTF-8：桌面路径可能含中文（OneDrive 重定向的
	// 目录名尤其常见），不钉的话 PowerShell 按 OEM 码页（GBK）输出，
	// Go 这边按 UTF-8 读就是乱码。
	//
	// 结尾用 Set-StrictMode + 空串兜底：脚本里任何一步"静默失败"都要变成
	// 非零退出。教训来自 2026-09-28——命令**报成功、文件却没被写**（lnk 时间戳
	// 一动不动），因为 COM 调用没抛错而 Save() 也没生效，两边都看不出来。
	// 桌面目录：默认走系统 API（能正确处理 OneDrive 重定向），
	// 测试注入时用注入值——见 desktopOverride 的注释
	// （不注入的话，测试的清理会删到用户桌面上的真快捷方式）。
	desktopExpr := "$d = [Environment]::GetFolderPath('Desktop'); "
	if desktopOverride != "" {
		desktopExpr = "$d = " + psQuote(desktopOverride) + "; "
	}

	script := "$ErrorActionPreference = 'Stop'; " +
		"trap { [Console]::Error.WriteLine($_.Exception.Message); exit 9 }; " +
		"[Console]::OutputEncoding = [Text.Encoding]::UTF8; " +
		"$ws = New-Object -ComObject WScript.Shell; " +
		desktopExpr +
		"if (-not (Test-Path $d)) { throw \"找不到桌面目录：$d\" }; " +
		"$lnk = Join-Path $d 'wbmux.lnk'; " +
		"$s = $ws.CreateShortcut($lnk); " +
		"$s.TargetPath = " + psQuote(exe) + "; " +
		"$s.WorkingDirectory = " + psQuote(workdir) + "; " +
		"$s.Description = 'wbmux — 用一份安装连两套后端'; " +
		// **先落盘一次，再设图标**：CreateShortcut 返回的对象在 Save 之前
		// 没有 IconLocation 属性（实测报 "The property 'IconLocation' cannot
		// be found on this object"）。它原来的写法把图标和其它属性一起设完
		// 再 Save，于是设图标那步抛错、而错误没被接住（既无
		// ErrorActionPreference 也无 trap），表现为"命令报成功、lnk 根本没写"。
		"$s.Save(); " +
		icoSet +
		"$s.Save(); " +
		"if (-not (Test-Path $lnk)) { throw \"Save() 之后仍找不到 $lnk\" }; " +
		"Write-Output $lnk"

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	// 图形界面多半是双击启动的，父进程自己已经把控制台藏了；
	// 此时子进程再弹一个黑窗口闪一下，用户会以为程序出错。
	hideConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// 把 PowerShell 的报错带出来。只给 "exit status 1" 等于什么都没说，
		// 用户会以为功能坏了却不知道为什么。
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("创建快捷方式失败：%s", detail)
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
