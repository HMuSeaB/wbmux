package webui

import (
	"net/http"
	"strconv"

	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
)

// handleGatewayStats 返回本地网关调用统计摘要与最近请求流水。
// 客户端可通过 ?limit=50 控制返回明细条数（默认 50，上限 200）。
func (s *Server) handleGatewayStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}

	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			if n > 200 {
				n = 200
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

	gatewayaudit.Default().Clear()
	writeJSON(w, map[string]any{"ok": true})
}
