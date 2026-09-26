package usage

import (
	"strconv"
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
