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
	if len(resp.Content) != 2 {
		t.Fatalf("content blocks 应为 2（结果块 + 文本引用块），得到 %d", len(resp.Content))
	}

	toolResult, ok := resp.Content[0].(messagesToolResultBlock)
	if !ok || toolResult.Type != "web_search_tool_result" {
		t.Errorf("第一个 block 应为 web_search_tool_result")
	}
	if len(toolResult.Content) != 1 || toolResult.Content[0].URL != "https://go.dev" {
		t.Errorf("web_search_tool_result content 不符: %+v", toolResult.Content)
	}

	textBlock, ok := resp.Content[1].(messagesTextBlock)
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
	if len(parsedResp.Content) != 2 {
		t.Errorf("响应 content blocks 长度应为 2，得到 %d", len(parsedResp.Content))
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
