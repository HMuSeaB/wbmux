package growth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

func TestGrowthClient_FetchStreak(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/activity/growth/streak" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		resp := map[string]interface{}{
			"code": 0,
			"msg":  "OK",
			"data": map[string]interface{}{
				"streak": map[string]interface{}{
					"days":                5,
					"month_total_days":    5,
					"next_tier":           "7d",
					"next_tier_remaining": 2,
				},
				"redemption_status": map[string]interface{}{
					"tier_7d_status":  "locked",
					"tier_14d_status": "locked",
					"tier_28d_status": "locked",
					"tiers": []map[string]interface{}{
						{"tier": "7d", "days": 7, "credit": 0, "energy": 2, "cards": 1},
						{"tier": "14d", "days": 14, "credit": 50, "energy": 3, "cards": 1},
						{"tier": "28d", "days": 28, "credit": 150, "energy": 5, "cards": 1},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	client := NewClient(mockServer.URL)
	cred := credential.Credential{Token: "test-token", UID: "user-123"}
	status, err := client.FetchStreak(cred)
	if err != nil {
		t.Fatalf("FetchStreak: %v", err)
	}

	if status.Days != 5 {
		t.Errorf("expected 5 days, got %d", status.Days)
	}
	if status.NextTierRemaining != 2 {
		t.Errorf("expected 2 days remaining, got %d", status.NextTierRemaining)
	}
	if len(status.Tiers) != 3 {
		t.Fatalf("expected 3 tiers, got %d", len(status.Tiers))
	}
	if status.Tiers[1].Credit != 50 {
		t.Errorf("expected 50 credits for 14d tier, got %d", status.Tiers[1].Credit)
	}
}

func TestGrowthClient_RedeemTier(t *testing.T) {
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/activity/growth/redeem" {
			http.NotFound(w, r)
			return
		}

		resp := map[string]interface{}{
			"code": 0,
			"msg":  "OK",
			"data": map[string]interface{}{
				"credit_granted": 50,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer mockServer.Close()

	client := NewClient(mockServer.URL)
	cred := credential.Credential{Token: "test-token", UID: "user-123"}
	granted, err := client.RedeemTier(cred, "14d")
	if err != nil {
		t.Fatalf("RedeemTier: %v", err)
	}
	if granted != 50 {
		t.Errorf("expected 50 granted, got %d", granted)
	}
}
