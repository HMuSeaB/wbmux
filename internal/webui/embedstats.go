package webui

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/embedstats"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/sessiontelemetry"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 嵌入胶囊的指标快照 ----------
//
// # 只回答三个问题
//
// 胶囊要常驻顶栏、每几秒刷新一次，所以这里刻意做得很窄：
// 上下文多大、上一个请求多快、今天用了多少。不返回明细列表——
// 那属于面板详情，塞进轮询接口只会让每次请求多传几十 KB。
//
// # 数据口径
//
// 全部来自网关审计环（请求级快照），不是实时流式计数：
// tokPerSec 表示"上一次请求首字之后、纯生成阶段的平均速度"，
// 首字等待与排队时间已扣掉。实时速率需要改 SSE 转发链路，暂不做。

// embedStatsSnapshot 是胶囊的轮询响应。
type embedStatsSnapshot struct {
	Last      *embedLastStats    `json:"last,omitempty"`
	Today     embedDayStats      `json:"today"`
	Totals    embedTotals        `json:"totals"`
	Daily     []embedDayPoint    `json:"daily"`
	Recent    []embedRecentStats `json:"recent"`
	Quota     *embedQuota        `json:"quota,omitempty"`
	UpdatedAt time.Time          `json:"updatedAt"`
}

// embedTotals 是跨天累计，回答"这个月用了多少 / 一共用了多少"。
// Total 只统计账本里还留着的日子——网关日志会滚动清理，这里不假装是
// 开天辟地以来的总数。
type embedTotals struct {
	Month int `json:"month"`
	Total int `json:"total"`
}

// embedDayPoint 是 30 天柱状图的一根柱子。没有记录的日子也占一格（tokens=0），
// 否则"最近没怎么用"这件事在图上会变成空白，看不出是缺数据还是没用。
type embedDayPoint struct {
	Date   string `json:"date"`
	Tokens int    `json:"tokens"`
}

// embedQuota 是面板额度区的数据：当前模型的限流态 + 两档主包余量。
//
// 只读内存态：限流态来自代理自己的观察（quotaFor），主包来自
// LiveAccounts 的缓存快照——5 秒轮询里绝不直调官方接口。
// 取不到（没记录、没包、Total<=0）就省略对应字段，前端不渲染，
// 绝不显示 0%——"没数据"和"用完了"是两回事，混成一个数会误导人。
type embedQuota struct {
	// Model 是 last.model 的限流记录，只在 rate_limited / exhausted
	// 时出现；upstream_error 是上游瞬时故障，不算额度，别往额度区塞。
	Model *ProxyQuota `json:"model,omitempty"`
	// Packages 每档一行（国内包/国际包），绝不合并成一个总数；
	// 没有数据的档位不出现。
	Packages []embedQuotaPackage `json:"packages,omitempty"`
}

// embedQuotaPackage 是一档的主包余量。
type embedQuotaPackage struct {
	// Side 是 "cn" / "intl"。
	Side string `json:"side"`
	// Percent 是 remain/total*100，保留一位小数。
	Percent float64 `json:"percent"`
	// CycleText 是周期文案原文：优先 LivePackage.ExpiryRuleZh
	// （如"每月 1 日重置（月底清零更新）"），没有才退 CycleEndTime
	// （"到期 2026-10-27 03:33:47"）。照抄，不自己编。
	CycleText string `json:"cycleText,omitempty"`
	// DaysLeft 是距离失效/重置还有几天（向上取整）。0 表示没有可用到期信息。
	DaysLeft int `json:"daysLeft,omitempty"`
	// Expiring 表示一周内就会清零——面板上要醒目标出来。
	Expiring bool `json:"expiring,omitempty"`
	// Cards 是这一档下的每一张积分卡（国内是"平台奖励积分"，多张卡各自
	// 独立到期）。合并成一行会丢掉"哪张快没了"，所以展开给面板逐张显示。
	Cards []embedQuotaCard `json:"cards,omitempty"`
}

