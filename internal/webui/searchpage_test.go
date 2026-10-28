package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/search"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestExtractMessagesQuery(t *testing.T) {
	cases := []struct {
		name string
		in   []messagesTurnItem
		want string
	}{
		{
			name: "简单纯字符串带前缀",
			in: []messagesTurnItem{
				{Role: "user", Content: json.RawMessage(`"Perform a web search for the query: 深度学习最新进展"`)},
			},
			want: "深度学习最新进展",
		},
		{
			name: "前缀大小写不同与空格",
			in: []messagesTurnItem{
				{Role: "user", Content: json.RawMessage(`"perform a WEB search for the query:   Go 1.24 特性   "`)},
			},
			want: "Go 1.24 特性",
		},
		{
			name: "无前缀普通文本",
			in: []messagesTurnItem{
				{Role: "user", Content: json.RawMessage(`"Python FastAPI 教程"`)},
			},
			want: "Python FastAPI 教程",
		},
		{
			name: "多轮会话取最后一条",
			in: []messagesTurnItem{
				{Role: "user", Content: json.RawMessage(`"第一轮问题"`)},
				{Role: "assistant", Content: json.RawMessage(`"回答一"`)},
				{Role: "user", Content: json.RawMessage(`"Perform a web search for the query: 第二轮问题"`)},
			},
			want: "第二轮问题",
		},
		{
			name: "Block 数组结构",
			in: []messagesTurnItem{
				{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"Perform a web search for the query: Vue3 组合式 API"}]`)},
			},
			want: "Vue3 组合式 API",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractMessagesQuery(tc.in)
			if got != tc.want {
				t.Errorf("extractMessagesQuery() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDeclaresWebSearch(t *testing.T) {
	if !declaresWebSearch([]messagesTool{{Type: "web_search_20250305"}}) {
		t.Error("type 以 web_search 开头应返回 true")
	}
	if !declaresWebSearch([]messagesTool{{Name: "web_search"}}) {
		t.Error("name 为 web_search 应返回 true")
	}
	if declaresWebSearch([]messagesTool{{Name: "bash", Type: "custom"}}) {
		t.Error("其他工具应返回 false")
	}
}

func TestBuildMessagesSearchResponse(t *testing.T) {
	outcome := &search.SearchOutcome{
		Query:        "Go语言",
		TotalResults: 1,
		Results: []search.SearchResult{
			{
				Title:   "Go官网",
				URL:     "https://go.dev",
				Snippet: "开源编程语言",
				Site:    "go.dev",
			},
		},
	}

	resp := buildMessagesSearchResponse("Go语言", "claude-3-5-sonnet", outcome)
	if !strings.HasPrefix(resp.ID, "msg_wbmux_") {
		t.Errorf("ID 应以 msg_wbmux_ 开头，得到 %s", resp.ID)
	}
	if resp.Role != "assistant" || resp.Model != "claude-3-5-sonnet" {
		t.Errorf("Role 或 Model 不符: %+v", resp)
	}
	if len(resp.Content) != 3 {
		t.Fatalf("content blocks 应为 3（server_tool_use + 结果块 + 文本引用块），得到 %d", len(resp.Content))
	}

	toolResult, ok := resp.Content[1].(messagesToolResultBlock)
	if !ok || toolResult.Type != "web_search_tool_result" {
		t.Errorf("第一个 block 应为 web_search_tool_result")
	}
	if len(toolResult.Content) != 1 || toolResult.Content[0].URL != "https://go.dev" {
		t.Errorf("web_search_tool_result content 不符: %+v", toolResult.Content)
	}

	textBlock, ok := resp.Content[2].(messagesTextBlock)
	if !ok || textBlock.Type != "text" {
		t.Errorf("第二个 block 应为 text")
	}
	if len(textBlock.Citations) != 1 || textBlock.Citations[0].CitedText != "开源编程语言" {
		t.Errorf("citations 不符: %+v", textBlock.Citations)
	}
}

func TestSearchMessagesEndpointIntegration(t *testing.T) {
	// 启动假上游服务
	var gotQuery string
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agenttool/v1/search" {
			http.NotFound(w, r)
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotQuery, _ = b["query"].(string)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "` + gotQuery + `",
			"type": "text2text",
			"results": [
				{"title":"搜寻结果标题","url":"https://example.com","snippet":"网页抓取摘要"}
			],
			"total_results": 1
		}`))
	}))
	defer fakeUpstream.Close()

	srv, err := New(Options{Token: "test_secret_token"})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	srv.upstreamBaseFn = func() string { return fakeUpstream.URL }
	srv.intlCredFn = func(p *variant.Probe) (usage.ProxyCredentials, error) {
		return usage.ProxyCredentials{Token: "intl_token_abc", UID: "uid_123"}, nil
	}

	mux := http.NewServeMux()
	srv.registerOpenAI(mux)

	// 1. 测试未带 web_search 工具声明被拒 (400)
	noToolBody := `{"model":"deepseek-chat","tools":[{"name":"bash"}],"messages":[{"role":"user","content":"hello"}]}`
	req1 := httptest.NewRequest("POST", "/v1/search/messages", strings.NewReader(noToolBody))
	req1.Header.Set("Authorization", "Bearer test_secret_token")
	w1 := httptest.NewRecorder()
	mux.ServeHTTP(w1, req1)
	if w1.Code != http.StatusBadRequest {
		t.Errorf("未声明 web_search 工具应返回 400，得到 %d", w1.Code)
	}

	// 2. 正常 DSH 搜索请求
	dshBody := `{
		"model": "deepseek-chat",
		"tools": [{"type": "web_search_20250305"}],
		"messages": [
			{"role": "user", "content": "Perform a web search for the query: 腾讯元宝模型特性"}
		]
	}`
	req2 := httptest.NewRequest("POST", "/v1/search/messages", strings.NewReader(dshBody))
	req2.Header.Set("Authorization", "Bearer test_secret_token")
	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("搜索请求应成功 200，得到 %d: %s", w2.Code, w2.Body.String())
	}
	if gotQuery != "腾讯元宝模型特性" {
		t.Errorf("上游收到的搜索词应为剥除前缀后的真实 Query，实际收到: %q", gotQuery)
	}

	var parsedResp messagesResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &parsedResp); err != nil {
		t.Fatalf("响应反序列化失败: %v", err)
	}
	if len(parsedResp.Content) != 3 {
		t.Errorf("响应 content blocks 长度应为 3，得到 %d", len(parsedResp.Content))
	}

	// 3. 极简结构化搜索 GET /v1/search?q=Golang
	req3 := httptest.NewRequest("GET", "/v1/search?q=Golang", nil)
	req3.Header.Set("Authorization", "Bearer test_secret_token")
	w3 := httptest.NewRecorder()
	mux.ServeHTTP(w3, req3)

	if w3.Code != http.StatusOK {
		t.Fatalf("GET /v1/search 应返回 200，得到 %d: %s", w3.Code, w3.Body.String())
	}
	var resMap map[string]any
	_ = json.Unmarshal(w3.Body.Bytes(), &resMap)
	if resMap["ok"] != true || resMap["query"] != "Golang" {
		t.Errorf("GET /v1/search 响应不符: %+v", resMap)
	}
}

