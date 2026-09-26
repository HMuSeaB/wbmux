package usage

import (
	"strconv"
	"strings"
	"testing"
)

// 原文来自真实 product.json 的 credits 字段；解析错了界面会显示错倍率，
// 用户会按错的倍率算消耗，所以单独钉住。
func TestParseRateRules(t *testing.T) {
	cases := []struct {
		raw  string
		want float64
	}{
		{"x2.00 credits", 2.0},
		{"x0.16 credits", 0.16},
		{"x0.00 credits", 0.0}, // 免费档也是有效倍率，不能当解析失败
		{"x10.00 credits", 10.0},
		{"x0.57credits", 0.57}, // 实测也可能没空格
	}
	for _, c := range cases {
		m := creditsRateRe.FindStringSubmatch(c.raw)
		if m == nil {
			t.Errorf("%q 应能解析出倍率", c.raw)
			continue
		}
		got, err := strconv.ParseFloat(m[1], 64)
		if err != nil || got != c.want {
			t.Errorf("%q：得到 %v（err=%v），期望 %v", c.raw, got, err, c.want)
		}
	}
}

// TestPickProbeModel 钉住测试模型的挑选顺序：限时免费 > 0 倍率 > 最低倍率。
// 顺序错了会把用户的积分花在测试上——测试的目的是验证归属，不是消费。
func TestPickProbeModel(t *testing.T) {
	mk := func(id string, rate float64, free bool) LiveModel {
		return LiveModel{ID: id, Rate: rate, FreeNow: free}
	}
	cases := []struct {
		name string
		in   []LiveModel
		want string
	}{
		{"限时免费优先，哪怕倍率最高", []LiveModel{mk("a", 5, false), mk("b", 0.11, true)}, "b"},
		{"没有免费则选 0 倍率", []LiveModel{mk("a", 5, false), mk("b", 0, false), mk("c", 0.06, false)}, "b"},
		{"再退最低正倍率", []LiveModel{mk("a", 5, false), mk("c", 0.06, false)}, "c"},
	}
	for _, c := range cases {
		if got := pickProbeModel(c.in); got != c.want {
			t.Errorf("%s：得到 %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestPackagesDelta 钉住账单差值：只有 Used 变化的包才报告。
func TestPackagesDelta(t *testing.T) {
	before := []LivePackage{{Code: "AAA_123456", Used: 250, Remain: 0, Total: 250}}
	if got := packagesDelta(before, []LivePackage{{Code: "AAA_123456", Used: 250, Remain: 0, Total: 250}}); got != "" {
		t.Errorf("无变化应返回空串，得到 %q", got)
	}
	after := []LivePackage{{Code: "AAA_123456", Used: 250.11, Remain: 0, Total: 250}}
	got := packagesDelta(before, after)
	if got == "" || !strings.Contains(got, "250→250.11") {
		t.Errorf("应报告用量变化，得到 %q", got)
	}
}
