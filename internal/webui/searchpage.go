package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/netroute"
	"github.com/HMuSeaB/wbmux/internal/search"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

var searchHTTPClient = &http.Client{
	Timeout:   search.DefaultTimeout,
	Transport: netroute.Transport(),
}

const (
	// messagesBodyLimit 限制 Messages 请求体为 1MB。
	messagesBodyLimit = 1 << 20

	// messagesQueryPrefix 是 DSH 及各官方客户端默认的提示词前缀。
	messagesQueryPrefix = "Perform a web search for the query:"
)

var msgIDSeq uint64

// searchAuthGuard 保护对外搜索端点：
// 支持标准 Authorization Bearer、Anthropic 的 x-api-key、X-Wbmux-Token、URL 参数 t；
// 同时也对本机 127.0.0.1 回环环境提供友好的开发放行。
func (s *Server) searchAuthGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			// Anthropic 协议（/v1/search/messages 就是照它做的）默认用
			// x-api-key 带凭据，不是 Authorization。少了这一条，凡是按
			// Anthropic 规范配置的客户端（ZCode 的「Headers Auth」、
			// Claude Code 之类）都会被判"令牌无效"——实测 2026-10-28。
			got = strings.TrimSpace(r.Header.Get("x-api-key"))
		}
		if got == "" {
			got = r.Header.Get("X-Wbmux-Token")
		}
		if got == "" {
			got = r.URL.Query().Get("t")
		}

		// 若与服务端 token 匹配则直接放行
		if got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1 {
			next(w, r)
			return
		}

		// 回环免密：本机 127.0.0.1 的请求直接放行，**不要求带任何 Key**。
		//
		// 为什么放宽到"不带 Key 也放行"（2026-10-28）：
		// 搜索端点要"一键接入各客户端"，而这些客户端的配置项千差万别——
		// 有的强制填 API Key（填 sk-xxx 占位），有的根本不允许留空认证头，
		// 有的发 x-api-key、有的发 Authorization、有的什么都不发。原先要求
		// "携带任意非空 Key"，结果 Qoder 这类不发认证头的客户端直接 403，
		// 用户只看到一句"令牌无效"，完全猜不到原因。
		//
		// 安全性：这个服务本来就只监听 127.0.0.1（非回环地址在启动时即被拒绝），
		// 能连上端口的进程已经是本机进程；搜索是**只读**操作，不写文件、不启动
		// 进程、不改账号状态，泄露面仅限于"本机程序能借用搜索额度"。
		// 需要严格鉴权的远程场景仍然走 token 比对那条路。
		if isLoopbackRequest(r) {
			next(w, r)
			return
		}

		// 远端请求：必须持有正确 token（上面的比对已经覆盖）。
		// 走到这里说明既不是回环、token 也不对。
		// 否则若 token 未提供或不匹配
		writeMessagesError(w, http.StatusForbidden, "authentication_error", "令牌无效：请把 WebUI 界面地址中 t= 的值作为 API Key 传入（支持 Authorization: Bearer 与 x-api-key）")
	}
}

