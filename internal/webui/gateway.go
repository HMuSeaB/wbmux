package webui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/netroute"
)

// gatewayHTTPClient 复用系统网络路由（与搜索端点一致），不设整体超时：
// 模型流式响应可能持续很久，取消由请求上下文负责。
var gatewayHTTPClient = &http.Client{
	Transport: netroute.Transport(),
}

// envOr 读取环境变量，为空时返回 fallback。
func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// jsonUnmarshalMessages 解析 Anthropic Messages 请求体。
func jsonUnmarshalMessages(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

// ---------- Anthropic Messages 网关模式 ----------
//
// 背景：ZCode / Claude Code / Cline 这类客户端只认一个端点
// （ANTHROPIC_BASE_URL / 自定义模型 Base URL），聊天和联网搜索的旁路
// 请求都发往同一地址。wbmux 的 /v1/search/messages 只处理搜索请求，
// 直接当主端点用，普通聊天会被拒——第三方工具因此需要一个外挂代理
// 把两者接起来。
//
// 网关模式把这个"外挂"内置：POST /v1/messages 按请求内容分流——
//
//	tools 声明 web_search  → 走 wbmux 免费联网搜索（复用已登录凭据）
//	其余                   → 字节级透传到配置的上游模型端点
//
// 上游预期为 Anthropic Messages 兼容端点（StepFun / GLM / DeepSeek /
// Anthropic 官方均是），因此透传无需协议转换，流式响应原样转发。
//
// 配置（环境变量）：
//
//	WBMUX_GATEWAY_UPSTREAM   上游 Base URL，如 https://api.stepfun.com/step_plan
//	WBMUX_GATEWAY_API_KEY    上游 API Key（以 x-api-key 发送）
//	WBMUX_GATEWAY_BEARER     设为 1 时上游 Key 改以 Authorization: Bearer 发送
//
// 未配置上游时，非搜索请求返回 503 并附带配置说明，搜索功能不受影响。

// gatewayBodyLimit 与 OpenAI 代理一致：客户端可能把百万级上下文整段发过来。
const gatewayBodyLimit = 32 << 20

// gatewayUpstream 返回网关模式的聊天上游；空串表示未配置。
func (s *Server) gatewayUpstream() string {
	if s.gatewayUpstreamFn != nil {
		return s.gatewayUpstreamFn()
	}
	return strings.TrimRight(envOr("WBMUX_GATEWAY_UPSTREAM", ""), "/")
}

func (s *Server) gatewayAPIKey() string {
	if s.gatewayAPIKeyFn != nil {
		return s.gatewayAPIKeyFn()
	}
	return envOr("WBMUX_GATEWAY_API_KEY", "")
}

func (s *Server) gatewayBearerStyle() bool {
	if s.gatewayBearerFn != nil {
		return s.gatewayBearerFn()
	}
	return envOr("WBMUX_GATEWAY_BEARER", "") == "1"
}

// handleGatewayMessages 处理 POST /v1/messages：搜索分流 + 聊天透传。
func (s *Server) handleGatewayMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMessagesError(w, http.StatusMethodNotAllowed, "invalid_request_error", "仅支持 POST 请求")
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, gatewayBodyLimit))
	if err != nil {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "读取请求体失败")
		return
	}

	var req messagesRequest
	if err := jsonUnmarshalMessages(raw, &req); err != nil {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "请求体必须为合法 JSON")
		return
	}

	// 分流：带 web_search 服务端工具 → 免费联网搜索（复用已读取的请求体）。
	if declaresWebSearch(req.Tools) {
		s.runWebSearch(w, r, &req, time.Now())
		return
	}

	// 其余 → 透传上游模型端点。
	upstream := s.gatewayUpstream()
	if upstream == "" {
		writeMessagesError(w, http.StatusServiceUnavailable, "api_error",
			"网关模式未配置聊天上游：请设置环境变量 WBMUX_GATEWAY_UPSTREAM（Anthropic Messages 兼容端点，如 https://api.stepfun.com/step_plan）后重试；"+
				"仅使用联网搜索的客户端请继续访问 /v1/search/messages")
		return
	}

	start := time.Now()
	s.forwardGatewayChat(w, r, raw, upstream)
	s.note(ProxyLogEntry{
		Level: "ok",
		Model: firstNonEmpty(req.Model, "gateway"),
		Text:  "网关聊天透传完成 · 耗时 " + time.Since(start).Round(time.Millisecond).String(),
	})
}

// forwardGatewayChat 字节级透传聊天请求到上游，流式/非流式均原样回给客户端。
// 客户端自带的认证头全部剥离，替换为配置的上游 Key——调用方填什么占位符
// 都不影响，凭据只从服务端配置读取。
func (s *Server) forwardGatewayChat(w http.ResponseWriter, r *http.Request, body []byte, upstream string) {
	target := upstream + r.URL.Path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeMessagesError(w, http.StatusBadGateway, "api_error", "构造上游请求失败: "+err.Error())
		return
	}

	// 复制协议相关头，剥离客户端认证与传输相关头。
	for k, vs := range r.Header {
		lk := strings.ToLower(k)
		switch lk {
		case "host", "content-length", "connection", "transfer-encoding",
			"x-api-key", "authorization", "accept-encoding":
			continue
		}
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	if outReq.Header.Get("Content-Type") == "" {
		outReq.Header.Set("Content-Type", "application/json")
	}
	if key := s.gatewayAPIKey(); key != "" {
		if s.gatewayBearerStyle() {
			outReq.Header.Set("Authorization", "Bearer "+key)
		} else {
			outReq.Header.Set("x-api-key", key)
		}
	}

	resp, err := gatewayHTTPClient.Do(outReq)
	if err != nil {
		writeMessagesError(w, http.StatusBadGateway, "api_error", "上游不可达: "+err.Error())
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		switch strings.ToLower(k) {
		case "content-length", "connection", "transfer-encoding":
			continue // 由 Go 自行计算
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	// SSE 转发：与 streamChatThrough 同一套打法——先甩响应头（否则调用方
	// 要等上游第一个 token 才知道请求被接受，推理模型 TTFT 可达数秒），
	// 禁用中间层缓冲，再逐块 flush，杜绝 32KB 缓冲滞后。
	if strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(resp.StatusCode)
		flusher, canFlush := w.(http.Flusher)
		if canFlush {
			flusher.Flush()
		}
		buf := make([]byte, 32*1024)
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					return // 调用方断开，上游留给 defer 关
				}
				if canFlush {
					flusher.Flush()
				}
			}
			if readErr != nil {
				return // io.EOF 或上游中断，流到此结束
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleGatewayCountTokens 转发 /v1/messages/count_tokens 到上游；
// 未配置上游时给出明确错误而不是伪造数字。
func (s *Server) handleGatewayCountTokens(w http.ResponseWriter, r *http.Request) {
	upstream := s.gatewayUpstream()
	if upstream == "" {
		writeMessagesError(w, http.StatusServiceUnavailable, "api_error",
			"网关模式未配置聊天上游：请设置环境变量 WBMUX_GATEWAY_UPSTREAM")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, gatewayBodyLimit))
	if err != nil {
		writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "读取请求体失败")
		return
	}
	s.forwardGatewayChat(w, r, body, upstream)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
