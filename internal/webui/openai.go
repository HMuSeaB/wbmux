package webui

import (
	"bufio"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 本地 OpenAI 兼容代理（国际侧） ----------
//
// 用户要"不开客户端随便调模型"。国际账号凭据是明文、官方接口是
// OpenAI 兼容格式（2026-09-26 实测通过），所以在这里做一个转发层：
//
//	GET  /v1/models            模型清单（实时，含限时免费标记）
//	POST /v1/chat/completions  聊天补全（流式/非流式都支持）
//
// 鉴权复用 GUI 令牌：工具侧把它当 API Key，写进
// Authorization: Bearer <令牌> 即可（openaiGuard 单独校验这种形态）。
// 上游凭据不经过调用方——token 只在服务端读取与使用。

// openaiGuard 校验 OpenAI 工具常用的 Bearer 形态，也兼容既有令牌头。
func (s *Server) openaiGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.Header.Get("X-Wbmux-Token")
		}
		if got == "" {
			got = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeOpenAIError(w, http.StatusForbidden, "令牌无效：请把界面地址里 t= 的值当 API Key 用")
			return
		}
		next(w, r)
	}
}

func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "wbmux_proxy_error"},
	})
}

// registerOpenAI 挂载 /v1/* 路由。单独用 openaiGuard：OpenAI 工具
// 生态只认 Authorization: Bearer，不认 X-Wbmux-Token。
func (s *Server) registerOpenAI(mux *http.ServeMux) {
	mux.HandleFunc("/v1/models", s.openaiGuard(s.handleOpenAIModels))
	mux.HandleFunc("/v1/chat/completions", s.openaiGuard(s.handleOpenAIChat))
}

// handleOpenAIModels 返回国际侧实时模型清单（OpenAI 格式）。
func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	live := usage.LiveAccounts(s.probe())[string(variant.Intl)]
	if live == nil || !live.OK {
		msg := "国际侧凭据不可用"
		if live != nil && live.Err != "" {
			msg = live.Err
		}
		writeOpenAIError(w, http.StatusServiceUnavailable, msg)
		return
	}
	data := make([]map[string]any, 0, len(live.Models))
	for _, m := range live.Models {
		owned := "workbuddy-ai"
		if m.FreeNow {
			owned = "workbuddy-ai:free-now"
		}
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "owned_by": owned,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list", "data": data,
	})
}

// openaiChatRequest 是工具发来的请求体（OpenAI 格式的常用子集）。
type openaiChatRequest struct {
	Model     string          `json:"model"`
	Messages  []openaiMessage `json:"messages"`
	Stream    bool            `json:"stream"`
	MaxTokens int             `json:"max_tokens"`
}

type openaiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ensureSystemFirst 官方接口要求第一条必须是 system（实测 code 11128）。
// 工具没发 system 就补一条默认的；发了但不在首位就挪到首位。
func ensureSystemFirst(msgs []openaiMessage) []openaiMessage {
	if len(msgs) == 0 {
		return []openaiMessage{{Role: "system", Content: json.RawMessage(`"You are a helpful assistant."`)}}
	}
	if strings.EqualFold(msgs[0].Role, "system") {
		return msgs
	}
	out := make([]openaiMessage, 0, len(msgs)+1)
	for i, m := range msgs {
		if strings.EqualFold(m.Role, "system") {
			out = append(out, m)
			out = append(out, msgs[:i]...)
			out = append(out, msgs[i+1:]...)
			return out
		}
	}
	return append([]openaiMessage{{Role: "system", Content: json.RawMessage(`"You are a helpful assistant."`)}}, msgs...)
}

// handleOpenAIChat 转发聊天补全。上游只支持流式，所以统一以流式
// 请求上游：工具要非流式就聚合，要流式就边收边转发。
func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	limit := int64(4 << 20)
	var req openaiChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, limit)).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "请求体无法解析："+err.Error())
		return
	}
	if req.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "缺少 model")
		return
	}
	live := usage.LiveAccounts(s.probe())[string(variant.Intl)]
	if live == nil || !live.OK {
		msg := "国际侧凭据不可用"
		if live != nil && live.Err != "" {
			msg = live.Err
		}
		writeOpenAIError(w, http.StatusServiceUnavailable, msg)
		return
	}
	cred, err := usage.IntlProxyCredentials(s.probe())
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	upstreamBody := map[string]any{
		"model":    req.Model,
		"stream":   true,
		"messages": ensureSystemFirst(req.Messages),
	}
	if req.MaxTokens > 0 {
		upstreamBody["max_tokens"] = req.MaxTokens
	}
	raw, _ := json.Marshal(upstreamBody)

	req2, err := http.NewRequest("POST", usage.IntlEndpoint()+"/v2/chat/completions", strings.NewReader(string(raw)))
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req2.Header.Set("Authorization", "Bearer "+cred.Token)
	req2.Header.Set("X-User-Id", cred.UID)
	req2.Header.Set("Accept", "text/event-stream")
	req2.Header.Set("Content-Type", "application/json")
	for k, v := range usage.IntlCommonHeaders() {
		req2.Header.Set(k, v)
	}
	resp, err := probeHTTPClient().Do(req2)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "上游请求失败："+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		writeOpenAIError(w, http.StatusBadGateway, fmt.Sprintf("上游 HTTP %d: %s", resp.StatusCode, cutStr(string(raw), 300)))
		return
	}

	if req.Stream {
		s.streamChatThrough(w, resp.Body)
		return
	}
	s.aggregateChat(w, resp.Body, req.Model)
}

// streamChatThrough 把上游 SSE 原样转发给工具（上游本就是 OpenAI 兼容块）。
func (s *Server) streamChatThrough(w http.ResponseWriter, upstream io.Reader) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, canFlush := w.(http.Flusher)
	sc := bufio.NewScanner(upstream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		fmt.Fprintln(w, line)
		if canFlush {
			flusher.Flush()
		}
	}
}

// aggregateChat 聚合上游流式块，组装成一份非流式的 OpenAI 响应。
func (s *Server) aggregateChat(w http.ResponseWriter, upstream io.Reader, requested string) {
	var served string
	var content strings.Builder
	var promptTok, completionTok int
	var credit float64
	sc := bufio.NewScanner(upstream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int     `json:"prompt_tokens"`
				CompletionTokens int     `json:"completion_tokens"`
				Credit           float64 `json:"credit"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Model != "" {
			served = chunk.Model
		}
		for _, ch := range chunk.Choices {
			content.WriteString(ch.Delta.Content)
		}
		if chunk.Usage != nil {
			promptTok = chunk.Usage.PromptTokens
			completionTok = chunk.Usage.CompletionTokens
			credit = chunk.Usage.Credit
		}
	}
	if served == "" {
		writeOpenAIError(w, http.StatusBadGateway, "上游没有返回任何内容")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "wbmux-proxy",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   served,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content.String()},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTok,
			"completion_tokens": completionTok,
			"total_tokens":      promptTok + completionTok,
			"credit":            credit,
		},
	})
}

// cutStr 截断长文本用于错误信息。
func cutStr(str string, n int) string {
	if len(str) <= n {
		return str
	}
	return str[:n] + "…"
}

// probeHTTPClient 是转发用的 HTTP 客户端（超时按长回复放宽）。
func probeHTTPClient() *http.Client {
	return &http.Client{Timeout: 120 * time.Second}
}
