package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
)

// embedStatsText 生成胶囊上那行指标文字与告警等级。
//
// 格式：`187k · 212/s · 今日 42M`
//
// 为什么上下文放在最前面：速率只是"这次快不快"，上下文才是"会不会出事"。
// 1M 上下文的模型很容易悄悄吃满然后被截断，用户毫无感知——所以
// 256k 起转黄、512k 转红，让它变成一眼可见的信号。
//
// 数据是请求级快照：tok/s 表示上一次请求首字之后的平均生成速度，
// 不是此刻正在流的实时值（后者要改 SSE 转发链路）。
func embedStatsText() (string, string) {
	sum := gatewayaudit.Default().GetSummary(1)
	if len(sum.RecentRecords) == 0 {
		return "暂无请求", ""
	}
	rec := sum.RecentRecords[0]

	// 超过 30 分钟没有新请求，上下文数字也一起失效：它属于上一次会话，
	// 跟眼前这个窗口可能毫无关系。这时候只说"空闲"，不摆过期数据。
	//
	// 已知局限：用户切到客户端自带模型时，请求不经过本机网关，
	// 这里看到的就是"最后一次代理请求"。真正的识别需要读 DOM 上的
	// 当前模型名（见 docs/INJECT.md 的后续计划）。
	if time.Since(rec.Timestamp) >= 30*time.Minute {
		return "空闲", ""
	}

	parts := []string{compactTokens(rec.PromptTokens)}
	if rec.IsStream {
		if rate := genRate(rec.CompletionTokens, rec.DurationMs, rec.TTFTMs); rate > 0 {
			parts = append(parts, fmt.Sprintf("%.0f/s", rate))
		}
	} else if rec.TTFTMs > 0 {
		parts = append(parts, fmt.Sprintf("首字 %.1fs", float64(rec.TTFTMs)/1000))
	}

	// 今日用量为 0 时整段省掉——"今日 0"只会让顶栏更吵。
	today := time.Now().Format("2006-01-02")
	for _, d := range sum.DailyStats {
		if d.Date == today {
			if d.TotalTokens > 0 {
				parts = append(parts, "今日 "+compactTokens(d.TotalTokens))
			}
			break
		}
	}

	level := ""
	switch {
	case rec.PromptTokens >= 512_000:
		level = "bad"
	case rec.PromptTokens >= 256_000:
		level = "warn"
	}
	return strings.Join(parts, " · "), level
}

// genRate 计算纯生成阶段的 token 速率。
//
// 少于 20 个 token、或生成阶段不足 200ms 的样本噪声太大，直接归零，
// 让胶囊显示别的指标而不是一个假的"300/s"。
func genRate(comp int, durMs, ttftMs int64) float64 {
	genMs := durMs - ttftMs
	if comp < 20 || genMs < 200 {
		return 0
	}
	return float64(comp) / float64(genMs) * 1000
}

// compactTokens 把 token 数压成适合顶栏的短格式。
func compactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%.0fk", float64(n)/1_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
