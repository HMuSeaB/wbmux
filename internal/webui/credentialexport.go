package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// handleCredentialExport 导出指定后端（默认国际版）的登录凭据与灾备配置。
//
// 支持单账号选择导出与全量多账号打包备份：
//  1. WebUI 页面直接查看/复制（返回选定账号、全量合辑与选项列表）；
//  2. 浏览器原生文件下载（download=true，携带 all=true 时下载全量备份包）。
func (s *Server) handleCredentialExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
		return
	}

	sideStr := strings.TrimSpace(r.URL.Query().Get("side"))
	if sideStr == "" {
		sideStr = string(variant.Intl)
	}
	targetID := variant.ID(sideStr)
	if targetID != variant.CN && targetID != variant.Intl {
		writeErr(w, http.StatusBadRequest, "不支持的目标档位："+sideStr)
		return
	}

	isDownload := r.URL.Query().Get("download") == "true"
	exportAll := r.URL.Query().Get("all") == "true"
	targetAccountID := strings.TrimSpace(r.URL.Query().Get("account_id"))

	// 读取多账号保险库与当前官方客户端真实物理凭据
	accs, _ := credential.ListAccounts(s.probe(), targetID)
	native, nativeErr := credential.Resolve(s.probe(), targetID)

	// 汇总所有可用账号选项（确保官方当前登录的账号标记为活跃）
	type accountOption struct {
		ID       string `json:"id"`
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
		Active   bool   `json:"active"`
	}
	var accountOptions []accountOption
	foundNativeInList := false

	for _, a := range accs {
		isCur := false
		if nativeErr == nil && native.Token != "" && a.UID == native.UID {
			foundNativeInList = true
			isCur = true
		}
		accountOptions = append(accountOptions, accountOption{
			ID:       a.ID,
			UID:      a.UID,
			Nickname: a.Nickname,
			Active:   isCur || a.Active,
		})
	}
	if !foundNativeInList && nativeErr == nil && native.Token != "" {
		name := native.Nickname
		if name == "" {
			name = "当前客户端账号"
		}
		accountOptions = append([]accountOption{{
			ID:       "native_current",
			UID:      native.UID,
			Nickname: name,
			Active:   true,
		}}, accountOptions...)
	}

	// 选定目标单账号
	token, refToken, uid, nickname, domain := "", "", "", "", ""
	var expiresAtMs, refExpiresAtMs int64

	if (targetAccountID == "" || targetAccountID == "active" || targetAccountID == "native_current") && nativeErr == nil && native.Token != "" {
		token = native.Token
		refToken = native.RefreshToken
		uid = native.UID
		nickname = native.Nickname
		domain = native.Domain
		expiresAtMs = native.ExpiresAtMs
		refExpiresAtMs = native.RefreshExpiresAtMs
		targetAccountID = "native_current"
	} else if targetAccountID != "" {
		for i := range accs {
			if accs[i].ID == targetAccountID {
				token = accs[i].Token
				refToken = accs[i].RefreshToken
				uid = accs[i].UID
				nickname = accs[i].Nickname
				domain = accs[i].Domain
				expiresAtMs = accs[i].ExpiresAt
				refExpiresAtMs = accs[i].RefreshExpiresAt
				break
			}
		}
	}

	// 兜底：若仍为空，取列表中首个可用账号
	if token == "" && len(accs) > 0 {
		token = accs[0].Token
		refToken = accs[0].RefreshToken
		uid = accs[0].UID
		nickname = accs[0].Nickname
		domain = accs[0].Domain
		expiresAtMs = accs[0].ExpiresAt
		refExpiresAtMs = accs[0].RefreshExpiresAt
		targetAccountID = accs[0].ID
	}

	if strings.TrimSpace(token) == "" && len(accountOptions) == 0 {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("未检测到可用的 %s 凭据，请先在官方客户端完成登录", targetID))
		return
	}

	if domain == "" {
		if targetID == variant.Intl {
			domain = "www.workbuddy.ai"
		} else {
			domain = "www.codebuddy.cn"
		}
	}
	if nickname == "" {
		nickname = uid
	}

	expiresHuman := "长期有效"
	if expiresAtMs > 0 {
		expiresHuman = time.UnixMilli(expiresAtMs).Format("2006-01-02 15:04:05")
	}

	// 单账号官方标准格式
	officialDoc := map[string]any{
		"auth": map[string]any{
			"accessToken":      token,
			"refreshToken":     refToken,
			"domain":           domain,
			"expiresAt":        expiresAtMs,
			"refreshExpiresAt": refExpiresAtMs,
		},
		"account": map[string]any{
			"uid":       uid,
			"nickname":  nickname,
			"type":      "personal",
			"lastLogin": true,
		},
	}

	// 全量多账号灾备备份包
	allBackup := map[string]any{
		"version":    1,
		"exportedAt": time.Now().Format(time.RFC3339),
		"side":       string(targetID),
		"accounts":   accs,
		"current":    officialDoc,
	}

	// 若为“全选导出”下载模式
	if isDownload && exportAll {
		filename := fmt.Sprintf("workbuddy-%s-all-accounts-backup-%s.json",
			targetID, time.Now().Format("20060102-150405"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(allBackup)
		return
	}

	// 若为“单账号导出”下载模式
	if isDownload {
		safeUID := uid
		if len(safeUID) > 8 {
			safeUID = safeUID[:8]
		}
		if safeUID == "" {
			safeUID = "acc"
		}
		filename := fmt.Sprintf("workbuddy-%s-credential-%s-%s.json",
			targetID, safeUID, time.Now().Format("20060102-150405"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(officialDoc)
		return
	}

	// Web 响应模式
	exportResult := map[string]any{
		"ok":             true,
		"side":           string(targetID),
		"version":        1,
		"exportedAt":     time.Now().Format(time.RFC3339),
		"options":        accountOptions,
		"selectedId":     targetAccountID,
		"account": map[string]any{
			"uid":            uid,
			"nickname":       nickname,
			"token":          token,
			"refreshToken":   refToken,
			"domain":         domain,
			"expiresAt":      expiresAtMs,
			"expiresAtHuman": expiresHuman,
		},
		"officialFormat": officialDoc,
		"allBackup":      allBackup,
	}

	writeJSON(w, exportResult)
}
