package webui

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 本地 OpenAI 兼容代理（国际侧） ----------
//
// 两个用途，共用同一套代码：
//
//	1. 外部工具直接调（Cherry Studio / 各种 OpenAI 客户端）
//	2. 注入进国内客户端的自定义模型清单，让国内壳子用国际模型
//
//	GET  /v1/models            模型清单（实时，含限时免费标记）
//	POST /v1/chat/completions  聊天补全（流式/非流式都支持）
//
// 鉴权复用 GUI 令牌：工具侧把它当 API Key，写进
// Authorization: Bearer <令牌> 即可（openaiGuard 单独校验这种形态）。
// 上游凭据不经过调用方——token 只在服务端读取与使用。

// customLocalPrefix 是客户端给"本地自定义模型"id 加的前缀，由
// ProductFeature["CustomModelIdPrefix"] 控制。5.6 客户端在真正发请求前
// 会自己剥掉它（客户端内 stripCustomLocalModelPrefix），所以正常情况
// 见不到；这里兜一层是因为"前缀是否剥离"属于客户端内部行为，
// 它换个版本改了，我们不该跟着失效。
const customLocalPrefix = "custom-local:"

// chatCompletionsPath 是国际后端的补全端点。
const chatCompletionsPath = "/v2/chat/completions"

// maxChatBodyBytes 是请求体上限。Agent 侧的系统提示 + 工具定义 +
// 历史消息很容易超过 1 MB，4 MB 留足余量。
const maxChatBodyBytes = 4 << 20

// normalizeModelName 把客户端送来的模型名还原成官方 id。
//
// # 为什么必须还原
//
// 注入进 models.json 的条目 id 是 "<IDPrefix><官方 id>"，而客户端是把
// 条目 id 当请求体的 model 字段原样发出的（它只剥自己那层前缀）。上游
// 只认官方 id，收到带前缀的名字直接 400 —— 2026-09-27 实测：
//
//	{"code":11102,"msg":"model [wbmux-intl-deepseek-v4.1-flash] service info not found"}
//
// 而客户端界面上只会显示一句「502 | Trace Id」，看不出任何原因。
//
// 循环剥而不是剥一次：两种前缀可能叠加出现。
func normalizeModelName(name string) string {
	s := strings.TrimSpace(name)
	for {
		switch {
		case strings.HasPrefix(s, customLocalPrefix):
			s = s[len(customLocalPrefix):]
		case strings.HasPrefix(s, custommodels.IDPrefix):
			s = s[len(custommodels.IDPrefix):]
		default:
			return s
		}
	}
}

// upstreamRequest 是改写后的转发请求。
type upstreamRequest struct {
	// Body 是发往上游的请求体。
	Body map[string]any
	// Model 是还原后的官方模型名（日志与错误信息用）。
	Model string
	// ClientStream 记录调用方原本要不要流式，决定我们是边收边转还是聚合。
	ClientStream bool
	// Tools 是调用方带来的工具定义数量（只用于日志：0 表示这次没法调工具）。
	Tools int
}

// buildUpstreamBody 把客户端请求体改写成上游能直接吃的形态。
//
// # 为什么用 map 承载而不是定义结构体
//
// 调用方会带 tools / tool_choice / temperature / max_tokens / reasoning
// 等一票字段，结构体必然漏字段。而漏掉 tools 的后果是把"模型能调工具"
// 这件事悄悄砍掉——国内壳子里的 Agent 全靠工具干活，表现成"模型能聊天
// 但什么也不做"，比直接报错难查得多（这正是本次一并修掉的问题）。
//
// 只动三处，其余原样透传：
//   - model：还原成官方 id
//   - stream：强制 true，上游只支持流式（调用方要非流式我们再聚合）
//   - messages：system 挪到首位（上游硬要求，实测 code 11128）
func buildUpstreamBody(raw []byte) (upstreamRequest, error) {
	var out upstreamRequest
	var body map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	// 与项目其它处一致：数字保持原文，不搬成 float64 再搬回来。
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return out, fmt.Errorf("请求体不是 JSON 对象：%w", err)
	}
	if body == nil {
		return out, fmt.Errorf("请求体为空")
	}

	name, _ := body["model"].(string)
	out.Model = normalizeModelName(name)
	if out.Model == "" {
		return out, fmt.Errorf("缺少 model")
	}
	out.ClientStream, _ = body["stream"].(bool)
	if tools, ok := body["tools"].([]any); ok {
		out.Tools = len(tools)
	}

	body["model"] = out.Model
	body["stream"] = true
	if msgs, ok := body["messages"].([]any); ok {
		body["messages"] = ensureSystemFirst(msgs)
	}
	out.Body = body
	return out, nil
}

