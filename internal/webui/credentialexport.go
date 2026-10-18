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
// 支持两种交互场景：
//  1. WebUI 页面直接查看/复制（返回标准 JSON 结构，包含明文、脱敏元数据与官方格式）；
//  2. 浏览器原生文件下载（Query 携带 download=true，附加 Content-Disposition 直接另存为 .json）。
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
	targetAccountID := strings.TrimSpace(r.URL.Query().Get("account_id"))

	// 1. 尝试从多账号保险库读取
	accs, _ := credential.ListAccounts(s.probe(), targetID)
	var activeAcc *credential.Account

	if targetAccountID != "" && targetAccountID != "active" {
		for i := range accs {
			if accs[i].ID == targetAccountID {
				activeAcc = &accs[i]
				break
			}
		}
	} else {
		for i := range accs {
			if accs[i].Active {
				activeAcc = &accs[i]
				break
			}
		}
	}

	// 2. 若多账号库未命中，直接探测官方客户端凭据
	token, refToken, uid, nickname, domain := "", "", "", "", ""
	var expiresAtMs, refExpiresAtMs int64

	if activeAcc != nil && activeAcc.Token != "" {
		token = activeAcc.Token
		refToken = activeAcc.RefreshToken
		uid = activeAcc.UID
		nickname = activeAcc.Nickname
		domain = activeAcc.Domain
		expiresAtMs = activeAcc.ExpiresAt
		refExpiresAtMs = activeAcc.RefreshExpiresAt
	} else {
		native, err := credential.Resolve(s.probe(), targetID)
		if err == nil && native.Token != "" {
			token = native.Token
			refToken = native.RefreshToken
			uid = native.UID
			nickname = native.Nickname
			domain = native.Domain
			expiresAtMs = native.ExpiresAtMs
			refExpiresAtMs = native.RefreshExpiresAtMs
		}
	}

	if strings.TrimSpace(token) == "" {
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

	// 构造官方原生文件格式（可直接还原到 auth/*.info）
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

	exportResult := map[string]any{
		"ok":         true,
		"side":       string(targetID),
		"version":    1,
		"exportedAt": time.Now().Format(time.RFC3339),
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
		"allAccounts":    accs,
	}

	// 若为直接下载模式，写入下载响应头
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
		_ = enc.Encode(exportResult)
		return
	}

	writeJSON(w, exportResult)
}
