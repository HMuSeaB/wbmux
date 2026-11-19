package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/embedstats"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/sessiontelemetry"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// embedStatsText 生成胶囊上那行指标文字与告警等级。
//
// 格式：`187k · 212/s · 今日 42M` 或 `412k · 混元 · 今日 42M`
//
// 为什么上下文放在最前面：速率只是"这次快不快"，上下文才是"会不会出事"。
// 1M 上下文的模型很容易悄悄吃满然后被截断，用户毫无感知——所以
// 256k 起转黄、512k 转红，让它变成一眼可见的信号。阈值与 stale 分档
// 的唯一口径在 internal/embedstats，别在这里另写一份。
//
// 双源智能融合（借鉴 chuanban 增量遥测架构）：
//   - 经过网关的代理请求（如国际限免模型）：可获得精确的流式速率；
//   - 国内自带直连模型（不经网关）：通过 sessiontelemetry 监听本地活跃
//     会话日志的 rawUsage，O(1) 提取上下文 tokens 与模型名，消灭统计盲区。
func embedStatsText() (string, string) {
	sum := gatewayaudit.Default().GetSummary(1)
	var gwRec *gatewayaudit.Record
	if len(sum.RecentRecords) > 0 {
		gwRec = &sum.RecentRecords[0]
	}

	// 读取本地会话日志中最近的交互
	sessMetric := sessiontelemetry.GetLatestSessionUsage(variant.DefaultProbe())

	// 判定哪个更新：若本地会话记录晚于网关记录，说明当前在用国内自带模型直连
	useLocal := false
	if sessMetric != nil && !sessMetric.Timestamp.IsZero() {
		if gwRec == nil {
			useLocal = true
		} else if sessMetric.Timestamp.After(gwRec.Timestamp.Add(2 * time.Second)) {
			useLocal = true
		}
	}

	if gwRec == nil && !useLocal {
		return "暂无请求", ""
	}

	var promptTokens int
	var timestamp time.Time
	var midPart string

	if useLocal {
		promptTokens = sessMetric.PromptTokens
		timestamp = sessMetric.Timestamp
		modelName := sessMetric.Model
		if modelName == "" {
			modelName = "国内自带"
		}
		midPart = cleanCapsuleModel(modelName)
	} else {
		promptTokens = gwRec.PromptTokens
		timestamp = gwRec.Timestamp
		if gwRec.IsStream {
			if rate := genRate(gwRec.CompletionTokens, gwRec.DurationMs, gwRec.TTFTMs); rate > 0 {
				midPart = fmt.Sprintf("%.0f/s", rate)
			}
		} else if gwRec.TTFTMs > 0 {
			midPart = fmt.Sprintf("首字 %.1fs", float64(gwRec.TTFTMs)/1000)
		}
	}

	// 新鲜度三档：30 分钟内正常显示；30–120 分钟显示旧值 + 多久前，
	// 不着色；超过 120 分钟才显示"空闲"。旧数据直接抹掉会丢掉"刚才
	// 上下文多大"这个最有用的信息，但把它当"现在"展示会误导人。
	freshness := embedstats.Classify(time.Since(timestamp))
	switch freshness {
	case embedstats.FreshnessIdle:
		return "空闲", ""
	case embedstats.FreshnessAged:
		return fmt.Sprintf("%s · %dm 前", compactTokens(promptTokens), embedstats.AgeMinutes(time.Since(timestamp))), ""
	}

	parts := []string{compactTokens(promptTokens)}
	if midPart != "" {
		parts = append(parts, midPart)
	}

	// 今日用量为 0 时整段省掉——"今日 0"只会让顶栏更吵。
	today := time.Now().Format("2006-01-02")
	todayTokens := 0
	for _, d := range sum.DailyStats {
		if d.Date == today {
			todayTokens = d.TotalTokens
			break
		}
	}
	if useLocal && timestamp.Format("2006-01-02") == today {
		todayTokens += sessMetric.TotalTokens
	}
	if todayTokens > 0 {
		parts = append(parts, "今日 "+compactTokens(todayTokens))
	}

	level := embedstats.Level(promptTokens, freshness)
	return strings.Join(parts, " · "), level
}

// cleanCapsuleModel 紧凑规范化顶栏胶囊上的模型名称，避免冗长厂商前缀或区服后缀挤压排版。
func cleanCapsuleModel(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, "-sg")
	name = strings.TrimSuffix(name, "-sg2")
	name = strings.TrimSuffix(name, "-latest")
	if len(name) > 16 {
		name = name[:14] + ".."
	}
	return name
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
