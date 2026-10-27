package credential

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestLoginFlow_EndToEnd(t *testing.T) {
	p := newEnv(t)
	restore := config.SetRoot(t.TempDir())
	defer restore()


	// Mock 官方服务
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/v2/plugin/auth/state"):
			_ = json.NewEncoder(w).Encode(authStateResponse{
				Code: 0,
				Msg:  "OK",
				Data: struct {
					State   string `json:"state"`
					AuthURL string `json:"authUrl"`
				}{
					State:   "test_state_12345",
					AuthURL: "https://www.workbuddy.ai/login?state=test_state_12345",
				},
			})
		case strings.Contains(r.URL.Path, "/v2/plugin/auth/token"):
			state := r.URL.Query().Get("state")
			if state == "pending_state" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 11217,
					"msg":  "login ing...",
				})
				return
			}
			if state == "failed_state" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 11000,
					"msg":  "auth rejected",
				})
				return
			}
			// 成功返回
			_ = json.NewEncoder(w).Encode(authTokenResponse{
				Code: 0,
				Msg:  "OK",
				Data: struct {
					AccessToken      string `json:"accessToken"`
					RefreshToken     string `json:"refreshToken"`
					ExpiresIn        int64  `json:"expiresIn"`
					RefreshExpiresIn int64  `json:"refreshExpiresIn"`
					Domain           string `json:"domain"`
				}{
					AccessToken: "ey_test_access_token_official_flow_123",
					ExpiresIn:   7200,
					Domain:      "www.workbuddy.ai",
				},
			})
		case strings.Contains(r.URL.Path, "/v2/plugin/account"):
			_ = json.NewEncoder(w).Encode(accountResponse{
				Code: 0,
				Msg:  "OK",
				Data: struct {
					UID      string `json:"uid"`
					Nickname string `json:"nickname"`
					Uin      string `json:"uin"`
					Avatar   string `json:"avatarUrl"`
				}{
					UID:      "test_uid_9999",
					Nickname: "测试拟真用户",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mgr := NewLoginFlowManager(p, server.Client())
	mgr.SetBaseURL(server.URL)

	ctx := context.Background()

	// 1. 测试不存在的 state
	res, err := mgr.PollLoginStatus(ctx, "non_existent_state")
	if err != nil {
		t.Fatalf("PollLoginStatus returned error: %v", err)
	}
	if res.Status != "expired" {
		t.Fatalf("expected status 'expired', got %s", res.Status)
	}

	// 2. 发起官方登录流程
	flow, err := mgr.StartLoginFlow(ctx)
	if err != nil {
		t.Fatalf("StartLoginFlow failed: %v", err)
	}
	if flow.State != "test_state_12345" {
		t.Fatalf("unexpected state: %s", flow.State)
	}
	if !strings.Contains(flow.AuthURL, "test_state_12345") {
		t.Fatalf("unexpected authUrl: %s", flow.AuthURL)
	}

	// 3. 注入 pending 流程并轮询
	mgr.flows["pending_state"] = &LoginFlow{
		State:     "pending_state",
		AuthURL:   "https://www.workbuddy.ai/login?state=pending_state",
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	pRes, err := mgr.PollLoginStatus(ctx, "pending_state")
	if err != nil {
		t.Fatalf("PollLoginStatus pending failed: %v", err)
	}
	if pRes.Status != "pending" {
		t.Fatalf("expected status 'pending', got %s", pRes.Status)
	}

	// 4. 轮询成功流程 (flow.State = test_state_12345)
	sRes, err := mgr.PollLoginStatus(ctx, flow.State)
	if err != nil {
		t.Fatalf("PollLoginStatus success failed: %v", err)
	}
	if sRes.Status != "success" {
		t.Fatalf("expected status 'success', got %s (msg: %s)", sRes.Status, sRes.Message)
	}
	if sRes.UID != "test_uid_9999" || sRes.Nickname != "测试拟真用户" {
		t.Fatalf("unexpected user info: uid=%s nickname=%s", sRes.UID, sRes.Nickname)
	}
	if sRes.Account == nil || !sRes.Account.Active {
		t.Fatalf("expected account to be active in redacted return")
	}

	// 5. 验证是否已成功落库到多账号池
	accounts, err := ListAccounts(p, variant.Intl)

	if err != nil {
		t.Fatalf("ListAccounts failed: %v", err)
	}
	if len(accounts) == 0 {
		t.Fatalf("expected at least 1 account in vault")
	}
	found := false
	for _, a := range accounts {
		if a.UID == "test_uid_9999" && a.Active {
			found = true
			if a.Token != "ey_test_access_token_official_flow_123" {
				t.Fatalf("unexpected token stored: %s", a.Token)
			}
		}
	}
	if !found {
		t.Fatalf("expected test_uid_9999 to be saved and active")
	}

	// 6. 测试过期自动清理
	mgr.flows["expired_state"] = &LoginFlow{
		State:     "expired_state",
		AuthURL:   "https://test",
		CreatedAt: time.Now().Add(-10 * time.Minute),
		ExpiresAt: time.Now().Add(-1 * time.Minute),
	}
	eRes, err := mgr.PollLoginStatus(ctx, "expired_state")
	if err != nil {
		t.Fatalf("PollLoginStatus expired failed: %v", err)
	}
	if eRes.Status != "expired" {
		t.Fatalf("expected status 'expired', got %s", eRes.Status)
	}
}

func TestExtractUIDFromJWT(t *testing.T) {
	// 1. 构造带有 uid 的有效 JWT
	fakeJWT := "header." + "eyJ1aWQiOiJ1c3JfMTIzNDU2NzgiLCJzdWIiOiJzdWJfODg4OCJ9" + ".sig"
	uid := extractUIDFromJWT(fakeJWT)
	if uid != "usr_12345678" {
		t.Errorf("expected uid usr_12345678, got %s", uid)
	}


	// 2. 构造只有 sub 的 JWT
	fakeJWTSub := "header." + "eyJzdWIiOiJzdWJfOTk5OTk5In0" + ".sig"
	sub := extractUIDFromJWT(fakeJWTSub)
	if sub != "sub_999999" {
		t.Errorf("expected sub sub_999999, got %s", sub)
	}

	// 3. 非法 Token 不 panic 并返回空
	if invalid := extractUIDFromJWT("not_a_jwt"); invalid != "" {
		t.Errorf("expected empty string for non-jwt, got %s", invalid)
	}
	if garbage := extractUIDFromJWT("a.???!!!.c"); garbage != "" {
		t.Errorf("expected empty string for invalid base64, got %s", garbage)
	}
}

func TestGetWebClientLoginURL_Fallback(t *testing.T) {
	// 空环境场景下的安全降级
	p := newEnv(t)

	urlIntl := GetWebClientLoginURL(p, variant.Intl, "/profile/usage")
	if urlIntl != "https://www.workbuddy.ai/profile/usage" {
		t.Errorf("expected fallback url, got %s", urlIntl)
	}

	urlCN := GetWebClientLoginURL(p, variant.CN, "/profile/plan")
	if urlCN != "https://www.workbuddy.cn/profile/plan" {
		t.Errorf("expected fallback url, got %s", urlCN)
	}
}