// embedQuotaCard 是一张卡的确定数据。
//
// 故意不带到期时间：官方"资源汇总"接口只给容量不给到期时刻，现有代码里
// 那个"30 天后清零"是按发放规则估出来的，不是真实值——宁可不说，也不编。
type embedQuotaCard struct {
	Total   float64 `json:"total"`
	Remain  float64 `json:"remain"`
	Used    float64 `json:"used"`
	Percent float64 `json:"percent"`
	// ExpiresAt 是官方给的到期时刻原文（如 "2026-11-22 13:31:41"）。
	// 来自 get-user-resource-free-packages，是真实值，不是推算。
	ExpiresAt string `json:"expiresAt,omitempty"`
	// DaysLeft 距离到期还有几天（向上取整，已过期为 0）。
	DaysLeft int `json:"daysLeft,omitempty"`
	// Reason 是发放来源（"Buddy 加油站签到"这类），用来区分同额度的卡。
	Reason string `json:"reason,omitempty"`
}

// expiringDays 是"快到清零日"的阈值（天）。
const expiringDays = 7

// resetsInDays 返回距离该包失效/重置还有几天（向上取整）。
//
// 0 表示没有可用的到期信息，绝不猜：优先 ExpiresAtMs（官方毫秒时间戳），
// 其次 CycleEndTime（"2026-10-27 03:33:47"，官方本地时间）。两者都没有
// 就不显示天数——"每月 1 日重置"这类纯规则文案推不出日期。
func resetsInDays(pkg *usage.LivePackage) int {
	var end time.Time
	if pkg.ExpiresAtMs > 0 {
		end = time.UnixMilli(pkg.ExpiresAtMs)
	} else if pkg.CycleEndTime != "" {
		if t, err := time.ParseInLocation("2006-01-02 15:04:05", pkg.CycleEndTime, time.Local); err == nil {
			end = t
		}
	}
	if end.IsZero() {
		return 0
	}
	days := int(math.Ceil(time.Until(end).Hours() / 24))
	if days < 0 {
		return 0
	}
	return days
}

// mainSubscriptionPackage 挑一侧的订阅主包：kind=="subscription" 里
// DeductPriority 最小的那个（1 为最优先扣的月度包）。
// 没有订阅包、或 Total<=0（算不出百分比）时返回 nil。
func mainSubscriptionPackage(acc *usage.LiveAccount) *usage.LivePackage {
	if acc == nil || !acc.OK {
		return nil
	}
	var best *usage.LivePackage
	for i := range acc.Packages {
		p := &acc.Packages[i]
		if p.Kind != "subscription" || p.Total <= 0 {
			continue
		}
		if best == nil || p.DeductPriority < best.DeductPriority ||
			(p.DeductPriority == best.DeductPriority && p.Total > best.Total) {
			best = p
		}
	}
	return best
}

// mainPackage 挑一侧用于额度显示的主包。
//
// 有月度订阅包就用它；没有订阅包时（国内账号是纯赠送包模式）把 bonus
// 合并成一个虚拟包：total/remain/used 求和，到期时间取最早的那张——
// 那才是最先清零的钱，提醒应该跟着它走。
//
// 2026-11-20 的教训：只认 subscription 会让国内档整个消失（用户看到
// "国内呢？"），而国内的额度恰恰全是 bonus。
func mainPackage(acc *usage.LiveAccount) *usage.LivePackage {
	if sub := mainSubscriptionPackage(acc); sub != nil {
		return sub
	}
	if acc == nil || !acc.OK {
		return nil
	}
	var merged *usage.LivePackage
	count := 0
	for i := range acc.Packages {
		p := &acc.Packages[i]
		if p.Kind != "bonus" || p.Total <= 0 {
			continue
		}
		count++
		if merged == nil {
			cp := *p
			merged = &cp
			continue
		}
		merged.Total += p.Total
		merged.Remain += p.Remain
		merged.Used += p.Used
		if p.ExpiresAtMs > 0 && (merged.ExpiresAtMs == 0 || p.ExpiresAtMs < merged.ExpiresAtMs) {
			merged.ExpiresAtMs = p.ExpiresAtMs
		}
	}
	if merged == nil {
		return nil
	}
	merged.Name = fmt.Sprintf("%d 个赠送包", count)
	return merged
}

