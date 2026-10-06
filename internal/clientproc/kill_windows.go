//go:build windows

package clientproc

// 终止正在运行的客户端进程。
//
// # 为什么用 taskkill 而不是 TerminateProcess
//
// 客户端是 Electron 程序，一个"应用"底下挂着好几个进程（主进程、渲染进程、
// GPU 进程、效用进程…）。TerminateProcess 只能按 PID 杀**一个**，
// 剩下的孤儿进程会继续占着用户数据目录的锁 —— 表现为"重启后打不开"或
// "配置读的是旧的"。taskkill /T 按进程树杀，才是"关掉这个应用"。
//
// # 为什么先温和再强制
//
// 直接 /F 等于断电式结束：客户端来不及把会话写盘，正在跑的一轮对话会丢。
// (附带一提，用户 2026-10-05 就遇到过蓝屏导致 ZCode 索引没落盘的事故。)
// 所以先发不带 /F 的（相当于请它正常退出），等一会儿；还不走才强杀。

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// hideWindow 让子进程不弹控制台窗口。
// 界面多半是双击启动的，子进程再闪一个黑窗口会让人以为出错。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// pidsOf 列出某镜像名的所有 PID。
func pidsOf(execName string) []string {
	cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq "+execName, "/NH", "/FO", "CSV")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var pids []string
	// CSV 每行形如 "WorkBuddy.exe","1234","Console","1","123,456 K"
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(line), strings.ToLower(`"`+execName+`"`)) {
			continue
		}
		fields := strings.Split(line, `","`)
		if len(fields) >= 2 {
			pid := strings.Trim(strings.TrimSpace(fields[1]), `"`)
			if pid != "" && pid != "0" {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

// Terminate 关掉某个客户端的所有进程。
//
// 返回"确实关掉了几个进程"。一个都没关到也算成功——用户可能自己已经关了，
// 那种情况下调用方应该照常重新启动。
func Terminate(execName string) (int, error) {
	if !strings.HasSuffix(strings.ToLower(execName), ".exe") {
		execName += ".exe"
	}

	pids := pidsOf(execName)
	if len(pids) == 0 {
		return 0, nil
	}

	// 第一轮：不带 /F，相当于"请它自己退"。/T 带上整棵进程树。
	// 用 /PID 逐个指定，避免 /IM 把同名进程全杀光（用户可能同时开了
	// 国内版和国际版，名字不同，但保险起见按 PID 更精确）。
	for _, pid := range pids {
		c := exec.Command("taskkill", "/T", "/PID", pid)
		hideWindow(c)
		_ = c.Run()
	}

	// 给它一点时间落盘。客户端退出时要写会话文件和设置，
	// 太急会正好打断这个过程。
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if len(pidsOf(execName)) == 0 {
			return len(pids), nil
		}
		time.Sleep(400 * time.Millisecond)
	}

	// 第二轮：还不走就强杀。这一步会丢未落盘的内容，所以放最后。
	left := pidsOf(execName)
	if len(left) == 0 {
		return len(pids), nil
	}
	for _, pid := range left {
		c := exec.Command("taskkill", "/F", "/T", "/PID", pid)
		hideWindow(c)
		_ = c.Run()
	}

	time.Sleep(700 * time.Millisecond)
	if still := pidsOf(execName); len(still) > 0 {
		return len(pids), fmt.Errorf("还有 %d 个进程没关掉（可能被别的程序占用或需要管理员权限）", len(still))
	}
	return len(pids), nil
}
