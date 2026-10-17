package credential

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestDoTokenRefresh_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("期望 POST 请求，实际 %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/v2/plugin/auth/token/refresh") {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		if r.Header.Get("X-Refresh-Token") != "sample_refresh_token" {
			t.Errorf("X-Refresh-Token 标头缺失或不符: %s", r.Header.Get("X-Refresh-Token"))
		}
		if r.Header.Get("X-Auth-Refresh-Source") != "plugin" {
			t.Errorf("X-Auth-Refresh-Source 标头缺失: %s", r.Header.Get("X-Auth-Refresh-Source"))
		}

		w.Header().Set("Content-Type", "application/json")
		res := tokenRefreshResponse{
			Code: 0,
			Msg:  "success",
		}
		res.Data.AccessToken = "new_access_token_123"
		res.Data.RefreshToken = "new_refresh_token_456"
		res.Data.ExpiresIn = 86400
		res.Data.RefreshExpiresIn = 2592000
		res.Data.Domain = "www.workbuddy.ai"
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer ts.Close()

	cleanup := SetAuthRefreshBaseURLForTest(ts.URL)
	defer cleanup()

	res, err := DoTokenRefresh(context.Background(), ts.Client(), nil, "sample_refresh_token", "www.workbuddy.ai")
	if err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	if res.AccessToken != "new_access_token_123" {
		t.Errorf("期望 new_access_token_123，实际 %s", res.AccessToken)
	}
	if res.RefreshToken != "new_refresh_token_456" {
		t.Errorf("期望 new_refresh_token_456，实际 %s", res.RefreshToken)
	}
	if res.ExpiresAt <= 0 || res.RefreshExpiresAt <= 0 {
		t.Errorf("到期时间戳未正确计算: %d, %d", res.ExpiresAt, res.RefreshExpiresAt)
	}
}

func TestDoTokenRefresh_ErrorCodes(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		res := tokenRefreshResponse{
			Code: 12153,
			Msg:  "12153:refresh token failed:10000:token format error",
		}
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer ts.Close()

	cleanup := SetAuthRefreshBaseURLForTest(ts.URL)
	defer cleanup()

	_, err := DoTokenRefresh(context.Background(), ts.Client(), nil, "bad_token", "")
	if err == nil || !strings.Contains(err.Error(), "12153") {
		t.Fatalf("期望返回 12153 错误，实际: %v", err)
	}
}

func TestRefreshAccount_Integration(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("WBMUX_CONFIG_DIR", tempDir)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		res := tokenRefreshResponse{
			Code: 0,
			Msg:  "success",
		}
		res.Data.AccessToken = "refreshed_access_xyz"
		res.Data.RefreshToken = "refreshed_refresh_xyz"
		res.Data.ExpiresIn = 3600
		res.Data.RefreshExpiresIn = 7200
		_ = json.NewEncoder(w).Encode(res)
	}))
	defer ts.Close()

	cleanup := SetAuthRefreshBaseURLForTest(ts.URL)
	defer cleanup()

	// 1. 保存一个带 RefreshToken 的账号
	acc := Account{
		ID:           "test_acc_1",
		UID:          "uid_1",
		Nickname:     "测试账号",
		Token:        "old_token",
		RefreshToken: "old_refresh",
		Domain:       "www.workbuddy.ai",
		Active:       true,
	}
	_, err := SaveAccount(nil, variant.Intl, acc, true)
	if err != nil {
		t.Fatalf("保存初始账号失败: %v", err)
	}

	// 2. 验证空 RefreshToken 拦截
	emptyAcc := Account{
		ID:       "empty_ref",
		UID:      "uid_2",
		Nickname: "无刷账号",
		Token:    "some_token",
		Active:   false,
	}
	_, _ = SaveAccount(nil, variant.Intl, emptyAcc, false)
	_, err = RefreshAccount(context.Background(), ts.Client(), nil, variant.Intl, "empty_ref")
	if err == nil {
		t.Errorf("对于无 RefreshToken 的账号，期望报错，实际无报错")
	}

	// 3. 正常刷新活跃账号
	refreshed, err := RefreshAccount(context.Background(), ts.Client(), nil, variant.Intl, "active")
	if err != nil {
		t.Fatalf("刷新活跃账号失败: %v", err)
	}
	if refreshed.Token != "refreshed_access_xyz" {
		t.Errorf("Token 未更新: %s", refreshed.Token)
	}
	if refreshed.RefreshToken != "refreshed_refresh_xyz" {
		t.Errorf("RefreshToken 未更新: %s", refreshed.RefreshToken)
	}

	// 4. 测试 RefreshActiveIfNeeded
	// 刚刷新完（TTL 3600s），此时 minTTL=2h（7200s），由于 3600s < 7200s，依然会判定需要刷新
	refreshed2, didRefresh, err := RefreshActiveIfNeeded(context.Background(), ts.Client(), nil, variant.Intl, 2*time.Hour)
	if err != nil {
		t.Fatalf("RefreshActiveIfNeeded 失败: %v", err)
	}
	if !didRefresh || refreshed2 == nil {
		t.Errorf("期望执行了刷新，实际 didRefresh=%v", didRefresh)
	}

	// 如果 minTTL 设为 10s，由于剩余 3600s > 10s，判定不需要刷新
	refreshed3, didRefresh2, err := RefreshActiveIfNeeded(context.Background(), ts.Client(), nil, variant.Intl, 10*time.Second)
	if err != nil {
		t.Fatalf("RefreshActiveIfNeeded 失败: %v", err)
	}
	if didRefresh2 {
		t.Errorf("期望未执行刷新，实际 didRefresh2=%v", didRefresh2)
	}
	if refreshed3 == nil || refreshed3.Token != "refreshed_access_xyz" {
		t.Errorf("返回的活跃账号不符: %v", refreshed3)
	}
}
