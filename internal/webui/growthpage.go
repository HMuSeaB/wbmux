package webui

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/growth"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// handleGrowthStreak 读取国际端当前账号的成长中心连续活跃打卡与阶梯积分状态。
func (s *Server) handleGrowthStreak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	p := s.probe()
	c, err := s.intlCred(p)
	if err != nil || c.Token == "" {
		writeErr(w, http.StatusNotFound, "未配置国际版账号凭证")
		return
	}
	client := growth.NewClient(s.upstreamBase())
	streak, err := client.FetchStreak(credential.Credential{Token: c.Token, UID: c.UID})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "读取成长中心状态失败: "+err.Error())
		return
	}
	writeJSON(w, streak)
}

// handleGrowthRedeem 兑换国际端连续活跃里程碑阶梯积分奖励（如 14d / 28d）。
func (s *Server) handleGrowthRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tier == "" {
		writeErr(w, http.StatusBadRequest, "请求缺少 tier 参数")
		return
	}

	p := s.probe()
	c, err := s.intlCred(p)
	if err != nil || c.Token == "" {
		writeErr(w, http.StatusNotFound, "未配置国际版账号凭证")
		return
	}

	client := growth.NewClient(s.upstreamBase())
	granted, err := client.RedeemTier(credential.Credential{Token: c.Token, UID: c.UID}, req.Tier)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	usage.InvalidateLiveCache(variant.Intl)
	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("已成功兑换国际端成长中心「%s」里程碑奖励，获得 %d 积分！", req.Tier, granted),
	})

	writeJSON(w, map[string]any{
		"ok":      true,
		"tier":    req.Tier,
		"granted": granted,
	})
}
