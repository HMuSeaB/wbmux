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
		pkg.ExpiresAtMs = monthEnd.UnixMilli()
		return
	}

	// 默认为活动/新用户赠送加量包（TCACA_code_007、017、021 等）
	pkg.Kind = "bonus"
	pkg.KindZh = "活动/赠送加量包"
	pkg.ExpiryRuleZh = "发放起 30 天有效（到期清零）"
	pkg.DeductPriority = 2
	// 若无具体订单创建时间，以当月末/30天为保守预估窗口
	thirtyDaysLater := now.Add(30 * 24 * time.Hour)
	pkg.ExpiresAtMs = thirtyDaysLater.UnixMilli()
}

// EnhancePackagesWithMeta 遍历积分包列表并赋予元数据解析。
func EnhancePackagesWithMeta(packages []LivePackage) []LivePackage {
	now := time.Now()
	for i := range packages {
		ParsePackageMeta(&packages[i], now)
	}
	return packages
}
