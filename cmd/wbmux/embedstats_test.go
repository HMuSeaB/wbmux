package main

import (
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/webui"
)

func TestCompactTokens(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{500, "500"},
		{1500, "1.5k"},
		{10000, "10k"},
		{408295, "408k"},
		{1200000, "1.20M"},
	}
	for _, c := range cases {
		if got := compactTokens(c.n); got != c.want {
			t.Errorf("compactTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestEmbedStatsTextEmpty(t *testing.T) {
	// 默认没有请求时，返回暂无请求或基于本地会话
	text, _ := embedStatsText(nil, nil)
	if text == "" {
		t.Error("embedStatsText 不应返回空字符串")
	}
}

func TestGenRate(t *testing.T) {
	// 少量 token 忽略
	if r := genRate(10, 1000, 100); r != 0 {
		t.Errorf("token < 20 时应返回 0，得到 %f", r)
	}
	// 正常流速计算
	r := genRate(100, 1000, 500) // genMs = 500ms, 100 tokens -> 200 tok/s
	if r != 200 {
		t.Errorf("genRate 期望 200，得到 %f", r)
	}
}

func TestEmbedStatsTextGatewayPriority(t *testing.T) {
	// 往网关记录塞一条最近数据
	now := time.Now()
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "deepseek-v4.1-flash",
		PromptTokens:     260000,
		CompletionTokens: 50,
		TotalTokens:      260050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        now,
	})

	text, level := embedStatsText(nil, nil)
	if !strings.Contains(text, "260k") {
		t.Errorf("期望包含 260k，得到 %q", text)
	}
	if level != "warn" {
		t.Errorf("260k 应为 warn 等级，得到 %q", level)
	}
}

func TestQuotaCapsuleText(t *testing.T) {
	// rate_limited：短模型名 + 恢复时刻，level=warn
	text, level, ok := quotaCapsuleText("hy4-preview-f", &webui.ProxyQuota{
		Model:     "hy4-preview-f",
		State:     "rate_limited",
		UntilText: "2026-09-29 22:18:43 UTC+8",
	})
	if !ok || text != "hy4-preview-f 限流 22:18 恢复" || level != "warn" {
		t.Errorf("限流文案应为 hy4-preview-f 限流 22:18 恢复/warn，得到 %q/%q/ok=%v", text, level, ok)
	}

	// 没给恢复时刻也不编时间
	text, level, ok = quotaCapsuleText("hy4-preview-f", &webui.ProxyQuota{
		Model: "hy4-preview-f",
		State: "rate_limited",
	})
	if !ok || text != "hy4-preview-f 限流" || level != "warn" {
		t.Errorf("无恢复时刻应为 hy4-preview-f 限流/warn，得到 %q/%q/ok=%v", text, level, ok)
	}

	// exhausted：固定文案，level=bad
	text, level, ok = quotaCapsuleText("deepseek-v4.1-flash", &webui.ProxyQuota{
		Model: "deepseek-v4.1-flash",
		State: "exhausted",
	})
	if !ok || text != "额度用尽" || level != "bad" {
		t.Errorf("用尽文案应为 额度用尽/bad，得到 %q/%q/ok=%v", text, level, ok)
	}

	// upstream_error 与 nil 都不改写
	if _, _, ok := quotaCapsuleText("m", &webui.ProxyQuota{State: "upstream_error"}); ok {
		t.Error("上游故障不应改写胶囊")
	}
	if _, _, ok := quotaCapsuleText("m", nil); ok {
		t.Error("nil 不应改写胶囊")
	}
}

func TestQuotaResetHM(t *testing.T) {
	// UntilMs 优先：按本地时区取 HH:MM
	ms := time.Date(2026, 9, 29, 22, 18, 43, 0, time.Local).UnixMilli()
	if got := quotaResetHM(&webui.ProxyQuota{UntilMs: ms}); got != "22:18" {
		t.Errorf("UntilMs 应解析为 22:18，得到 %q", got)
	}
	// 没有 UntilMs 时从 UntilText 原文里抓
	if got := quotaResetHM(&webui.ProxyQuota{UntilText: "2026-09-29 22:18:43 UTC+8"}); got != "22:18" {
		t.Errorf("UntilText 应解析为 22:18，得到 %q", got)
	}
	if got := quotaResetHM(&webui.ProxyQuota{}); got != "" {
		t.Errorf("都没有时应返回空，得到 %q", got)
	}
	if got := quotaResetHM(nil); got != "" {
		t.Errorf("nil 应返回空，得到 %q", got)
	}
}

