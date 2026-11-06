package growth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

func TestSimulateIntlActiveSuccess(t *testing.T) {
	var chatHeaders http.Header
	var chatBody map[string]any
	var reportCount int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/chat/completions":
			chatHeaders = r.Header.Clone()
			_ = json.NewDecoder(r.Body).Decode(&chatBody)

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatalf("expected flusher")
			}

			// 发送首个 chunk
			fmt.Fprintf(w, "data: {\"id\":\"cmb-test-123\",\"choices\":[{\"delta\":{\"content\":\"早上好！\"}}]}\n\n")
			flusher.Flush()

			// 发送第二个 chunk
			fmt.Fprintf(w, "data: {\"id\":\"cmb-test-123\",\"choices\":[{\"delta\":{\"content\":\"祝你编码愉快！\"},\"finish_reason\":\"stop\"}]}\n\n")
			flusher.Flush()

			// 发送结束标头
			fmt.Fprintf(w, "data: [DONE]\n\n")
			flusher.Flush()

		case "/v2/report":
			reportCount++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	cred := credential.Credential{
		Token: "test-token-xyz",
		UID:   "test-uid-888",
	}

	res, err := client.SimulateIntlActive(cred, "deepseek-v4.1-flash-sg")
	if err != nil {
		t.Fatalf("SimulateIntlActive 失败: %v", err)
	}

	if !res.OK {
		t.Fatalf("期望 res.OK 为 true")
	}
	if res.Model != "deepseek-v4.1-flash-sg" {
		t.Errorf("期望 model 为 deepseek-v4.1-flash-sg，实际: %s", res.Model)
	}
	if res.Reply != "早上好！祝你编码愉快！" {
		t.Errorf("期望 reply 为 '早上好！祝你编码愉快！'，实际: %s", res.Reply)
	}
	if !res.Reported {
		t.Errorf("期望 res.Reported 为 true")
	}
	if reportCount != 1 {
		t.Errorf("期望 /v2/report 被调用 1 次，实际: %d", reportCount)
	}

	// 验证 8 大必需标头
	required := []string{
		"X-Conversation-Id",
		"X-Parent-Conversation-Id",
		"X-Session-Id",
		"X-Conversation-Request-Id",
		"X-Conversation-Message-Id",
		"X-Request-Id",
		"X-Root-Request-Id",
		"X-Trace-Id",
	}
	for _, h := range required {
		if chatHeaders.Get(h) == "" {
			t.Errorf("期望包含请求头 %s", h)
		}
	}
	if chatHeaders.Get("Authorization") != "Bearer test-token-xyz" {
		t.Errorf("Authorization 标头不正确: %s", chatHeaders.Get("Authorization"))
	}
}

func TestSimulateIntlActiveModelFallback(t *testing.T) {
	var requestedModels []string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/chat/completions":
			var body struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			requestedModels = append(requestedModels, body.Model)

			// 模拟 deepseek-v4.1-flash-sg 报 11102 (不存在)，后续 deepseek-v4.1-flash 成功
			if body.Model == "deepseek-v4.1-flash-sg" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":11102,"msg":"model [deepseek-v4.1-flash-sg] not exist"}`))
				return
			}

			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "data: {\"id\":\"cmb-fallback-456\",\"choices\":[{\"delta\":{\"content\":\"回退成功！\"}}]}\n\n")
			fmt.Fprintf(w, "data: [DONE]\n\n")

		case "/v2/report":
			w.WriteHeader(http.StatusOK)

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	cred := credential.Credential{
		Token: "test-token",
		UID:   "test-uid",
	}

	res, err := client.SimulateIntlActive(cred, "deepseek-v4.1-flash-sg")
	if err != nil {
		t.Fatalf("期望回退成功，但报错: %v", err)
	}

	if res.Model != "deepseek-v4.1-flash" {
		t.Errorf("期望最终成功模型为 deepseek-v4.1-flash，实际: %s", res.Model)
	}
	if len(requestedModels) != 2 {
		t.Errorf("期望依次尝试了 2 个模型，实际: %v", requestedModels)
	}
	if !strings.Contains(res.Reply, "回退成功") {
		t.Errorf("期望回复包含回退成功，实际: %s", res.Reply)
	}
}

func TestSimulateIntlActiveEmptyToken(t *testing.T) {
	client := NewClient("https://mock.com")
	_, err := client.SimulateIntlActive(credential.Credential{}, "")
	if err == nil {
		t.Fatalf("期望 Token 为空时报错")
	}
	if !strings.Contains(err.Error(), "Token 为空") {
		t.Errorf("期望提示 Token 为空，实际: %v", err)
	}
}