func isLoopbackRequest(r *http.Request) bool {
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// ---------------- Anthropic Messages 协议数据结构 ----------------

type messagesRequest struct {
	Model    string             `json:"model"`
	Stream   bool               `json:"stream"`
	Tools    []messagesTool     `json:"tools"`
	Messages []messagesTurnItem `json:"messages"`
}

type messagesTool struct {
	Type           string   `json:"type"`
	Name           string   `json:"name"`
	MaxUses        int      `json:"max_uses"`
	AllowedDomains []string `json:"allowed_domains"`
	BlockedDomains []string `json:"blocked_domains"`
}

// webSearchToolSpec 从请求的 tools 里提取 web_search 服务端工具的声明参数。
// 找不到时返回 nil。
func webSearchToolSpec(tools []messagesTool) *messagesTool {
	for i := range tools {
		t := tools[i]
		if strings.HasPrefix(t.Type, "web_search") || t.Name == "web_search" {
			return &tools[i]
		}
	}
	return nil
}

// domainAllowed 按 Anthropic web_search 的域名过滤语义判断单条结果：
// blocked_domains 优先（命中即排除），allowed_domains 非空时要求命中其一。
// 域名匹配为后缀匹配，"go.dev" 同时命中 "go.dev" 与 "www.go.dev"。
func domainAllowed(rawURL string, allowed, blocked []string) bool {
	host := urlHost(rawURL)
	if host == "" {
		return true // 拿不到主机名时不拦，避免误杀
	}
	for _, b := range blocked {
		if host == b || strings.HasSuffix(host, "."+b) {
			return false
		}
	}
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// filterResultsByDomain 按 allowed/blocked 过滤搜索结果，保持原顺序。
func filterResultsByDomain(results []search.SearchResult, allowed, blocked []string) []search.SearchResult {
	if len(allowed) == 0 && len(blocked) == 0 {
		return results
	}
	out := make([]search.SearchResult, 0, len(results))
	for _, r := range results {
		if domainAllowed(r.URL, allowed, blocked) {
			out = append(out, r)
		}
	}
	return out
}

// urlHost 提取 URL 的小写主机名（尽力而为，失败返回空串）。
func urlHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

type messagesTurnItem struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type messagesContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func declaresWebSearch(tools []messagesTool) bool {
	for _, t := range tools {
		if strings.HasPrefix(t.Type, "web_search") || t.Name == "web_search" {
			return true
		}
	}
	return false
}

func extractMessagesQuery(items []messagesTurnItem) string {
	joined := ""
	for _, m := range items {
		if len(m.Content) == 0 {
			continue
		}
		var plain string
		if err := json.Unmarshal(m.Content, &plain); err == nil {
			if t := strings.TrimSpace(plain); t != "" {
				joined = t
			}
			continue
		}
		var blocks []messagesContentBlock
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			continue
		}
		var text []string
		for _, b := range blocks {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				text = append(text, b.Text)
			}
		}
		if t := strings.TrimSpace(strings.Join(text, "\n")); t != "" {
			joined = t
		}
	}

	// 不区分大小写剥除固定前缀
	if len(joined) >= len(messagesQueryPrefix) &&
		strings.EqualFold(joined[:len(messagesQueryPrefix)], messagesQueryPrefix) {
		joined = strings.TrimSpace(joined[len(messagesQueryPrefix):])
	}
	return joined
}

type messagesSearchResultItem struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

type messagesToolResultBlock struct {
	Type      string                     `json:"type"`
	ToolUseID string                     `json:"tool_use_id"`
	Content   []messagesSearchResultItem `json:"content"`
}

type messagesCitation struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	CitedText string `json:"cited_text"`
}

type messagesTextBlock struct {
	Type      string             `json:"type"`
	Text      string             `json:"text"`
	Citations []messagesCitation `json:"citations,omitempty"`
}

type messagesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	// ServerToolUse 对齐 Anthropic 的服务端工具计费字段。ZCode 等客户端
	// 的用量 schema 里有 web_search_requests，缺失时按 0 处理；显式给出
	// 1 可以让调用方在界面上正确显示"发生了一次联网搜索"。
	ServerToolUse struct {
		WebSearchRequests int `json:"web_search_requests"`
	} `json:"server_tool_use,omitempty"`
}

// messagesServerToolUseBlock 是 Anthropic 真实流式/非流式响应里
// web_search_tool_result 之前的 server_tool_use 块，携带搜索调用本身。
type messagesServerToolUseBlock struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input struct {
		Query string `json:"query"`
	} `json:"input"`
}

type messagesResponse struct {
	ID           string        `json:"id"`
	Type         string        `json:"type"`
	Role         string        `json:"role"`
	Model        string        `json:"model"`
	Content      []any         `json:"content"`
	StopReason   string        `json:"stop_reason"`
	StopSequence any           `json:"stop_sequence"`
	Usage        messagesUsage `json:"usage"`
}

func randomHexID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%0*x", n*2, uint64(time.Now().UnixNano())^atomic.AddUint64(&msgIDSeq, 1))
	}
	return hex.EncodeToString(b)
}

func writeMessagesError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": msg,
		},
	})
}

