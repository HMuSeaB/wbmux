package webui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// liveProxyEnv 打开后才跑真上游的联调用例。
//
// # 为什么要在仓库里留一个默认跳过的用例
//
// "模型名对不对"只有上游说了算：本地无论怎么测都发现不了
// 上游不认这个名字。2026-09-27 那次"注入的模型一用就 502"正是这类错误
// ——代码路径全对，只是转发出去的名字带着我们自己的 id 前缀。
// 想复现这类问题，必须真发一次请求。
//
//	WBMUX_LIVE_PROBE=1 go test ./internal/webui/ -run LiveUpstream -v
//
// 用的是限时免费模型，消耗 0 积分；凭据从本机国际客户端认证文件读。
func liveGuard(t *testing.T) *Server {
	t.Helper()
	if os.Getenv("WBMUX_LIVE_PROBE") == "" {
		t.Skip("需要 WBMUX_LIVE_PROBE=1（会真的请求国际后端）")
	}
	probe := variant.DefaultProbe()
	srv, err := New(Options{Token: "live", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := srv.intlCred(probe); err != nil {
		t.Skipf("本机国际侧凭据不可用：%v", err)
	}
	return srv
}

// livePayload 用**注入条目真实的形态**发请求：model 带 IDPrefix，
// 带工具定义，非流式。这正是国内客户端会发出的东西。
func livePayload(stream bool) string {
	body := map[string]any{
		"model":      custommodels.IDPrefix + "deepseek-v4.1-flash",
		"stream":     stream,
		"max_tokens": 128,
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        "read_file",
				"description": "读取一个文件的全部内容",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []any{"path"},
				},
			},
		}},
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant. Use tools when needed."},
			map[string]any{"role": "user", "content": "读一下 D:/tmp/a.txt 的内容"},
		},
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

// TestLiveUpstreamNonStream 非流式：应拿到聚合后的 tool_calls。
func TestLiveUpstreamNonStream(t *testing.T) {
	srv := liveGuard(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(livePayload(false)))
	rec := httptest.NewRecorder()
	srv.handleOpenAIChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	if resp.Model != "deepseek-v4.1-flash" {
		t.Errorf("上游服务的模型名应为去掉前缀的官方名，得到 %q", resp.Model)
	}
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) == 0 {
		t.Fatalf("模型没有回工具调用（注入条目声明了 supportsToolCall=true，这不该发生）：%s", rec.Body.String())
	}
	call := resp.Choices[0].Message.ToolCalls[0]
	if call.Function.Name != "read_file" {
		t.Errorf("工具名不对：%q", call.Function.Name)
	}
	if !json.Valid([]byte(call.Function.Arguments)) {
		t.Errorf("工具参数没拼成合法 JSON：%q", call.Function.Arguments)
	}
}

// TestLiveUpstreamStream 流式：SSE 里必须带着 tool_calls 增量原样过。
func TestLiveUpstreamStream(t *testing.T) {
	srv := liveGuard(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(livePayload(true)))
	rec := httptest.NewRecorder()
	srv.handleOpenAIChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	body, _ := io.ReadAll(rec.Body)
	text := string(body)
	if !strings.Contains(text, "data:") || !strings.Contains(text, "[DONE]") {
		t.Fatalf("SSE 形状不对：%s", cutStr(text, 300))
	}
	if !strings.Contains(text, `"tool_calls"`) {
		t.Errorf("流式响应里没有工具调用增量：%s", cutStr(text, 500))
	}
	if !strings.Contains(text, "data: {\"id\"") {
		t.Errorf("SSE 数据行被改动了：%s", cutStr(text, 300))
	}
}
