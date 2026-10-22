package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/search"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

const (
	// messagesBodyLimit 限制 Messages 请求体为 1MB。
	messagesBodyLimit = 1 << 20

	// messagesQueryPrefix 是 DSH 及各官方客户端默认的提示词前缀。
	messagesQueryPrefix = "Perform a web search for the query:"
)

var msgIDSeq uint64

// searchAuthGuard 保护对外搜索端点：
// 支持标准 Authorization Bearer、X-Wbmux-Token、URL 参数 t；
// 同时也对本机 127.0.0.1 回环环境提供友好的开发放行。
func (s *Server) searchAuthGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
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

		// 零配置生态兼容：如果请求来自本机回环地址（127.0.0.1 / ::1），且客户端已携带任意 Bearer Key
		// （例如 DSH/第三方插件默认填写的占位符 "sk-xxx"），允许直接透传调用，消除繁琐的手动配置摩擦
		if isLoopbackRequest(r) && got != "" {
			next(w, r)
			return
		}

		// 否则若 token 未提供或不匹配
		writeMessagesError(w, http.StatusForbidden, "authentication_error", "令牌无效：请把 WebUI 界面地址中 t= 的值作为 API Key 传入")
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
	Tools    []messagesTool     `json:"tools"`
	Messages []messagesTurnItem `json:"messages"`
}

type messagesTool struct {
	Type string `json:"type"`
	Name string `json:"name"`
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
	content := []any{messagesToolResultBlock{
		Type:      "web_search_tool_result",
		ToolUseID: "srvtoolu_" + callID,
		Content:   items,
	}}
	if len(citations) > 0 {
		content = append(content, messagesTextBlock{
			Type:      "text",
			Text:      "",
			Citations: citations,
		})
	}
	return messagesResponse{
		ID:           "msg_wbmux_" + callID,
		Type:         "message",
		Role:         "assistant",
		Model:        model,
		Content:      content,
		StopReason:   "end_turn",
		StopSequence: nil,
		Usage:        messagesUsage{InputTokens: 0, OutputTokens: 0},
	}
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

	query := extractMessagesQuery(req.Messages)
	if len(query) < search.MinQueryLen {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("搜索词长度至少需要 %d 个字符", search.MinQueryLen))
		return
	}

	endpoint, token, uid, err := s.resolveSearchCredentials()
	if err != nil {
		writeMessagesError(w, http.StatusServiceUnavailable, "api_error", err.Error())
		return
	}

	out, err := search.DoSearch(r.Context(), nil, endpoint, token, uid, query, search.DefaultMaxResults)
	duration := time.Since(start)

	// 纳入网关调用审计
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
		writeMessagesError(w, http.StatusBadGateway, "api_error", err.Error())
		return
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
	out, err := search.DoSearch(r.Context(), nil, endpoint, token, uid, query, maxResults)
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
	out, err := search.DoSearch(r.Context(), nil, endpoint, token, uid, q, req.MaxResults)
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