func buildMessagesSearchResponse(query, model string, out *search.SearchOutcome) messagesResponse {
	items := make([]messagesSearchResultItem, 0, len(out.Results))
	citations := make([]messagesCitation, 0, len(out.Results))
	for _, r := range out.Results {
		title := r.Title
		if title == "" {
			title = r.URL
		}
		items = append(items, messagesSearchResultItem{
			Type:  "web_search_result",
			URL:   r.URL,
			Title: title,
		})
		if r.Snippet != "" {
			citations = append(citations, messagesCitation{
				Type:      "web_search_result_location",
				URL:       r.URL,
				CitedText: r.Snippet,
			})
		}
	}
	if model == "" {
		model = "workbuddy-web-search"
	}
	callID := randomHexID(16)
	content := []any{}
	// server_tool_use 块：与 Anthropic 真实响应形态一致，客户端可据此
	// 关联 tool_use_id 并在界面上展示"发起了一次搜索"。
	serverUse := messagesServerToolUseBlock{
		Type: "server_tool_use",
		ID:   "srvtoolu_" + callID,
		Name: "web_search",
	}
	serverUse.Input.Query = query
	content = append(content, serverUse)
	content = append(content, messagesToolResultBlock{
		Type:      "web_search_tool_result",
		ToolUseID: "srvtoolu_" + callID,
		Content:   items,
	})
	if len(citations) > 0 {
		content = append(content, messagesTextBlock{
			Type:      "text",
			Text:      "",
			Citations: citations,
		})
	}
	resp := messagesResponse{
		ID:           "msg_wbmux_" + callID,
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      content,
		StopReason:   "end_turn",
		StopSequence: nil,
	}
	resp.Usage.ServerToolUse.WebSearchRequests = 1
	return resp
}

// ---------------- SSE 流式合成 ----------------

// writeMessagesSearchSSE 把完整的搜索响应合成为 Anthropic Messages SSE
// 事件序列，使按流式解析的客户端（ZCode / Claude Code，transport=sse）
// 能直接消费。wbmux 自身不生成模型文本，因此文本块只携带 citations。
//
// 事件序列（对齐 Anthropic 真实流）：
//
//	ping → message_start → server_tool_use(start/delta/stop)
//	      → web_search_tool_result(start/stop)
//	      → text(start/delta/stop，如有 citations)
//	      → message_delta → message_stop
func writeMessagesSearchSSE(w http.ResponseWriter, resp *messagesResponse) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(event string, payload any) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	emit("ping", map[string]string{"type": "ping"})
	emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            resp.ID,
			"type":          "message",
			"role":          "assistant",
			"model":         resp.Model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":    resp.Usage.InputTokens,
				"output_tokens":   0,
				"server_tool_use": map[string]any{"web_search_requests": 0},
			},
		},
	})

	index := 0
	for _, block := range resp.Content {
		switch b := block.(type) {
		case messagesServerToolUseBlock:
			emit("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type":  "server_tool_use",
					"id":    b.ID,
					"name":  b.Name,
					"input": map[string]any{},
				},
			})
			queryJSON, _ := json.Marshal(b.Input.Query)
			emit("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": index,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": string(queryJSON)},
			})
			emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
			index++
		case messagesToolResultBlock:
			emit("content_block_start", map[string]any{
				"type":  "content_block_start",
				"index": index,
				"content_block": map[string]any{
					"type":        "web_search_tool_result",
					"tool_use_id": b.ToolUseID,
					"content":     b.Content,
				},
			})
			emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
			index++
		case messagesTextBlock:
			emit("content_block_start", map[string]any{
				"type":          "content_block_start",
				"index":         index,
				"content_block": map[string]any{"type": "text", "text": ""},
			})
			if b.Text != "" {
				emit("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{"type": "text_delta", "text": b.Text},
				})
			}
			for _, c := range b.Citations {
				emit("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": index,
					"delta": map[string]any{"type": "citations_delta", "citation": c},
				})
			}
			emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
			index++
		}
	}

	emit("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": resp.StopReason, "stop_sequence": nil},
		"usage": map[string]any{
			"output_tokens":   resp.Usage.OutputTokens,
			"server_tool_use": map[string]any{"web_search_requests": resp.Usage.ServerToolUse.WebSearchRequests},
		},
	})
	emit("message_stop", map[string]any{"type": "message_stop"})
}