// liveAccounts 是额度数据的唯一来源：测试可注入 liveAccountsFn，
// 生产读 LiveAccountsCached() 的内存/磁盘快照，绝不发网络请求。
// 面板 quota 段与胶囊常驻段共用它，两边看到的包是同一份。
func (s *Server) liveAccounts() map[string]*usage.LiveAccount {
	if s.liveAccountsFn != nil {
		return s.liveAccountsFn(s.probe())
	}
	return usage.LiveAccountsCached()
}

// freeCardsCache 缓存逐张卡明细。
//
// get-user-resource-free-packages 一次返回 77KB（81 张卡），而面板 5 秒一刷——
// 每轮都真调等于自己 DDoS 自己的账号。60 秒足够：卡是每天发的，不会秒变。
var freeCardsCache struct {
	mu     sync.Mutex
	at     time.Time
	bySide map[string][]usage.FreeCard
}

const freeCardsTTL = 60 * time.Second

// freeCards 返回某一侧的逐张卡明细；取不到就退回空（面板只少显示细节）。
func (s *Server) freeCards(side string, codes []string) []usage.FreeCard {
	if len(codes) == 0 {
		return nil
	}
	freeCardsCache.mu.Lock()
	defer freeCardsCache.mu.Unlock()
	if time.Since(freeCardsCache.at) < freeCardsTTL {
		if v, ok := freeCardsCache.bySide[side]; ok {
			return v
		}
	}
	id := variant.CN
	if side == "intl" {
		id = variant.Intl
	}
	cards, err := usage.FreeCardsFor(s.probe(), id, codes)
	if err != nil {
		return nil
	}
	if freeCardsCache.bySide == nil {
		freeCardsCache.bySide = make(map[string][]usage.FreeCard, 2)
	}
	freeCardsCache.bySide[side] = cards
	freeCardsCache.at = time.Now()
	return cards
}

// CapsulePackage 是胶囊常驻段里一档的主包余量。
type CapsulePackage struct {
	// Side 是 "cn" / "intl"。
	Side string
	// Percent 是 remain/total*100，四舍五入到整数（面板保留一位小数，
	// 胶囊地方小只放整数）。0..100。
	Percent int
	// DaysLeft 是距离清零还有几天；0 表示没有可用到期信息。
	DaysLeft int
	// Expiring 表示一周内清零且还有余额——胶囊要标出来，别让额度悄悄没了。
	Expiring bool
}

// CapsulePackages 返回各档主包余量百分比（国内在前、国际在后），
// 供胶囊常驻显示。只读缓存快照，绝不直调官方接口；没数据的档位
// 不出现，绝不返回 0%——"没数据"和"用完了"是两回事。
func (s *Server) CapsulePackages() []CapsulePackage {
	accounts := s.liveAccounts()
	out := make([]CapsulePackage, 0, 2)
	for _, side := range []string{"cn", "intl"} {
		pkg := mainPackage(accounts[side])
		if pkg == nil {
			continue
		}
		pct := int(math.Round(pkg.Remain / pkg.Total * 100))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		// 到期信息可能有也可能没有（"每月 1 日重置"这种纯规则文案推不出日期），
		// 推不出就留 0，胶囊不显示天数。
		d := resetsInDays(pkg)
		out = append(out, CapsulePackage{Side: side, Percent: pct, DaysLeft: d, Expiring: d > 0 && d <= expiringDays})
	}
	return out
}

