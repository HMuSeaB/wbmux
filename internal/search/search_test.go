package search

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchRequestShape(t *testing.T) {
	var gotPath string
	var gotRawBody string
	var gotHeaders http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeaders = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		gotRawBody = string(raw)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"query":"Go语言","type":"text2text","results":[],"total_results":0}`))
	}))
	defer srv.Close()

	out, err := DoSearch(context.Background(), srv.Client(), srv.URL, "token_123", "uid_456", "  Go语言  ", 0)
	if err != nil {
		t.Fatalf("DoSearch: %v", err)
	}
	if out.Query != "Go语言" {
		t.Errorf("query=%q want 'Go语言'", out.Query)
	}

	if gotPath != "/agenttool/v1/search" {
		t.Errorf("path=%q want /agenttool/v1/search", gotPath)
	}

	// 验证请求体严格对齐官方客户端序列化结构：query → type → max_results
	wantBody := `{"query":"Go语言","type":"text2text","max_results":5}`
	if gotRawBody != wantBody {
		t.Errorf("rawBody=%s, want=%s", gotRawBody, wantBody)
	}

	// 关键请求头验证
	if gotHeaders.Get("Accept") != "application/json" {
		t.Errorf("Accept=%q want application/json", gotHeaders.Get("Accept"))
	}
	if gotHeaders.Get("Content-Type") != "application/json;charset=UTF-8" {
		t.Errorf("Content-Type=%q want application/json;charset=UTF-8", gotHeaders.Get("Content-Type"))
	}
	if gotHeaders.Get("X-Agent-Intent") != "craft" {
		t.Errorf("X-Agent-Intent=%q want craft", gotHeaders.Get("X-Agent-Intent"))
	}
	if gotHeaders.Get("X-Agent-Runtime") != "other" {
		t.Errorf("X-Agent-Runtime=%q want other", gotHeaders.Get("X-Agent-Runtime"))
	}
	if gotHeaders.Get("Authorization") != "Bearer token_123" {
		t.Errorf("Authorization=%q want Bearer token_123", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("X-User-Id") != "uid_456" {
		t.Errorf("X-User-Id=%q want uid_456", gotHeaders.Get("X-User-Id"))
	}
	if gotHeaders.Get("X-Conversation-Request-ID") == "" {
		t.Errorf("X-Conversation-Request-ID missing")
	}
}

func TestSearchFlatResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"query": "Golang",
			"type": "text2text",
			"provider": "tencent",
			"results": [
				{
					"title": "Go 编程语言官网",
					"url": "https://go.dev",
					"snippet": "Go is an open source programming language...",
					"site": "go.dev",
					"favicon": "https://go.dev/favicon.ico"
				}
			],
			"total_results": 1,
			"response_time_ms": 180
		}`))
	}))
	defer srv.Close()

	out, err := DoSearch(context.Background(), srv.Client(), srv.URL, "t", "u", "Golang", 5)
	if err != nil {
		t.Fatalf("DoSearch failed: %v", err)
	}

	if out.Query != "Golang" || out.TotalResults != 1 || out.ResponseTimeMs != 180 {
		t.Errorf("outcome mismatch: %+v", out)
	}
	if len(out.Results) != 1 {
		t.Fatalf("results len=%d want 1", len(out.Results))
	}
	r0 := out.Results[0]
	if r0.Title != "Go 编程语言官网" || r0.URL != "https://go.dev" || r0.Site != "go.dev" || r0.Favicon != "https://go.dev/favicon.ico" {
		t.Errorf("result item mismatch: %+v", r0)
	}
}

func TestSearchClampMaxResults(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, 5},
		{-10, 5},
		{1, 1},
		{5, 5},
		{10, 10},
		{50, 10},
	}
	for _, tc := range cases {
		got := ClampSearchMaxResults(tc.in)
		if got != tc.want {
			t.Errorf("ClampSearchMaxResults(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSearchRejectShortQuery(t *testing.T) {
	for _, q := range []string{"", " ", "a", " 1 "} {
		_, err := DoSearch(context.Background(), nil, "http://localhost", "t", "u", q, 5)
		if err == nil {
			t.Errorf("query=%q expected error, got nil", q)
		}
	}
}

func TestSearchBusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":15003,"msg":"unsupported search type"}`))
	}))
	defer srv.Close()

	_, err := DoSearch(context.Background(), srv.Client(), srv.URL, "t", "u", "Golang", 5)
	if err == nil || !strings.Contains(err.Error(), "15003") {
		t.Errorf("expected code 15003 error, got %v", err)
	}
}
