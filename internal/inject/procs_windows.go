//go:build windows

package inject

import (
	"os/exec"
	"strconv"
	"strings"
)

// clientListeningPorts 返回客户端主进程正在监听的端口。
//
// 为什么要这么绕：WorkBuddy 用 --remote-debugging-port=0 启动时**不写**
// DevToolsActivePort（其他 Electron 应用如 Antigravity、Qoder 都会写），
// 但 Chromium 确实监听了随机端口。2026-11-20 实测：15462 是 CDP，
// 18488 是内部服务——只能把进程的监听端口都拿出来，让上层逐个探测。
func clientListeningPorts() []int {
	pids := clientPIDs()
	if len(pids) == 0 {
		return nil
	}
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return nil
	}
	var ports []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// 形如: TCP 127.0.0.1:15462 0.0.0.0:0 LISTENING 45156
		if len(f) < 5 || f[3] != "LISTENING" || !pids[f[4]] {
			continue
		}
		idx := strings.LastIndex(f[1], ":")
		if idx < 0 {
			continue
		}
		if p, err := strconv.Atoi(f[1][idx+1:]); err == nil && p > 0 {
			ports = append(ports, p)
		}
	}
	return ports
}

// clientPIDs 取所有 WorkBuddy.exe 主进程的 PID。
// 只用来过滤 netstat 结果，拿不全也不影响正确性（少几个候选而已）。
func clientPIDs() map[string]bool {
	out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq WorkBuddy.exe", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil
	}
	pids := make(map[string]bool, 8)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "信息") || strings.HasPrefix(line, "INFO") {
			continue
		}
		f := strings.SplitN(line, `","`, 3)
		if len(f) < 2 {
			continue
		}
		pid := strings.Trim(f[1], `"`)
		if pid != "" {
			pids[pid] = true
		}
	}
	return pids
}