// embedQuotaFor 拼当前轮询的 quota 段。lastModel 为空（暂无请求）
// 时只拼主包；限流记录只取 last.model 那一条。
func (s *Server) embedQuotaFor(lastModel string) *embedQuota {
	accounts := s.liveAccounts()

	out := &embedQuota{}
	if lastModel != "" {
		if q := s.QuotaFor(lastModel); q != nil &&
			(q.State == "rate_limited" || q.State == "exhausted") {
			out.Model = q
		}
	}
	for _, side := range []string{"cn", "intl"} {
		pkg := mainPackage(accounts[side])
		if pkg == nil {
			continue
		}
		pct := math.Round(pkg.Remain/pkg.Total*1000) / 10
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		row := embedQuotaPackage{Side: side, Percent: pct}
		switch {
		case pkg.ExpiryRuleZh != "":
			row.CycleText = pkg.ExpiryRuleZh
		case pkg.CycleEndTime != "":
			row.CycleText = "到期 " + pkg.CycleEndTime
		}
		if pkg.Kind == "bonus" {
			// 国内是"平台奖励积分"：多张卡各自独立到期，合并成一行会丢掉
			// "哪张快没了"。逐张列出，但只放确定的容量——官方汇总接口不给
			// 到期时刻，代码里的 30 天是按发放规则估的，不能当真实值展示。
			var codes []string
			for _, p := range accounts[side].Packages {
				if p.Kind == "bonus" && p.Code != "" {
					codes = append(codes, p.Code)
				}
			}
			for _, c := range s.freeCards(side, codes) {
				card := embedQuotaCard{
					Total:  c.Total,
					Remain: c.Remain,
					Used:   c.Used,
					Reason: c.Reason,
				}
				card.Percent = math.Round(c.Remain/c.Total*1000) / 10
				if c.EndMs > 0 {
					card.ExpiresAt = c.CycleEnd
					card.DaysLeft = int(math.Ceil(time.Until(time.UnixMilli(c.EndMs)).Hours() / 24))
					if card.DaysLeft < 0 {
						card.DaysLeft = 0
					}
				}
				row.Cards = append(row.Cards, card)
			}
			row.CycleText = ""
		} else if d := resetsInDays(pkg); d > 0 {
			// 订阅包按自然月重置，月末是算得准的，天数照常给。
			row.DaysLeft = d
			row.Expiring = d <= expiringDays
		}
		out.Packages = append(out.Packages, row)
	}
	if out.Model == nil && len(out.Packages) == 0 {
		return nil
	}
	return out
}

// embedRecentStats 是面板详情区里的一行。
//
// 只留一行放得下的字段；轮询接口带 5 条，体积约 600 字节，
// 跟只带 1 条几乎没差别。
//
// Source/Side 标明这条来自哪边：网关（side 暂无，别编）还是直连
// （side 为 "cn"/"intl"），双开时胶囊在两边跳变才有据可查。
type embedRecentStats struct {
	Model        string  `json:"model"`
	PromptTokens int     `json:"promptTokens"`
	TotalTokens  int     `json:"totalTokens"`
	TokPerSec    float64 `json:"tokPerSec"`
	OK           bool    `json:"ok"`
	AgeSec       int64   `json:"ageSec"`
	Source       string  `json:"source,omitempty"`
	Side         string  `json:"side,omitempty"`
}

// embedLastStats 是最近一次网关请求的摘要。
type embedLastStats struct {
	Model            string  `json:"model"`
	PromptTokens     int     `json:"promptTokens"`
	CompletionTokens int     `json:"completionTokens"`
	TokPerSec        float64 `json:"tokPerSec"`
	TTFTMs           int64   `json:"ttftMs"`
	ContextScale     string  `json:"contextScale,omitempty"`
	IsStream         bool    `json:"isStream"`
	AgeSec           int64   `json:"ageSec"`
	Source           string  `json:"source,omitempty"`
	Side             string  `json:"side,omitempty"`
	Freshness        string  `json:"freshness,omitempty"`
	// ContextLimit 是当前模型声明的上下文上限（maxInputTokens）。
	// 0 表示查不到——不猜，前端就不显示 "x / 上限"。
	ContextLimit int `json:"contextLimit,omitempty"`
}

// embedDayStats 是当天的累计用量。
type embedDayStats struct {
	Requests   int `json:"requests"`
	Tokens     int `json:"tokens"`
	Prompt     int `json:"promptTokens"`
	Completion int `json:"completionTokens"`
}

