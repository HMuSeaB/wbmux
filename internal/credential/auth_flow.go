package credential

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/telemetry"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

const (
	OfficialAuthDomain   = "www.workbuddy.ai"
	OfficialBaseURL      = "https://www.workbuddy.ai"
	OfficialAuthStateURL = OfficialBaseURL + "/v2/plugin/auth/state?platform=workbuddy-ai"
	OfficialAuthTokenURL = OfficialBaseURL + "/v2/plugin/auth/token"
	OfficialAccountURL   = OfficialBaseURL + "/v2/plugin/account"

	// CodeLoginPending 官方定义的正在登录中错误码
	CodeLoginPending = 11217

	// DefaultFlowTimeout 单次登录流程最长等待时间（5分钟）
	DefaultFlowTimeout = 5 * time.Minute
)

// LoginFlow 代表一个处于生命周期内的拟真登录会话
type LoginFlow struct {
	State     string    `json:"state"`
	AuthURL   string    `json:"authUrl"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// FlowPollResult 代表轮询登录的结果
type FlowPollResult struct {
	Status   string           `json:"status"` // "pending", "success", "expired", "failed"
	Message  string           `json:"message,omitempty"`
	Account  *RedactedAccount `json:"account,omitempty"`
	Nickname string           `json:"nickname,omitempty"`
	UID      string           `json:"uid,omitempty"`
}

// LoginFlowManager 管理官方拟真登录流程
type LoginFlowManager struct {
	mu      sync.RWMutex
	flows   map[string]*LoginFlow
	client  *http.Client
	probe   *variant.Probe
	baseURL string
}

var (
	defaultFlowManager *LoginFlowManager
	flowManagerOnce    sync.Once
)

// GetDefaultFlowManager 获取全局拟真登录管理器单例
func GetDefaultFlowManager(probe *variant.Probe) *LoginFlowManager {
	flowManagerOnce.Do(func() {
		defaultFlowManager = NewLoginFlowManager(probe, nil)
	})
	if probe != nil && defaultFlowManager.probe == nil {
		defaultFlowManager.probe = probe
	}
	return defaultFlowManager
}

// NewLoginFlowManager 创建新的拟真登录管理器
func NewLoginFlowManager(probe *variant.Probe, client *http.Client) *LoginFlowManager {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &LoginFlowManager{
		flows:   make(map[string]*LoginFlow),
		client:  client,
		probe:   probe,
		baseURL: OfficialBaseURL,
	}
}

// SetBaseURL 仅供单元测试或自建镜像网关注入使用
func (m *LoginFlowManager) SetBaseURL(url string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.baseURL = strings.TrimRight(url, "/")
}

func (m *LoginFlowManager) getBaseURL() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.baseURL == "" {
		return OfficialBaseURL
	}
	return m.baseURL
}

func (m *LoginFlowManager) getHomeDir() string {
	if m.probe != nil {
		return m.probe.Home
	}
	return ""
}

// cleanExpiredLocked 清理过期未完成的会话
func (m *LoginFlowManager) cleanExpiredLocked() {
	now := time.Now()
	for k, v := range m.flows {
		if now.After(v.ExpiresAt) {
			delete(m.flows, k)
		}
	}
}

type authStateResponse struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	RequestId string `json:"requestId"`
	Data      struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	} `json:"data"`
}

type authTokenResponse struct {
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

type accountResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
		Uin      string `json:"uin"`
		Avatar   string `json:"avatarUrl"`
	} `json:"data"`
}

// StartLoginFlow 发起官方拟真登录会话，向官方申请 state 和 authUrl
func (m *LoginFlowManager) StartLoginFlow(ctx context.Context) (*LoginFlow, error) {
	stateURL := m.getBaseURL() + "/v2/plugin/auth/state?platform=workbuddy-ai"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stateURL, bytes.NewBufferString("{}"))
	if err != nil {
		return nil, fmt.Errorf("创建授权请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Domain", OfficialAuthDomain)
	req.Header.Set("User-Agent", telemetry.UserAgent(""))
	if devID := telemetry.DeviceID(m.getHomeDir()); devID != "" {
		req.Header.Set("X-Device-Id", devID)
	}


	resp, err := m.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("向官方网关请求授权地址失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取授权响应失败: %w", err)
	}

	var res authStateResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("解析授权响应 JSON 失败: %w", err)
	}

	if res.Code != 0 || res.Data.State == "" || res.Data.AuthURL == "" {
		return nil, fmt.Errorf("官方网关拒绝创建登录态: code=%d msg=%s", res.Code, res.Msg)
	}

	now := time.Now()
	flow := &LoginFlow{
		State:     res.Data.State,
		AuthURL:   res.Data.AuthURL,
		CreatedAt: now,
		ExpiresAt: now.Add(DefaultFlowTimeout),
	}

	m.mu.Lock()
	m.cleanExpiredLocked()
	m.flows[flow.State] = flow
	m.mu.Unlock()

	return flow, nil
}

// PollLoginStatus 轮询指定 state 的官方登录进度
func (m *LoginFlowManager) PollLoginStatus(ctx context.Context, state string) (*FlowPollResult, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return &FlowPollResult{Status: "failed", Message: "state 不能为空"}, errors.New("state 不能为空")
	}

	m.mu.RLock()
	flow, exists := m.flows[state]
	m.mu.RUnlock()

	if !exists {
		return &FlowPollResult{
			Status:  "expired",
			Message: "登录会话不存在或已过期，请重新点击登录",
		}, nil
	}

	if time.Now().After(flow.ExpiresAt) {
		m.mu.Lock()
		delete(m.flows, state)
		m.mu.Unlock()
		return &FlowPollResult{
			Status:  "expired",
			Message: "登录等待已超时（5分钟），请重新发起",
		}, nil
	}

	// 请求官方获取 Token 接口
	tokenURL := fmt.Sprintf("%s/v2/plugin/auth/token?state=%s", m.getBaseURL(), state)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL, nil)

	if err != nil {
		return nil, fmt.Errorf("创建轮询请求失败: %w", err)
	}
	req.Header.Set("X-Domain", OfficialAuthDomain)
	req.Header.Set("User-Agent", telemetry.UserAgent(""))
	if devID := telemetry.DeviceID(m.getHomeDir()); devID != "" {
		req.Header.Set("X-Device-Id", devID)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return &FlowPollResult{Status: "pending", Message: "网络请求暂未连通，继续等待..."}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &FlowPollResult{Status: "pending", Message: "读取响应中..."}, nil
	}

	var tokenRes authTokenResponse
	if err := json.Unmarshal(body, &tokenRes); err != nil {
		return &FlowPollResult{Status: "pending", Message: "解析响应中..."}, nil
	}

	// 用户还在网页登录中
	if tokenRes.Code == CodeLoginPending {
		return &FlowPollResult{
			Status:  "pending",
			Message: "等待用户在浏览器中完成登录...",
		}, nil
	}

	// 官方返回其他非零错误码（如超时或已被撤销）
	if tokenRes.Code != 0 {
		return &FlowPollResult{
			Status:  "failed",
			Message: fmt.Sprintf("官方授权失败: [%d] %s", tokenRes.Code, tokenRes.Msg),
		}, nil
	}

	// 登录成功！提取 Token
	accessToken := strings.TrimSpace(tokenRes.Data.AccessToken)
	if accessToken == "" {
		return &FlowPollResult{
			Status:  "failed",
			Message: "授权返回成功但未能获取有效 Token",
		}, nil
	}

	// 进一步拉取该 Token 对应的用户详情
	uid, nickname := m.fetchAccountInfo(ctx, accessToken)
	if uid == "" {
		uid = extractUIDFromJWT(accessToken)
	}
	if uid == "" {
		uid = "intl_user"
	}
	if nickname == "" {
		nickname = "国际版用户"
	}

	// 组装并保存进多账号池
	domain := tokenRes.Data.Domain
	if domain == "" {
		domain = OfficialAuthDomain
	}

	acc := Account{
		UID:      uid,
		Nickname: nickname,
		Token:    accessToken,
		Domain:   domain,
	}

	saved, err := SaveAccount(m.probe, variant.Intl, acc, true)
	if err != nil {
		return &FlowPollResult{
			Status:  "failed",
			Message: fmt.Sprintf("保存账号凭据失败: %v", err),
		}, nil
	}

	// 流程圆满完成，从活跃 flow 列表中移除
	m.mu.Lock()
	delete(m.flows, state)
	m.mu.Unlock()

	redacted := saved.Redact()
	return &FlowPollResult{
		Status:   "success",
		Message:  "官方拟真授权成功，账号已安全入库并激活！",
		Account:  &redacted,
		Nickname: nickname,
		UID:      uid,
	}, nil
}

// fetchAccountInfo 尝试使用获取到的 accessToken 换取真实用户资料
func (m *LoginFlowManager) fetchAccountInfo(ctx context.Context, token string) (string, string) {
	accountURL := m.getBaseURL() + "/v2/plugin/account"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, accountURL, nil)
	if err != nil {
		return "", ""
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Domain", OfficialAuthDomain)
	req.Header.Set("User-Agent", telemetry.UserAgent(""))
	if devID := telemetry.DeviceID(m.getHomeDir()); devID != "" {
		req.Header.Set("X-Device-Id", devID)
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return "", ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", ""
	}

	var accRes accountResponse
	if err := json.Unmarshal(body, &accRes); err != nil {
		return "", ""
	}

	if accRes.Code == 0 && accRes.Data.UID != "" {
		name := accRes.Data.Nickname
		if name == "" {
			name = accRes.Data.Uin
		}
		return accRes.Data.UID, name
	}
	return "", ""
}

// extractUIDFromJWT 尝试从 JWT payload 中提取 uid 或 sub
func extractUIDFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	if l := len(seg)%4; l > 0 {
		seg += strings.Repeat("=", 4-l)
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(seg)
		if err != nil {
			return ""
		}
	}
	var payload struct {
		UID    string `json:"uid"`
		Sub    string `json:"sub"`
		UserId string `json:"userId"`
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if payload.UID != "" {
		return payload.UID
	}
	if payload.Sub != "" {
		return payload.Sub
	}
	if payload.UserId != "" {
		return payload.UserId
	}
	return payload.UserID
}