// TestSearchLoopbackAuthMatrix 钉住"一键接入各客户端"的鉴权边界。
//
// 2026-10-28 的教训：各客户端带凭据的方式五花八门——有的发
// Authorization、有的发 x-api-key、有的只带一个 sk-xxx 占位、还有的
// 压根不发认证头。原先要求"回环 + 携带任意非空 Key"，结果不发头的客户端
// 直接 403，用户只看到"令牌无效"，根本猜不到是自己没填 Key。
//
// 规矩定成两条：
//   - 回环（本机）：怎么带都行，包括不带 —— 只读搜索，越权面仅限本机
//   - 远端：必须持有正确 token
func TestSearchLoopbackAuthMatrix(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":"x","results":[{"title":"t","url":"https://e.com"}],"total_results":1}`))
	}))
	defer fake.Close()

	srv, err := New(Options{Token: "secret_tok"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.upstreamBaseFn = func() string { return fake.URL }
	srv.intlCredFn = func(p *variant.Probe) (usage.ProxyCredentials, error) {
		return usage.ProxyCredentials{Token: "intl", UID: "uid"}, nil
	}
	mux := http.NewServeMux()
	srv.registerOpenAI(mux)

	cases := []struct {
		name    string
		remote  string
		headers map[string]string
		want    int
	}{
		{"回环+无任何认证头", "127.0.0.1:5555", nil, http.StatusOK},
		{"回环+占位Key", "127.0.0.1:5555", map[string]string{"Authorization": "Bearer sk-xxx"}, http.StatusOK},
		{"回环+x-api-key占位", "127.0.0.1:5555", map[string]string{"x-api-key": "anything"}, http.StatusOK},
		{"回环+IPv6", "[::1]:5555", nil, http.StatusOK},
		{"远端+无Key", "203.0.113.9:1234", nil, http.StatusForbidden},
		{"远端+错误Key", "203.0.113.9:1234", map[string]string{"Authorization": "Bearer nope"}, http.StatusForbidden},
		{"远端+正确Key", "203.0.113.9:1234", map[string]string{"Authorization": "Bearer secret_tok"}, http.StatusOK},
		{"远端+x-api-key正确", "203.0.113.9:1234", map[string]string{"x-api-key": "secret_tok"}, http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/search?q=hello", nil)
			req.RemoteAddr = c.remote
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != c.want {
				t.Errorf("期望 %d，得到 %d：%s", c.want, w.Code, w.Body.String())
			}
		})
	}
}
