package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// handleAccountsList 返回指定档位已保存的全部脱敏账号列表。
func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	side := variant.ID(strings.TrimSpace(r.URL.Query().Get("side")))
	if side == "" {
		side = variant.Intl
	}
	accounts, err := credential.ListAccounts(s.probe(), side)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "获取账号列表失败: "+err.Error())
		return
	}
	redacted := make([]credential.RedactedAccount, 0, len(accounts))
	for _, a := range accounts {
		redacted = append(redacted, a.Redact())
	}
	writeJSON(w, map[string]any{
		"side":     string(side),
		"accounts": redacted,
	})
}

// handleAccountSwitch 切换当前活跃账号。
func (s *Server) handleAccountSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Side string `json:"side"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	side := variant.ID(strings.TrimSpace(req.Side))
	if side == "" {
		side = variant.Intl
	}
	acc, err := credential.SwitchAccount(s.probe(), side, strings.TrimSpace(req.ID))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	usage.InvalidateLiveCache(side)
	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("已切换至账号：%s (%s)", acc.Nickname, acc.UID),
	})
	writeJSON(w, map[string]any{
		"ok":      true,
		"account": acc.Redact(),
	})
}

// handleAccountDelete 移除指定账号。
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Side string `json:"side"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	side := variant.ID(strings.TrimSpace(req.Side))
	if side == "" {
		side = variant.Intl
	}
	if err := credential.DeleteAccount(s.probe(), side, strings.TrimSpace(req.ID)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	usage.InvalidateLiveCache(side)
	writeJSON(w, map[string]any{"ok": true})
}

// handleAccountRefresh 使用 RefreshToken 刷新指定账号（或当前激活账号）的 AccessToken。
func (s *Server) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Side string `json:"side"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	side := variant.ID(strings.TrimSpace(req.Side))
	if side == "" {
		side = variant.Intl
	}
	acc, err := credential.RefreshAccount(r.Context(), probeHTTPClient(), s.probe(), side, strings.TrimSpace(req.ID))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "刷新失败: "+err.Error())
		return
	}
	usage.InvalidateLiveCache(side)
	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("已成功刷新并保鲜账号：%s (%s)", acc.Nickname, acc.UID),
	})
	writeJSON(w, map[string]any{
		"ok":      true,
		"account": acc.Redact(),
	})
}

// handleAuthStart 发起官方原生拟真登录授权流程。
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	mgr := credential.GetDefaultFlowManager(s.probe())
	flow, err := mgr.StartLoginFlow(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"state":   flow.State,
		"authUrl": flow.AuthURL,
	})
}

// handleAuthPoll 轮询官方登录授权状态。
func (s *Server) handleAuthPoll(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		writeErr(w, http.StatusBadRequest, "state 不能为空")
		return
	}
	mgr := credential.GetDefaultFlowManager(s.probe())
	res, err := mgr.PollLoginStatus(r.Context(), state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.Status == "success" {
		usage.InvalidateLiveCache(variant.Intl)
		s.note(ProxyLogEntry{
			Level: "info",
			Text:  fmt.Sprintf("官方拟真授权成功，已纳管并激活账号：%s (%s)", res.Nickname, res.UID),
		})
	}
	writeJSON(w, res)
}
