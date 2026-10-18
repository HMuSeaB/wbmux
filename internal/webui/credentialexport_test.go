package webui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestHandleCredentialExport(t *testing.T) {
	e := newTestEnv(t, Options{})

	// 1. 无令牌被拒 (403 Forbidden)
	rNoToken := e.do(t, http.MethodGet, "/api/credentials/export?side=intl", "", false)
	if rNoToken.code != http.StatusForbidden {
		t.Fatalf("期望未授权请求返回 403 Forbidden，实际: %d", rNoToken.code)
	}

	// 2. 非法 side 校验 (400 Bad Request)
	rBadSide := e.do(t, http.MethodGet, "/api/credentials/export?side=invalid", "", true)
	if rBadSide.code != http.StatusBadRequest {
		t.Fatalf("期望非法 side 返回 400 Bad Request，实际: %d", rBadSide.code)
	}

	// 3. 注入一个合法测试账号至隔离多账号库
	testAcc := credential.Account{
		ID:           "test_export_acc",
		UID:          "wb_user_export_999",
		Nickname:     "导出测试账号",
		Token:        "eyJhbGciOi-export-test-token-1234567890",
		RefreshToken: "ref_export_token_1234567890",
		Domain:       "www.workbuddy.ai",
		ExpiresAt:    time.Now().Add(48 * time.Hour).UnixMilli(),
		Active:       true,
	}
	_, err := credential.SaveAccount(e.srv.probe(), variant.Intl, testAcc, true)
	if err != nil {
		t.Fatalf("保存测试账号失败: %v", err)
	}

	// 4. 正常导出 (JSON 模式)
	rExport := e.do(t, http.MethodGet, "/api/credentials/export?side=intl", "", true)
	if rExport.code != http.StatusOK {
		t.Fatalf("期望导出返回 200 OK，实际: %d (%s)", rExport.code, string(rExport.body))
	}

	var res struct {
		OK      bool   `json:"ok"`
		Side    string `json:"side"`
		Account struct {
			UID            string `json:"uid"`
			Nickname       string `json:"nickname"`
			Token          string `json:"token"`
			RefreshToken   string `json:"refreshToken"`
			ExpiresAtHuman string `json:"expiresAtHuman"`
		} `json:"account"`
		OfficialFormat struct {
			Auth struct {
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
			} `json:"auth"`
			Account struct {
				UID string `json:"uid"`
			} `json:"account"`
		} `json:"officialFormat"`
	}
	if err := json.Unmarshal(rExport.body, &res); err != nil {
		t.Fatalf("反序列化导出结果失败: %v", err)
	}
	if !res.OK || res.Side != "intl" {
		t.Errorf("响应字段不符合预期: ok=%v, side=%s", res.OK, res.Side)
	}
	if res.Account.UID != testAcc.UID || res.Account.Token != testAcc.Token {
		t.Errorf("导出的账号信息不匹配: uid=%s, token=%s", res.Account.UID, res.Account.Token)
	}
	if res.OfficialFormat.Auth.AccessToken != testAcc.Token {
		t.Errorf("官方格式 accessToken 不匹配: %s", res.OfficialFormat.Auth.AccessToken)
	}

	// 5. 校验直接下载模式 (download=true)
	reqDownload, _ := http.NewRequest(http.MethodGet, "http://"+e.srv.Addr()+"/api/credentials/export?side=intl&download=true", nil)
	reqDownload.Header.Set("X-Wbmux-Token", e.srv.token)
	resp, err := http.DefaultClient.Do(reqDownload)
	if err != nil {
		t.Fatalf("发起下载请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望下载模式返回 200 OK，实际: %d", resp.StatusCode)
	}
	disp := resp.Header.Get("Content-Disposition")
	if !strings.Contains(disp, "attachment") || !strings.Contains(disp, "workbuddy-intl-credential") {
		t.Errorf("期望 Content-Disposition 包含 attachment 和文件名，实际: %q", disp)
	}

	// 6. 校验全量导出模式 (download=true&all=true)
	reqDownloadAll, _ := http.NewRequest(http.MethodGet, "http://"+e.srv.Addr()+"/api/credentials/export?side=intl&download=true&all=true", nil)
	reqDownloadAll.Header.Set("X-Wbmux-Token", e.srv.token)
	respAll, err := http.DefaultClient.Do(reqDownloadAll)
	if err != nil {
		t.Fatalf("发起全量下载请求失败: %v", err)
	}
	defer respAll.Body.Close()
	if respAll.StatusCode != http.StatusOK {
		t.Fatalf("期望全量下载模式返回 200 OK，实际: %d", respAll.StatusCode)
	}
	dispAll := respAll.Header.Get("Content-Disposition")
	if !strings.Contains(dispAll, "all-accounts-backup") {
		t.Errorf("期望 Content-Disposition 包含 all-accounts-backup，实际: %q", dispAll)
	}
}