// handleEmbedStats 返回胶囊轮询所需的极简指标。
func (s *Server) handleEmbedStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}

	sum := gatewayaudit.Default().GetSummary(5)
	out := embedStatsSnapshot{UpdatedAt: time.Now(), Recent: make([]embedRecentStats, 0, 5)}

	now := time.Now()

	// 本月 / 累计 + 近 30 天柱子，全部从日度聚合出——面板要回答
	// "这个月到底用了多少"，只给一个今日总量太单薄。
	monthKey := now.Format("2006-01")
	byDate := make(map[string]int, len(sum.DailyStats))
	for _, d := range sum.DailyStats {
		out.Totals.Total += d.TotalTokens
		if len(d.Date) >= 7 && d.Date[:7] == monthKey {
			out.Totals.Month += d.TotalTokens
		}
		byDate[d.Date] = d.TotalTokens
	}
	out.Daily = make([]embedDayPoint, 0, 30)
	for i := 29; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		out.Daily = append(out.Daily, embedDayPoint{Date: day, Tokens: byDate[day]})
	}
	for _, rec := range sum.RecentRecords {
		if len(out.Recent) >= 5 {
			break
		}
		row := embedRecentStats{
			Model:        rec.ModelRequested,
			PromptTokens: rec.PromptTokens,
			TotalTokens:  rec.TotalTokens,
			OK:           rec.StatusCode >= 200 && rec.StatusCode < 300,
			AgeSec:       int64(now.Sub(rec.Timestamp).Seconds()),
			Source:       recordSource(rec),
			Side:         rec.Side,
		}
		if rate := streamRate(rec); rate > 0 {
			row.TokPerSec = rate
		}
		out.Recent = append(out.Recent, row)
	}

	// 日度统计按本地时区归档，比对日期字符串即可。
	today := time.Now().Format("2006-01-02")
	for _, d := range sum.DailyStats {
		if d.Date == today {
			out.Today = embedDayStats{
				Requests:   d.TotalRequests,
				Tokens:     d.TotalTokens,
				Prompt:     d.PromptTokens,
				Completion: d.CompTokens,
			}
			break
		}
	}

	// lastAt 是融合出的 last 的时间戳，供兜底比较新旧。
	var lastAt time.Time
	if len(sum.RecentRecords) > 0 {
		rec := sum.RecentRecords[0]
		last := &embedLastStats{
			Model:            rec.ModelRequested,
			PromptTokens:     rec.PromptTokens,
			CompletionTokens: rec.CompletionTokens,
			TTFTMs:           rec.TTFTMs,
			ContextScale:     rec.ContextScale,
			IsStream:         rec.IsStream,
			AgeSec:           int64(time.Since(rec.Timestamp).Seconds()),
			Source:           recordSource(rec),
			Side:             rec.Side,
			Freshness:        string(embedstats.Classify(time.Since(rec.Timestamp))),
			ContextLimit:     s.contextLimitFor(rec.ModelRequested),
		}
		// 生成速率只在流式、且确实存在生成阶段时计算；
		// 少量 token（<20）的请求算出来的数字噪声太大，直接留 0 让前端跳过。
		if rate := streamRate(rec); rate > 0 {
			last.TokPerSec = rate
		}
		out.Last = last
		lastAt = rec.Timestamp
	}

	// 双源融合：检测本地活跃会话日志（捕获国内自带模型直连）
	sessMetric := sessiontelemetry.GetLatestSessionUsage(s.probe())
	if sessMetric != nil && !sessMetric.Timestamp.IsZero() {
		useLocal := false
		if len(sum.RecentRecords) == 0 {
			useLocal = true
		} else if sessMetric.Timestamp.After(sum.RecentRecords[0].Timestamp.Add(2 * time.Second)) {
			useLocal = true
		}

		if useLocal {
			modelName := sessMetric.Model
			if modelName == "" {
				modelName = "国内自带模型"
			}
			out.Last = &embedLastStats{
				Model:            modelName,
				PromptTokens:     sessMetric.PromptTokens,
				CompletionTokens: sessMetric.CompletionTokens,
				AgeSec:           int64(time.Since(sessMetric.Timestamp).Seconds()),
				Source:           "local",
				Side:             sessMetric.Side,
				Freshness:        string(embedstats.Classify(time.Since(sessMetric.Timestamp))),
				ContextLimit:     s.contextLimitFor(modelName),
			}
			lastAt = sessMetric.Timestamp
			// 直连记录已由 daemon 落进账本（ObserveLocalSession），Recent 与
			// Today 直接读账本，不再另拼本地行、也不再把最新一条叠进今日——
			// 否则同一条会在账本与叠加里各算一次。
		}
	}

	// 兜底：遥测一时找不到新鲜记录时（新一轮生成中旧记录被挤出尾部窗口），
	// 别跌回几天前的网关记录，先拿上一次新鲜的 last 老化显示；Today 累计
	// 与 Recent 列表不受影响。切换会话时兜底可能短暂显示旧会话的数，随后
	// 被新会话的第一条用量记录替换——可接受的折中，不另做会话切换检测。
	out.Last = s.serveEmbedLast(out.Last, lastAt)

	// 额度段：只读内存态（代理观察 + LiveAccounts 缓存快照），
	// 5 秒轮询里绝不直调官方接口。last 在网关/直连融合之后才定，
	// 所以拼在这里，不能提前。
	lastModel := ""
	if out.Last != nil {
		lastModel = out.Last.Model
	}
	out.Quota = s.embedQuotaFor(lastModel)

	writeJSON(w, out)
}

