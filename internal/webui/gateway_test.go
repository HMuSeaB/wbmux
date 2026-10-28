package webui

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/search"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// newGatewayTestServer 搭建一个带假上游的服务器：搜索上游与网关聊天上游
// 分别是两个 httptest 实例，便于分别断言两条链路收到的内容。
func newGatewayTestServer(t *testing.T, searchUpstream, chatUpstream *httptest.Server) *Server {
	t.Helper()
	srv, err := New(Options{Token: "test_secret_token"})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	srv.upstreamBaseFn = func() string { return searchUpstream.URL }
	srv.intlCredFn = func(p *variant.Probe) (usage.ProxyCredentials, error) {
		return usage.ProxyCredentials{Token: "intl_token_abc", UID: "uid_123"}, nil
	}
	srv.gatewayUpstreamFn = func() string { return chatUpstream.URL }
	return srv
}

// fakeSearchUpstream 返回一个恒定结果的假搜索上游，并记录收到的 max_results。
func fakeSearchUpstream(t *testing.T, gotMaxResults *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agenttool/v1/search" {
			http.NotFound(w, r)
			return
		}
		var b struct {
			Query      string `json:"query"`
			MaxResults int    `json:"max_results"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		if gotMaxResults != nil {
			*gotMaxResults = b.MaxResults
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "` + b.Query + `",
			"results": [
				{"title":"Go 官网","url":"https://go.dev/doc","snippet":"Go 编程语言"},
				{"title":"GitHub","url":"https://github.com/golang/go","snippet":"源码"},
				{"title":"示例站点","url":"https://example.com/page","snippet":"无关站点"}
			],
			"total_results": 3
		}`))
	}))
}

// ---------- SSE 流式 ----------

func TestSearchMessagesSSEStream(t *testing.T) {
	gotMax := 0
	searchSrv := fakeSearchUpstream(t, &gotMax)
	defer searchSrv.Close()
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("流式搜索请求不应打到聊天上游")
	}))
	defer chatSrv.Close()

	srv := newGatewayTestServer(t, searchSrv, chatSrv)
	mux := http.NewServeMux()
	srv.registerOpenAI(mux)

	body := `{"model":"step-5-preview","stream":true,
		"tools":[{"type":"web_search_20260209","name":"web_search","max_uses":3}],
		"messages":[{"role":"user","content":"Perform a web search for the query: Go 1.24 特性"}]}`
	req := httptest.NewRequest("POST", "/v1/search/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test_secret_token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "event-stream") {
		t.Fatalf("content-type = %q, want event-stream", ct)
	}
	if gotMax != 3 {
		t.Errorf("上游 max_results = %d, want 3 (max_uses 应被尊重)", gotMax)
	}

	events := parseSSEEvents(t, w.Body.String())
	wantOrder := []string{
		"ping", "message_start",
		"content_block_start", "content_block_delta", "content_block_stop", // server_tool_use
		"content_block_start", "content_block_stop", // web_search_tool_result
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_delta", "content_block_stop", // text + 3 citations
		"message_delta", "message_stop",
	}
	if len(events) != len(wantOrder) {
		t.Fatalf("事件数 = %d %v, want %d %v", len(events), sseEventNames(events), len(wantOrder), wantOrder)
	}
	for i, want := range wantOrder {
		if events[i].event != want {
			t.Errorf("event[%d] = %q, want %q", i, events[i].event, want)
		}
	}

	raw := w.Body.String()
	for _, want := range []string{
		`"type":"server_tool_use"`,
		`"type":"web_search_tool_result"`,
		"https://go.dev/doc",
		`"stop_reason":"end_turn"`,
		`"web_search_requests":1`,
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("SSE 流缺少 %s", want)
		}
	}
}

// ---------- 网关模式路由 ----------