// ---------------- 凭据与上游基础调度 ----------------

func (s *Server) resolveSearchCredentials() (string, string, string, error) {
	endpoint := s.upstreamBase()
	p := s.probe()

	// 优先读取国际侧凭据
	if intl, err := s.intlCred(p); err == nil && intl.Token != "" {
		return endpoint, intl.Token, intl.UID, nil
	}

	// 降级尝试国内侧凭据
	if curr, err := credential.Read(p, variant.CN); err == nil && curr.Token != "" {
		return endpoint, curr.Token, curr.UID, nil
	}

	return "", "", "", fmt.Errorf("无可用登录凭据，请先登录官方客户端或导入 AccessToken")
}

// ---------------- HTTP 控制器处理函数 ----------------

// handleSearchMessages 处理 POST /v1/search/messages (Anthropic Messages 兼容形态)。
func (s *Server) handleSearchMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMessagesError(w, http.StatusMethodNotAllowed, "invalid_request_error", "仅支持 POST 请求")
		return
	}

	start := time.Now()
	raw, err := io.ReadAll(io.LimitReader(r.Body, messagesBodyLimit))
	if err != nil {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "读取请求体失败")
		return
	}

	var req messagesRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "请求体必须为合法 JSON")
		return
	}

	if !declaresWebSearch(req.Tools) {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error",
			"本端点专供联网搜索，请在 tools 中声明 web_search 工具")
		return
	}

	s.runWebSearch(w, r, &req, start)
}

