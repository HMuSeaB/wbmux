package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/ccswitch"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/usage"
)

// errAbort 表示"这次改动被业务规则拒绝"，对应的 HTTP 状态码由调用方决定
// （400 / 404 等），不该被当成 500 内部错误。
var errAbort = errors.New("aborted")

// ---------- 自备提供方（BYOK）----------
//
// 一张卡片 = 一个 OpenAI 兼容端点 + 它的 key + 要注入的模型清单。
// 保存后立刻重新注入，国内客户端的模型选择器里就会出现「<卡片名> · <模型>」。
//
// 安全上两条硬规矩：
//   - key **永不回给界面**，只回最后 4 位（界面留空就表示"不改动"）；
//   - 注入到客户端目录里的条目持有的是**本机代理的令牌**，不是上游 key，
//     所以上游 key 不会落到客户端的数据目录里。

// providerView 是回给界面的卡片形态。
type providerView struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	BaseURL          string   `json:"baseURL"`
	KeyTail          string   `json:"keyTail"` // 只回最后 4 位
	HasKey           bool     `json:"hasKey"`
	Models           []string `json:"models"`
	MaxInputTokens   int      `json:"maxInputTokens"`
	MaxOutputTokens  int      `json:"maxOutputTokens"`
	SupportsToolCall bool     `json:"supportsToolCall"`
	SupportsImages   bool     `json:"supportsImages"`
}

// toView 转成界面形态，顺带把 key 截成尾巴。
func toView(p config.Provider) providerView {
	return providerView{
		ID:               p.ID,
		Name:             p.Name,
		BaseURL:          p.BaseURL,
		KeyTail:          keyTail(p.APIKey),
		HasKey:           p.APIKey != "",
		Models:           p.Models,
		MaxInputTokens:   p.MaxInputTokens,
		MaxOutputTokens:  p.MaxOutputTokens,
		SupportsToolCall: p.ToolsSupported(),
		SupportsImages:   p.ImagesSupported(),
	}
}

// keyTail 只保留最后 4 位。短 key 一律不给尾巴——那样等于把 key 给出去了。
func keyTail(k string) string {
	if len(k) <= 4 {
		return ""
	}
	return k[len(k)-4:]
}

// providerRequest 是界面提交的卡片内容。
type providerRequest struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	BaseURL          string   `json:"baseURL"`
	APIKey           string   `json:"apiKey"` // 留空 = 不改动已有 key
	Models           []string `json:"models"`
	MaxInputTokens   int      `json:"maxInputTokens"`
	MaxOutputTokens  int      `json:"maxOutputTokens"`
	SupportsToolCall *bool    `json:"supportsToolCall"`
	SupportsImages   *bool    `json:"supportsImages"`
	Remove           bool     `json:"remove"`
}

// handleProviders 读取 / 保存自备提供方。
//
//	GET  /api/providers  → 卡片列表（key 只有尾巴）
//	POST /api/providers  → 新增或修改一张卡（remove=true 则删除），保存后重新注入
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		views := make([]providerView, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			views = append(views, toView(p))
		}
		writeJSON(w, map[string]any{"providers": views})
	case http.MethodPost:
		s.saveProvider(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET / POST")
	}
}

func (s *Server) saveProvider(w http.ResponseWriter, r *http.Request) {
	var req providerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析："+err.Error())
		return
	}

	// 读-改-写在 updateConfig 的锁内完成：并发保存两张卡片时，后一次不会
	// 用旧快照把前一次刚加的卡片盖掉。
	var notFound bool
	var badReq string
	err := s.updateConfig(func(cfg *config.Config) error {
		if req.Remove {
			if strings.TrimSpace(req.ID) == "" {
				badReq = "缺少 id，不知道要删哪一张"
				return errAbort
			}
			list, found := config.RemoveProvider(cfg.Providers, req.ID)
			if !found {
				notFound = true
				return errAbort
			}
			cfg.Providers = list
			return nil
		}

		p := config.Provider{
			ID:               strings.TrimSpace(req.ID),
			Name:             strings.TrimSpace(req.Name),
			BaseURL:          strings.TrimSpace(req.BaseURL),
			APIKey:           strings.TrimSpace(req.APIKey),
			Models:           cleanModels(req.Models),
			MaxInputTokens:   req.MaxInputTokens,
			MaxOutputTokens:  req.MaxOutputTokens,
			SupportsToolCall: req.SupportsToolCall,
			SupportsImages:   req.SupportsImages,
		}
		if p.ID == "" {
			p.ID = config.NextProviderID(cfg.Providers)
		}
		// 留空表示"沿用原来的 key"：界面只显示尾巴，逼用户重新粘贴整串
		// 既没必要，也容易让人误以为 key 丢了。
		if p.APIKey == "" {
			if old := config.FindProvider(cfg.Providers, p.ID); old != nil {
				p.APIKey = old.APIKey
			}
		}
		if err := p.Validate(); err != nil {
			badReq = err.Error()
			return errAbort
		}
		cfg.Providers = config.UpsertProvider(cfg.Providers, p)
		return nil
	})
	if err == errAbort {
		switch {
		case badReq != "":
			writeErr(w, http.StatusBadRequest, badReq)
		case notFound:
			writeErr(w, http.StatusNotFound, "没有这张卡片（可能已经被删掉了）")
		}
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 保存后立刻重新注入：卡片改完就该能在客户端的模型选择器里看到
	// （客户端仍需重启一次才会重读 models.json，这一点在 note 里说清）。
	added, note, syncErr := s.syncInto()
	if syncErr != nil {
		s.note(ProxyLogEntry{Level: "bad", Text: "保存提供方后重新注入失败：" + compactError(syncErr.Error())})
		writeErr(w, http.StatusInternalServerError, "已保存，但重新注入失败："+syncErr.Error())
		return
	}
	cfg, _ := config.Load()
	views := make([]providerView, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		views = append(views, toView(p))
	}
	writeJSON(w, map[string]any{
		"providers": views,
		"added":     added,
		"note":      note,
	})
}