func TestGatewayChatPassthrough(t *testing.T) {
	searchSrv := fakeSearchUpstream(t, nil)
	defer searchSrv.Close()

	var gotPath, gotAuth, gotVersion, gotBody string
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotVersion = r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_up","type":"message","role":"assistant","model":"step-5-preview","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn"}`))
	}))
	defer chatSrv.Close()

	srv := newGatewayTestServer(t, searchSrv, chatSrv)
	srv.gatewayAPIKeyFn = func() string { return "gateway-up-key" }
	mux := http.NewServeMux()
	srv.registerOpenAI(mux)

	reqBody := `{"model":"step-5-preview","messages":[{"role":"user","content":"ping"}],"tools":[{"name":"WebSearch","input_schema":{"type":"object"}}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer test_secret_token")
	req.Header.Set("x-api-key", "client-placeholder")
	req.Header.Set("anthropic-version", "2023-06-01")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Errorf("上游路径 = %q, want /v1/messages", gotPath)
	}
	if gotAuth != "gateway-up-key" {
		t.Errorf("上游 x-api-key = %q, want 配置的网关 Key（客户端占位符应被替换）", gotAuth)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 透传", gotVersion)
	}
	if !strings.Contains(gotBody, "ping") {
		t.Errorf("上游请求体未完整透传: %q", gotBody)
	}
	if !strings.Contains(w.Body.String(), "pong") {
		t.Errorf("上游响应未透传: %s", w.Body.String())
	}
}

func TestGatewaySearchBranch(t *testing.T) {
	searchSrv := fakeSearchUpstream(t, nil)
	defer searchSrv.Close()
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("带 web_search 工具的请求不应打到聊天上游")
	}))
	defer chatSrv.Close()

	srv := newGatewayTestServer(t, searchSrv, chatSrv)
	mux := http.NewServeMux()
	srv.registerOpenAI(mux)

	// 与搜索端点相同的请求体，只是走 /v1/messages（网关模式）。
	body := `{"model":"step-5-preview",
		"tools":[{"type":"web_search_20260209","name":"web_search"}],
		"messages":[{"role":"user","content":"Perform a web search for the query: 网关分流测试"}]}`
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test_secret_token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "web_search_tool_result") {
		t.Errorf("网关未分流到搜索: %s", w.Body.String())
	}
}

func TestGatewayNoUpstreamConfigured(t *testing.T) {
	searchSrv := fakeSearchUpstream(t, nil)
	defer searchSrv.Close()
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer chatSrv.Close()

	srv := newGatewayTestServer(t, searchSrv, chatSrv)
	srv.gatewayUpstreamFn = func() string { return "" } // 未配置
	mux := http.NewServeMux()
	srv.registerOpenAI(mux)

	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer test_secret_token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if !strings.Contains(w.Body.String(), "WBMUX_GATEWAY_UPSTREAM") {
		t.Errorf("错误信息应指明如何配置: %s", w.Body.String())
	}

	// 未配置上游时搜索仍然可用（走搜索分支，不需要网关上游）。
	searchReq := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(
		`{"model":"m","tools":[{"name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: 仍可搜索"}]}`))
	searchReq.Header.Set("Authorization", "Bearer test_secret_token")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, searchReq)
	if w2.Code != http.StatusOK {
		t.Errorf("未配置上游时搜索应仍可用，得到 %d: %s", w2.Code, w2.Body.String())
	}
}

func TestGatewayCountTokens(t *testing.T) {
	searchSrv := fakeSearchUpstream(t, nil)
	defer searchSrv.Close()
	var gotPath string
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"input_tokens":42}`))
	}))
	defer chatSrv.Close()

	srv := newGatewayTestServer(t, searchSrv, chatSrv)
	mux := http.NewServeMux()
	srv.registerOpenAI(mux)

	req := httptest.NewRequest("POST", "/v1/messages/count_tokens", strings.NewReader(`{"model":"m","messages":[]}`))
	req.Header.Set("Authorization", "Bearer test_secret_token")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK || gotPath != "/v1/messages/count_tokens" {
		t.Errorf("count_tokens 转发异常: status=%d path=%q", w.Code, gotPath)
	}
}

// ---------- 域名过滤 ----------

func TestDomainAllowed(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		allowed []string
		blocked []string
		want    bool
	}{
		{"无过滤", "https://go.dev/doc", nil, nil, true},
		{"blocked 命中", "https://ads.example.com/x", nil, []string{"example.com"}, false},
		{"blocked 子域命中", "https://sub.example.com/x", nil, []string{"example.com"}, false},
		{"blocked 未命中", "https://go.dev", nil, []string{"example.com"}, true},
		{"allowed 命中", "https://go.dev/doc", []string{"go.dev"}, nil, true},
		{"allowed 子域命中", "https://www.go.dev/doc", []string{"go.dev"}, nil, true},
		{"allowed 未命中", "https://example.com", []string{"go.dev"}, nil, false},
		{"blocked 优先于 allowed", "https://go.dev", []string{"go.dev"}, []string{"go.dev"}, false},
		{"坏 URL 不拦", "://bad", nil, []string{"example.com"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := domainAllowed(c.url, c.allowed, c.blocked); got != c.want {
				t.Errorf("domainAllowed(%q) = %v, want %v", c.url, got, c.want)
			}
		})
	}
}

func TestFilterResultsByDomain(t *testing.T) {
	results := []search.SearchResult{
		{Title: "A", URL: "https://go.dev/doc"},
		{Title: "B", URL: "https://github.com/golang/go"},
		{Title: "C", URL: "https://example.com/page"},
	}
	got := filterResultsByDomain(results, []string{"go.dev", "github.com"}, nil)
	if len(got) != 2 || got[0].Title != "A" || got[1].Title != "B" {
		t.Errorf("allowed 过滤结果不符: %+v", got)
	}
	got = filterResultsByDomain(results, nil, []string{"example.com"})
	if len(got) != 2 || got[0].Title != "A" || got[1].Title != "B" {
		t.Errorf("blocked 过滤结果不符: %+v", got)
	}
	// 无过滤条件时原样返回。
	if got := filterResultsByDomain(results, nil, nil); len(got) != 3 {
		t.Errorf("无过滤应原样返回 3 条，得到 %d", len(got))
	}
}

// ---------- SSE 解析辅助 ----------

type sseEvent struct {
	event string
	data  string
}

func parseSSEEvents(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var cur sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.event != "" || cur.data != "" {
				events = append(events, cur)
				cur = sseEvent{}
			}
		}
	}
	if cur.event != "" {
		events = append(events, cur)
	}
	return events
}

func sseEventNames(events []sseEvent) []string {
	names := make([]string, len(events))
	for i, e := range events {
		names[i] = e.event
	}
	return names
}
