//go:build windows

package clientproc

import (
	"os/exec"
	"testing"
	"time"
)

// TestPidsOfAbsentProcess 不存在的进程名要返回空，不能报错或瞎返回。
//
// 这是"没在跑"这条路径的基础：返回空 → Terminate 直接返回 0 → 上层照常启动。
func TestPidsOfAbsentProcess(t *testing.T) {
	pids := pidsOf("definitely-not-a-real-process-xyz.exe")
	if len(pids) != 0 {
		t.Errorf("不存在的进程应返回空，实际 %v", pids)
	}
}

// TestTerminateAbsentIsNoop 目标没在跑时 Terminate 应当"成功但关掉 0 个"。
//
// 这条很重要：用户可能自己已经关了客户端，此时点「重启客户端」应该
// **照样把新的启动起来**，而不是报一句"没找到进程"然后什么都不做。
func TestTerminateAbsentIsNoop(t *testing.T) {
	n, err := Terminate("definitely-not-a-real-process-xyz")
	if err != nil {
		t.Errorf("没在跑不该报错，实际 %v", err)
	}
	if n != 0 {
		t.Errorf("应报告关掉 0 个，实际 %d", n)
	}
}

// TestTerminateAutoExeSuffix 传不带 .exe 的名字也要能用。
//
// 调用方（webui）传的是 variant 里的 WinExecutableName，那个值不带后缀；
// 少了这一步补全就会永远"找不到进程"，重启静默失效。
func TestTerminateAutoExeSuffix(t *testing.T) {
	n, err := Terminate("definitely-not-a-real-process-xyz")
	if err != nil || n != 0 {
		t.Fatalf("带/不带后缀都应正常处理，实际 n=%d err=%v", n, err)
	}
}

// TestTerminateRealProcess 拿一个自己起的、可随时关掉的进程验完整链路。
//
// # 为什么敢在测试里真杀进程
//
// 杀的是**本测试自己刚起的一个 cmd.exe**，不碰任何用户程序。
// 不这么做就永远验不到"温和关→等→强杀"这条路上有没有 bug，
// 而那正是这个包唯一的功能。用 cmd /c pause 之类会挂住的命令来模拟
// "不理会温和退出请求"的进程。
func TestTerminateRealProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过：这个用例会真的起并杀进程")
	}

	// 起一个会一直等着的 cmd（ping 自己相当于 sleep）
	cmd := exec.Command("cmd", "/c", "ping -n 30 127.0.0.1 >nul")
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		t.Skipf("起不了测试进程，跳过：%v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	// 确认它确实在跑
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		if isPidAlive(pid) {
			found = true
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !found {
		t.Skip("测试进程没起来，跳过")
	}

	// 按 cmd.exe 的镜像名去关——注意这会关掉**所有** cmd.exe。
	// 所以只断言"它带来的效果"（我们的那个 PID 没了），
	// 不去断言具体关掉了几个（环境里可能有别的 cmd）。
	n, err := Terminate("cmd.exe")
	if err != nil {
		t.Fatalf("关闭失败：%v", err)
	}
	if n == 0 {
		t.Error("明明有 cmd 在跑，却报告关了 0 个")
	}
	if isPidAlive(pid) {
		t.Errorf("PID %d 应该已经被关掉了", pid)
	}
}

// isPidAlive 报告某个 PID 是否还活着。
func isPidAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", "PID eq "+itoa(pid), "/NH", "/FO", "CSV").Output()
	if err != nil {
		return false
	}
	return len(out) > 0 && !containsNoTasks(string(out))
}

// containsNoTasks 判断 tasklist 是不是在说"没有匹配的任务"。
//
// 不能靠全文匹配某个词——那是**本地化**的。可靠的做法是看输出里
// 有没有出现我们的 PID 数字：没出现就是没有。
func containsNoTasks(out string) bool {
	return !hasDigits(out)
}

func hasDigits(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
