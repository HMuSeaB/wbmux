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
	text, _ := embedStatsText(nil)
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

	text, level := embedStatsText(nil)
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
	text, level := embedStatsText(lookup)
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
	text, level := embedStatsText(lookup)
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
	text, level := embedStatsText(func(string) *webui.ProxyQuota { return nil })
	if !strings.Contains(text, "260k") || level != "warn" {
		t.Errorf("无额度数据时应保持原样，得到 %q/%q", text, level)
	}
}
