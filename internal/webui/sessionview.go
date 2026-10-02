// 「直接看一个会话」——在浏览器新标签页里打开完整对话。
//
// # 为什么不复用导出那条路
//
// 导出是"写文件给别人带走"，这里是"在当前这台机器上看一眼"。区别在于：
//
//	导出   写盘、有落点、看完还在
//	看     不落盘、URL 可分享给同一个服务、关掉就没了
//
// 但**渲染必须是同一份代码**。所以这里调的是 `sessions.RenderCodexHTML`
// ——和导出用的是同一个函数。分两套实现迟早会出现"导出的页面和点开的
// 页面长得不一样"。
//
// # 为什么服务端渲染而不是给前端 JSON
//
// 渲染器已经在 Go 里写好了（含图片内嵌、环境注入标注、工具调用折叠）。
// 让前端再实现一遍就是两份要同步维护的东西。而且会话正文动辄几十 MB，
// 序列化成 JSON 再让浏览器拼 DOM 更慢。
package webui

import (
	"net/http"

	"github.com/HMuSeaB/wbmux/internal/sessions"
)

// 默认只带最近这么多项。
//
// 依据：实测最大的会话 237 MB / 2732 项，全渲染是 36 MB 的 HTML
// （图片占 16 MB）。一次全塞给浏览器会明显卡。120 项对绝大多数会话
// 就是全部；大会话也能先看到最近的，再按需往前翻。
const defaultViewTurns = 120

// handleSessionView 渲染单个会话的完整页面。
func (s *Server) handleSessionView(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/session" {
		http.NotFound(w, r)
		return
	}
	// 页面与 API 同一把令牌：这个页面会内嵌会话正文（可能含代码、路径），
	// 不能让它被随便嵌进别的页面。
	if !s.checkToken(r) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("令牌无效。请从会话中心点进来。\n"))
		return
	}

	id := r.URL.Query().Get("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}

	// 找到这个会话的正文文件。id 是客户端生成的，可能带路径分隔符之类的
	// 意外字符 —— 但这里只用它去**比对**扫描结果里已有的路径，
	// 不拿它拼路径，所以没有目录穿越的问题。
	path, err := s.findSessionBody(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}

	limit := intParam(r.URL.Query().Get("limit"), defaultViewTurns)
	tr, err := sessions.ParseCodexTail(path, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取会话失败："+err.Error())
		return
	}

	title := r.URL.Query().Get("title")
	if title == "" {
		title = id
	}
	page := sessions.RenderCodexHTML(tr, sessions.CodexHTMLOptions{
		Title:         title,
		WithTools:     true,
		WithReasoning: true,
		MaxImageBytes: 8 << 20,
		// 页面里给出"还有更早的"入口，以及"看全部"的链接。
		ShowMore:    tr.Truncated,
		LoadedTurns: len(tr.Turns),
		TotalTurns:  tr.TotalTurns,
		// 带上令牌，"看全部"才能再请求一次。
		Token: s.token,
		ID:    id,
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(page))
}

// findSessionBody 按 id 在扫描结果里找出正文文件路径。
//
// 依赖扫描而不是自己拼路径：会话在哪只有各适配器知道（Codex 按年/月/日分层、
// Claude 按项目 slug 分目录），拼错了会"找不到"或更糟——找到别人的。
func (s *Server) findSessionBody(id string) (string, error) {
	idx, err := sessions.Scan(s.probe(), sessions.Options{})
	if err != nil {
		return "", err
	}
	for _, ss := range idx.Sessions {
		if ss.ID != id {
			continue
		}
		if ss.Source.Path == "" {
			return "", errNoBody
		}
		if !sessions.CanRenderInline(ss.Vendor) {
			return "", errUnsupportedVendor
		}
		return ss.Source.Path, nil
	}
	return "", errSessionNotFound
}

var (
	errNoBody            = errString("这个会话没有独立正文文件")
	errUnsupportedVendor = errString("这个来源的会话暂不支持在界面里直接查看")
	errSessionNotFound   = errString("没有找到这个会话（试试回会话中心点「重新扫描」）")
)

// errString 让上面几条错误有个类型，省得每次 fmt.Errorf 一遍。
type errString string

func (e errString) Error() string { return string(e) }
