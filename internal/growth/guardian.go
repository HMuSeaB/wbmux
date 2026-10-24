package growth

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

// GuardianStatus 描述守护器的当前运行与收割统计。
type GuardianStatus struct {
	Running             bool      `json:"running"`
	LastCheckAt         time.Time `json:"lastCheckAt,omitempty"`
	NextCheckAt         time.Time `json:"nextCheckAt,omitempty"`
	LastState           string    `json:"lastState,omitempty"` // arrived, traveling, idle, unauthenticated, error
	LastAction          string    `json:"lastAction,omitempty"`
	LastActionDetail    string    `json:"lastActionDetail,omitempty"`
	LastActionAt        time.Time `json:"lastActionAt,omitempty"`
	TotalHarvests       int       `json:"totalHarvests"`       // 累计自动收割次数
	TotalHarvestCredits int64     `json:"totalHarvestCredits"` // 累计通过猫猫旅行收获的免费积分
	LastDailySweepDate  string    `json:"lastDailySweepDate,omitempty"`
	LastError           string    `json:"lastError,omitempty"`
}

// GuardianConfig 守护器启动配置。
type GuardianConfig struct {
	Endpoint       string
	CredGetter     func() (credential.Credential, error)
	OnNote         func(level, text string) // 代理日志通知回调
	MinInterval    time.Duration            // 最小重试间隔，默认 30s
	MaxInterval    time.Duration            // 最大兜底间隔，默认 15m
	AutoDailySweep bool                     // 是否开启每日跨天自动全量扫荡日常任务
	WakeCh         chan struct{}            // 手动立即唤醒通道
}

// TravelGuardian 是猫咪旅行后台巡检与全天候静默收割守护系统。
type TravelGuardian struct {
	mu     sync.RWMutex
	cfg    GuardianConfig
	client *Client
	status GuardianStatus
	wakeCh chan struct{}
}

// NewTravelGuardian 创建一个猫咪旅行后台守护器。
func NewTravelGuardian(cfg GuardianConfig) *TravelGuardian {
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = 30 * time.Second
	}
	if cfg.MaxInterval <= 0 {
		cfg.MaxInterval = 15 * time.Minute
	}
	wake := cfg.WakeCh
	if wake == nil {
		wake = make(chan struct{}, 1)
	}
	return &TravelGuardian{
		cfg:    cfg,
		client: NewClient(cfg.Endpoint),
		status: GuardianStatus{
			Running: false,
		},
		wakeCh: wake,
	}
}

// GetStatus 获取守护器当前状态快照。
func (g *TravelGuardian) GetStatus() GuardianStatus {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.status
}

// TriggerNow 立即唤醒守护器执行一次巡检。
func (g *TravelGuardian) TriggerNow() {
	select {
	case g.wakeCh <- struct{}{}:
	default:
	}
}

// Start 启动后台守护主循环，直到 ctx 被取消。
func (g *TravelGuardian) Start(ctx context.Context) {
	g.mu.Lock()
	g.status.Running = true
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.status.Running = false
		g.mu.Unlock()
	}()

	// 首次启动立即执行一次巡检
	nextWait := g.checkOnce(ctx)

	for {
		g.mu.Lock()
		g.status.NextCheckAt = time.Now().Add(nextWait)
		g.mu.Unlock()

		timer := time.NewTimer(nextWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-g.wakeCh:
			timer.Stop()
			nextWait = g.checkOnce(ctx)
		case <-timer.C:
			nextWait = g.checkOnce(ctx)
		}
	}
}

