package inject

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// devToolsActivePortCandidates 是各档位可能写入 DevToolsActivePort 的目录。
//
// 用 --remote-debugging-port=0 启动时，Chromium 会把实际端口写进
// <user-data-dir>/DevToolsActivePort 的第一行。不同客户端版本与启动路径
// 用的 user-data-dir 不完全一致（有的直接是 ~/.workbuddy，有的多一层 app），
// 所以三个都试一遍——读不到就是没有，不报错。
func devToolsActivePortCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".workbuddy"),
		filepath.Join(home, ".workbuddy", "app"),
		filepath.Join(home, ".workbuddy-ai"),
	}
}

// readDevToolsActivePort 解析 DevToolsActivePort，返回第一行的端口号。
// 文件不存在、格式不对或端口越界都返回 0（调用方按"没有"处理）。
func readDevToolsActivePort(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	port, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || port <= 0 || port > 65535 {
		return 0
	}
	return port
}

// DiscoverPorts 返回本轮要尝试的调试端口。
//
// 顺序：先放 --remote-debugging-port=0 留下的实际端口（新启动路径），
// 再并上固定端口 9222/9223（历史会话、或用户用 bat 带固定端口启动的客户端）。
//
// 为什么不再"写死两个端口"：固定端口被别的程序占用时只能放弃注入，
// 而 0 让 Chromium 自己挑，DevToolsActivePort 告诉我们挑了哪个——
// 双开也不会互相抢。
func DiscoverPorts() []int {
	return discoverPorts(devToolsActivePortCandidates(), DefaultCDPPorts)
}

// discoverPorts 是可测的内层：目录与回退端口都从参数来。
func discoverPorts(dirs []string, fallback []int) []int {
	seen := make(map[int]bool, 4)
	var out []int
	for _, dir := range dirs {
		if p := readDevToolsActivePort(filepath.Join(dir, "DevToolsActivePort")); p > 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range fallback {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}
