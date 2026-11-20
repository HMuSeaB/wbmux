// Package embedstats 是胶囊与嵌入面板共用的唯一口径：上下文告警阈值与
// 数据新鲜度分档。
//
// 之前阈值与 stale 时长在 cmd/wbmux 与 embed.html 里各写了一份硬编码，
// 改一处忘另一处就会漂移——这里是唯一来源，embed.html 侧由
// internal/webui 的 TestEmbedHTMLThresholds 锁死一致。
package embedstats

import (
	"sync"
	"time"
)

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

// LastHold 记住上一次给出的新鲜 last，供下一轮在新数据缺失或更旧时兜底。
//
// 场景：直连遥测只读会话日志尾部，新一轮生成中思考内容与工具输出不断追加，
// 上一轮的用量记录可能被挤出窗口、新用量又未落盘，遥测一时返回 nil；此时
// 若直接拿 nil 去融合，就会跌回几天前的网关记录、显示"空闲"。LastHold 只
// 比较时间戳：新算出的比兜底新就替换，否则给兜底；调用方每次按兜底的时间戳
// 重算年龄，于是它自然老化 fresh → aged → idle，任何更新的记录一到就替换它。
//
// 泛型是因为两处消费者的 last 类型不同（面板是 embedLastStats，胶囊是
// cmd 内的 lastSample），但"挑新者、存一份"的逻辑只有一份，不值得各写一份
// 再漂移。只放内存，进程重启清空可以接受。
type LastHold[T any] struct {
	mu  sync.Mutex
	at  time.Time
	val T
	ok  bool
}

// NewLastHold 建一个空兜底。
func NewLastHold[T any]() *LastHold[T] { return &LastHold[T]{} }

// Serve 从 candidate 与兜底里挑新者。candidate 为 nil 表示本轮没算出，
// at 是 candidate 的时间戳。返回 served、served 的时间戳、以及两边是
// 否有值（都无时 ok=false，调用方按"暂无数据"处理）。返回的是兜底的
// 副本，调用方改它（如重算 AgeSec）不会污染存着的那份。
// h 为 nil 时等价于无兜底：直接返回 candidate。
func (h *LastHold[T]) Serve(candidate *T, at time.Time) (*T, time.Time, bool) {
	if h == nil {
		if candidate == nil {
			return nil, time.Time{}, false
		}
		return candidate, at, true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if candidate != nil && (!h.ok || at.After(h.at)) {
		h.at = at
		h.val = *candidate
		h.ok = true
	}
	if !h.ok {
		return nil, time.Time{}, false
	}
	v := h.val
	return &v, h.at, true
}
