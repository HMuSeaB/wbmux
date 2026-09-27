package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readProxyLog 拉一次界面用的日志接口。
func readProxyLog(t *testing.T, srv *Server) []ProxyLogEntry {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleProxyLog(rec, httptest.NewRequest(http.MethodGet, "/api/proxy-log", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	var res struct {
		Entries []ProxyLogEntry `json:"entries"`
		Limit   int             `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if res.Limit != proxyLogLimit {
		t.Errorf("应回传缓冲上限 %d，得到 %d", proxyLogLimit, res.Limit)
	}
	return res.Entries
}

// TestProxyLogRecordsRequestLifecycle 一次转发要留下"收到"和"完成"两条：
// 只有"收到"没有"完成"就是卡在上游——这正是界面能不能当监控用的分界。
func TestProxyLogRecordsRequestLifecycle(t *testing.T) {
	var got map[string]any
	sse := "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	up := fakeUpstream(t, sse, &got, nil)
	defer up.Close()

	var fileLog []string
	srv := newProxyForTest(t, up.URL, &fileLog)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"wbmux-intl-m","stream":true,"tools":[{"type":"function"}],"messages":[{"role":"user","content":"x"}]}`))
	srv.handleOpenAIChat(httptest.NewRecorder(), req)

	entries := readProxyLog(t, srv)
	if len(entries) != 2 {
		t.Fatalf("应留下 2 条（收到 + 完成），得到 %d：%+v", len(entries), entries)
	}
	// 接口按新→旧返回，界面上看到的第一条就是刚发生的事。
	if !strings.Contains(entries[0].Text, "转发完成") {
		t.Errorf("第一条应是完成事件：%q", entries[0].Text)
	}
	if !strings.Contains(entries[1].Text, "收到请求") {
		t.Errorf("第二条应是收到事件：%q", entries[1].Text)
	}
	if entries[1].Model != "m" || entries[1].Tools != 1 {
		t.Errorf("事件应带上还原后的模型名与工具数：%+v", entries[1])
	}

	// 同一份内容必须也落到日志文件（gui.log）：界面上翻不到的旧记录靠它。
	joined := strings.Join(fileLog, "\n")
	if !strings.Contains(joined, "收到请求") || !strings.Contains(joined, "转发完成") {
		t.Errorf("内存与文件两份日志应同源，实际落文件的是：%s", joined)
	}
}

// TestProxyLogRecordsRejection 被上游拒绝要留一条失败事件，并带上状态码与原因。
func TestProxyLogRecordsRejection(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":11102,"msg":"model x not found","displayMsg":{"zh":"当前模型不可用。"}}`)
	}))
	defer up.Close()

	srv := newProxyForTest(t, up.URL, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"wbmux-intl-x","messages":[{"role":"user","content":"x"}]}`))
	srv.handleOpenAIChat(httptest.NewRecorder(), req)

	entries := readProxyLog(t, srv)
	if len(entries) != 2 {
		t.Fatalf("应留下 2 条，得到 %d", len(entries))
	}
	top := entries[0]
	if top.Level != "bad" {
		t.Errorf("失败事件的 level 应为 bad，得到 %q", top.Level)
	}
	if !strings.Contains(top.Text, "400") || !strings.Contains(top.Text, "当前模型不可用") {
		t.Errorf("失败事件要写明状态码与上游原因：%q", top.Text)
	}
}

// TestProxyLogRingKeepsNewest 缓冲满了砍最旧的：界面不该被无限增长的历史撑爆，
// 也不能反过来把最新的挤掉。
func TestProxyLogRingKeepsNewest(t *testing.T) {
	srv, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	total := proxyLogLimit + 20
	for i := 0; i < total; i++ {
		srv.note(ProxyLogEntry{Level: "info", Text: fmt.Sprintf("e%d", i)})
	}
	got := srv.proxyLogSnapshot()
	if len(got) != proxyLogLimit {
		t.Fatalf("缓冲应封顶在 %d 条，得到 %d", proxyLogLimit, len(got))
	}
	if got[0].Text != fmt.Sprintf("e%d", total-1) {
		t.Errorf("最新的应排在最前，得到 %q", got[0].Text)
	}
	if last := got[len(got)-1].Text; last != fmt.Sprintf("e%d", total-proxyLogLimit) {
		t.Errorf("最旧的应被砍掉，尾部得到 %q", last)
	}
}

// TestProxyLogRejectsNonGet 只用 GET；其它方法明确回 405 而不是静默返回空。
func TestProxyLogRejectsNonGet(t *testing.T) {
	srv, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.handleProxyLog(rec, httptest.NewRequest(http.MethodPost, "/api/proxy-log", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("应回 405，得到 %d", rec.Code)
	}
}
