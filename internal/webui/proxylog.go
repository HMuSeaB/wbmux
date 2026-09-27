package webui

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---------- 代理日志 ----------
//
// # 为什么要有它
//
// 注入模式下模型是跑在国内客户端里的：出问题时用户能看到的只有客户端那
// 一句报错（常常只剩一个状态码和一个 Trace Id），真正的原因在服务端。
// 原本这些只落 ~/.wbmux/gui.log，而"让人去翻文件"等于没有可观测性——
// 2026-09-27 排查那两次故障，每一步都得先找到那个文件、翻到最后、再对照。
//
// 所以：内存里留一份环形缓冲（界面直接看，2 秒刷新），同时照旧落文件
// （事后查、能贴着时间线看启动过程）。

// proxyLogLimit 是内存里保留的条数。够覆盖"刚发了几轮消息"这个排查窗口，
// 也不至于让界面一次渲染上千行。
const proxyLogLimit = 200

// ProxyLogEntry 是一条代理事件。
type ProxyLogEntry struct {
	// At 是本地时间 HH:MM:SS（只给界面看，精确日期在 gui.log 里）。
	At string `json:"at"`
	// Level 决定界面配色：ok / bad / info。
	Level string `json:"level"`
	// Model 是还原后的官方模型名。
	Model string `json:"model,omitempty"`
	// Tools 是调用方带来的工具定义数量。
	Tools int `json:"tools,omitempty"`
	// Stream 表示调用方要的是流式。
	Stream bool `json:"stream"`
	// MS 是转发耗时（毫秒），仅完成事件有。
	MS int64 `json:"ms,omitempty"`
	// Text 是给人读的一行说明，同时就是 gui.log 里的内容。
	Text string `json:"text"`
}

// note 记一条代理事件：进内存缓冲，并落日志文件。
func (s *Server) note(e ProxyLogEntry) {
	if e.At == "" {
		e.At = time.Now().Format("15:04:05")
	}
	s.proxyMu.Lock()
	s.proxyLog = append(s.proxyLog, e)
	if len(s.proxyLog) > proxyLogLimit {
		// 从头砍掉最旧的：界面只关心最近发生了什么。
		s.proxyLog = append(s.proxyLog[:0], s.proxyLog[len(s.proxyLog)-proxyLogLimit:]...)
	}
	s.proxyMu.Unlock()

	if s.logFn != nil {
		s.logFn("%s", e.Text)
	}
}

// proxyLogSnapshot 返回从新到旧的事件副本，供界面渲染。
func (s *Server) proxyLogSnapshot() []ProxyLogEntry {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	out := make([]ProxyLogEntry, 0, len(s.proxyLog))
	for i := len(s.proxyLog) - 1; i >= 0; i-- {
		out = append(out, s.proxyLog[i])
	}
	return out
}

// handleProxyLog 把内存里的代理事件回给界面。
//
// 不走 SSE/长连接而走轮询：界面每 2 秒拉一次，服务端不用维护长连接，
// 页面切走也不会留着一个悬着的连接（这个服务是要能自动退出的）。
func (s *Server) handleProxyLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	writeJSON(w, map[string]any{
		"entries": s.proxyLogSnapshot(),
		"limit":   proxyLogLimit,
	})
}

// describeTools 把工具数拼成日志里的一段（0 个时点出来，那是"没带工具"，
// 与"带了但模型没用"是两件事）。
func describeTools(n int) string {
	if n == 0 {
		return "tools=0"
	}
	return fmt.Sprintf("tools=%d", n)
}

// describeBytes 把字节数换算成人看的单位。
func describeBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// compactError 把上游错误压成一行，避免日志被多行 JSON 撑爆。
func compactError(msg string) string {
	return strings.Join(strings.Fields(msg), " ")
}
