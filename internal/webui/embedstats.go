package webui

import (
	"math"
	"net/http"
	"time"

	"github.com/HMuSeaB/wbmux/internal/embedstats"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/sessiontelemetry"
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
	UpdatedAt time.Time          `json:"updatedAt"`
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
