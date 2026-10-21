package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// handleInjectCustomModels 把 wbmux 能提供的模型注入国内客户端的自定义
// 模型清单：国际限时免费模型（经代理转发、扣国际额度）+ 自备提供方（BYOK），
// 实现"国内壳子里无缝用别的模型"。
func (s *Server) handleInjectCustomModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	added, note, err := s.syncInto()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"added": added, "note": note})
}

// handleIntlModelSelection 获取或更新自选的国际付费模型注入列表
func (s *Server) handleIntlModelSelection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"selected": cfg.SelectedIntlModels,
		})
	case http.MethodPost:
		var req struct {
			Selected []string `json:"selected"`
			Model    string   `json:"model"`
			Action   string   `json:"action"` // "toggle", "add", "remove"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求解析失败："+err.Error())
			return
		}

		cfg, err := config.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		var updated []string
		if req.Model != "" {
			modelID := strings.TrimSpace(req.Model)
			current := make(map[string]bool)
			for _, m := range cfg.SelectedIntlModels {
				current[strings.ToLower(m)] = true
			}
			targetLower := strings.ToLower(modelID)
			switch req.Action {
			case "remove":
				delete(current, targetLower)
			case "add":
				current[targetLower] = true
			case "toggle", "":
				if current[targetLower] {
					delete(current, targetLower)
				} else {
					current[targetLower] = true
				}
			}
			// 维持已有顺序并去重
			for _, m := range cfg.SelectedIntlModels {
				if current[strings.ToLower(m)] {
					updated = append(updated, m)
					delete(current, strings.ToLower(m))
				}
			}
			if current[targetLower] {
				updated = append(updated, modelID)
			}
		} else {
			// 批量覆盖模式，清洗与去重
			seen := make(map[string]bool)
			for _, m := range req.Selected {
				clean := strings.TrimSpace(m)
				if clean != "" && !seen[strings.ToLower(clean)] {
					seen[strings.ToLower(clean)] = true
					updated = append(updated, clean)
				}
			}
		}

		cfg.SelectedIntlModels = updated
		if err := config.Save(cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
			return
		}

		// 每次选定或取消后，立即触发 syncInto()，实时同步到 models.json
		added, note, syncErr := s.syncInto()
		errMsg := ""
		if syncErr != nil {
			errMsg = syncErr.Error()
		}

		writeJSON(w, map[string]any{
			"ok":       true,
			"selected": cfg.SelectedIntlModels,
			"added":    added,
			"note":     note,
			"syncErr":  errMsg,
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
	}
}

// syncInto 把当前能提供的模型写进国内客户端的清单，返回注入条数与一句
// 给用户看的话。
//
// 来源：国际侧免费/自选模型 + 自备提供方（BYOK）。互相独立，国际侧凭据不可用时
// 仍然要注入自备提供方的模型。
func (s *Server) syncInto() (int, string, error) {
	cfg, err := config.Load()
	if err != nil {
		// 设置读不动只影响 BYOK 那部分，不该挡住国际侧的注入。
		s.note(ProxyLogEntry{Level: "bad", Text: "读取设置失败（自备提供方将不注入）：" + compactError(err.Error())})
		cfg = config.Config{}
	}

	var intlErr string
	var intl []usage.LiveModel
	if live := usage.LiveAccounts(s.probe())[string(variant.Intl)]; live != nil && live.OK {
		intl = live.Models
	} else if live != nil && live.Err != "" {
		intlErr = live.Err
	} else {
		intlErr = "国际侧凭据不可用"
	}

	base := "http://" + s.Addr() + "/v1/chat/completions"
	path := filepath.Join(s.probe().DataDir(variant.CN), "models.json")

	// 遵循"限流也不能不注入"原则：全量注入所有可用模型。
	// 即使某个模型刚才偶发频控，也不能剥夺客户端的模型入口；
	// 网关底层已配备 Auto-Failover 多账号故障转移与重试兜底。
	var rateLimitedWarn []string
	for _, m := range intl {
		if q := s.quotaFor(m.ID); q != nil && (q.State == "rate_limited" || q.State == "exhausted") {
			rateLimitedWarn = append(rateLimitedWarn, m.ID)
		}
	}

	added, err := custommodels.Sync(path, base, s.token, intl, cfg.Providers, cfg.SelectedIntlModels)
	if err != nil {
		return 0, "", err
	}

	selectedSet := make(map[string]bool, len(cfg.SelectedIntlModels))
	for _, id := range cfg.SelectedIntlModels {
		if clean := strings.ToLower(strings.TrimSpace(id)); clean != "" {
			selectedSet[clean] = true
		}
	}

	free := 0
	paid := 0
	for _, m := range intl {
		if m.FreeNow {
			free++
		} else if selectedSet[strings.ToLower(m.ID)] {
			paid++
		}
	}
	own := 0
	for _, p := range cfg.Providers {
		own += len(p.Models)
	}

	var detailParts []string
	if free > 0 {
		detailParts = append(detailParts, fmt.Sprintf("国际免费 %d", free))
	}
	if paid > 0 {
		detailParts = append(detailParts, fmt.Sprintf("国际付费 %d", paid))
	}
	if own > 0 {
		detailParts = append(detailParts, fmt.Sprintf("自备提供方 %d", own))
	}
	detailStr := strings.Join(detailParts, " + ")
	if detailStr == "" {
		detailStr = "0"
	}

	parts := fmt.Sprintf("已注入 %d 个模型（%s）", added, detailStr)
	if intlErr != "" {
		parts += "；国际侧这一轮没注入：" + intlErr
	}
	if len(rateLimitedWarn) > 0 {
		parts += "；注：" + strings.Join(rateLimitedWarn, "、") + " 刚才曾有上游频控，网关已配备自动故障转移守护"
	}
	if added == free {
		// 一个自有模型都没注入：多半是还没建卡片，顺带提一句入口在哪。
		parts += "。想接自己的 key：侧边栏「提供方」页"
	}
	parts += "。重启国内客户端后在模型选择器里可见"
	return added, parts, nil
}

// SyncCustomModels 是 GUI 启动时的自动同步入口（模型清单会变，每次对齐
// 一遍）。失败只记日志，不该挡住界面。
func (s *Server) SyncCustomModels() (int, error) {
	added, note, err := s.syncInto()
	if err != nil {
		return 0, err
	}
	if note != "" {
		s.note(ProxyLogEntry{Level: "info", Text: note})
	}
	return added, nil
}
