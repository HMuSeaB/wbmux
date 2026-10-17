package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/telemetry"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// OfficialAuthRefreshURL 官方标准 Token 刷新接口地址
const OfficialAuthRefreshURL = OfficialBaseURL + "/v2/plugin/auth/token/refresh"

// tokenRefreshResponse 对应官方 POST /v2/plugin/auth/token/refresh 响应结构
type tokenRefreshResponse struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	RequestId string `json:"requestId"`
	Data      struct {
		AccessToken      string `json:"accessToken"`
		RefreshToken     string `json:"refreshToken"`
		ExpiresIn        int64  `json:"expiresIn"`
		RefreshExpiresIn int64  `json:"refreshExpiresIn"`
		Domain           string `json:"domain"`
	} `json:"data"`
}

// RefreshResult 承载 Token 刷新的返回值
type RefreshResult struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt"`
	Domain           string `json:"domain"`
}

var authRefreshBaseURL = OfficialBaseURL

// SetAuthRefreshBaseURLForTest 供单元测试注入 mock 授权中心地址
func SetAuthRefreshBaseURLForTest(u string) func() {
	prev := authRefreshBaseURL
	authRefreshBaseURL = u
	return func() {
		authRefreshBaseURL = prev
	}
}

// DoTokenRefresh 向官方授权中心发起 POST /v2/plugin/auth/token/refresh 进行令牌刷新。
func DoTokenRefresh(ctx context.Context, client *http.Client, probe *variant.Probe, refreshToken, domain string) (*RefreshResult, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("refreshToken 不能为空")
	}

	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}

	if domain == "" {
		domain = OfficialAuthDomain
	}

	refreshURL := authRefreshBaseURL + "/v2/plugin/auth/token/refresh"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, refreshURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, fmt.Errorf("创建刷新请求失败: %w", err)
	}

	homeDir := ""
	if probe != nil {
		homeDir = probe.Home
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domain", domain)
	req.Header.Set("X-Refresh-Token", refreshToken)
	req.Header.Set("X-Auth-Refresh-Source", "plugin")
	req.Header.Set("User-Agent", telemetry.UserAgent(homeDir))
	if devID := telemetry.DeviceID(homeDir); devID != "" {
		req.Header.Set("X-Device-Id", devID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("向官方刷新网关通信失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取刷新响应正文失败: %w", err)
	}

	var res tokenRefreshResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("解析刷新响应 JSON 失败: %w", err)
	}

	if res.Code != 0 {
		return nil, fmt.Errorf("官方刷新令牌失败: code=%d msg=%s", res.Code, res.Msg)
	}

	newAccess := strings.TrimSpace(res.Data.AccessToken)
	if newAccess == "" {
		return nil, errors.New("官方网关返回成功但未包含新 AccessToken")
	}

	nowMs := time.Now().UnixMilli()
	var expiresAt, refreshExpiresAt int64
	if res.Data.ExpiresIn > 0 {
		expiresAt = nowMs + res.Data.ExpiresIn*1000
	}
	if res.Data.RefreshExpiresIn > 0 {
		refreshExpiresAt = nowMs + res.Data.RefreshExpiresIn*1000
	}

	newRefresh := strings.TrimSpace(res.Data.RefreshToken)
	if newRefresh == "" {
		newRefresh = refreshToken // 若服务端未轮换 RefreshToken，则保留原 RefreshToken
	}

	retDomain := res.Data.Domain
	if retDomain == "" {
		retDomain = domain
	}

	return &RefreshResult{
		AccessToken:      newAccess,
		RefreshToken:     newRefresh,
		ExpiresAt:        expiresAt,
		RefreshExpiresAt: refreshExpiresAt,
		Domain:           retDomain,
	}, nil
}

// RefreshAccount 针对多账号库中的指定账号（或当前激活账号）执行 Token 刷新并持久化。
func RefreshAccount(ctx context.Context, client *http.Client, probe *variant.Probe, side variant.ID, accountID string) (*Account, error) {
	list, err := ListAccounts(probe, side)
	if err != nil {
		return nil, fmt.Errorf("读取账号列表失败: %w", err)
	}

	var target *Account
	for i := range list {
		if (accountID == "" || accountID == "active") && list[i].Active {
			target = &list[i]
			break
		}
		if list[i].ID == accountID {
			target = &list[i]
			break
		}
	}

	if target == nil {
		if accountID == "" || accountID == "active" {
			return nil, errors.New("当前没有活跃的账号")
		}
		return nil, fmt.Errorf("未找到 ID 为 %s 的账号", accountID)
	}

	if strings.TrimSpace(target.RefreshToken) == "" {
		return nil, fmt.Errorf("账号 [%s] 未保存 RefreshToken，无法自动刷新", target.Nickname)
	}

	res, err := DoTokenRefresh(ctx, client, probe, target.RefreshToken, target.Domain)
	if err != nil {
		return nil, err
	}

	target.Token = res.AccessToken
	if res.RefreshToken != "" {
		target.RefreshToken = res.RefreshToken
	}
	if res.ExpiresAt > 0 {
		target.ExpiresAt = res.ExpiresAt
	}
	if res.RefreshExpiresAt > 0 {
		target.RefreshExpiresAt = res.RefreshExpiresAt
	}
	target.Domain = res.Domain
	target.UpdatedAt = time.Now().UnixMilli()

	saved, err := SaveAccount(probe, side, *target, target.Active)
	if err != nil {
		return nil, fmt.Errorf("持久化刷新后的账号失败: %w", err)
	}

	return saved, nil
}

// RefreshActiveIfNeeded 检查当前活跃账号是否即将在 minTTL 内到期，若是且具备 RefreshToken，则执行自动刷新。
// 返回刷新后的账号、是否执行了刷新动作以及可能的错误。
func RefreshActiveIfNeeded(ctx context.Context, client *http.Client, probe *variant.Probe, side variant.ID, minTTL time.Duration) (*Account, bool, error) {
	list, err := ListAccounts(probe, side)
	if err != nil {
		return nil, false, err
	}

	var active *Account
	for i := range list {
		if list[i].Active {
			active = &list[i]
			break
		}
	}

	if active == nil {
		return nil, false, nil
	}

	if strings.TrimSpace(active.RefreshToken) == "" {
		return active, false, nil
	}

	// 若未设置到期时间，或者已到期，或者距离到期小于 minTTL，则触发刷新
	nowMs := time.Now().UnixMilli()
	needRefresh := false
	if active.ExpiresAt <= 0 {
		// 没有到期时间戳，可能由旧版本迁移而来，主动触发一次以补齐时间戳
		needRefresh = true
	} else if active.ExpiresAt-nowMs <= minTTL.Milliseconds() {
		needRefresh = true
	}

	if !needRefresh {
		return active, false, nil
	}

	refreshed, err := RefreshAccount(ctx, client, probe, side, active.ID)
	if err != nil {
		return active, false, err
	}
	return refreshed, true, nil
}