func TestEmbedStatsTextQuotaLimited(t *testing.T) {
	now := time.Now()
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "hy4-preview-f",
		PromptTokens:     260000,
		CompletionTokens: 50,
		TotalTokens:      260050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        now,
	})
	lookup := func(model string) *webui.ProxyQuota {
		if model == "hy4-preview-f" {
			return &webui.ProxyQuota{
				Model:     "hy4-preview-f",
				State:     "rate_limited",
				UntilText: "2026-09-29 22:18:43 UTC+8",
			}
		}
		return nil
	}
	text, level := embedStatsText(lookup, nil)
	if text != "hy4-preview-f 限流 22:18 恢复" || level != "warn" {
		t.Errorf("限流时胶囊应改写，得到 %q/%q", text, level)
	}
}

func TestEmbedStatsTextQuotaExhausted(t *testing.T) {
	now := time.Now()
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "deepseek-v4.1-flash",
		PromptTokens:     100000,
		CompletionTokens: 50,
		TotalTokens:      100050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        now,
	})
	lookup := func(model string) *webui.ProxyQuota {
		if model == "deepseek-v4.1-flash" {
			return &webui.ProxyQuota{Model: "deepseek-v4.1-flash", State: "exhausted"}
		}
		return nil
	}
	text, level := embedStatsText(lookup, nil)
	if text != "额度用尽" || level != "bad" {
		t.Errorf("用尽时胶囊应为 额度用尽/bad，得到 %q/%q", text, level)
	}
}

func TestEmbedStatsTextQuotaLookupMiss(t *testing.T) {
	// 查不到限流（lookup 返回 nil）时胶囊一个字不改
	now := time.Now()
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "deepseek-v4.1-flash",
		PromptTokens:     260000,
		CompletionTokens: 50,
		TotalTokens:      260050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        now,
	})
	text, level := embedStatsText(func(string) *webui.ProxyQuota { return nil }, nil)
	if !strings.Contains(text, "260k") || level != "warn" {
		t.Errorf("无额度数据时应保持原样，得到 %q/%q", text, level)
	}
}

// seedCapsuleRecord 往网关账本塞一条"现在"的流式记录，让胶囊走新鲜态。
// 各常驻段测试共用：调用后 embedStatsText 的基础文案是 "100k · 62/s · 今日 …"。
func seedCapsuleRecord(t *testing.T) {
	t.Helper()
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "deepseek-v4.1-flash",
		PromptTokens:     100000,
		CompletionTokens: 50,
		TotalTokens:      100050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        time.Now(),
	})
}

