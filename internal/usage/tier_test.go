package usage

import "testing"

// TestApplyTiers 钉住 2026-10-26 实测的官方结构：
// modelTiers 把需要更高订阅的模型单独列出，badge 为 "Pro Only"。
func TestApplyTiers(t *testing.T) {
	models := []LiveModel{
		{ID: "deepseek-v4.1-flash", FreeNow: true},
		{ID: "kimi-k3.1-pro", Rate: 0.97},
		{ID: "gpt-6.1-sol", Rate: 1.33},
		{ID: "gpt-6-sol", Rate: 1.33},
	}
	tiers := []liveTierRaw{{
		ID:      "gpt-pro-only",
		Enabled: true,
		ModelIDs: []string{
			"gpt-6-astra", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna",
		},
		Badge: struct {
			Label string `json:"label"`
		}{Label: "Pro Only"},
		Hover: struct {
			TextZh string `json:"textZh"`
			TextEn string `json:"textEn"`
		}{TextZh: "模型仅面向 Pro 用户开放。"},
	}}

	applyTiers(models, tiers)

	byID := map[string]LiveModel{}
	for _, m := range models {
		byID[m.ID] = m
	}
	for _, id := range []string{"gpt-6.1-sol", "gpt-6-sol"} {
		if !byID[id].Locked {
			t.Errorf("%s 应被标记为锁定", id)
		}
		if byID[id].TierBadge != "Pro Only" {
			t.Errorf("%s 徽章应为 Pro Only，得到 %q", id, byID[id].TierBadge)
		}
		if byID[id].TierHint == "" {
			t.Errorf("%s 应带上官方说明原文", id)
		}
	}
	for _, id := range []string{"deepseek-v4.1-flash", "kimi-k3.1-pro"} {
		if byID[id].Locked {
			t.Errorf("%s 不该被锁定", id)
		}
	}
}

// TestApplyTiersDisabled 档位活动关掉就等于放开，不能继续拦。
func TestApplyTiersDisabled(t *testing.T) {
	models := []LiveModel{{ID: "gpt-6-sol"}}
	applyTiers(models, []liveTierRaw{{
		ID:       "gpt-pro-only",
		Enabled:  false,
		ModelIDs: []string{"gpt-6-sol"},
	}})
	if models[0].Locked {
		t.Error("enabled=false 的档位不该标记锁定")
	}
}

// TestApplyTiersNoTiers 响应里没有 modelTiers（老版本或结构变化）时，
// 不能因此把所有模型当锁定——没有信息就按原样放行。
func TestApplyTiersNoTiers(t *testing.T) {
	models := []LiveModel{{ID: "gpt-6-sol", Rate: 1.33}}
	applyTiers(models, nil)
	if models[0].Locked {
		t.Error("没有档位信息时不该标记锁定")
	}
}
