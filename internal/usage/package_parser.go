package usage

import (
	"strings"
	"time"
)

// ParsePackageMeta 根据官方 PackageCode 及容量特征，解析其生命周期、包类型与失效规则。
// 规则依据官方前端计费定义与实测行为：
// 1. 月度订阅/体验基础包（如 TCACA_code_035_*）：按自然月周期重置，扣费优先级最高；
// 2. 活动/加量赠送包（如 TCACA_code_007_* / 017_* / 021_*）：自发放日起 30 天有效，到期清零；
// 3. 官方扣费策略：优先消耗基础月度包，次按到期时间优先扣除临期的活动赠送包。
func ParsePackageMeta(pkg *LivePackage, now time.Time) {
	if pkg == nil {
		return
	}
	code := strings.ToUpper(pkg.Code)

	// 月末 23:59:59 计算（当月自然月重置时刻）
	year, month, _ := now.Date()
	nextMonthFirst := time.Date(year, month+1, 1, 0, 0, 0, 0, now.Location())
	monthEnd := nextMonthFirst.Add(-time.Second)

	// 判断是否为月度基础订阅包
	// 常见特征：TCACA_code_035（Free 月度包 100 credits）、001/002（订阅包）
	if strings.Contains(code, "CODE_035") || strings.Contains(code, "SUBSCRIPTION") || strings.Contains(code, "FREE") {
		pkg.Kind = "subscription"
		pkg.KindZh = "月度订阅基础包"
		pkg.ExpiryRuleZh = "每月 1 日重置（月底清零更新）"
		pkg.DeductPriority = 1
		// 面板的 pace（剩余% 对 剩余时间%）要周期起止都齐全才算，
		// 而官方明细接口实测对 TCACA_code_035 并不返回 CycleStartTime
		// （见 live.go 的 fetchPackageDetailsMap），旧代码只在这里填
		// ExpiresAtMs，起点永远空着 → quotaCycle 恒 false、pace 永不出现。
		//
		// 这里按上面同一条官方规则「每月 1 日重置」取当月自然月：
		// 起点 = 当月 1 日 00:00:00，终点 = 当月最后一天 23:59:59。
		// 与 ExpiresAtMs 用的是同一个 monthEnd，没有引入新假设。
		// 若将来明细接口真的给了官方周期，已有值优先，不覆盖。
		monthStart := time.Date(year, month, 1, 0, 0, 0, 0, now.Location())
		if pkg.CycleStartTime == "" {
			pkg.CycleStartTime = monthStart.Format("2006-01-02 15:04:05")
		}
		if pkg.CycleEndTime == "" {
			pkg.CycleEndTime = monthEnd.Format("2006-01-02 15:04:05")
		}
		// 到期时刻跟周期终点保持一致：官方给了周期就用官方的终点，
		// 没给（常见情况）才退回当月月底——两者不能各说一套。
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", pkg.CycleEndTime, time.Local); err == nil {
			pkg.ExpiresAtMs = t.UnixMilli()
		} else {
			pkg.ExpiresAtMs = monthEnd.UnixMilli()
		}
		return
	}

	// 默认为活动/新用户赠送加量包（TCACA_code_007、017、021 等）
	pkg.Kind = "bonus"
	pkg.KindZh = "活动/赠送加量包"
	pkg.ExpiryRuleZh = "发放起 30 天有效（到期清零）"
	pkg.DeductPriority = 2
	// 若无官方明细接口返回的真实到期时间，才以 30 天为保守预估窗口
	if pkg.ExpiresAtMs == 0 {
		thirtyDaysLater := now.Add(30 * 24 * time.Hour)
		pkg.ExpiresAtMs = thirtyDaysLater.UnixMilli()
	}
}

// EnhancePackagesWithMeta 遍历积分包列表并赋予元数据解析。
func EnhancePackagesWithMeta(packages []LivePackage) []LivePackage {
	now := time.Now()
	for i := range packages {
		ParsePackageMeta(&packages[i], now)
	}
	return packages
}