// runWebSearch 执行一次联网搜索并写出响应。请求体由调用方读取并解析——
// 网关模式（/v1/messages）分流进来时复用同一段逻辑，避免重复消费 Body。
func (s *Server) runWebSearch(w http.ResponseWriter, r *http.Request, req *messagesRequest, start time.Time) {
	query := extractMessagesQuery(req.Messages)
	if len(query) < search.MinQueryLen {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("搜索词长度至少需要 %d 个字符", search.MinQueryLen))
		return
	}

	// web_search 工具声明参数：max_uses 决定结果条数，域名过滤按
	// Anthropic 语义（blocked 优先，allowed 非空时要求命中）。
	spec := webSearchToolSpec(req.Tools)
	maxResults := search.DefaultMaxResults
	var allowed, blocked []string
	if spec != nil {
		if spec.MaxUses > 0 {
			maxResults = search.ClampSearchMaxResults(spec.MaxUses)
		}
		allowed, blocked = spec.AllowedDomains, spec.BlockedDomains
	}

	endpoint, token, uid, err := s.resolveSearchCredentials()
	if err != nil {
		writeMessagesError(w, http.StatusServiceUnavailable, "api_error", err.Error())
		return
	}

	out, err := search.DoSearch(r.Context(), searchHTTPClient, endpoint, token, uid, query, maxResults)
	duration := time.Since(start)

	// 纳入网关调用审计
	record := gatewayaudit.Record{
		Timestamp:        start,
		ModelRequested:   "web-search",
		ModelMapped:      "WorkBuddy联网搜索",
		AccountID:        uid,
		AccountMask:      maskUID(uid),
		IsStream:         req.Stream,
		StatusCode:       http.StatusOK,
		DurationMs:       duration.Milliseconds(),
		TTFTMs:           duration.Milliseconds(),
		PromptTokens:     len(query),
		CompletionTokens: 0,
		TotalTokens:      len(query),
	}

	if err != nil {
		record.StatusCode = http.StatusBadGateway
		record.ErrorMsg = err.Error()
		gatewayaudit.Default().RecordRequest(record)
		writeMessagesError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}

	if dropped := len(out.Results) - len(filterResultsByDomain(out.Results, allowed, blocked)); dropped > 0 {
		out.Results = filterResultsByDomain(out.Results, allowed, blocked)
		out.TotalResults = len(out.Results)
	}

	record.CompletionTokens = len(out.Results) * 60
	record.TotalTokens = record.PromptTokens + record.CompletionTokens
	gatewayaudit.Default().RecordRequest(record)

	s.note(ProxyLogEntry{
		Level: "ok",
		Model: "web-search",
		Text:  fmt.Sprintf("全网联网搜索成功：「%s」· 命中 %d 条 · 耗时 %dms", query, len(out.Results), duration.Milliseconds()),
	})

	resp := buildMessagesSearchResponse(query, req.Model, out)
	if req.Stream {
		// 流式：合成 Anthropic SSE 事件序列（ZCode/Claude Code 按 sse 解析）。
		writeMessagesSearchSSE(w, &resp)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleSearchNative 处理 POST /v1/search 及 GET /v1/search (极简结构化搜索端点)。
func (s *Server) handleSearchNative(w http.ResponseWriter, r *http.Request) {
	var query string
	var maxResults int

	if r.Method == http.MethodGet {
		query = strings.TrimSpace(r.URL.Query().Get("q"))
		if query == "" {
			query = strings.TrimSpace(r.URL.Query().Get("query"))
		}
		if nStr := r.URL.Query().Get("n"); nStr != "" {
			maxResults, _ = strconv.Atoi(nStr)
		}
	} else if r.Method == http.MethodPost {
		var body struct {
			Query      string `json:"query"`
			Q          string `json:"q"`
			MaxResults int    `json:"max_results"`
			Limit      int    `json:"limit"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, messagesBodyLimit)).Decode(&body); err == nil {
			query = strings.TrimSpace(body.Query)
			if query == "" {
				query = strings.TrimSpace(body.Q)
			}
			maxResults = body.MaxResults
			if maxResults <= 0 {
				maxResults = body.Limit
			}
		}
	} else {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
		return
	}

	if len(query) < search.MinQueryLen {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("搜索词长度至少需要 %d 个字符", search.MinQueryLen))
		return
	}

	endpoint, token, uid, err := s.resolveSearchCredentials()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	start := time.Now()
	out, err := search.DoSearch(r.Context(), searchHTTPClient, endpoint, token, uid, query, maxResults)
	duration := time.Since(start)

	record := gatewayaudit.Record{
		Timestamp:        start,
		ModelRequested:   "web-search",
		ModelMapped:      "WorkBuddy联网搜索",
		AccountID:        uid,
		AccountMask:      maskUID(uid),
		IsStream:         false,
		StatusCode:       http.StatusOK,
		DurationMs:       duration.Milliseconds(),
		TTFTMs:           duration.Milliseconds(),
		PromptTokens:     len(query),
		CompletionTokens: 0,
		TotalTokens:      len(query),
	}

	if err != nil {
		record.StatusCode = http.StatusBadGateway
		record.ErrorMsg = err.Error()
		gatewayaudit.Default().RecordRequest(record)
		writeErr(w, http.StatusBadGateway, "搜索请求失败: "+err.Error())
		return
	}

	record.CompletionTokens = len(out.Results) * 60
	record.TotalTokens = record.PromptTokens + record.CompletionTokens
	gatewayaudit.Default().RecordRequest(record)

	writeJSON(w, map[string]any{
		"ok":               true,
		"query":            out.Query,
		"total_results":    out.TotalResults,
		"response_time_ms": duration.Milliseconds(),
		"provider":         out.Provider,
		"results":          out.Results,
	})
}

// handleSearchDebug 处理 WebUI 内部发起的免 Bearer 搜索调试接口。
func (s *Server) handleSearchDebug(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求解析失败")
		return
	}
	q := strings.TrimSpace(req.Query)
	if len(q) < search.MinQueryLen {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("搜索词长度至少需要 %d 个字符", search.MinQueryLen))
		return
	}

	endpoint, token, uid, err := s.resolveSearchCredentials()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	start := time.Now()
	out, err := search.DoSearch(r.Context(), searchHTTPClient, endpoint, token, uid, q, req.MaxResults)
	duration := time.Since(start)

	if err != nil {
		writeErr(w, http.StatusBadGateway, "联网搜索失败: "+err.Error())
		return
	}

	writeJSON(w, map[string]any{
		"ok":               true,
		"query":            out.Query,
		"total_results":    out.TotalResults,
		"response_time_ms": duration.Milliseconds(),
		"results":          out.Results,
	})
}

func maskUID(uid string) string {
	if len(uid) <= 6 {
		return uid
	}
	return uid[:3] + "…" + uid[len(uid)-3:]
}
