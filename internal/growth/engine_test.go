package growth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

func TestClient_RunTaskAndSweepAll(t *testing.T) {
	callCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			callCount++
			// 第一次查询：未完成；第二次查询（轮询回读）：已完成
			current := int64(0)
			claimable := false
			if callCount >= 2 {
				current = 1
				claimable = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"tasks": []map[string]any{
						{
							"task_code":     "create_canvas",
							"title":         "设计创意画布创建",
							"reward_credit": 300,
							"reward_energy": 5,
							"target":        1,
							"current":       current,
							"accept_status": "accepted",
							"claimable":     claimable,
						},
					},
				},
			})

		case "/v2/activity/growth/tasks/accept":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})

		case "/v2/activity/growth/tasks/reward/claim":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"reward_credit": 300,
					"reward_energy": 5,
				},
			})

		case DesktopReportPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})

		case "/v2/billing/meter/compensation-status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"active":     false,
					"credit_num": 0,
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	cred := credential.Credential{Token: "test-token", UID: "uid-002"}

	// 1. 测试单任务执行与自动领奖
	res, err := client.RunTask(cred, "create_canvas")
	if err != nil {
		t.Fatalf("RunTask failed: %v", err)
	}
	if !res.Claimed || res.CreditGranted != 300 || res.EnergyGranted != 5 {
		t.Fatalf("unexpected RunTask result: %+v", res)
	}

	// 2. 测试 SweepAll
	sweepRes, err := client.SweepAll(cred)
	if err != nil {
		t.Fatalf("SweepAll failed: %v", err)
	}
	if len(sweepRes.Details) == 0 {
		t.Fatalf("expected sweep details, got empty")
	}
}
