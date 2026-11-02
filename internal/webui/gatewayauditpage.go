package webui

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
)

// handleGatewayStats 返回本地网关调用统计摘要与最近请求流水。
// 客户端可通过 ?limit=100 控制返回明细条数（默认 50，上限 1000）。
func (s *Server) handleGatewayStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}

	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			if n > 1000 {
				n = 1000
			}
			limit = n
		}
	}

	summary := gatewayaudit.Default().GetSummary(limit)
	writeJSON(w, summary)
}

// handleGatewayClear 清空全部网关调用流水与统计指标。
func (s *Server) handleGatewayClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	gatewayaudit.Default().ClearAll()
	writeJSON(w, map[string]any{"ok": true})
}

// handleGatewayExport 将网关调用审计记录导出为 CSV 或 JSON 文件。
// query 参数：
//   format: "csv"（默认，带 UTF-8 BOM，Excel 双击直接可读）| "json"
//   limit: 记录条数，默认 0 表示导出全部内存中的审计流水
func (s *Server) handleGatewayExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}

	limit := 0 // 默认导出全部
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}

	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "csv"
	}

	summary := gatewayaudit.Default().GetSummary(limit)
	nowStr := time.Now().Format("20060102-150405")

	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"wbmux-gateway-audit-%s.json\"", nowStr))
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)

	case "csv":
		fallthrough
	default:
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"wbmux-gateway-audit-%s.csv\"", nowStr))
		// 写入 UTF-8 BOM，确保 Windows Excel / WPS 中文不乱码
		_, _ = w.Write([]byte("\xef\xbb\xbf"))
		writer := csv.NewWriter(w)

		// 表头
		_ = writer.Write([]string{
			"请求ID",
			"时间",
			"请求模型",
			"映射模型",
			"调用账号",
			"模式",
			"状态码",
			"TTFT(ms)",
			"总用时(ms)",
			"提示词Tokens",
			"补全Tokens",
			"总Tokens",
			"故障转移",
			"算力防抖重试",
			"说明/异常详情",
		})

		for _, rec := range summary.RecentRecords {
			acc := rec.AccountMask
			if acc == "" {
				acc = rec.AccountID
			}
			if acc == "" {
				acc = "默认"
			}
			modeStr := "非流式"
			if rec.IsStream {
				modeStr = "流式"
			}
			failoverStr := "否"
			if rec.FailoverTriggered {
				failoverStr = "是"
			}

			_ = writer.Write([]string{
				rec.ID,
				rec.Timestamp.Format("2006-01-02 15:04:05"),
				rec.ModelRequested,
				rec.ModelMapped,
				acc,
				modeStr,
				strconv.Itoa(rec.StatusCode),
				strconv.FormatInt(rec.TTFTMs, 10),
				strconv.FormatInt(rec.DurationMs, 10),
				strconv.Itoa(rec.PromptTokens),
				strconv.Itoa(rec.CompletionTokens),
				strconv.Itoa(rec.TotalTokens),
				failoverStr,
				strconv.Itoa(rec.ModelBusyRetries),
				rec.ErrorMsg,
			})
		}
		writer.Flush()
	}
}

