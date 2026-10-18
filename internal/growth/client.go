package growth

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/telemetry"
)

// TierInfo 代表连续活跃的阶段奖励信息。
type TierInfo struct {
	Tier   string `json:"tier"`   // "7d", "14d", "28d"
	Days   int    `json:"days"`   // 达成天数 (7, 14, 28)
	Credit int    `json:"credit"` // 积分奖励 (0, 50, 150)
	Energy int    `json:"energy"` // 能量奖励
	Cards  int    `json:"cards"`  // 补签/抽奖卡
	Status string `json:"status"` // "locked", "claimable", "claimed"
}

// StreakStatus 是国际端成长中心连续活跃与阶梯积分状态。
type StreakStatus struct {
	Days              int        `json:"days"`              // 当前连续活跃天数
	MonthTotalDays    int        `json:"monthTotalDays"`    // 本月累计活跃天数
	NextTier          string     `json:"nextTier"`          // 下一个里程碑
	NextTierRemaining int        `json:"nextTierRemaining"` // 距离下一个里程碑还剩几天
	Tiers             []TierInfo `json:"tiers"`             // 阶梯奖励详情
	LastSyncedAt      time.Time  `json:"lastSyncedAt"`      // 最近同步时间
}

// Client 是国际端成长中心与积分激励客户端。
type Client struct {
	httpClient *http.Client
	endpoint   string
}

// NewClient 构造客户端。
func NewClient(endpoint string) *Client {
	if endpoint == "" {
		endpoint = "https://www.workbuddy.ai"
	}
	return &Client{
		httpClient: &http.Client{Timeout: 10 * time.Second},
		endpoint:   endpoint,
	}
}

// FetchStreak 查询当前国际账号的连续活跃天数与阶梯奖励状态。
func (c *Client) FetchStreak(cred credential.Credential) (*StreakStatus, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("credential token is empty")
	}

	url := c.endpoint + "/activity/growth/streak"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	applyHeaders(req, cred)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream returned %d: %s", resp.StatusCode, string(body))
	}

	var rawDoc struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			Streak struct {
				Days              int    `json:"days"`
				MonthTotalDays    int    `json:"month_total_days"`
				NextTier          string `json:"next_tier"`
				NextTierRemaining int    `json:"next_tier_remaining"`
			} `json:"streak"`
			RedemptionStatus struct {
				Tier7dStatus  string `json:"tier_7d_status"`
				Tier14dStatus string `json:"tier_14d_status"`
				Tier28dStatus string `json:"tier_28d_status"`
				Tiers         []struct {
					Tier   string `json:"tier"`
					Days   int    `json:"days"`
					Credit int    `json:"credit"`
					Energy int    `json:"energy"`
					Cards  int    `json:"cards"`
				} `json:"tiers"`
			} `json:"redemption_status"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &rawDoc); err != nil {
		return nil, fmt.Errorf("unmarshal streak response: %w", err)
	}

	statusMap := map[string]string{
		"7d":  rawDoc.Data.RedemptionStatus.Tier7dStatus,
		"14d": rawDoc.Data.RedemptionStatus.Tier14dStatus,
		"28d": rawDoc.Data.RedemptionStatus.Tier28dStatus,
	}

	res := &StreakStatus{
		Days:              rawDoc.Data.Streak.Days,
		MonthTotalDays:    rawDoc.Data.Streak.MonthTotalDays,
		NextTier:          rawDoc.Data.Streak.NextTier,
		NextTierRemaining: rawDoc.Data.Streak.NextTierRemaining,
		LastSyncedAt:      time.Now(),
	}

	for _, t := range rawDoc.Data.RedemptionStatus.Tiers {
		st := statusMap[t.Tier]
		if st == "" {
			st = "locked"
		}
		res.Tiers = append(res.Tiers, TierInfo{
			Tier:   t.Tier,
			Days:   t.Days,
			Credit: t.Credit,
			Energy: t.Energy,
			Cards:  t.Cards,
			Status: st,
		})
	}

	return res, nil
}

// RedeemTier 领取阶段性连续活跃积分奖励（如 "14d" 或 "28d"）。
func (c *Client) RedeemTier(cred credential.Credential, tier string) (int, error) {
	if cred.Token == "" {
		return 0, fmt.Errorf("credential token is empty")
	}

	nonceBytes := make([]byte, 8)
	_, _ = rand.Read(nonceBytes)
	clientToken := fmt.Sprintf("redeem-%s-%s", tier, hex.EncodeToString(nonceBytes))

	payload, _ := json.Marshal(map[string]string{
		"tier":         tier,
		"client_token": clientToken,
	})

	url := c.endpoint + "/activity/growth/redeem"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}

	applyHeaders(req, cred)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var resDoc struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			CreditGranted int `json:"credit_granted"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &resDoc); err != nil {
		return 0, fmt.Errorf("unmarshal redeem response: %w", err)
	}

	if resp.StatusCode != http.StatusOK || resDoc.Code != 0 {
		return 0, fmt.Errorf("redeem failed: %s", resDoc.Msg)
	}

	return resDoc.Data.CreditGranted, nil
}

// applyHeaders 注入反风控指纹与标准授权头。
func applyHeaders(req *http.Request, cred credential.Credential) {
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	req.Header.Set("X-User-Id", cred.UID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", telemetry.UserAgent(""))
	req.Header.Set("X-Device-Id", telemetry.DeviceID(""))
}