// ensureSystemFirst 官方接口要求第一条消息必须是 system（实测 code 11128）。
// 调用方没发 system 就补一条默认的；发了但不在首位就挪到首位。
//
// 元素用 any 而不是具体结构体：消息体是原样透传的，这里只认 role，
// 其余字段（tool_calls / tool_call_id / name / content 的多模态数组…）
// 必须一个不动地传下去。
func ensureSystemFirst(msgs []any) []any {
	if len(msgs) == 0 {
		return []any{defaultSystemMessage()}
	}
	if roleOf(msgs[0]) == "system" {
		return msgs
	}
	for i, m := range msgs {
		if roleOf(m) == "system" {
			out := make([]any, 0, len(msgs))
			out = append(out, m)
			out = append(out, msgs[:i]...)
			out = append(out, msgs[i+1:]...)
			return out
		}
	}
	return append([]any{defaultSystemMessage()}, msgs...)
}

// roleOf 取一条消息的 role（小写）；取不到返回空串。
func roleOf(m any) string {
	obj, ok := m.(map[string]any)
	if !ok {
		return ""
	}
	role, _ := obj["role"].(string)
	return strings.ToLower(strings.TrimSpace(role))
}

func defaultSystemMessage() map[string]any {
	return map[string]any{"role": "system", "content": "You are a helpful assistant."}
}

// writeOpenAIError 按 OpenAI 的错误结构回错，工具与客户端都认这个形状。
func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "wbmux_proxy_error"},
	})
}

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

