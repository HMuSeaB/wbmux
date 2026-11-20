package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/embedstats"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/sessiontelemetry"
	"github.com/HMuSeaB/wbmux/internal/webui"
)

// lastSample 是胶囊"上一次请求"的快照（网关或直连，以时间戳新者为准）。
// 只含渲染所需的字段；兜底时整条照搬，年龄按 Timestamp 重算。
type lastSample struct {
	promptTokens int
	timestamp    time.Time
	midPart      string // 速率（网关）或模型名（直连）
	model        string
}

// capsuleLastHold 记住上一次给出的新鲜 last（daemon 进程内，内存态）。
// 新一轮生成中遥测一时找不到记录时，胶囊先拿它老化显示，别跌回空闲。
var capsuleLastHold = embedstats.NewLastHold[lastSample]()

// embedStatsText 生成胶囊上那行指标文字与告警等级。
//
// 格式：`187k · 212/s · 今日 42M · 国62%·际41%` 或 `412k · 混元 · 今日 42M`
//
// 为什么上下文放在最前面：速率只是"这次快不快"，上下文才是"会不会出事"。
// 1M 上下文的模型很容易悄悄吃满然后被截断，用户毫无感知——所以
// 256k 起转黄、512k 转红，让它变成一眼可见的信号。阈值与 stale 分档
// 的唯一口径在 internal/embedstats，别在这里另写一份。
//
// 末尾的额度段是常驻的（用户明确要）：两档主包余量压成一段，国内在前、
// 国际在后（`国62%·际41%`），只有一边有数据就只显示那一边，没数据
// 不显示、绝不显示 0%。百分比是整数（面板保留一位小数）。限流/用尽
// 改写仍优先于一切，那时常驻段不出现；陈旧（aged）与空闲时也不显示。
//
// 双源智能融合（借鉴 chuanban 增量遥测架构）：
//   - 经过网关的代理请求（如国际限免模型）：可获得精确的流式速率；
//   - 国内自带直连模型（不经网关）：通过 sessiontelemetry 监听本地活跃
//     会话日志的 rawUsage，O(1) 提取上下文 tokens 与模型名，消灭统计盲区。
//
// quotaLookup 是查"当前模型限流态"的回调（cmd_gui.go 接
// webui.Server.QuotaFor）；packageLookup 是取"各档主包余量"的回调
// （cmd_gui.go 接 webui.Server.CapsulePackages），只读缓存快照，
// 3 秒巡检里绝不直调官方接口。两个回调传 nil 表示没有额度数据，
// 胶囊行为与原来一模一样——测试与非 GUI 路径都走这条。
// hold 是兜底状态（daemon 传 capsuleLastHold）；传 nil 表示不兜底。
// sessMetric 是本地会话的最新用量，由调用方探测后传入——取值与写盘
// （observeLocalUsage）都在调用方，这个函数保持只读，测试可注入。
func embedStatsText(sessMetric *sessiontelemetry.SessionMetric, quotaLookup func(string) *webui.ProxyQuota, packageLookup func() []webui.CapsulePackage, hold *embedstats.LastHold[lastSample]) (string, string) {
	return embedStatsTextFrom(gatewayaudit.Default().GetSummary(1), sessMetric, quotaLookup, packageLookup, hold)
}

// observeLocalUsage 把一条直连用量落进审计账本（P2-12）。
//
// 全仓库唯一的直连写盘点：daemon 3 秒巡检只要客户端连着就会调。
// 与取文案分开，是为了让 embedStatsText 保持只读——否则测试一跑就会
// 往真实账本里写记录，且探测路径无法注入（PR #7 原实现的问题）。
func observeLocalUsage(m *sessiontelemetry.SessionMetric) {
	if m == nil {
		return
	}
	gatewayaudit.Default().ObserveLocalSession(gatewayaudit.LocalSessionUsage{
		SessionID:        m.SessionID,
		Model:            m.Model,
		Side:             m.Side,
		Timestamp:        m.Timestamp,
		PromptTokens:     m.PromptTokens,
		CompletionTokens: m.CompletionTokens,
		TotalTokens:      m.TotalTokens,
	})
}

