package growth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

func TestBuddyAndTravel_Mock(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/activity/growth/buddy/info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"buddy": map[string]any{
						"instance_id": 12345,
						"name":        "暴富喵",
						"rarity":      "UR",
						"soul_desc":   "清醒务实，搞钱目标感极强",
					},
				},
			})
		case "/activity/growth/buddy/open":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"count": 1,
					"results": []map[string]any{
						{
							"instance": map[string]any{
								"id":          999,
								"template_id": 18,
								"is_shiny":    0,
							},
							"template": map[string]any{
								"id":          18,
								"name":        "摸鱼喵",
								"rarity":      "R",
								"description": "松弛管理师",
							},
						},
					},
				},
			})
		case "/activity/growth/buddy/travel/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"state":    "traveling",
					"buddy_id": 12345,
					"location": map[string]any{
						"id":             1,
						"name":           "咖啡馆",
						"duration_hours": 2,
					},
					"arrive_at":  1000,
					"server_now": 900,
				},
			})
		case "/activity/growth/buddy/travel/depart":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"state": "traveling",
					"location": map[string]any{
						"id":   1,
						"name": "咖啡馆",
					},
				},
			})
		case "/activity/growth/buddy/travel/claim":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"reward_credit": 10,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	cred := credential.Credential{Token: "test_token", UID: "test_uid"}

	// 1. FetchBuddyInfo
	buddy, err := client.FetchBuddyInfo(cred)
	if err != nil || buddy == nil || buddy.Name != "暴富喵" {
		t.Fatalf("FetchBuddyInfo failed: %v, %+v", err, buddy)
	}

	// 2. OpenBlindBox
	openRes, err := client.OpenBlindBox(cred, 1)
	if err != nil || openRes == nil || len(openRes.Results) != 1 || openRes.Results[0].Name != "摸鱼喵" {
		t.Fatalf("OpenBlindBox failed: %v, %+v", err, openRes)
	}

	// 3. FetchTravelStatus
	st, err := client.FetchTravelStatus(cred)
	if err != nil || st == nil || st.State != "traveling" {
		t.Fatalf("FetchTravelStatus failed: %v, %+v", err, st)
	}

	// 4. DepartTravel
	depSt, err := client.DepartTravel(cred, 1)
	if err != nil || depSt == nil || depSt.State != "traveling" {
		t.Fatalf("DepartTravel failed: %v, %+v", err, depSt)
	}

	// 5. ClaimTravelReward
	credit, err := client.ClaimTravelReward(cred)
	if err != nil || credit != 10 {
		t.Fatalf("ClaimTravelReward failed: %v, credit=%d", err, credit)
	}

	// 6. AutoTravelSweep (current state is traveling)
	msg, err := client.AutoTravelSweep(cred)
	if err != nil || msg == "" {
		t.Fatalf("AutoTravelSweep failed: %v, msg=%s", err, msg)
	}
}
