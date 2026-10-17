package telemetry

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Status 记录遥测守护器的当前健康与运行状态。
type Status struct {
	Running     bool      `json:"running"`
	DeviceID    string    `json:"deviceId"`
	LastPingAt  time.Time `json:"lastPingAt,omitempty"`
	LastPingOK  bool      `json:"lastPingOk"`
	LastPingErr string    `json:"lastPingErr,omitempty"`
	TotalPings  int       `json:"totalPings"`
}

// Guardian 是后台自适应遥测与活跃心跳守护器。
type Guardian struct {
	mu         sync.RWMutex
	homeDir    string
	goos       string
	endpoint   string
	credGetter func() (token, uid string, err error)
	status     Status
	client     *http.Client
}

// NewGuardian 创建一个心跳守护器实例。
func NewGuardian(homeDir, goos, endpoint string, credGetter func() (token, uid string, err error)) *Guardian {
	if endpoint == "" {
		endpoint = "https://www.workbuddy.ai"
	}
	return &Guardian{
		homeDir:    homeDir,
		goos:       goos,
		endpoint:   endpoint,
		credGetter: credGetter,
		client:     &http.Client{Timeout: 15 * time.Second},
		status: Status{
			DeviceID: DeviceID(homeDir),
		},
	}
}

// GetStatus 获取当前遥测守护状态的快照。
func (g *Guardian) GetStatus() Status {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.status
}

// PingOnce 发送一次轻量级心跳与配置刷新。
func (g *Guardian) PingOnce(ctx context.Context) error {
	if g.credGetter == nil {
		return fmt.Errorf("未配置凭据获取器")
	}
	token, uid, err := g.credGetter()
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("无可用登录令牌")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", g.endpoint+"/v3/config", nil)
	if err != nil {
		return err
	}

	// 注入拟真头与凭据
	for k, v := range CommonHeaders(variant.Intl, g.homeDir, g.goos) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if uid != "" {
		req.Header.Set("X-User-Id", uid)
	}

	resp, err := g.client.Do(req)
	now := time.Now()

	g.mu.Lock()
	defer g.mu.Unlock()

	g.status.LastPingAt = now
	g.status.TotalPings++

	if err != nil {
		g.status.LastPingOK = false
		g.status.LastPingErr = err.Error()
		return err
	}
	_ = resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		g.status.LastPingOK = true
		g.status.LastPingErr = ""
		return nil
	}

	err = fmt.Errorf("服务端返回 HTTP %d", resp.StatusCode)
	g.status.LastPingOK = false
	g.status.LastPingErr = err.Error()
	return err
}

// Start 启动后台守护循环。
//
// 默认间隔为 12 分钟，并在每次心跳前增加 ±90 秒的伪随机抖动，
// 避免呈现出严格的机器周期特征，最大限度契合人类客户端日常前台活跃模型。
func (g *Guardian) Start(ctx context.Context) {
	g.mu.Lock()
	g.status.Running = true
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.status.Running = false
		g.mu.Unlock()
	}()

	// 启动后先做一次自适应初始心跳（延迟 5 秒，不与系统启动竞争）
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second):
		_ = g.PingOnce(ctx)
	}

	for {
		// 计算带抖动的等待周期：10m ~ 13m
		jitterSeconds := randomJitter(180) - 90
		wait := 12*time.Minute + time.Duration(jitterSeconds)*time.Second
		if wait < 5*time.Minute {
			wait = 5 * time.Minute
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			_ = g.PingOnce(ctx)
		}
	}
}

func randomJitter(maxSeconds int64) int64 {
	if maxSeconds <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(maxSeconds))
	if err != nil {
		return maxSeconds / 2
	}
	return n.Int64()
}
