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
}
