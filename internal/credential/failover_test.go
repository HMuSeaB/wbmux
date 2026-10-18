package credential

import (
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestFailoverTracker_Cooldown(t *testing.T) {
	ft := &FailoverTracker{
		cooldowns: make(map[string]time.Time),
	}

	accID := "acc_test_1"
	isCool, rem := ft.GetCooldown(accID)
	if isCool || rem > 0 {
		t.Fatalf("初始状态不应处于冷却期")
	}

	// 设定 50ms 冷却
	ft.ReportRateLimited(accID, 50*time.Millisecond)
	isCool, rem = ft.GetCooldown(accID)
	if !isCool || rem <= 0 {
		t.Fatalf("设定后应处于冷却期")
	}

	time.Sleep(60 * time.Millisecond)
	isCool, _ = ft.GetCooldown(accID)
	if isCool {
		t.Fatalf("超时后应自动解除冷却期")
	}
}

func TestFailoverTracker_PickNextAndFailover(t *testing.T) {
	tempDir := t.TempDir()
	restore := config.SetRoot(tempDir)
	defer restore()

	ft := &FailoverTracker{
		cooldowns: make(map[string]time.Time),
	}

	// 1. 只有一个账号时，无法故障转移
	acc1 := Account{
		ID:       "acc_1",
		UID:      "uid_1",
		Nickname: "账号1",
		Token:    "token_1",
		Active:   true,
	}
	_, err := SaveAccount(nil, variant.Intl, acc1, true)
	if err != nil {
		t.Fatalf("保存账号1失败: %v", err)
	}

	_, err = ft.PickNextHealthyAccount(nil, variant.Intl, "acc_1")
	if err == nil {
		t.Fatalf("单账号时应当报错无备用账号")
	}

	// 2. 添加第二个与第三个账号
	acc2 := Account{
		ID:       "acc_2",
		UID:      "uid_2",
		Nickname: "账号2",
		Token:    "token_2",
		Active:   false,
	}
	acc3 := Account{
		ID:       "acc_3",
		UID:      "uid_3",
		Nickname: "账号3",
		Token:    "token_3",
		Active:   false,
	}
	_, _ = SaveAccount(nil, variant.Intl, acc2, false)
	_, _ = SaveAccount(nil, variant.Intl, acc3, false)

	// 当 acc_1 限流时，应挑选 acc_2
	next, err := ft.PickNextHealthyAccount(nil, variant.Intl, "acc_1")
	if err != nil {
		t.Fatalf("挑选备用账号失败: %v", err)
	}
	if next.ID != "acc_2" {
		t.Errorf("期望挑选 acc_2，实际 %s", next.ID)
	}

	// 将 acc_2 标记冷却，应挑选 acc_3
	ft.ReportRateLimited("acc_2", 10*time.Minute)
	next, err = ft.PickNextHealthyAccount(nil, variant.Intl, "acc_1")
	if err != nil {
		t.Fatalf("挑选备用账号失败: %v", err)
	}
	if next.ID != "acc_3" {
		t.Errorf("期望挑选 acc_3，实际 %s", next.ID)
	}

	// 将 acc_3 也标记冷却，全部账号冷却
	ft.ReportRateLimited("acc_3", 10*time.Minute)
	_, err = ft.PickNextHealthyAccount(nil, variant.Intl, "acc_1")
	if err == nil {
		t.Fatalf("全部冷却时期望报错，实际无报错")
	}

	// 3. 测试 FailoverToNext：解除 acc_3 冷却，执行 Failover
	ft.mu.Lock()
	delete(ft.cooldowns, "acc_3")
	ft.mu.Unlock()

	switched, err := ft.FailoverToNext(nil, variant.Intl, "acc_1", 10*time.Minute)
	if err != nil {
		t.Fatalf("FailoverToNext 失败: %v", err)
	}
	if switched.ID != "acc_3" || !switched.Active {
		t.Errorf("期望切换并激活 acc_3，实际: %v", switched)
	}

	// 确认 acc_1 已进入冷却
	isCool, _ := ft.GetCooldown("acc_1")
	if !isCool {
		t.Errorf("acc_1 应处于冷却中")
	}
}
