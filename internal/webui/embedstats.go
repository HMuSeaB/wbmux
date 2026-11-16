package webui

import (
	"math"
	"net/http"
	"time"

	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
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
	Last      *embedLastStats `json:"last,omitempty"`
	Today     embedDayStats   `json:"today"`
	UpdatedAt time.Time       `json:"updatedAt"`
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

	sum := gatewayaudit.Default().GetSummary(1)
	out := embedStatsSnapshot{UpdatedAt: time.Now()}

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
		}
		// 生成速率只在流式、且确实存在生成阶段时计算；
		// 少量 token（<20）的请求算出来的数字噪声太大，直接留 0 让前端跳过。
		genMs := rec.DurationMs - rec.TTFTMs
		if rec.IsStream && rec.CompletionTokens >= 20 && genMs >= 200 {
			last.TokPerSec = math.Round(float64(rec.CompletionTokens)/float64(genMs)*1000*10) / 10
		}
		out.Last = last
	}

	writeJSON(w, out)
}