// handleOpenAIChat 转发聊天补全。上游只支持流式，所以统一以流式
// 请求上游：调用方要非流式就聚合，要流式就边收边转发。
func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodyBytes))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "读请求体失败："+err.Error())
		return
	}
	up, err := buildUpstreamBody(raw)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 这里**不**查实时账号（LiveAccounts）：那是两个网络往返外加一把全局锁，
	// 会把首字延迟从毫秒级拖到秒级，而它对"这次能不能转发"没有任何额外保证
	// ——凭据读得出来就够。模型是否存在交给上游回答，它的报错比我们猜的准。
	cred, err := s.intlCred(s.probe())
	if err != nil {
		s.logf("代理拒绝 model=%s：凭据不可用：%v", up.Model, err)
		writeOpenAIError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	out, err := json.Marshal(up.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req2, err := http.NewRequest("POST", s.upstreamBase()+chatCompletionsPath, bytes.NewReader(out))
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

	started := time.Now()
	resp, err := probeHTTPClient().Do(req2)
	if err != nil {
		s.logf("代理转发失败 model=%s tools=%d: %v", up.Model, up.Tools, err)
		writeOpenAIError(w, http.StatusBadGateway, "上游请求失败："+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		msg := upstreamMessage(body)
		// 透传上游的真实状态码，而不是一律 502：502 等于告诉客户端
		// "网关挂了"，客户端会据此触发模型故障转移，把用户的问题
		// 从"模型名不对"带偏成"后端不稳"。
		s.logf("代理被上游拒绝 model=%s tools=%d 上游 HTTP %d: %s",
			up.Model, up.Tools, resp.StatusCode, cutStr(msg, 500))
		writeOpenAIError(w, resp.StatusCode, fmt.Sprintf("上游 HTTP %d: %s", resp.StatusCode, msg))
		return
	}
	s.logf("代理转发成功 model=%s tools=%d stream=%v 用时 %s",
		up.Model, up.Tools, up.ClientStream, time.Since(started).Round(time.Millisecond))

	if up.ClientStream {
		s.streamChatThrough(w, resp.Body)
		return
	}
	s.aggregateChat(w, resp.Body)
}

// streamChatThrough 把上游 SSE 原样转发（上游本就是 OpenAI 兼容块，
// 含 tool_calls 增量，转一道手就是为了不破坏它的形状）。
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

// toolCallAcc 是聚合 tool_calls 增量时的累加器。
//
// tool_calls 是**按 index 分片**流下来的：id 与函数名只在第一片出现，
// arguments 则是逐片字符串拼接。聚合时少拼一片，调用方拿到的就是
// 半截 JSON 参数，工具调用会以"参数解析失败"这种莫名其妙的形式失败。
type toolCallAcc struct {
	id   string
	name string
	args strings.Builder
}

// aggregateChat 聚合上游流式块，组装成一份非流式的 OpenAI 响应。
func (s *Server) aggregateChat(w http.ResponseWriter, upstream io.Reader) {
	var served string
	var finish string
	var content strings.Builder
	var promptTok, completionTok int
	var credit float64
	toolCalls := map[int]*toolCallAcc{}
	var toolOrder []int

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
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
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
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			for _, tc := range ch.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				acc, ok := toolCalls[idx]
				if !ok {
					acc = &toolCallAcc{}
					toolCalls[idx] = acc
					toolOrder = append(toolOrder, idx)
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				acc.args.WriteString(tc.Function.Arguments)
			}
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

	msg := map[string]any{"role": "assistant", "content": content.String()}
	finishReason := "stop"
	if len(toolOrder) > 0 {
		calls := make([]map[string]any, 0, len(toolOrder))
		for _, idx := range toolOrder {
			acc := toolCalls[idx]
			calls = append(calls, map[string]any{
				"id":   acc.id,
				"type": "function",
				"function": map[string]any{
					"name":      acc.name,
					"arguments": acc.args.String(),
				},
			})
		}
		msg["tool_calls"] = calls
		finishReason = "tool_calls"
	} else if finish != "" {
		finishReason = finish
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "wbmux-proxy",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   served,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTok,
			"completion_tokens": completionTok,
			"total_tokens":      promptTok + completionTok,
			"credit":            credit,
		},
	})
}

// upstreamMessage 从上游的错误体里挑一句人能看的话。
//
// 官方错误体形如 {"code":11102,"msg":"...","displayMsg":{"zh":"当前模型不可用，
// 请切换其他模型后重试。"}}。优先取中文面向上游的说法，再退回通用字段，
// 都不像就原样截一段——绝不把原因吞成一句"未知错误"。
func upstreamMessage(raw []byte) string {
	var obj struct {
		Msg        string `json:"msg"`
		Message    string `json:"message"`
		DisplayMsg struct {
			Zh   string `json:"zh"`
			En   string `json:"en"`
			EnUS string `json:"en-US"`
		} `json:"displayMsg"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, cand := range []string{obj.DisplayMsg.Zh, obj.Msg, obj.Message, obj.DisplayMsg.En, obj.DisplayMsg.EnUS} {
			if s := flatten(cand); s != "" {
				return s
			}
		}
		if obj.Error != nil {
			if s := flatten(obj.Error.Message); s != "" {
				return s
			}
		}
	}
	return cutStr(flatten(string(raw)), 300)
}

// flatten 压掉换行与多余空白，避免日志与错误信息被多行 JSON 撑爆。
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
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