// embedStatsTextFrom 是可测的内层：sum 与 sessMetric 由调用方注入，
// 兜底状态由 hold 注入。只兜底 last，今日累计仍按本轮新鲜数据算。
func embedStatsTextFrom(sum gatewayaudit.Summary, sessMetric *sessiontelemetry.SessionMetric, quotaLookup func(string) *webui.ProxyQuota, packageLookup func() []webui.CapsulePackage, hold *embedstats.LastHold[lastSample]) (string, string) {
	var gwRec *gatewayaudit.Record
	if len(sum.RecentRecords) > 0 {
		gwRec = &sum.RecentRecords[0]
	}

	// 判定哪个更新：若本地会话记录晚于网关记录，说明当前在用国内自带模型直连
	useLocal := false
	if sessMetric != nil && !sessMetric.Timestamp.IsZero() {
		if gwRec == nil {
			useLocal = true
		} else if sessMetric.Timestamp.After(gwRec.Timestamp.Add(2 * time.Second)) {
			useLocal = true
		}
	}

	var cand *lastSample
	if useLocal {
		modelName := sessMetric.Model
		if modelName == "" {
			modelName = "国内自带"
		}
		cand = &lastSample{
			promptTokens: sessMetric.PromptTokens,
			timestamp:    sessMetric.Timestamp,
			midPart:      cleanCapsuleModel(modelName),
			model:        sessMetric.Model,
		}
	} else if gwRec != nil {
		cand = &lastSample{
			promptTokens: gwRec.PromptTokens,
			timestamp:    gwRec.Timestamp,
			model:        gwRec.ModelRequested,
		}
		// 直连记录已由 daemon 落进账本：它没有流式速率，中段显示模型名，
		// 与以前 useLocal 分支的行为一致。
		if gwRec.Source == "local" {
			name := gwRec.ModelRequested
			if name == "" {
				name = "国内自带"
			}
			cand.midPart = cleanCapsuleModel(name)
		} else if gwRec.IsStream {
			if rate := genRate(gwRec.CompletionTokens, gwRec.DurationMs, gwRec.TTFTMs); rate > 0 {
				cand.midPart = fmt.Sprintf("%.0f/s", rate)
			}
		} else if gwRec.TTFTMs > 0 {
			cand.midPart = fmt.Sprintf("首字 %.1fs", float64(gwRec.TTFTMs)/1000)
		}
	}

	// 兜底：新一轮生成中遥测一时找不到记录时，先拿上一次新鲜的 last
	// 老化显示，别跌回几天前的网关记录。两边都没有才显示"暂无请求"。
	var served *lastSample
	var ok bool
	if cand != nil {
		served, _, ok = hold.Serve(cand, cand.timestamp)
	} else {
		served, _, ok = hold.Serve(nil, time.Time{})
	}
	if !ok {
		return "暂无请求", ""
	}
	promptTokens := served.promptTokens
	timestamp := served.timestamp
	lastModel := served.model

	// 额度改写优先于新鲜度三档：限流答的是"现在能不能用"，比
	// "多久前的数"更要紧——模型被限着时，顶栏继续显示"空闲"或
	// 旧值都是误导。没记录 / 上游瞬时故障时一个字不改。
	if quotaLookup != nil && lastModel != "" {
		if q := quotaLookup(lastModel); q != nil {
			if text, level, ok := quotaCapsuleText(lastModel, q); ok {
				return text, level
			}
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

	// 胶囊只留两个"会不会出事"的信号：上下文与额度。速率、模型名、
	// 今日用量都进面板——顶栏宽度装不下四段，装下了也来不及看
	// （2026-11-20 用户反馈"胶囊显示的信息太多了"）。
	parts := []string{compactTokens(promptTokens)}

	// 常驻额度段：只在新鲜态末尾加一段。packageLookup 为 nil 或
	// 没数据时不加，文案与原来一模一样。
	if packageLookup != nil {
		if q := capsuleQuotaText(packageLookup()); q != "" {
			parts = append(parts, q)
		}
	}

	level := embedstats.Level(promptTokens, freshness)
	return strings.Join(parts, " · "), level
}

// capsuleQuotaText 把各档主包余量压成胶囊里的一段：两边都有是
// "国62%·际41%"，只有一边是 "国62%" / "际41%"，都没数据返回 ""。
// 单字档位（国/际）是面板"国内包/国际包"的缩写，顶栏地方小，
// 但档位不能含糊——光一个 "62%" 不知道是哪边。
// 档位顺序固定国内在前、国际在后，不跟着 last 的档位跳变，
// 否则切模型时同一段数字来回换位置，没法一眼扫。
func capsuleQuotaText(pkgs []webui.CapsulePackage) string {
	var parts []string
	for _, p := range pkgs {
		label := ""
		switch p.Side {
		case "cn":
			label = "国"
		case "intl":
			label = "际"
		default:
			continue
		}
		seg := fmt.Sprintf("%s%d%%", label, p.Percent)
		// 一周内清零且还有余额：把天数贴在后面（`国62%·3d`），
		// 让"额度要没了"在顶栏一眼可见。已经 0% 的不提醒——那时
		// 限流/用尽的文案会接管，再挂个天数只是噪音。
		if p.Expiring && p.Percent > 0 && p.DaysLeft > 0 {
			seg = fmt.Sprintf("%s%d%%·%dd", label, p.Percent, p.DaysLeft)
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, "·")
}

// quotaCapsuleText 把限流态翻成胶囊文案。只在 rate_limited /
// exhausted 时改写（ok=false 表示不改，胶囊保持原样）：
// upstream_error 是上游瞬时故障，重试即可，也不改。
// 限流改写优先于常驻额度段：被限着时顶栏只报"限流几点恢复" /
// "额度用尽"，不另挂余量百分比。
func quotaCapsuleText(model string, q *webui.ProxyQuota) (string, string, bool) {
	if q == nil {
		return "", "", false
	}
	switch q.State {
	case "rate_limited":
		short := cleanCapsuleModel(model)
		if hm := quotaResetHM(q); hm != "" {
			return short + " 限流 " + hm + " 恢复", "warn", true
		}
		return short + " 限流", "warn", true
	case "exhausted":
		return "额度用尽", "bad", true
	default:
		return "", "", false
	}
}

// resetHMRe 从 UntilText 原文（"2026-09-29 22:18:43 UTC+8"）
// 里抓 HH:MM。
var resetHMRe = regexp.MustCompile(`(\d{2}):(\d{2}):\d{2}`)

// quotaResetHM 从限流记录里抠恢复时刻的 HH:MM。UntilMs 优先
// （已按上游时区换算成绝对时刻）；没有就从 UntilText 原文里抓；
// 两者都没有就不编时间，调用方只显示"限流"。
func quotaResetHM(q *webui.ProxyQuota) string {
	if q == nil {
		return ""
	}
	if q.UntilMs > 0 {
		return time.UnixMilli(q.UntilMs).Format("15:04")
	}
	if m := resetHMRe.FindStringSubmatch(q.UntilText); m != nil {
		return m[1] + ":" + m[2]
	}
	return ""
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