// TestCapsuleQuotaText 钉住常驻段的拼法：两边都有压成一段，
// 只有一边只显示那一边，没数据返回 ""，未知档位跳过不编。
func TestCapsuleQuotaText(t *testing.T) {
	cases := []struct {
		in   []webui.CapsulePackage
		want string
	}{
		{nil, ""},
		{[]webui.CapsulePackage{}, ""},
		{[]webui.CapsulePackage{{Side: "cn", Percent: 62}}, "国62%"},
		{[]webui.CapsulePackage{{Side: "intl", Percent: 41}}, "际41%"},
		{[]webui.CapsulePackage{{Side: "cn", Percent: 62}, {Side: "intl", Percent: 41}}, "国62%·际41%"},
		{[]webui.CapsulePackage{{Side: "xx", Percent: 50}}, ""},
	}
	for _, c := range cases {
		if got := capsuleQuotaText(c.in); got != c.want {
			t.Errorf("capsuleQuotaText(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEmbedStatsTextCapsuleQuotaBoth 两边有数据时胶囊末尾多一段
// "国62%·际41%"，且是最后一段。
func TestEmbedStatsTextCapsuleQuotaBoth(t *testing.T) {
	seedCapsuleRecord(t)
	pkgs := func() []webui.CapsulePackage {
		return []webui.CapsulePackage{{Side: "cn", Percent: 62}, {Side: "intl", Percent: 41}}
	}
	text, _ := embedStatsText(nil, pkgs)
	if !strings.Contains(text, "100k") {
		t.Errorf("基础文案应含 100k，得到 %q", text)
	}
	if !strings.HasSuffix(text, "国62%·际41%") {
		t.Errorf("两边有数据时胶囊末尾应为 国62%%·际41%%，得到 %q", text)
	}
}

// TestEmbedStatsTextCapsuleQuotaOneSide 只有一边有数据时只显示那一边。
func TestEmbedStatsTextCapsuleQuotaOneSide(t *testing.T) {
	seedCapsuleRecord(t)
	text, _ := embedStatsText(nil, func() []webui.CapsulePackage {
		return []webui.CapsulePackage{{Side: "cn", Percent: 62}}
	})
	if !strings.Contains(text, "国62%") || strings.Contains(text, "际") {
		t.Errorf("只有国内档时应只显示 国62%%，得到 %q", text)
	}
}

// TestEmbedStatsTextCapsuleQuotaNoData 没数据时文案与原来一模一样：
// 同一份账本状态下，回调返回空与回调为 nil 的两次调用输出完全相同，
// 且都不含额度段。
func TestEmbedStatsTextCapsuleQuotaNoData(t *testing.T) {
	seedCapsuleRecord(t)
	a, levelA := embedStatsText(nil, nil)
	b, levelB := embedStatsText(nil, func() []webui.CapsulePackage { return nil })
	if a != b || levelA != levelB {
		t.Errorf("没数据时两次调用应完全相同，得到 %q/%q 与 %q/%q", a, levelA, b, levelB)
	}
	if strings.Contains(a, "国") || strings.Contains(a, "际") {
		t.Errorf("没数据时不应出现额度段，得到 %q", a)
	}
}

// TestEmbedStatsTextCapsuleQuotaLimited 限流改写仍优先：常驻段不出现。
func TestEmbedStatsTextCapsuleQuotaLimited(t *testing.T) {
	now := time.Now()
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "hy4-preview-f",
		PromptTokens:     260000,
		CompletionTokens: 50,
		TotalTokens:      260050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        now,
	})
	lookup := func(model string) *webui.ProxyQuota {
		if model == "hy4-preview-f" {
			return &webui.ProxyQuota{
				Model:     "hy4-preview-f",
				State:     "rate_limited",
				UntilText: "2026-09-29 22:18:43 UTC+8",
			}
		}
		return nil
	}
	pkgs := func() []webui.CapsulePackage {
		return []webui.CapsulePackage{{Side: "cn", Percent: 62}, {Side: "intl", Percent: 41}}
	}
	text, level := embedStatsText(lookup, pkgs)
	if text != "hy4-preview-f 限流 22:18 恢复" || level != "warn" {
		t.Errorf("限流时胶囊应改写且不带常驻段，得到 %q/%q", text, level)
	}
}

// TestEmbedStatsTextCapsuleQuotaExhausted 用尽改写仍优先：常驻段不出现。
func TestEmbedStatsTextCapsuleQuotaExhausted(t *testing.T) {
	now := time.Now()
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "deepseek-v4.1-flash",
		PromptTokens:     100000,
		CompletionTokens: 50,
		TotalTokens:      100050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        now,
	})
	lookup := func(model string) *webui.ProxyQuota {
		if model == "deepseek-v4.1-flash" {
			return &webui.ProxyQuota{Model: "deepseek-v4.1-flash", State: "exhausted"}
		}
		return nil
	}
	pkgs := func() []webui.CapsulePackage {
		return []webui.CapsulePackage{{Side: "cn", Percent: 62}}
	}
	text, level := embedStatsText(lookup, pkgs)
	if text != "额度用尽" || level != "bad" {
		t.Errorf("用尽时胶囊应为 额度用尽/bad 且不带常驻段，得到 %q/%q", text, level)
	}
}
