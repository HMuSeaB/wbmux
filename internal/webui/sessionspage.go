package webui

import (
	"crypto/subtle"
	"net/http"
	"strconv"
	"sync"

	"github.com/HMuSeaB/wbmux/internal/sessions"
)

// ---------- 会话中心 ----------
//
// # 为什么要有这一页
//
// 用户装了太多各家的 AI IDE，历史会话各自关在各自的数据目录里，
// 想知道"某个项目都用哪些工具聊过什么"就得挨个客户端翻。
// 这一页把六个源（WB 国内/国际、ZCode、Codex、Claude Code）按项目
// 归组，一次看全；顺手把"可清理候选"标出来。
//
// # 只读，且候选只标记
//
// 扫描全程 immutable/只读；候选只是个布尔标记，页面上没有任何
// "执行清理"的按钮。要不要动、怎么动（归档还是删），是下一期的事，
// 而且必须等规则跑稳之后。

// scanMu 串行化扫描：会话中心冷启动要十几秒（三起 Electron 子进程 +
// 遍历 1.4G 的 Codex 目录），两个标签页同时点刷新就是双倍浪费，
// 还可能把 better-sqlite3 的子进程挤到超时。
var scanMu sync.Mutex

// handleSessionsPage 服务会话中心页面。
//
// 与首页同一套令牌模型：页面本身校验 ?t=，API 再校验一层。
func (s *Server) handleSessionsPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/sessions" {
		http.NotFound(w, r)
		return
	}
	got := r.URL.Query().Get("t")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("令牌无效。请通过 `wbmux sessions` 重新打开界面。\n"))
		return
	}
	page, err := assets.ReadFile("assets/sessions.html")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "界面资源缺失")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// handleSessionsAPI 返回会话索引。
//
// 参数：refresh=1 强制重扫；days / keep 调候选规则（默认 30 / 1）。
// 缓存命中的话毫秒级返回；冷扫描可能要十几秒，前端要给加载态。
func (s *Server) handleSessionsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	q := r.URL.Query()
	refresh := q.Get("refresh") == "1"
	days := intParam(q.Get("days"), 30)
	keep := intParam(q.Get("keep"), 1)

	scanMu.Lock()
	defer scanMu.Unlock()
	idx, err := sessions.Scan(s.probe(), sessions.Options{
		Refresh:        refresh,
		KeepDays:       days,
		KeepPerProject: keep,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, idx)
}

func intParam(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
