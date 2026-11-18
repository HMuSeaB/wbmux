package main

import (
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
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
	text, _ := embedStatsText()
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

	text, level := embedStatsText()
	if !strings.Contains(text, "260k") {
		t.Errorf("期望包含 260k，得到 %q", text)
	}
	if level != "warn" {
		t.Errorf("260k 应为 warn 等级，得到 %q", level)
	}
}
