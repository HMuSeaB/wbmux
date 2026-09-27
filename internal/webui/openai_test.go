package webui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 模型名还原 ----------

// TestNormalizeModelName 钉住 2026-09-27 那个"注入的模型一用就 502"的根因：
// 客户端把注入条目的 id 原样当 model 发出，带前缀的名字上游不认。
func TestNormalizeModelName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"注入前缀要剥", custommodels.IDPrefix + "deepseek-v4.1-flash", "deepseek-v4.1-flash"},
		{"客户端前缀要剥", "custom-local:gpt-5.6-sol", "gpt-5.6-sol"},
		{"两种前缀叠加时都剥", "custom-local:" + custommodels.IDPrefix + "hy4-preview-f", "hy4-preview-f"},
		{"官方 id 原样通过", "deepseek-v4.1-flash", "deepseek-v4.1-flash"},
		{"只加自定义前缀也要剥", "custom-local:" + custommodels.IDPrefix + "hy3", "hy3"},
		{"两端空白要清", "  " + custommodels.IDPrefix + "hy3  ", "hy3"},
		{"只剩前缀时还原为空（调用方按缺 model 处理）", custommodels.IDPrefix, ""},
		{"空串", "", ""},
		// 前缀只能在开头：出现在中间的名字是官方名字的一部分，不能动。
		{"中间出现前缀不动", "foo-wbmux-intl-bar", "foo-wbmux-intl-bar"},
	}

	for _, c := range cases {
		if got := normalizeModelName(c.in); got != c.want {
			t.Errorf("%s：normalizeModelName(%q) = %q，期望 %q", c.name, c.in, got, c.want)
		}
	}
}

// ---------- system 首条 ----------

// TestEnsureSystemFirst 钉住官方接口的硬要求：第一条必须是 system
// （实测 code 11128）。四种形态：缺 system 要补、system 不在首位要挪、
// 已在首位不动、空消息列表给默认。
func TestEnsureSystemFirst(t *testing.T) {
	msg := func(role string) map[string]any { return map[string]any{"role": role, "content": "x"} }
	firstIsSystem := func(msgs []any) bool { return len(msgs) > 0 && roleOf(msgs[0]) == "system" }

	got := ensureSystemFirst([]any{msg("user")})
	if !firstIsSystem(got) || len(got) != 2 {
		t.Errorf("缺 system 时应补默认 system，得到 %v", got)
	}

	got = ensureSystemFirst([]any{msg("user"), msg("system")})
	if !firstIsSystem(got) || len(got) != 2 {
		t.Errorf("不在首位的 system 应挪到首位，得到 %v", got)
	}

	orig := []any{msg("system"), msg("user")}
	if got = ensureSystemFirst(orig); len(got) != 2 || roleOf(got[1]) != "user" {
		t.Errorf("首位已是 system 不应改动：%v", got)
	}

	if got = ensureSystemFirst(nil); !firstIsSystem(got) {
		t.Errorf("空列表应给默认 system")
	}
}

// TestEnsureSystemFirstKeepsMessageFields 挪动 system 时其余消息必须
// 逐字保留——tool_calls / tool_call_id 这些字段丢了，多轮工具调用就断了。
func TestEnsureSystemFirstKeepsMessageFields(t *testing.T) {
	toolMsg := map[string]any{
		"role":         "tool",
		"tool_call_id": "call_1",
		"content":      "42",
	}
	got := ensureSystemFirst([]any{toolMsg, map[string]any{"role": "system", "content": "sys"}})
	if len(got) != 2 {
		t.Fatalf("挪动而非新增，应仍是 2 条，得到 %d", len(got))
	}
	last, ok := got[1].(map[string]any)
	if !ok || last["tool_call_id"] != "call_1" || last["role"] != "tool" {
		t.Errorf("消息字段被改动了：%v", got[1])
	}
}

// ---------- 请求体改写 ----------

