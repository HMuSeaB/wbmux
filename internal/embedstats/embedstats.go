// Package embedstats 是胶囊与嵌入面板共用的唯一口径：上下文告警阈值与
// 数据新鲜度分档。
//
// 之前阈值与 stale 时长在 cmd/wbmux 与 embed.html 里各写了一份硬编码，
// 改一处忘另一处就会漂移——这里是唯一来源，embed.html 侧由
// internal/webui 的 TestEmbedHTMLThresholds 锁死一致。
package embedstats

import "time"

// 上下文告警阈值：256k 转黄、512k 转红。1M 上下文的模型很容易悄悄
// 吃满然后被截断，用户毫无感知，所以阈值宁可早报不要晚报。
const (
	ContextWarnTokens = 256_000
	ContextBadTokens  = 512_000
)

// 新鲜度分档：30 分钟内是"现在"，30–120 分钟是"旧值（标多久前）"，
// 超过 120 分钟才算空闲。旧数据直接抹掉会丢信息，但把它当"现在"
// 展示会误导人——三档是两者的折中。
const (
	AgedAfter = 30 * time.Minute
	IdleAfter = 120 * time.Minute
)

// Freshness 是数据的时效档位。
type Freshness string

const (
	FreshnessFresh Freshness = "fresh" // 30 分钟内
	FreshnessAged  Freshness = "aged"  // 30–120 分钟：显示旧值 + 多久前
	FreshnessIdle  Freshness = "idle"  // 超过 120 分钟：只显示"空闲"
)

// Classify 按"最后一条记录距今多久"分档。
func Classify(age time.Duration) Freshness {
	switch {
	case age >= IdleAfter:
		return FreshnessIdle
	case age >= AgedAfter:
		return FreshnessAged
	default:
		return FreshnessFresh
	}
}

// Level 按上下文大小与新鲜度给告警等级。旧值不配报警：过期档强制
// 返回空等级，哪怕它当年曾经到过 512k。
func Level(promptTokens int, f Freshness) string {
	if f != FreshnessFresh {
		return ""
	}
	switch {
	case promptTokens >= ContextBadTokens:
		return "bad"
	case promptTokens >= ContextWarnTokens:
		return "warn"
	default:
		return ""
	}
}

// AgeMinutes 把时长压成"xx 分钟前"的分钟数（向下取整）。
func AgeMinutes(age time.Duration) int {
	return int(age / time.Minute)
}
