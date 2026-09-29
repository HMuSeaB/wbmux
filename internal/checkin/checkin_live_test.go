package checkin

import (
	"os"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// 本文件里的用例默认**跳过**。用法：
//
//	WBMUX_LIVE_PROBE=1 go test ./internal/checkin/ -run Live -v
//
// # 为什么查询可以随便跑，领取要另开一个开关
//
// Query 打的是 checkin-activity-status，语义上是查询、重复调用无副作用，
// 所以跟着 WBMUX_LIVE_PROBE 一起跑没问题。
//
// Claim 打的是 daily-checkin，**它是写操作**。虽然接口幂等（已签过不会多领），
// 但"跑个测试顺便动了账号状态"这种事不该由默认开关决定，因此单独一个更明确的
// 环境变量守着：
//
//	WBMUX_LIVE_PROBE=1 WBMUX_LIVE_CLAIM=1 go test ./internal/checkin/ -run LiveClaim -v
//
// 在有登录态的机器上，这条用例验证的是"已签到"那条分支——它必须被翻译成
// 正常结果而不是错误，这是整套早晚各跑一次用法的前提。

func liveDeps(t *testing.T) Deps {
	t.Helper()
	p := variant.DefaultProbe()
	// 先确认本机真能解开凭据，否则后面的失败会在别处冒出来，看不清主因。
	credential.ClearCache()
	if _, err := credential.Resolve(p, variant.CN); err != nil {
		t.Skipf("本机没有可用的国内侧登录态：%v", err)
	}
	return Deps{Probe: p}
}

func TestLiveQueryCN(t *testing.T) {
	if os.Getenv("WBMUX_LIVE_PROBE") != "1" {
		t.Skip("联调用例：设 WBMUX_LIVE_PROBE=1 才跑")
	}
	d := liveDeps(t)

	st, err := Query(d, variant.CN)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !st.Active {
		t.Fatalf("国内侧应当开着签到活动: %+v", st)
	}
	if st.DailyCredit <= 0 {
		t.Fatalf("每日积分应当为正: %+v", st)
	}
	if st.TodayCheckedIn && st.StreakDays <= 0 {
		t.Fatalf("今天已签但连签天数为 0，字段可能读错了: %+v", st)
	}
	// 只报读数，不涉及任何账号标识。
	t.Logf("国内侧：active=%v 今日已签=%v 连签=%d 天 每日=%d 今日=%d 本周=%d",
		st.Active, st.TodayCheckedIn, st.StreakDays, st.DailyCredit, st.TodayCredit, st.WeekCheckinDays)
}

func TestLiveQueryIntlReportsInactive(t *testing.T) {
	if os.Getenv("WBMUX_LIVE_PROBE") != "1" {
		t.Skip("联调用例：设 WBMUX_LIVE_PROBE=1 才跑")
	}
	p := variant.DefaultProbe()
	if _, err := credential.Resolve(p, variant.Intl); err != nil {
		t.Skipf("本机没有可用的国际侧登录态：%v", err)
	}

	// 这条是给"界面别给国际侧摆一个永远空的签到页"提供依据的：
	// 国际侧接口在、活动没开。
	st, err := Query(Deps{Probe: p}, variant.Intl)
	if err != nil {
		t.Fatalf("Query(intl): %v", err)
	}
	if st.Active {
		t.Logf("注意：国际侧现在 active=true，界面可以给国际侧也放签到入口了")
	} else {
		t.Logf("国际侧确认没开签到活动（active=false），界面只该给国内侧入口")
	}
}

func TestLiveClaimIsIdempotent(t *testing.T) {
	if os.Getenv("WBMUX_LIVE_PROBE") != "1" || os.Getenv("WBMUX_LIVE_CLAIM") != "1" {
		t.Skip("写操作联调：需同时设 WBMUX_LIVE_PROBE=1 与 WBMUX_LIVE_CLAIM=1")
	}
	d := liveDeps(t)

	before, err := Query(d, variant.CN)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	res, err := Claim(d, variant.CN)
	if err != nil {
		// 已签过的账号上这里报错，就说明"幂等"没被正确翻译——这正是要验的。
		t.Fatalf("领取不该报错（已签过应视为正常结果）: %v", err)
	}
	if before.TodayCheckedIn && !res.AlreadyCheckedIn {
		t.Fatalf("领取前已签，结果却没标成已签过: %+v", res)
	}
	// 无论哪条分支，连签天数都不该因为多跑一次而倒退。
	if res.Status != nil && res.Status.StreakDays < before.StreakDays {
		t.Fatalf("连签天数倒退了：%d → %d", before.StreakDays, res.Status.StreakDays)
	}
	t.Logf("领取结果：已签过=%v 本次积分=%d 原话=%q", res.AlreadyCheckedIn, res.Credit, res.Message)
}