// serveEmbedLast 把新算出的 last 与兜底里挑新者，并按结果的时间戳重算
// AgeSec 与 Freshness——兜底存的是当时的快照，年龄会变，上一轮调的
// AgeSec 不能接着用。lastHold 为 nil（测试直接拼的 Server）时不兜底，
// 原样返回 candidate。
// contextLimitFor 按模型名查上下文上限（maxInputTokens）。
//
// 匹配先精确、再归一化（忽略大小写、连字符、下划线、空格、点）后做包含判断——
// 客户端里的模型名和官方 id 经常一处带连字符一处不带。查不到返回 0：
// 面板宁可不显示"上限"，也不编一个数字出来。
func (s *Server) contextLimitFor(model string) int {
	model = strings.TrimSpace(model)
	if model == "" {
		return 0
	}
	norm := func(v string) string {
		v = strings.ToLower(strings.TrimSpace(v))
		return strings.NewReplacer("-", "", "_", "", " ", "", ".", "").Replace(v)
	}
	want := norm(model)
	if want == "" {
		return 0
	}
	accounts := s.liveAccounts()
	for _, side := range []string{"cn", "intl"} {
		acc := accounts[side]
		if acc == nil || !acc.OK {
			continue
		}
		for _, m := range acc.Models {
			if m.Ctx <= 0 {
				continue
			}
			id, name := norm(m.ID), norm(m.Name)
			if id == want || name == want ||
				(id != "" && strings.Contains(want, id)) ||
				(name != "" && strings.Contains(name, want)) {
				return m.Ctx
			}
		}
	}
	return 0
}

func (s *Server) serveEmbedLast(candidate *embedLastStats, at time.Time) *embedLastStats {
	if s.lastHold == nil {
		return candidate
	}
	served, servedAt, ok := s.lastHold.Serve(candidate, at)
	if !ok {
		return nil
	}
	served.AgeSec = int64(time.Since(servedAt).Seconds())
	served.Freshness = string(embedstats.Classify(time.Since(servedAt)))
	return served
}

// recordSource 返回账本记录的来源：直连为 "local"，网关（包括落盘在先、
// Source 为空的老记录）为 "gateway"。Side 只从记录里取——网关记录没有
// 档位归属，别编。
func recordSource(rec gatewayaudit.Record) string {
	if rec.Source != "" {
		return rec.Source
	}
	return "gateway"
}

// streamRate 计算纯生成阶段的 token 速率；样本太小时返回 0。
func streamRate(rec gatewayaudit.Record) float64 {
	genMs := rec.DurationMs - rec.TTFTMs
	if !rec.IsStream || rec.CompletionTokens < 20 || genMs < 200 {
		return 0
	}
	return math.Round(float64(rec.CompletionTokens)/float64(genMs)*1000*10) / 10
}
