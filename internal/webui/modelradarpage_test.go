package webui

import (
	"net/http"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/modelradar"
)

func TestHandleModelRadarStatus(t *testing.T) {
	env := newTestEnv(t, Options{})

	// 1. 无令牌被拒
	res := env.do(t, http.MethodGet, "/api/model-radar/status", "", false)
	if res.code != http.StatusForbidden {
		t.Fatalf("期望 403 Forbidden，得到 %d", res.code)
	}

	// 2. 带有效令牌获取状态
	modelradar.Default().Clear()
	modelradar.Default().SetRecord(modelradar.ProbeRecord{
		Model:       "deepseek-chat",
		DisplayName: "DeepSeek V3",
		IsFree:      true,
		Status:      modelradar.StatusFast,
		TTFTMs:      290,
	})

	res2 := env.do(t, http.MethodGet, "/api/model-radar/status", "", true)
	if res2.code != http.StatusOK {
		t.Fatalf("期望 200 OK，得到 %d: %s", res2.code, string(res2.body))
	}

	var data map[string]any
	res2.decode(t, &data)

	if data["fastestFreeModel"] != "deepseek-chat" {
		t.Errorf("期望 fastestFreeModel 为 deepseek-chat，得到 %v", data["fastestFreeModel"])
	}
	if data["hasHealthyFree"] != true {
		t.Errorf("期望 hasHealthyFree 为 true，得到 %v", data["hasHealthyFree"])
	}
	records, ok := data["records"].([]any)
	if !ok || len(records) != 1 {
		t.Errorf("期望 records 长度为 1，得到 %v", records)
	}
}

func TestHandleModelRadarPingValidation(t *testing.T) {
	env := newTestEnv(t, Options{})

	// 1. 只接受 POST
	res := env.do(t, http.MethodGet, "/api/model-radar/ping", "", true)
	if res.code != http.StatusMethodNotAllowed {
		t.Errorf("期望 405 MethodNotAllowed，得到 %d", res.code)
	}

	// 2. 无有效凭据时应返回 503 且信息明确
	res2 := env.do(t, http.MethodPost, "/api/model-radar/ping", `{"model":"deepseek-chat"}`, true)
	if res2.code != http.StatusServiceUnavailable {
		t.Errorf("无凭据环境期望 503 ServiceUnavailable，得到 %d: %s", res2.code, string(res2.body))
	}
}
