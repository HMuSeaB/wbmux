package growth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

func TestClient_ListTasksAndClaim(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			if r.Method != http.MethodGet {
				t.Fatalf("expected GET, got %s", r.Method)
			}
			isMP := r.Header.Get("X-Client-Platform") == "miniprogram"
			tasks := []map[string]any{
				{
					"task_code":     "first_buddy",
					"title":         "领养首只猫咪",
					"reward_credit": 300,
					"reward_energy": 8,
					"progress":      map[string]any{"current": 1, "target": 1},
					"accept_status": "completed",
				},
			}
			if isMP {
				tasks = append(tasks, map[string]any{
					"task_code":     "Sequential_Tasks_1",
					"title":         "小程序首对话",
					"reward_credit": 100,
					"reward_energy": 5,
					"target":        1,
					"current":       0,
					"accept_status": "accepted",
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"tasks": tasks},
			})

		case "/v2/activity/growth/tasks/accept":
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
			})

		case "/v2/activity/growth/tasks/reward/claim":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.Header().Set("Content-Type", "application/json")
			if req["task_code"] == "first_buddy" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"msg":  "ok",
					"data": map[string]any{
						"reward_credit": 300,
						"reward_energy": 8,
					},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 10002,
					"msg":  "already claimed",
				})
			}

		case "/v2/billing/meter/compensation-status":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"active":          true,
					"claimed":         false,
					"credit_num":      500,
					"validity_period": 1,
					"end_time":        "2026-10-31 23:59:59",
				},
			})

		case "/v2/billing/meter/claim-compensation":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"credit_granted": 500,
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	cred := credential.Credential{Token: "test-token", UID: "uid-001"}

	// 1. 普通列表
	tasks, err := client.ListTasks(cred, false)
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TaskCode != "first_buddy" || !tasks[0].Claimable {
		t.Fatalf("unexpected tasks: %+v", tasks)
	}

	// 2. MP 列表
	mpTasks, err := client.ListTasks(cred, true)
	if err != nil {
		t.Fatalf("ListTasks MP failed: %v", err)
	}
	if len(mpTasks) != 2 {
		t.Fatalf("expected 2 tasks in MP list, got %d", len(mpTasks))
	}

	// 3. Accept
	if err := client.AcceptTasks(cred, []string{"Sequential_Tasks_1"}, true); err != nil {
		t.Fatalf("AcceptTasks failed: %v", err)
	}

	// 4. Claim Reward
	c, e, err := client.ClaimReward(cred, "first_buddy", false)
	if err != nil {
		t.Fatalf("ClaimReward failed: %v", err)
	}
	if c != 300 || e != 8 {
		t.Fatalf("expected 300c 8e, got %dc %de", c, e)
	}

	// 重复领取容错
	c2, e2, err := client.ClaimReward(cred, "already_done", false)
	if err != nil || c2 != 0 || e2 != 0 {
		t.Fatalf("expected 0,0 for already claimed, got %dc %de, err: %v", c2, e2, err)
	}

	// 5. Compensation Status & Claim
	comp, err := client.FetchCompensationStatus(cred)
	if err != nil {
		t.Fatalf("FetchCompensationStatus failed: %v", err)
	}
	if !comp.Pending() || comp.CreditNum != 500 {
		t.Fatalf("unexpected compensation status: %+v", comp)
	}

	granted, err := client.ClaimCompensation(cred)
	if err != nil {
		t.Fatalf("ClaimCompensation failed: %v", err)
	}
	if granted != 500 {
		t.Fatalf("expected 500 granted, got %d", granted)
	}
}
