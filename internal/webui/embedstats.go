package webui

import (
	"math"
	"net/http"
	"time"

	"github.com/HMuSeaB/wbmux/internal/embedstats"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/sessiontelemetry"
	"github.com/HMuSeaB/wbmux/internal/usage"
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
	Recent    []embedRecentStats `json:"recent"`
	Quota     *embedQuota        `json:"quota,omitempty"`
	UpdatedAt time.Time          `json:"updatedAt"`
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
}

// mainSubscriptionPackage 挑一侧的主包：kind=="subscription" 里
// DeductPriority 最小的那个（1 为最优先扣的月度包）。
// 没有订阅包、或 Total<=0（算不出百分比）时返回 nil，调用方
// 直接省略该档。
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

// liveAccounts 是额度数据的唯一来源：测试可注入 liveAccountsFn，
// 生产读 LiveAccountsCached() 的内存/磁盘快照，绝不发网络请求。
// 面板 quota 段与胶囊常驻段共用它，两边看到的包是同一份。
func (s *Server) liveAccounts() map[string]*usage.LiveAccount {
	if s.liveAccountsFn != nil {
		return s.liveAccountsFn(s.probe())
	}
	return usage.LiveAccountsCached()
}

// CapsulePackage 是胶囊常驻段里一档的主包余量。
type CapsulePackage struct {
	// Side 是 "cn" / "intl"。
	Side string
	// Percent 是 remain/total*100，四舍五入到整数（面板保留一位小数，
	// 胶囊地方小只放整数）。0..100。
	Percent int
}

// CapsulePackages 返回各档主包余量百分比（国内在前、国际在后），
// 供胶囊常驻显示。只读缓存快照，绝不直调官方接口；没数据的档位
// 不出现，绝不返回 0%——"没数据"和"用完了"是两回事。
func (s *Server) CapsulePackages() []CapsulePackage {
	accounts := s.liveAccounts()
	out := make([]CapsulePackage, 0, 2)
	for _, side := range []string{"cn", "intl"} {
		pkg := mainSubscriptionPackage(accounts[side])
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
		out = append(out, CapsulePackage{Side: side, Percent: pct})
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
		pkg := mainSubscriptionPackage(accounts[side])
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
			Source:       "gateway",
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
			Source:           "gateway",
			Freshness:        string(embedstats.Classify(time.Since(rec.Timestamp))),
		}
		// 生成速率只在流式、且确实存在生成阶段时计算；
		// 少量 token（<20）的请求算出来的数字噪声太大，直接留 0 让前端跳过。
		if rate := streamRate(rec); rate > 0 {
			last.TokPerSec = rate
		}
		out.Last = last
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
			}
			localRow := embedRecentStats{
				Model:        modelName,
				PromptTokens: sessMetric.PromptTokens,
				TotalTokens:  sessMetric.TotalTokens,
				OK:           true,
				AgeSec:       int64(time.Since(sessMetric.Timestamp).Seconds()),
				Source:       "local",
				Side:         sessMetric.Side,
			}
			out.Recent = append([]embedRecentStats{localRow}, out.Recent...)
			if len(out.Recent) > 5 {
				out.Recent = out.Recent[:5]
			}
			if sessMetric.Timestamp.Format("2006-01-02") == today {
				if out.Today.Tokens == 0 {
					out.Today.Tokens = sessMetric.TotalTokens
					out.Today.Prompt = sessMetric.PromptTokens
					out.Today.Completion = sessMetric.CompletionTokens
					out.Today.Requests = 1
				} else {
					out.Today.Tokens += sessMetric.TotalTokens
					out.Today.Prompt += sessMetric.PromptTokens
					out.Today.Completion += sessMetric.CompletionTokens
					out.Today.Requests++
				}
			}
		}
	}

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

// streamRate 计算纯生成阶段的 token 速率；样本太小时返回 0。
func streamRate(rec gatewayaudit.Record) float64 {
	genMs := rec.DurationMs - rec.TTFTMs
	if !rec.IsStream || rec.CompletionTokens < 20 || genMs < 200 {
		return 0
	}
	return math.Round(float64(rec.CompletionTokens)/float64(genMs)*1000*10) / 10
}