// cleanModels 去掉空白项与重复项，保持用户填的顺序。
func cleanModels(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// handleCCSwitchPreview 探测并预览 cc-switch 数据库中的模型提供方配置。
func (s *Server) handleCCSwitchPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	customPath := strings.TrimSpace(r.URL.Query().Get("path"))
	dbPath, exists := ccswitch.DetectDB(customPath)
	if !exists {
		writeJSON(w, map[string]any{
			"ok":    false,
			"found": false,
			"path":  dbPath,
			"note":  "未检测到 cc-switch 数据库文件（路径：" + dbPath + "）",
		})
		return
	}

	items, err := ccswitch.PreviewFromDB(s.probe(), dbPath)
	if err != nil {
		writeJSON(w, map[string]any{
			"ok":    false,
			"found": true,
			"path":  dbPath,
			"error": "读取数据库失败：" + err.Error(),
		})
		return
	}

	writeJSON(w, map[string]any{
		"ok":    true,
		"found": true,
		"path":  dbPath,
		"count": len(items),
		"items": items,
	})
}

// handleCCSwitchApply 将选中的 cc-switch 提供方批量导入为 BYOK 提供方，并立即同步生效。
func (s *Server) handleCCSwitchApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	var req struct {
		Items     []ccswitch.ImportItem `json:"items"`
		Overwrite bool                  `json:"overwrite"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求解析失败："+err.Error())
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, http.StatusBadRequest, "未选择任何要导入的提供方条目")
		return
	}

	var added, updated int
	err := s.updateConfig(func(cfg *config.Config) error {
		a, u, err := ccswitch.ApplyImport(cfg, req.Items, req.Overwrite)
		if err != nil {
			return err
		}
		added = a
		updated = u
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}

	// 重新注入到两端客户端
	_, syncNote, _ := s.syncInto()
	_, cnSyncNote, _ := s.syncCNIntoIntl()

	cfg, _ := config.Load()
	views := make([]providerView, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		views = append(views, toView(p))
	}

	note := fmt.Sprintf("成功导入 %d 个新提供方，更新 %d 个已有提供方", added, updated)
	writeJSON(w, map[string]any{
		"ok":         true,
		"added":      added,
		"updated":    updated,
		"total":      len(views),
		"note":       note,
		"syncNote":   syncNote,
		"cnSyncNote": cnSyncNote,
		"providers":  views,
	})
}

// handleCCSwitchExportPreview 探测 cc-switch 并准备导出 WorkBuddy/wbmux 模型配置的清单与预设。
func (s *Server) handleCCSwitchExportPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	customPath := strings.TrimSpace(r.URL.Query().Get("path"))
	dbPath, exists := ccswitch.DetectDB(customPath)

	cfg, _ := config.Load()

	var accounts map[string]*usage.LiveAccount
	if s.liveAccountsFn != nil {
		accounts = s.liveAccountsFn(s.probe())
	} else {
		accounts = usage.LiveAccounts(s.probe())
	}

	mode := strings.TrimSpace(r.URL.Query().Get("mode"))
	if mode == "" {
		mode = "merged"
	}
	models := ccswitch.ListExportableModels(&cfg, accounts, mode)

	baseURL := "http://" + s.Addr()
	if s.Addr() == "" {
		baseURL = "http://127.0.0.1:23333"
	}

	writeJSON(w, map[string]any{
		"ok":       true,
		"found":    exists,
		"path":     dbPath,
		"baseURL":  baseURL,
		"token":    s.token,
		"mode":     mode,
		"models":   models,
		"appTypes": []string{"claude", "claude-desktop", "opencode", "codex"},
	})
}

// handleCCSwitchExportApply 将选中的 WorkBuddy 模型一键导出写入到 cc-switch (cc-switch.db)。
func (s *Server) handleCCSwitchExportApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}

	var req ccswitch.ExportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求解析失败："+err.Error())
		return
	}

	if req.BaseURL == "" {
		req.BaseURL = "http://" + s.Addr()
	}
	if req.Token == "" {
		req.Token = s.token
	}

	inserted, err := ccswitch.ExportToCCSwitch(s.probe(), req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "导出至 cc-switch 失败："+err.Error())
		return
	}

	note := fmt.Sprintf("成功将 %d 个配置项一键导入到 cc-switch！您可在 Claude Code、OpenCode 或 Codex 中一键切换", inserted)
	writeJSON(w, map[string]any{
		"ok":       true,
		"inserted": inserted,
		"note":     note,
	})
}
