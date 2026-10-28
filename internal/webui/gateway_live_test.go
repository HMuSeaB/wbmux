package webui

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 真实 TCP 端到端 ----------
//
// 下面这组用例与 gateway_test.go 的 recorder 级测试互补：这里把网关 mux
// 挂在真实 net.Listener 上，用真实 HTTP 客户端（含流式读取）打过去，
// 覆盖「Content-Type 判定 → 分块写出 → flush 时序」这些 recorder 级
// 测试看不到的行为。SSE 之所以在 recorder 测试里能过、到真实客户端却
// 可能挂，通常就是 flush/Transfer-Encoding 的问题。

// startGateway 在随机端口启动网关 mux，返回基地址。
func startGateway(t *testing.T, srv *Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	srv.registerOpenAI(mux)
	httpSrv := &http.Server{Handler: mux}
	go func() { _ = httpSrv.Serve(ln) }()
	t.Cleanup(func() { _ = httpSrv.Close() })
	return "http://" + ln.Addr().String()
}

func newLiveGatewayServer(t *testing.T, searchUp, chatUp string) *Server {
	t.Helper()
	srv, err := New(Options{Token: "test_secret_token"})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	srv.upstreamBaseFn = func() string { return searchUp }
	srv.intlCredFn = func(p *variant.Probe) (usage.ProxyCredentials, error) {
		return usage.ProxyCredentials{Token: "intl_token_abc", UID: "uid_123"}, nil
	}
	srv.gatewayUpstreamFn = func() string { return chatUp }
	return srv
}

// liveSearchUpstream 恒定结果的假搜索上游。
func liveSearchUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "x",
			"results": [
				{"title":"Go 1.24 Release Notes","url":"https://go.dev/doc/go1.24","snippet":"Go 1.24 arrives in February 2025."},
				{"title":"golang/go 源码","url":"https://github.com/golang/go","snippet":"The Go programming language"}
			],
			"total_results": 2
		}`))
	}))
}

func TestLiveGatewayChatOverTCP(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("x-api-key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_live","type":"message","role":"assistant","model":"step-5-preview","content":[{"type":"text","text":"真实 TCP 透传成功"}],"stop_reason":"end_turn"}`))
	}))
	defer chat.Close()
	search := liveSearchUpstream(t)
	defer search.Close()

	srv := newLiveGatewayServer(t, search.URL, chat.URL)
	srv.gatewayAPIKeyFn = func() string { return "live-up-key" }
	base := startGateway(t, srv)

	reqBody := `{"model":"step-5-preview","max_tokens":1024,"messages":[{"role":"user","content":"你好"}]}`
	resp, err := http.Post(base+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	if gotPath != "/v1/messages" || gotAuth != "live-up-key" {
		t.Errorf("上游收到 path=%q auth=%q，want /v1/messages + live-up-key", gotPath, gotAuth)
	}
	if !strings.Contains(gotBody, "你好") {
		t.Errorf("上游请求体不完整: %q", gotBody)
	}
	if !strings.Contains(string(body), "真实 TCP 透传成功") {
		t.Errorf("响应未透传: %s", body)
	}
}

func TestLiveGatewaySearchSSEOverTCP(t *testing.T) {
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("带 web_search 工具的请求不应到达聊天上游")
	}))
	defer chat.Close()
	search := liveSearchUpstream(t)
	defer search.Close()

	srv := newLiveGatewayServer(t, search.URL, chat.URL)
	base := startGateway(t, srv)

	// 流式搜索：客户端按 SSE 逐行读取，验证 flush 时序真实可用。
	reqBody := `{"model":"step-5-preview","stream":true,
		"tools":[{"type":"web_search_20260209","name":"web_search","max_uses":5}],
		"messages":[{"role":"user","content":"Perform a web search for the query: Go 1.24 发布说明"}]}`
	resp, err := http.Post(base+"/v1/messages", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/messages: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Fatalf("content-type = %q, want event-stream", ct)
	}

	var events []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("读取 SSE 流: %v", err)
	}

	wantOrder := []string{
		"ping", "message_start",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	if len(events) != len(wantOrder) {
		t.Fatalf("SSE 事件 = %v, want %v", events, wantOrder)
	}
	for i, want := range wantOrder {
		if events[i] != want {
			t.Errorf("event[%d] = %q, want %q", i, events[i], want)
		}
	}
}

func TestLiveGatewayCountTokensOverTCP(t *testing.T) {
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("path = %q, want /v1/messages/count_tokens", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"input_tokens":128}`))
	}))
	defer chat.Close()
	search := liveSearchUpstream(t)
	defer search.Close()

	srv := newLiveGatewayServer(t, search.URL, chat.URL)
	base := startGateway(t, srv)

	resp, err := http.Post(base+"/v1/messages/count_tokens", "application/json",
		strings.NewReader(`{"model":"m","messages":[]}`))
	if err != nil {
		t.Fatalf("POST count_tokens: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	var parsed struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if parsed.InputTokens != 128 {
		t.Errorf("input_tokens = %d, want 128", parsed.InputTokens)
	}
}

func TestLiveSearchMessagesNoToolRejected(t *testing.T) {
	search := liveSearchUpstream(t)
	defer search.Close()
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer chat.Close()

	srv := newLiveGatewayServer(t, search.URL, chat.URL)
	base := startGateway(t, srv)

	// 纯搜索端点保持原有语义：不声明 web_search 一律拒绝。
	resp, err := http.Post(base+"/v1/search/messages", "application/json",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
