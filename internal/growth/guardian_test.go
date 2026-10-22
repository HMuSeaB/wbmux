package growth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

func TestTravelGuardian_Decisions(t *testing.T) {
	var (
		statusCalls atomic.Int32
		claimCalls  atomic.Int32
		departCalls atomic.Int32
		currentState = "arrived"
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/activity/growth/buddy/travel/status":
			statusCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"state":      currentState,
					"buddy_id":   1001,
					"arrive_at":  time.Now().Unix() + 1800,
					"server_now": time.Now().Unix(),
				},
			})
		case "/activity/growth/buddy/travel/claim":
			claimCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"reward_credit": 8,
				},
			})
		case "/activity/growth/buddy/travel/depart":
			departCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"msg":  "OK",
				"data": map[string]any{
					"state":      "traveling",
					"arrive_at":  time.Now().Unix() + 1800,
					"server_now": time.Now().Unix(),
					"location": map[string]any{
						"id":   1,
						"name": "咖啡馆",
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	var notes []string
	cfg := GuardianConfig{
		Endpoint: ts.URL,
		CredGetter: func() (credential.Credential, error) {
			return credential.Credential{Token: "test-token", UID: "uid-1"}, nil
		},
		OnNote: func(level, text string) {
			notes = append(notes, text)
		},
		MinInterval:    10 * time.Millisecond,
		MaxInterval:    100 * time.Millisecond,
		AutoDailySweep: false,
	}

	guardian := NewTravelGuardian(cfg)

	// 1. 测试 arrived 状态：应当自动 Claim 领奖并自动 Depart 重新出发
	currentState = "arrived"
	wait := guardian.checkOnce(context.Background())
	if wait < 10*time.Millisecond {
		t.Fatalf("expected wait >= minInterval, got %v", wait)
	}

	if claimCalls.Load() != 1 {
		t.Errorf("expected 1 claim call, got %d", claimCalls.Load())
	}
	if departCalls.Load() != 1 {
		t.Errorf("expected 1 depart call, got %d", departCalls.Load())
	}

	st := guardian.GetStatus()
	if st.TotalHarvests != 1 {
		t.Errorf("expected TotalHarvests=1, got %d", st.TotalHarvests)
	}
	if st.TotalHarvestCredits != 8 {
		t.Errorf("expected TotalHarvestCredits=8, got %d", st.TotalHarvestCredits)
	}
	if len(notes) == 0 {
		t.Errorf("expected note logged, got 0")
	}

	// 2. 测试 idle 状态：应当自动 Depart 出发
	currentState = "idle"
	guardian.checkOnce(context.Background())
	if departCalls.Load() != 2 {
		t.Errorf("expected 2 depart calls after idle, got %d", departCalls.Load())
	}

	// 3. 测试 traveling 状态：应当休眠且不调用 claim/depart
	currentState = "traveling"
	guardian.checkOnce(context.Background())
	if claimCalls.Load() != 1 {
		t.Errorf("claim calls should remain 1, got %d", claimCalls.Load())
	}
	if departCalls.Load() != 2 {
		t.Errorf("depart calls should remain 2, got %d", departCalls.Load())
	}
}

func TestTravelGuardian_StartAndCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "OK",
			"data": map[string]any{
				"state":      "traveling",
				"arrive_at":  time.Now().Unix() + 10,
				"server_now": time.Now().Unix(),
			},
		})
	}))
	defer ts.Close()

	cfg := GuardianConfig{
		Endpoint: ts.URL,
		CredGetter: func() (credential.Credential, error) {
			return credential.Credential{Token: "test-token", UID: "uid-1"}, nil
		},
		MinInterval:    20 * time.Millisecond,
		MaxInterval:    50 * time.Millisecond,
		AutoDailySweep: false,
	}

	guardian := NewTravelGuardian(cfg)
	ctx, cancel := context.WithCancel(context.Background())

	doneCh := make(chan struct{})
	go func() {
		guardian.Start(ctx)
		close(doneCh)
	}()

	// 等待启动
	time.Sleep(30 * time.Millisecond)
	if !guardian.GetStatus().Running {
		t.Errorf("expected guardian to be running")
	}

	// 触发立即唤醒测试
	guardian.TriggerNow()
	time.Sleep(20 * time.Millisecond)

	// 取消上下文
	cancel()

	select {
	case <-doneCh:
		// 成功退出
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("guardian failed to exit on context cancellation")
	}

	if guardian.GetStatus().Running {
		t.Errorf("expected guardian to stop running")
	}
}