// TestBuildUpstreamBodyKeepsEverything 钉住"透传"这件事：除了
// model / stream / messages 三处，调用方发来的一律原样带到上游。
// 尤其是 tools —— 少了它，国内壳子里的 Agent 就永远不调工具。
func TestBuildUpstreamBodyKeepsEverything(t *testing.T) {
	in := `{
	  "model": "custom-local:wbmux-intl-deepseek-v4.1-flash",
	  "stream": false,
	  "temperature": 0.3,
	  "max_tokens": 1024,
	  "tools": [{"type":"function","function":{"name":"Read"}},{"type":"function","function":{"name":"Bash"}}],
	  "tool_choice": "auto",
	  "messages": [{"role":"user","content":"hi"}]
	}`
	up, err := buildUpstreamBody([]byte(in))
	if err != nil {
		t.Fatalf("buildUpstreamBody: %v", err)
	}
	if up.Model != "deepseek-v4.1-flash" {
		t.Errorf("模型名未还原：%q", up.Model)
	}
	if up.Body["model"] != "deepseek-v4.1-flash" {
		t.Errorf("请求体里的 model 未还原：%v", up.Body["model"])
	}
	if up.Body["stream"] != true {
		t.Errorf("上游只支持流式，stream 必须强制为 true：%v", up.Body["stream"])
	}
	if up.ClientStream {
		t.Errorf("调用方要的是非流式，ClientStream 应为 false")
	}
	if up.Tools != 2 {
		t.Errorf("工具定义数量应为 2，得到 %d", up.Tools)
	}
	if _, ok := up.Body["tools"]; !ok {
		t.Errorf("tools 被丢掉了：%v", up.Body)
	}
	if up.Body["tool_choice"] != "auto" {
		t.Errorf("tool_choice 被丢掉了：%v", up.Body)
	}
	if up.Body["temperature"] != json.Number("0.3") {
		t.Errorf("temperature 应保持原样，得到 %v", up.Body["temperature"])
	}
	if up.Body["max_tokens"] != json.Number("1024") {
		t.Errorf("max_tokens 应保持原样，得到 %v", up.Body["max_tokens"])
	}
	msgs, _ := up.Body["messages"].([]any)
	if len(msgs) != 2 || roleOf(msgs[0]) != "system" {
		t.Errorf("system 应被补到首位：%v", msgs)
	}
}

func TestBuildUpstreamBodyRejectsGarbage(t *testing.T) {
	for _, in := range []string{`[]`, `"str"`, `{`, `null`, `{"model":""}`, `{"model":"wbmux-intl-"}`} {
		if _, err := buildUpstreamBody([]byte(in)); err == nil {
			t.Errorf("输入 %q 应当报错", in)
		}
	}
}

// ---------- 上游错误信息 ----------

// TestUpstreamMessage 官方错误体要取出中文那句话，而不是把
// "502"这种没头没尾的东西抛给用户。
func TestUpstreamMessage(t *testing.T) {
	raw := []byte(`{"code":11102,"msg":"model [x] service info not found",` +
		`"displayMsg":{"en":"The requested model is not available.","zh":"当前模型不可用，请切换其他模型后重试。"}}`)
	if got := upstreamMessage(raw); got != "当前模型不可用，请切换其他模型后重试。" {
		t.Errorf("应优先取 displayMsg.zh，得到 %q", got)
	}
	if got := upstreamMessage([]byte(`{"error":{"message":"bad key"}}`)); got != "bad key" {
		t.Errorf("OpenAI 形态的错误应能取到，得到 %q", got)
	}
	if got := upstreamMessage([]byte("plain\ntext")); got != "plain text" {
		t.Errorf("非 JSON 应压平空白后原样返回，得到 %q", got)
	}
}

// ---------- 端到端：假上游 ----------