// checkOnce 执行单次巡检与自动决策。返回下次建议等待时长。
func (g *TravelGuardian) checkOnce(ctx context.Context) time.Duration {
	now := time.Now()
	g.mu.Lock()
	g.status.LastCheckAt = now
	g.mu.Unlock()

	if g.cfg.CredGetter == nil {
		g.setError("未配置凭据获取器")
		return g.cfg.MaxInterval
	}

	cred, err := g.cfg.CredGetter()
	if err != nil || cred.Token == "" {
		g.mu.Lock()
		g.status.LastState = "unauthenticated"
		g.status.LastError = "暂未检测到国内有效登录凭证"
		g.mu.Unlock()
		// 未登录时退避 5 分钟
		return 5 * time.Minute
	}

	// 1. 每日跨天静默全量扫荡
	today := now.Format("2006-01-02")
	g.mu.RLock()
	lastSweepDate := g.status.LastDailySweepDate
	g.mu.RUnlock()

	if g.cfg.AutoDailySweep && lastSweepDate != today {
		go g.runDailySweep(cred, today)
	}

	// 2. 检查猫咪旅行状态
	st, err := g.client.FetchTravelStatus(cred)
	if err != nil {
		g.setError("获取猫咪旅行状态失败: " + err.Error())
		return g.cfg.MinInterval + jitterSeconds(10, 20)
	}

	g.mu.Lock()
	g.status.LastState = st.State
	g.status.LastError = ""
	g.mu.Unlock()

	switch st.State {
	case "arrived":
		// 猫咪已到达，自动收割并再次派遣
		credit, claimErr := g.client.ClaimTravelReward(cred)
		if claimErr != nil {
			g.setError("自动领取猫咪旅行奖励失败: " + claimErr.Error())
			return g.cfg.MinInterval
		}

		g.mu.Lock()
		g.status.TotalHarvests++
		g.status.TotalHarvestCredits += credit
		g.status.LastAction = "harvest_and_depart"
		g.status.LastActionAt = time.Now()
		g.status.LastActionDetail = fmt.Sprintf("收割 +%d 积分", credit)
		g.mu.Unlock()

		// 立即重新派出咖啡馆旅行
		newSt, departErr := g.client.DepartTravel(cred, 1)
		locName := "咖啡馆"
		remSeconds := int64(1800)
		if departErr == nil && newSt != nil {
			if newSt.Location != nil {
				locName = newSt.Location.Name
			}
			if newSt.ArriveAt > newSt.ServerNow {
				remSeconds = newSt.ArriveAt - newSt.ServerNow
			}
		}

		noteMsg := fmt.Sprintf("🐱 [旅行守护者] 猫咪旅行归来，自动收割 +%d 积分！已再次启程奔赴「%s」~", credit, locName)
		if g.cfg.OnNote != nil {
			g.cfg.OnNote("info", noteMsg)
		}

		// 根据剩余时间自适应休眠
		wait := time.Duration(remSeconds)*time.Second + jitterSeconds(10, 30)
		if wait < g.cfg.MinInterval {
			wait = g.cfg.MinInterval
		}
		return wait

	case "idle":
		// 猫咪闲置，自动派遣出发
		newSt, departErr := g.client.DepartTravel(cred, 1)
		if departErr != nil {
			g.setError("自动派遣旅行失败: " + departErr.Error())
			return g.cfg.MinInterval
		}

		locName := "咖啡馆"
		remSeconds := int64(1800)
		if newSt != nil {
			if newSt.Location != nil {
				locName = newSt.Location.Name
			}
			if newSt.ArriveAt > newSt.ServerNow {
				remSeconds = newSt.ArriveAt - newSt.ServerNow
			}
		}

		g.mu.Lock()
		g.status.LastAction = "depart"
		g.status.LastActionAt = time.Now()
		g.status.LastActionDetail = fmt.Sprintf("派遣前往 %s", locName)
		g.mu.Unlock()

		noteMsg := fmt.Sprintf("🐱 [旅行守护者] 检测到猫咪闲置，已自动派遣启程前往「%s」旅行！", locName)
		if g.cfg.OnNote != nil {
			g.cfg.OnNote("info", noteMsg)
		}

		wait := time.Duration(remSeconds)*time.Second + jitterSeconds(10, 30)
		if wait < g.cfg.MinInterval {
			wait = g.cfg.MinInterval
		}
		return wait

	case "traveling":
		// 正在旅行中，计算剩余时间并休眠到站
		rem := st.ArriveAt - st.ServerNow
		if rem <= 0 {
			// 可能刚到达或服务端尚未翻转状态，短时间后再次探测
			return g.cfg.MinInterval + jitterSeconds(5, 15)
		}
		wait := time.Duration(rem)*time.Second + jitterSeconds(10, 30)
		if wait > g.cfg.MaxInterval {
			// 如果旅行时间很长，设为兜底保活检测
			wait = g.cfg.MaxInterval
		}
		return wait

	default:
		return g.cfg.MaxInterval
	}
}

// runDailySweep 执行每日跨天后台静默任务扫荡。
func (g *TravelGuardian) runDailySweep(cred credential.Credential, today string) {
	g.mu.Lock()
	// 先标记已扫荡，防止并发重复触发
	g.status.LastDailySweepDate = today
	g.mu.Unlock()

	summary, err := g.client.SweepAll(cred)
	if err != nil {
		g.mu.Lock()
		g.status.LastError = "每日任务自动扫荡异常: " + err.Error()
		g.mu.Unlock()
		return
	}

	var noteText string
	if summary.TotalCreditGranted > 0 || summary.TotalEnergyGranted > 0 {
		noteText = fmt.Sprintf("🌅 [每日守护] 今日成长任务自动扫荡入账：达成 %d 项任务，获得 +%d 积分与 +%d 能量！",
			summary.TasksCompleted, summary.TotalCreditGranted, summary.TotalEnergyGranted)
	} else if summary.TasksSkipped > 0 {
		noteText = fmt.Sprintf("🌅 [每日守护] 今日成长任务巡检完毕：此前已领 %d 项日常（当前无需重复领奖）。", summary.TasksSkipped)
	} else {
		noteText = "🌅 [每日守护] 今日成长任务巡检完毕：当前账号已是最新状态。"
	}
	if summary.CompensationNotice != "" {
		noteText += fmt.Sprintf("（%s）", summary.CompensationNotice)
	}

	if g.cfg.OnNote != nil {
		g.cfg.OnNote("info", noteText)
	}
}

func (g *TravelGuardian) setError(errStr string) {
	g.mu.Lock()
	g.status.LastError = errStr
	g.status.LastState = "error"
	g.mu.Unlock()
}

// jitterSeconds 生成 [min, max] 范围内的安全伪随机秒数。
func jitterSeconds(min, max int) time.Duration {
	if max <= min {
		return time.Duration(min) * time.Second
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return time.Duration(min) * time.Second
	}
	return time.Duration(min+int(n.Int64())) * time.Second
}
