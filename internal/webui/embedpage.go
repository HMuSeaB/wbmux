package webui

import (
	"crypto/subtle"
	"net/http"
)

// ---------- 嵌入面板（/embed） ----------
//
// # 这一页是干什么的
//
// 主界面是"桌面管理后台"：侧边栏 + 十几个页面，适合在浏览器里慢慢用。
// 而 WorkBuddy 客户端里通过 CDP 注入的弹层只有约 400px 宽，放不下那套布局。
// /embed 是给注入弹层用的紧凑视图：单列、暗色、只保留高频动作
// （注入模型、双开、重启、复制端点），数据全部复用现有 API。
//
// 与首页、会话中心同一套令牌模型：页面校验 ?t=，API 再各自校验一层。
func (s *Server) handleEmbedPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/embed" {
		http.NotFound(w, r)
		return
	}
	got := r.URL.Query().Get("t")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("令牌无效。请通过 wbmux 界面地址重新访问。\n"))
		return
	}
	page, err := assets.ReadFile("assets/embed.html")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "界面资源缺失")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}
