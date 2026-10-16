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
//
// 匹配用**精确相等**而不是前缀：`WorkBuddy` 是 `WorkBuddyAI` 的子串，
// 前缀匹配会让"关国内版"顺手把国际版也列进来（反之亦然）。两个客户端
// 可以同时开着，各用各的数据目录——误杀另一个等于把用户没在用的那个
// 也关了，而它的界面和会话状态就白丢了。
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
		fields := splitCSV(line)
		if len(fields) < 2 {
			continue
		}
		if !strings.EqualFold(fields[0], execName) {
			continue
		}
		if fields[1] != "" && fields[1] != "0" {
			pids = append(pids, fields[1])
		}
	}
	return pids
}

// splitCSV 拆 tasklist 的一行 CSV，去掉每个字段外层的引号。
//
// tasklist 的 CSV 不接受逗号以外的转义，这里够用；
// **但要注意"没有匹配结果"时它不输出 CSV**，而是一句
// "信息: 没有运行的任务匹配指定标准。" —— 那种行拆出来字段数不足，
// 上面按 len(fields) 拦住即可。
func splitCSV(line string) []string {
	if line == "" {
		return nil
	}
	parts := strings.Split(line, `","`)
	for i := range parts {
		parts[i] = strings.Trim(parts[i], `"`)
	}
	return parts
}

// Terminate 关掉某个客户端的所有进程。
//
// 返回"确实关掉了几个进程"。一个都没关到也算成功——用户可能自己已经关了，
// 那种情况下调用方应该照常重新启动。
func Terminate(execName string) (int, error) {
	return terminateByNames([]string{execName})
}

// TerminateAny 关掉**正在运行的任意一个**客户端，返回关掉了几个进程。
//
// # 为什么需要它（2026-10-16 的真实故障）
//
// 「重启客户端」这个按钮的语义是"把我正在用的那个重启一下"，但实现是拿
// **目标后端**的可执行名去 tasklist 里找。只要用户点的后端与实际在跑的
// 那个不一致，就会"已关闭 0 个进程"——进程没关，新实例又被单实例锁挡住，
// 表现成"点了重启但什么都没变"（用户原话："面板里面的重启根本没用"）。
//
// 而两个名字长得很像（`WorkBuddy` / `WorkBuddyAI`）正是最容易踩的雷：
// 用户从界面上看不出自己点的是哪个后端，更看不出进程名对不上。
//
// 所以这里改成"按实际在跑的那个关"：把两侧的名字都试一遍，谁在跑关谁。
// 返回顺序固定（调用方传进来的顺序），保证行为可预期。
func TerminateAny(execNames ...string) (int, error) {
	return terminateByNames(execNames)
}

// target 是一个"待关闭的进程"：它的镜像名 + PID。
//
// 记下镜像名是为了后面重试时按**原名**再查一次——不能拿别的名字去查，
// 那会把另一个客户端的 PID 也算进来。
type target struct{ name, pid string }

func terminateByNames(candidates []string) (int, error) {
	// 逐个候选名收集 PID，并记住"每个 PID 是用哪条规则找到的"——
	// 后面重试时要按**原位图名**再查一次，不能拿别的名字去查
	// （那会把另一个客户端的 PID 也算进来）。
	var targets []target
	for _, raw := range candidates {
		name := raw
		if !strings.HasSuffix(strings.ToLower(name), ".exe") {
			name += ".exe"
		}
		for _, pid := range pidsOf(name) {
			targets = append(targets, target{name: name, pid: pid})
		}
	}
	if len(targets) == 0 {
		return 0, nil
	}

	// 第一轮：不带 /F，相当于"请它自己退"。/T 带上整棵进程树。
	// 用 /PID 逐个指定，避免 /IM 把同名进程全杀光。
	for _, t := range targets {
		c := exec.Command("taskkill", "/T", "/PID", t.pid)
		hideWindow(c)
		_ = c.Run()
	}

	// 给它一点时间落盘。客户端退出时要写会话文件和设置，
	// 太急会正好打断这个过程。
	if waitGone(targets, 6*time.Second) {
		return len(targets), nil
	}

	// 第二轮：还不走就强杀。这一步会丢未落盘的内容，所以放最后。
	for _, t := range aliveOf(targets) {
		c := exec.Command("taskkill", "/F", "/T", "/PID", t.pid)
		hideWindow(c)
		_ = c.Run()
	}

	// 强杀之后**不能只等一次就下结论**。
	//
	// 实测踩到：这里原本只 sleep 700ms 然后查一次，结果偶发报
	// "还有 1 个进程没关掉"——进程其实正在退出，只是 tasklist 还没反映过来
	// （tasklist 自己也要几百毫秒），而且它的退出还需要一点时间。
	// 这种假失败很坏：调用方会因此**拒绝启动新实例**，用户看到的是
	// "重启失败"，可客户端其实已经关掉了，等于卡在半路。
	//
	// 所以改成"给一段窗口、期间反复确认"：只要在窗口内观察到它没了就算成功。
	if waitGone(targets, 5*time.Second) {
		return len(targets), nil
	}

	left := aliveOf(targets)
	return len(targets), fmt.Errorf("还有 %d 个进程没关掉（可能被别的程序占用或需要管理员权限）", len(left))
}

// aliveOf 报告这批目标里还有哪些活着。
//
// 按**它自己的镜像名**重新查一次再比对 PID，而不是直接查名字计数：
// 同名进程可能在我们动手期间被用户新开一个，那不是我们的目标。
func aliveOf(targets []target) []target {
	live := map[string]bool{}
	for _, t := range targets {
		live[t.name] = true
	}
	now := map[string]map[string]bool{}
	for name := range live {
		set := map[string]bool{}
		for _, pid := range pidsOf(name) {
			set[pid] = true
		}
		now[name] = set
	}
	var out []target
	for _, t := range targets {
		if now[t.name][t.pid] {
			out = append(out, t)
		}
	}
	return out
}

// waitGone 在 window 时间内反复确认这批 PID 都没了。
func waitGone(targets []target, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for {
		if len(aliveOf(targets)) == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}
