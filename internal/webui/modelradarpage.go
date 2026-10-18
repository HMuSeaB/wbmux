package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/modelradar"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// handleModelRadarStatus 返回当前模型雷达的测速记录与最速限免推荐。
func (s *Server) handleModelRadarStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}

	radar := modelradar.Default()
	records := radar.GetAllRecords()
	fastest, found := radar.FastestFreeModel()

	writeJSON(w, map[string]any{
		"records":          records,
		"fastestFreeModel": fastest,
		"hasHealthyFree":   found,
	})
}

// radarPingRequest 定义前端发起的测速请求体。
type radarPingRequest struct {
	Model string `json:"model,omitempty"` // 为空表示测速全量限免模型
}

// handleModelRadarPing 触发单模型或全量限免模型的轻量流式探针测速。
func (s *Server) handleModelRadarPing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	var req radarPingRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	// 1. 获取当前国际端凭据与端点
	cred, err := s.intlCred(s.probe())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "国际侧有效凭据不可用: "+err.Error())
		return
	}

	endpoint := s.upstreamBase() + chatCompletionsPath

	// 2. 获取国际端实时模型元数据，解析费率与是否限免
	var liveModels []usage.LiveModel
	if live := usage.LiveAccounts(s.probe())[string(variant.Intl)]; live != nil && live.OK {
		liveModels = live.Models
	}

	modelMetaMap := make(map[string]modelradar.ModelMeta)
	for _, lm := range liveModels {
		modelMetaMap[strings.ToLower(lm.ID)] = modelradar.ModelMeta{
			ID:          lm.ID,
			DisplayName: lm.Name,
			IsFree:      lm.FreeNow,
			Rate:        lm.Rate,
		}
	}

	radar := modelradar.Default()

	targetModel := strings.TrimSpace(req.Model)
	if targetModel != "" {
		// 单模型测速
		meta, ok := modelMetaMap[strings.ToLower(targetModel)]
		if !ok {
			meta = modelradar.ModelMeta{
				ID:          targetModel,
				DisplayName: targetModel,
				IsFree:      false,
			}
		}
		rec := radar.Ping(r.Context(), endpoint, cred.Token, meta)
		fastest, found := radar.FastestFreeModel()
		writeJSON(w, map[string]any{
			"records":          []modelradar.ProbeRecord{rec},
			"fastestFreeModel": fastest,
			"hasHealthyFree":   found,
		})
		return
	}

	// 批量测速全量限免模型
	var probeTargets []modelradar.ModelMeta
	for _, lm := range liveModels {
		if lm.FreeNow {
			probeTargets = append(probeTargets, modelradar.ModelMeta{
				ID:          lm.ID,
				DisplayName: lm.Name,
				IsFree:      true,
				Rate:        lm.Rate,
			})
		}
	}

	// 若未发现限免模型，兜底探测常见模型
	if len(probeTargets) == 0 {
		probeTargets = append(probeTargets, modelradar.ModelMeta{
			ID:          "deepseek-chat",
			DisplayName: "DeepSeek V3",
			IsFree:      true,
		})
	}

	results := radar.PingBatch(r.Context(), endpoint, cred.Token, probeTargets, 2)
	fastest, found := radar.FastestFreeModel()

	writeJSON(w, map[string]any{
		"records":          results,
		"fastestFreeModel": fastest,
		"hasHealthyFree":   found,
	})
}
