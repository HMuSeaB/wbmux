package usage

import (
	"testing"
	"time"
)

func TestParsePackageMeta(t *testing.T) {
	fixedTime := time.Date(2026, 10, 18, 12, 0, 0, 0, time.Local)

	subPkg := &LivePackage{
		Code:   "TCACA_code_035_ArVxJcGDsm",
		Total:  100,
		Remain: 100,
		Unit:   "credits",
	}
	ParsePackageMeta(subPkg, fixedTime)
	if subPkg.Kind != "subscription" {
		t.Errorf("expected subscription, got %q", subPkg.Kind)
	}
	if subPkg.DeductPriority != 1 {
		t.Errorf("expected priority 1, got %d", subPkg.DeductPriority)
	}
	if subPkg.KindZh != "月度订阅基础包" {
		t.Errorf("unexpected KindZh: %q", subPkg.KindZh)
	}
	// 月度订阅包必须带上当月自然月周期：面板 pace 靠这两个字段才算得出来，
	// 缺了就会静默退化（2026-11-22 实测 pace 从未出现，根因就是这里没填）。
	if got, want := subPkg.CycleStartTime, "2026-10-01 00:00:00"; got != want {
		t.Errorf("CycleStartTime = %q, want %q", got, want)
	}
	if got, want := subPkg.CycleEndTime, "2026-10-31 23:59:59"; got != want {
		t.Errorf("CycleEndTime = %q, want %q", got, want)
	}
	if subPkg.ExpiresAtMs != time.Date(2026, 10, 31, 23, 59, 59, 0, time.Local).UnixMilli() {
		t.Errorf("ExpiresAtMs 应与 CycleEndTime 同一时刻，得到 %d", subPkg.ExpiresAtMs)
	}

	bonusPkg := &LivePackage{
		Code:   "TCACA_code_007_nzdH5h4Nl0",
		Total:  60,
		Remain: 59.95,
		Unit:   "credit",
	}
	ParsePackageMeta(bonusPkg, fixedTime)
	if bonusPkg.Kind != "bonus" {
		t.Errorf("expected bonus, got %q", bonusPkg.Kind)
	}
	if bonusPkg.DeductPriority != 2 {
		t.Errorf("expected priority 2, got %d", bonusPkg.DeductPriority)
	}
	if bonusPkg.KindZh != "活动/赠送加量包" {
		t.Errorf("unexpected KindZh: %q", bonusPkg.KindZh)
	}
	// 赠送包没有周期概念，不能凭空给起止（给了会让 pace 用错周期）。
	if bonusPkg.CycleStartTime != "" || bonusPkg.CycleEndTime != "" {
		t.Errorf("bonus 包不应带周期起止，得到 %q ~ %q", bonusPkg.CycleStartTime, bonusPkg.CycleEndTime)
	}

	// 官方真的给了周期时优先用官方的，不被自然月推算覆盖。
	officialPkg := &LivePackage{
		Code:           "TCACA_code_035_official",
		CycleStartTime: "2026-10-18 10:00:00",
		CycleEndTime:   "2026-11-18 10:00:00",
	}
	ParsePackageMeta(officialPkg, fixedTime)
	if officialPkg.CycleStartTime != "2026-10-18 10:00:00" || officialPkg.CycleEndTime != "2026-11-18 10:00:00" {
		t.Errorf("官方周期应被保留，得到 %q ~ %q", officialPkg.CycleStartTime, officialPkg.CycleEndTime)
	}
	if want := time.Date(2026, 11, 18, 10, 0, 0, 0, time.Local).UnixMilli(); officialPkg.ExpiresAtMs != want {
		t.Errorf("ExpiresAtMs 应跟官方 CycleEndTime 一致（%d），得到 %d", want, officialPkg.ExpiresAtMs)
	}

	// 验证已有官方精确到期时间时不被覆盖
	exactTimePkg := &LivePackage{
		Code:        "TCACA_code_007_exact",
		ExpiresAtMs: 1793043227000,
	}
	ParsePackageMeta(exactTimePkg, fixedTime)
	if exactTimePkg.ExpiresAtMs != 1793043227000 {
		t.Errorf("expected ExpiresAtMs to be preserved, got %d", exactTimePkg.ExpiresAtMs)
	}
}