// fakeUpstream 收下一份请求体，按脚本回 SSE。
func fakeUpstream(t *testing.T, sse string, gotBody *map[string]any, wantPath *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantPath != nil {
			*wantPath = r.URL.Path
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		*gotBody = body
		if r.Header.Get("Authorization") == "" {
			t.Errorf("未带上游鉴权头")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
}

// newProxyForTest 造一个只差上游地址与凭据的服务端（不监听端口）。
func newProxyForTest(t *testing.T, upstream string, logs *[]string) *Server {
	t.Helper()
	srv, err := New(Options{Token: "test-token", Probe: &variant.Probe{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.upstreamBaseFn = func() string { return upstream }
	srv.intlCredFn = func(*variant.Probe) (usage.ProxyCredentials, error) {
		return usage.ProxyCredentials{Token: "up-token", UID: "u1"}, nil
	}
	if logs != nil {
		srv.logFn = func(format string, args ...any) {
			*logs = append(*logs, fmt.Sprintf(format, args...))
		}
	}
	return srv
}

// TestChatForwardsToolsAndStripsPrefix 是这次故障的回归测试：
// 带前缀的模型名 + 带工具的请求，必须原样（除三处外）打到上游，
// 并且把上游结果回给调用方。
func TestChatForwardsToolsAndStripsPrefix(t *testing.T) {
	var got map[string]any
	var path string
	sse := "data: {\"model\":\"deepseek-v4.1-flash\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	up := fakeUpstream(t, sse, &got, &path)
	defer up.Close()

	srv := newProxyForTest(t, up.URL, nil)
	body := `{"model":"wbmux-intl-deepseek-v4.1-flash","stream":false,
	  "tools":[{"type":"function","function":{"name":"Read"}}],
	  "messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleOpenAIChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d，期望 200：%s", rec.Code, rec.Body.String())
	}
	if path != chatCompletionsPath {
		t.Errorf("上游路径 %q，期望 %q", path, chatCompletionsPath)
	}
	if got["model"] != "deepseek-v4.1-flash" {
		t.Errorf("上游收到的模型名未剥前缀：%v", got["model"])
	}
	if got["stream"] != true {
		t.Errorf("上游应收到 stream=true：%v", got["stream"])
	}
	if _, ok := got["tools"]; !ok {
		t.Errorf("上游没收到 tools：%v", got)
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 JSON：%v（%s）", err, rec.Body.String())
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hi" {
		t.Errorf("内容聚合不对：%s", rec.Body.String())
	}
}

// TestChatNonStreamAggregatesToolCalls 非流式下的工具调用必须拼回完整
// 参数：arguments 是一片片流下来的，少拼一片调用方就解析不出参数。
func TestChatNonStreamAggregatesToolCalls(t *testing.T) {
	var got map[string]any
	sse := "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Read\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"a.txt\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	up := fakeUpstream(t, sse, &got, nil)
	defer up.Close()

	srv := newProxyForTest(t, up.URL, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"wbmux-intl-m","stream":false,"messages":[{"role":"user","content":"x"}]}`))
	rec := httptest.NewRecorder()
	srv.handleOpenAIChat(rec, req)

	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
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
	if len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("工具调用未聚合：%s", rec.Body.String())
	}
	tc := resp.Choices[0].Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "Read" || tc.Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("工具调用拼装错误：%+v", tc)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish_reason 应为 tool_calls，得到 %q", resp.Choices[0].FinishReason)
	}
}

// TestChatPassesThroughUpstreamStatus 上游拒绝时不能再报 502：
// 502 会被客户端理解成"网关挂了"而去切换模型，把排查方向带偏。
func TestChatPassesThroughUpstreamStatus(t *testing.T) {
	var got map[string]any
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":11102,"msg":"model x not found","displayMsg":{"zh":"当前模型不可用。"}}`)
	}))
	defer up.Close()

	var logs []string
	srv := newProxyForTest(t, up.URL, &logs)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"wbmux-intl-x","messages":[{"role":"user","content":"x"}]}`))
	rec := httptest.NewRecorder()
	srv.handleOpenAIChat(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("上游 400 应原样透传，得到 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "当前模型不可用") {
		t.Errorf("错误信息应带上上游的可读说明：%s", rec.Body.String())
	}
	// 没有日志出口的话，这类失败在用户侧只剩一句状态码，排查无从下手。
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "上游 HTTP 400") || !strings.Contains(joined, "当前模型不可用") {
		t.Errorf("失败必须落日志，实际日志：%s", joined)
	}
}

// TestChatRejectsBadRequests 调用方的问题要在本地就挡住，别浪费一次上游往返。
func TestChatRejectsBadRequests(t *testing.T) {
	var logs []string
	srv := newProxyForTest(t, "http://127.0.0.1:1", &logs)
	cases := []struct {
		method string
		body   string
		want   int
	}{
		{method: http.MethodGet, body: "", want: http.StatusMethodNotAllowed},
		{method: http.MethodPost, body: "{", want: http.StatusBadRequest},
		{method: http.MethodPost, body: `{"messages":[]}`, want: http.StatusBadRequest},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/v1/chat/completions", bytes.NewReader([]byte(c.body)))
		rec := httptest.NewRecorder()
		srv.handleOpenAIChat(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %q → %d，期望 %d", c.method, c.body, rec.Code, c.want)
		}
	}
}

// TestChatStreamIsByteExact 流式转发必须逐字节原样。
//
// SSE 的事件分隔是空行：少了空行，解析方会把整段流当成一个永远没结束的
// 事件，表现成"跑了几秒什么都没收到"（2026-09-27 国内客户端里那个没头没尾
// 的错误码就是这么来的）。所以这里断言的不是"含有关键字"，而是字节全等。
func TestChatStreamIsByteExact(t *testing.T) {
	sse := "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n" +
		": keep-alive 注释行也要原样过\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n" +
		"data: [DONE]\n\n"
	var got map[string]any
	up := fakeUpstream(t, sse, &got, nil)
	defer up.Close()

	srv := newProxyForTest(t, up.URL, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"wbmux-intl-m","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	rec := httptest.NewRecorder()
	srv.handleOpenAIChat(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); body != sse {
		t.Errorf("流式转发不该改动任何字节。\n得到 %q\n期望 %q", body, sse)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type 应为 text/event-stream，得到 %q", ct)
	}
}
