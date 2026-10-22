package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/growth"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestHandleGrowthDomesticTasksAndSweep(t *testing.T) {
	// 启动假上游服务器模拟 CodeBuddy / WorkBuddy 接口
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/activity/growth/tasks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"tasks": []map[string]any{
						{
							"task_code":     "chat_5",
							"title":         "对话活跃 5 次",
							"reward_credit": 100,
							"reward_energy": 0,
							"target":        5,
							"current":       5,
							"accept_status": "completed",
							"claimable":     true,
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
				"data": map[string]any{"reward_credit": 100, "reward_energy": 0},
			})

		case growth.DesktopReportPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok"})

		case "/v2/billing/meter/compensation-status":
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
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"credit_granted": 500},
			})

		case "/activity/growth/buddy/info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"buddy": map[string]any{
						"instance_id": 1,
						"name":        "暴富喵",
						"rarity":      "UR",
					},
				},
			})

		case "/activity/growth/buddy/open":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"count": 1,
					"results": []map[string]any{
						{
							"instance": map[string]any{"id": 101, "template_id": 18},
							"template": map[string]any{"id": 18, "name": "摸鱼喵", "rarity": "R"},
						},
					},
				},
			})

		case "/activity/growth/buddy/travel/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"state":    "traveling",
					"buddy_id": 1,
					"location": map[string]any{"id": 1, "name": "咖啡馆"},
				},
			})

		case "/activity/growth/buddy/travel/depart":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{
					"state":    "traveling",
					"location": map[string]any{"id": 1, "name": "咖啡馆"},
				},
			})

		case "/activity/growth/buddy/travel/claim":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "ok",
				"data": map[string]any{"reward_credit": 10},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	srv := &Server{
		opts: Options{
			Probe: variant.DefaultProbe(),
		},
		domesticCredFn: func(*variant.Probe) (credential.Credential, error) {
			return credential.Credential{Token: "mock-token", UID: "mock-uid"}, nil
		},
		domesticEndpointFn: func() string {
			return ts.URL
		},
	}
	srv.growthGuardian = growth.NewTravelGuardian(growth.GuardianConfig{
		Endpoint: ts.URL,
		CredGetter: func() (credential.Credential, error) {
			return credential.Credential{Token: "mock-token", UID: "mock-uid"}, nil
		},
	})

	// 1. GET /api/growth/domestic/tasks
	req := httptest.NewRequest(http.MethodGet, "/api/growth/domestic/tasks", nil)
	rec := httptest.NewRecorder()
	srv.handleGrowthDomesticTasks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}
	var tasksResp DomesticTasksResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tasksResp); err != nil {
		t.Fatalf("unmarshal tasks response failed: %v", err)
	}
	if len(tasksResp.Tasks) != 1 || tasksResp.PotentialCredit != 100 {
		t.Fatalf("unexpected tasks response: %+v", tasksResp)
	}
	if tasksResp.Compensation == nil || !tasksResp.Compensation.Pending() {
		t.Fatalf("compensation should be pending: %+v", tasksResp.Compensation)
	}

	// 2. POST /api/growth/domestic/run
	runReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/run", strings.NewReader(`{"task_code":"chat_5"}`))
	runRec := httptest.NewRecorder()
	srv.handleGrowthDomesticRun(runRec, runReq)

	if runRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for run, got %d: %s", runRec.Code, runRec.Body.String())
	}

	// 3. POST /api/growth/domestic/sweep
	sweepReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/sweep", nil)
	sweepRec := httptest.NewRecorder()
	srv.handleGrowthDomesticSweep(sweepRec, sweepReq)

	if sweepRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for sweep, got %d: %s", sweepRec.Code, sweepRec.Body.String())
	}

	// 4. POST /api/growth/domestic/compensation/claim
	compReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/compensation/claim", nil)
	compRec := httptest.NewRecorder()
	srv.handleGrowthDomesticCompensationClaim(compRec, compReq)

	if compRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for compensation claim, got %d: %s", compRec.Code, compRec.Body.String())
	}

	// 5. POST /api/growth/domestic/claim (单任务纯领奖)
	claimReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/claim", strings.NewReader(`{"task_code":"chat_5"}`))
	claimRec := httptest.NewRecorder()
	srv.handleGrowthDomesticClaim(claimRec, claimReq)

	if claimRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for domestic claim, got %d: %s", claimRec.Code, claimRec.Body.String())
	}
	var claimResp map[string]any
	if err := json.Unmarshal(claimRec.Body.Bytes(), &claimResp); err != nil || claimResp["claimed"] != true {
		t.Fatalf("unexpected claim response: %+v, err: %v", claimResp, err)
	}

	// 6. POST /api/growth/domestic/claim-all (一键全量领奖)
	claimAllReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/claim-all", nil)
	claimAllRec := httptest.NewRecorder()
	srv.handleGrowthDomesticClaimAll(claimAllRec, claimAllReq)

	if claimAllRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for domestic claim-all, got %d: %s", claimAllRec.Code, claimAllRec.Body.String())
	}
	var sweepResp growth.SweepResult
	if err := json.Unmarshal(claimAllRec.Body.Bytes(), &sweepResp); err != nil {
		t.Fatalf("unexpected claim-all response: %+v, err: %v", sweepResp, err)
	}

	// 7. GET /api/growth/domestic/buddy
	buddyReq := httptest.NewRequest(http.MethodGet, "/api/growth/domestic/buddy", nil)
	buddyRec := httptest.NewRecorder()
	srv.handleGrowthDomesticBuddy(buddyRec, buddyReq)
	if buddyRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for buddy status, got %d: %s", buddyRec.Code, buddyRec.Body.String())
	}

	// 8. POST /api/growth/domestic/buddy/open
	openReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/buddy/open", strings.NewReader(`{"count":1}`))
	openRec := httptest.NewRecorder()
	srv.handleGrowthDomesticBuddyOpen(openRec, openReq)
	if openRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for buddy open, got %d: %s", openRec.Code, openRec.Body.String())
	}

	// 9. POST /api/growth/domestic/buddy/travel
	travelReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/buddy/travel", strings.NewReader(`{"action":"auto"}`))
	travelRec := httptest.NewRecorder()
	srv.handleGrowthDomesticBuddyTravel(travelRec, travelReq)
	if travelRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for buddy travel, got %d: %s", travelRec.Code, travelRec.Body.String())
	}

	// 10. GET /api/growth/domestic/guardian
	guardReq := httptest.NewRequest(http.MethodGet, "/api/growth/domestic/guardian", nil)
	guardRec := httptest.NewRecorder()
	srv.handleGrowthDomesticGuardian(guardRec, guardReq)
	if guardRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for guardian status, got %d: %s", guardRec.Code, guardRec.Body.String())
	}

	// 11. POST /api/growth/domestic/guardian/trigger
	trigReq := httptest.NewRequest(http.MethodPost, "/api/growth/domestic/guardian/trigger", nil)
	trigRec := httptest.NewRecorder()
	srv.handleGrowthDomesticGuardianTrigger(trigRec, trigReq)
	if trigRec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for guardian trigger, got %d: %s", trigRec.Code, trigRec.Body.String())
	}
}
