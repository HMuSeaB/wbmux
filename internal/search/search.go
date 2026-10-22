package search

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// SearchPath 是官方客户端 WebSearchTool 调用的真实搜索端点。
	SearchPath = "/agenttool/v1/search"

	// DefaultTimeout 搜索请求整体超时，对齐官方 WEB_SEARCH_TIMEOUT_MS（20s）。
	DefaultTimeout = 20 * time.Second

	// DefaultMaxResults 官方默认返回条数。
	DefaultMaxResults = 5

	// MaxResultsCap 服务端实际截断上限（官方约 10 条）。
	MaxResultsCap = 10

	// MinQueryLen 官方最小搜索词长度。
	MinQueryLen = 2
)

// SearchResult 单条网页搜索结果条目。
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Site    string `json:"site,omitempty"`
	Favicon string `json:"favicon,omitempty"`
}

// SearchOutcome 一次搜索的完整结果集合。
type SearchOutcome struct {
	Query          string         `json:"query"`
	Results        []SearchResult `json:"results"`
	TotalResults   int            `json:"total_results"`
	Provider       string         `json:"provider,omitempty"`
	ResponseTimeMs int64          `json:"response_time_ms,omitempty"`
}

// ClampSearchMaxResults 把条数收敛到 [1, MaxResultsCap]，<=0 时回落默认值。
func ClampSearchMaxResults(n int) int {
	if n <= 0 {
		return DefaultMaxResults
	}
	if n > MaxResultsCap {
		return MaxResultsCap
	}
	return n
}

// searchRequestBody 出站搜索请求体。
// 必须严格使用结构体以保证 JSON 字段序列化顺序与官方客户端一致：query → type → max_results。
type searchRequestBody struct {
	Query      string `json:"query"`
	Type       string `json:"type"`
	MaxResults int    `json:"max_results"`
}

func randomUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ApplySearchHeaders 注入对齐官方客户端 WebSearchTool 的出站请求头族。
func ApplySearchHeaders(req *http.Request, token, uid string) {
	reqID := randomUUID()
	req.Header.Set("User-Agent", "WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Agent-Runtime", "other")
	req.Header.Set("X-Agent-Type", "chat")
	req.Header.Set("X-IDE-Type", "WorkBuddy")
	req.Header.Set("X-IDE-Name", "WorkBuddy")
	req.Header.Set("X-IDE-Version", "5.5.6")
	req.Header.Set("X-Product-Version", "5.5.6")

	// 会话标识头族（与真实桌面端对齐）
	req.Header.Set("X-Conversation-Request-ID", reqID)
	req.Header.Set("X-Conversation-Message-ID", reqID)
	req.Header.Set("X-Request-ID", reqID)
	req.Header.Set("X-Root-Request-ID", reqID)
	req.Header.Set("X-Trace-ID", reqID)

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
}

// DoSearch 发起一次纯 HTTP 上游搜索调用。
func DoSearch(ctx context.Context, client *http.Client, endpoint, token, uid, query string, maxResults int) (*SearchOutcome, error) {
	q := strings.TrimSpace(query)
	if len(q) < MinQueryLen {
		return nil, fmt.Errorf("搜索关键词长度至少需要 %d 个字符", MinQueryLen)
	}

	bodyBytes, err := json.Marshal(searchRequestBody{
		Query:      q,
		Type:       "text2text", // 官方固定值
		MaxResults: ClampSearchMaxResults(maxResults),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	url := strings.TrimRight(endpoint, "/") + SearchPath
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}

	ApplySearchHeaders(httpReq, token, uid)

	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read search response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("上游搜索服务异常 (HTTP %d): %s", resp.StatusCode, string(raw))
	}

	// 官方响应为扁平结构，但发生业务错误时会带 code / msg
	var parsed struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		SearchOutcome
	}

	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("parse search response: %w (raw: %s)", err, string(raw))
	}

	if parsed.Code != 0 {
		return nil, fmt.Errorf("上游业务错误 (code=%d): %s", parsed.Code, parsed.Msg)
	}

	out := parsed.SearchOutcome
	if out.Results == nil {
		out.Results = []SearchResult{}
	}
	if out.TotalResults == 0 && len(out.Results) > 0 {
		out.TotalResults = len(out.Results)
	}

	return &out, nil
}
