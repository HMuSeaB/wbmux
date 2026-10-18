package credential

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// DefaultCooldownDuration 默认限流冷却保护期（15 分钟）
const DefaultCooldownDuration = 15 * time.Minute

// FailoverTracker 管理多账号池在遭遇 429 或配额耗尽时的健康度与冷却状态。
type FailoverTracker struct {
	mu        sync.RWMutex
	cooldowns map[string]time.Time
}

var (
	defaultFailoverTracker *FailoverTracker
	failoverOnce           sync.Once
)

// GetDefaultFailoverTracker 获取全局故障转移跟踪器单例。
func GetDefaultFailoverTracker() *FailoverTracker {
	failoverOnce.Do(func() {
		defaultFailoverTracker = &FailoverTracker{
			cooldowns: make(map[string]time.Time),
		}
	})
	return defaultFailoverTracker
}

// ReportRateLimited 报告指定账号遭遇 429 限流或额度用尽，并设定冷却时间。
func (ft *FailoverTracker) ReportRateLimited(accountID string, duration time.Duration) {
	if accountID == "" {
		return
	}
	if duration <= 0 {
		duration = DefaultCooldownDuration
	}

	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.cooldowns[accountID] = time.Now().Add(duration)
}

// GetCooldown 查询指定账号是否处于冷却期，以及剩余时长。
func (ft *FailoverTracker) GetCooldown(accountID string) (bool, time.Duration) {
	if accountID == "" {
		return false, 0
	}

	ft.mu.Lock()
	defer ft.mu.Unlock()

	until, exists := ft.cooldowns[accountID]
	if !exists {
		return false, 0
	}

	remaining := time.Until(until)
	if remaining <= 0 {
		delete(ft.cooldowns, accountID)
		return false, 0
	}

	return true, remaining
}

// PickNextHealthyAccount 在账号池中挑选下一个未处于限流冷却期的健康账号。
func (ft *FailoverTracker) PickNextHealthyAccount(probe *variant.Probe, side variant.ID, failedAccountID string) (*Account, error) {
	list, err := ListAccounts(probe, side)
	if err != nil {
		return nil, fmt.Errorf("读取账号列表失败: %w", err)
	}

	if len(list) <= 1 {
		return nil, errors.New("账号池中无其他备用账号")
	}

	// 找到 failedAccountID 在列表中的索引，优先按列表环状顺延挑选
	startIndex := 0
	for i := range list {
		if list[i].ID == failedAccountID {
			startIndex = (i + 1) % len(list)
			break
		}
	}

	// 环状遍历寻找非冷却的健康账号
	var earliestCoolingAccount *Account
	var earliestCoolingDuration time.Duration

	now := time.Now()
	for i := 0; i < len(list); i++ {
		idx := (startIndex + i) % len(list)
		candidate := list[idx]
		if candidate.ID == failedAccountID {
			continue
		}

		ft.mu.RLock()
		until, inCooldown := ft.cooldowns[candidate.ID]
		ft.mu.RUnlock()

		if !inCooldown || now.After(until) {
			// 找到一个完全健康的备用账号！
			return &candidate, nil
		}

		rem := until.Sub(now)
		if earliestCoolingAccount == nil || rem < earliestCoolingDuration {
			earliestCoolingAccount = &candidate
			earliestCoolingDuration = rem
		}
	}

	// 全池账号均处于限流冷却中
	minRemText := fmt.Sprintf("%d 秒", int(earliestCoolingDuration.Seconds()))
	if earliestCoolingDuration.Minutes() >= 1 {
		minRemText = fmt.Sprintf("%.1f 分钟", earliestCoolingDuration.Minutes())
	}
	return nil, fmt.Errorf("全部 %d 个账号配额均已耗尽或限流，最早将于 %s 后恢复", len(list), minRemText)
}

// FailoverToNext 标记指定账号限流，并自动故障转移切换至下一个健康账号。
func (ft *FailoverTracker) FailoverToNext(probe *variant.Probe, side variant.ID, failedAccountID string, cooldown time.Duration) (*Account, error) {
	ft.ReportRateLimited(failedAccountID, cooldown)

	nextAcc, err := ft.PickNextHealthyAccount(probe, side, failedAccountID)
	if err != nil {
		return nil, err
	}

	// 原子激活新账号并写盘
	switched, err := SwitchAccount(probe, side, nextAcc.ID)
	if err != nil {
		return nil, fmt.Errorf("切换至新账号失败: %w", err)
	}

	return switched, nil
}

// ClearCooldownsForTest 供单元测试重置冷却状态。
func (ft *FailoverTracker) ClearCooldownsForTest() {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	ft.cooldowns = make(map[string]time.Time)
}
