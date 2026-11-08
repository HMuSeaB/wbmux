// Package ccswitch 支持从本地 cc-switch (Claude Code / OpenCode / Codex 跨客户端模型配置管理器)
// 的 SQLite 数据库（~/.cc-switch/cc-switch.db）中直接提取第三方 Provider 模型端点与密钥，
// 一键快捷导入为 wbmux 的自备提供方（BYOK）。
package ccswitch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// DefaultDBPath 返回 cc-switch 默认的数据库存储路径。
func DefaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cc-switch", "cc-switch.db")
}

// DetectDB 检查指定路径（若为空则为默认路径）的 cc-switch 数据库是否存在。
func DetectDB(customPath string) (string, bool) {
	target := strings.TrimSpace(customPath)
	if target == "" {
		target = DefaultDBPath()
	}
	if target == "" {
		return "", false
	}
	fi, err := os.Stat(target)
	if err != nil || fi.IsDir() {
		return target, false
	}
	return target, true
}

// ImportItem 是从 cc-switch 提取后用于展示与导入的条目。
type ImportItem struct {
	ID               string   `json:"id"`
	AppType          string   `json:"appType"`
	Name             string   `json:"name"`
	BaseURL          string   `json:"baseURL"`
	APIKey           string   `json:"apiKey,omitempty"`
	KeyTail          string   `json:"keyTail"`
	HasKey           bool     `json:"hasKey"`
	Models           []string `json:"models"`
	WebsiteURL       string   `json:"websiteURL,omitempty"`
	SupportsToolCall bool     `json:"supportsToolCall"`
	SupportsImages   bool     `json:"supportsImages"`
	Selected         bool     `json:"selected"`
}

// keyTail 只保留凭据后 4 位，短凭据或空凭据不给尾巴。
func keyTail(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 4 {
		return ""
	}
	return k[len(k)-4:]
}

var (
	reCodexBaseURL = regexp.MustCompile(`(?i)base_url\s*=\s*["']([^"']+)["']`)
	reCodexModel   = regexp.MustCompile(`(?i)model\s*=\s*["']([^"']+)["']`)
)

// ParseSettingsConfig 解析 cc-switch 存放在 settings_config 列中的 JSON/配置串，
// 提取 BaseURL、APIKey 与模型名称列表。
func ParseSettingsConfig(appType, name, rawConfig string) (baseURL, apiKey string, models []string) {
	appType = strings.ToLower(strings.TrimSpace(appType))
	rawConfig = strings.TrimSpace(rawConfig)
	if rawConfig == "" {
		return "", "", nil
	}

	var root map[string]any
	_ = json.Unmarshal([]byte(rawConfig), &root)

	modelSet := make(map[string]bool)
	addModel := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !modelSet[m] {
			modelSet[m] = true
			models = append(models, m)
		}
	}

	if root != nil {
		// 1. claude / claude-desktop 类型
		if env, ok := root["env"].(map[string]any); ok {
			for _, k := range []string{"ANTHROPIC_BASE_URL", "BASE_URL", "ANTHROPIC_URL"} {
				if v, _ := env[k].(string); strings.TrimSpace(v) != "" {
					baseURL = strings.TrimSpace(v)
					break
				}
			}
			for _, k := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "API_KEY"} {
				if v, _ := env[k].(string); strings.TrimSpace(v) != "" {
					apiKey = strings.TrimSpace(v)
					break
				}
			}
			for _, k := range []string{
				"ANTHROPIC_MODEL",
				"ANTHROPIC_DEFAULT_SONNET_MODEL",
				"ANTHROPIC_DEFAULT_OPUS_MODEL",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL",
				"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME",
				"CLAUDE_CODE_SUBAGENT_MODEL",
			} {
				if v, _ := env[k].(string); strings.TrimSpace(v) != "" {
					addModel(v)
				}
			}
		}

		// 2. opencode 类型
		if opts, ok := root["options"].(map[string]any); ok {
			if baseURL == "" {
				for _, k := range []string{"baseURL", "baseUrl", "base_url"} {
					if v, _ := opts[k].(string); strings.TrimSpace(v) != "" {
						baseURL = strings.TrimSpace(v)
						break
					}
				}
			}
			if apiKey == "" {
				for _, k := range []string{"apiKey", "apikey", "api_key"} {
					if v, _ := opts[k].(string); strings.TrimSpace(v) != "" {
						apiKey = strings.TrimSpace(v)
						break
					}
				}
			}
		}
		if mObj, ok := root["models"].(map[string]any); ok {
			for k := range mObj {
				addModel(k)
			}
		} else if mArr, ok := root["models"].([]any); ok {
			for _, item := range mArr {
				if s, ok := item.(string); ok {
					addModel(s)
				} else if mItem, ok := item.(map[string]any); ok {
					if id, ok := mItem["id"].(string); ok {
						addModel(id)
					} else if nm, ok := mItem["name"].(string); ok {
						addModel(nm)
					}
				}
			}
		}

		// 3. codex 类型
		if auth, ok := root["auth"].(map[string]any); ok {
			if apiKey == "" {
				for _, k := range []string{"OPENAI_API_KEY", "apiKey", "api_key"} {
					if v, _ := auth[k].(string); strings.TrimSpace(v) != "" {
						apiKey = strings.TrimSpace(v)
						break
					}
				}
			}
		}
		if cnfStr, ok := root["config"].(string); ok && cnfStr != "" {
			if baseURL == "" {
				if m := reCodexBaseURL.FindStringSubmatch(cnfStr); len(m) > 1 {
					baseURL = strings.TrimSpace(m[1])
				}
			}
			if m := reCodexModel.FindStringSubmatch(cnfStr); len(m) > 1 {
				addModel(m[1])
			}
		}

		// 4. 根字段兜底（如 hermes 或通用 OpenAI 提供方）
		if baseURL == "" {
			for _, k := range []string{"base_url", "baseURL", "baseUrl", "endpoint", "url"} {
				if v, _ := root[k].(string); strings.TrimSpace(v) != "" {
					baseURL = strings.TrimSpace(v)
					break
				}
			}
		}
		if apiKey == "" {
			for _, k := range []string{"api_key", "apiKey", "key", "token"} {
				if v, _ := root[k].(string); strings.TrimSpace(v) != "" {
					apiKey = strings.TrimSpace(v)
					break
				}
			}
		}
	}

	// 如果没有从配置中解析出模型名，尝试将 Provider 的名称作为模型兜底
	if len(models) == 0 && strings.TrimSpace(name) != "" {
		cleaned := strings.TrimSpace(name)
		// 如果名字带有斜杠如 minimaxai/minimax-m2.1 或带 - 或包含特定模型关键字
		addModel(cleaned)
	}

	return baseURL, apiKey, models
}

// PreviewFromDB 从指定路径的 cc-switch 数据库中读取所有可用的 Provider 条目。
func PreviewFromDB(probe *variant.Probe, dbPath string) ([]ImportItem, error) {
	if probe == nil {
		probe = variant.DefaultProbe()
	}
	dbPath, exists := DetectDB(dbPath)
	if !exists {
		return nil, fmt.Errorf("找不到 cc-switch 数据库文件: %s", dbPath)
	}

	sql := "SELECT id, app_type, name, settings_config, website_url FROM providers ORDER BY app_type, name"
	rows, err := migrate.QueryDB(probe, variant.CN, dbPath, sql)
	if err != nil {
		return nil, fmt.Errorf("读取 cc-switch 数据库失败: %w", err)
	}

	var items []ImportItem
	for _, r := range rows {
		id, _ := r["id"].(string)
		appType, _ := r["app_type"].(string)
		name, _ := r["name"].(string)
		rawConfig, _ := r["settings_config"].(string)
		webURL, _ := r["website_url"].(string)

		baseURL, apiKey, models := ParseSettingsConfig(appType, name, rawConfig)
		// 忽略既没有 BaseURL 又没有 APIKey 的纯预设占位项（例如官方直连空卡片）
		if baseURL == "" {
			continue
		}

		cleanName := strings.TrimSpace(name)
		if cleanName == "" {
			cleanName = fmt.Sprintf("cc-%s-%s", appType, id[:8])
		}

		item := ImportItem{
			ID:               id,
			AppType:          appType,
			Name:             cleanName,
			BaseURL:          baseURL,
			APIKey:           apiKey,
			KeyTail:          keyTail(apiKey),
			HasKey:           apiKey != "",
			Models:           models,
			WebsiteURL:       webURL,
			SupportsToolCall: true,
			SupportsImages:   false,
			Selected:         true,
		}
		items = append(items, item)
	}

	return items, nil
}

// safeProviderID 生成格式规范的 Provider ID（形如 p_cc_xxxx）。
func safeProviderID(rawID, name string) string {
	h := sha256.Sum256([]byte(rawID + "\n" + name))
	return "cc_" + hex.EncodeToString(h[:4])
}

// ApplyImport 将选中的 ImportItem 合并导入到当前的 wbmux config.Providers 中。
// overwrite 参数决定当存在同名提供方时是否覆盖已有卡片。
func ApplyImport(cfg *config.Config, selectedItems []ImportItem, overwrite bool) (added int, updated int, err error) {
	if cfg == nil {
		return 0, 0, fmt.Errorf("配置对象为空")
	}

	existingByName := make(map[string]int)
	existingByID := make(map[string]int)
	for idx, p := range cfg.Providers {
		existingByName[strings.ToLower(strings.TrimSpace(p.Name))] = idx
		existingByID[p.ID] = idx
	}

	for _, item := range selectedItems {
		nameKey := strings.ToLower(strings.TrimSpace(item.Name))
		idx, foundByName := existingByName[nameKey]

		toolsVal := item.SupportsToolCall
		imagesVal := item.SupportsImages

		targetID := safeProviderID(item.ID, item.Name)
		if foundByName {
			if !overwrite {
				// 不覆盖已有同名提供方
				continue
			}
			// 覆盖更新
			old := &cfg.Providers[idx]
			old.BaseURL = item.BaseURL
			if item.APIKey != "" {
				old.APIKey = item.APIKey
			}
			if len(item.Models) > 0 {
				old.Models = item.Models
			}
			old.SupportsToolCall = &toolsVal
			old.SupportsImages = &imagesVal
			updated++
		} else {
			// 如果生成的 targetID 冲突，追加序号
			baseID := targetID
			counter := 1
			for {
				if _, exists := existingByID[targetID]; !exists {
					break
				}
				targetID = fmt.Sprintf("%s_%d", baseID, counter)
				counter++
			}

			newP := config.Provider{
				ID:               targetID,
				Name:             item.Name,
				BaseURL:          item.BaseURL,
				APIKey:           item.APIKey,
				Models:           item.Models,
				SupportsToolCall: &toolsVal,
				SupportsImages:   &imagesVal,
			}
			cfg.Providers = append(cfg.Providers, newP)
			existingByName[nameKey] = len(cfg.Providers) - 1
			existingByID[targetID] = len(cfg.Providers) - 1
			added++
		}
	}

	return added, updated, nil
}

// ExportModel 是准备导出至 cc-switch 的模型条目。
type ExportModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"` // "auto", "cn", "intl", "byok"
	Selected bool   `json:"selected"`
}

// ExportRequest 定义将 wbmux 模型导出至 cc-switch 的请求参数。
type ExportRequest struct {
	DBPath   string        `json:"dbPath,omitempty"`
	AppTypes []string      `json:"appTypes"` // e.g. ["claude", "claude-desktop", "opencode", "codex"]
	Models   []ExportModel `json:"models"`
	BaseURL  string        `json:"baseURL"`  // e.g. "http://127.0.0.1:23333"
	Token    string        `json:"token"`
}

// safeModelSlug 将模型 ID 转为适用于 cc-switch provider id 的安全字符串。
func safeModelSlug(id string) string {
	s := strings.ToLower(strings.TrimSpace(id))
	reg := regexp.MustCompile(`[^a-z0-9_-]+`)
	s = reg.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// ListExportableModels 根据当前配置与实时可用模型，生成导出到 cc-switch 的候选模型列表。
func ListExportableModels(cfg *config.Config, accounts map[string]*usage.LiveAccount) []ExportModel {
	var list []ExportModel

	// 1. 智能免积分优选
	list = append(list, ExportModel{
		ID:       "auto-free",
		Name:     "智能免积分优选 (Auto-Free)",
		Category: "auto",
		Selected: true,
	})

	// 2. 国内端核心精选
	curatedSet := make(map[string]bool)
	for _, m := range config.DefaultCuratedCNModels {
		curatedSet[strings.ToLower(m)] = true
	}
	if cfg != nil && len(cfg.SelectedCNModels) > 0 {
		curatedSet = make(map[string]bool)
		for _, m := range cfg.SelectedCNModels {
			curatedSet[strings.ToLower(m)] = true
		}
	}

	curatedNames := []struct {
		id   string
		name string
	}{
		{"minimax-m2.5", "MiniMax-M2.5 (国内包月)"},
		{"glm-5.1", "GLM-5.1 (国内包月)"},
		{"kimi-k2.6", "Kimi-K2.6 (国内包月)"},
		{"hy3", "混元 3.0 (国内包月)"},
		{"deepseek-v4-flash", "DeepSeek-V4-Flash (国内专享)"},
		{"hunyuan-chat", "混元聊天 (国内包月)"},
	}

	for _, item := range curatedNames {
		sel := curatedSet[strings.ToLower(item.id)]
		list = append(list, ExportModel{
			ID:       custommodels.IDPrefixCN + item.id,
			Name:     item.name,
			Category: "cn",
			Selected: sel,
		})
	}

	// 3. 国际端限免模型
	list = append(list, ExportModel{
		ID:       "deepseek-chat",
		Name:     "DeepSeek-Chat (国际限免/官方)",
		Category: "intl",
		Selected: true,
	})
	list = append(list, ExportModel{
		ID:       "gpt-4o",
		Name:     "GPT-4o (国际高级)",
		Category: "intl",
		Selected: false,
	})

	// 4. 用户自备 BYOK
	if cfg != nil {
		for _, p := range cfg.Providers {
			for _, m := range p.Models {
				m = strings.TrimSpace(m)
				if m == "" {
					continue
				}
				list = append(list, ExportModel{
					ID:       p.EntryPrefix() + m,
					Name:     fmt.Sprintf("%s · %s (BYOK)", p.Name, m),
					Category: "byok",
					Selected: false,
				})
			}
		}
	}

	return list
}

// ExportToCCSwitch 将指定的 wbmux 模型和网关配置写入到 cc-switch 的 SQLite 数据库中。
func ExportToCCSwitch(probe *variant.Probe, req ExportRequest) (int, error) {
	if probe == nil {
		probe = variant.DefaultProbe()
	}
	dbPath, exists := DetectDB(req.DBPath)
	if !exists {
		return 0, fmt.Errorf("找不到 cc-switch 数据库文件: %s", dbPath)
	}
	if len(req.AppTypes) == 0 {
		req.AppTypes = []string{"claude", "claude-desktop"}
	}
	if len(req.Models) == 0 {
		return 0, fmt.Errorf("未选择任何要导出的模型")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:23333"
	}
	token := strings.TrimSpace(req.Token)

	nowMs := time.Now().UnixMilli()
	var stmts []migrate.Statement

	for _, appType := range req.AppTypes {
		appType = strings.ToLower(strings.TrimSpace(appType))
		switch appType {
		case "claude", "claude-desktop":
			for _, m := range req.Models {
				id := fmt.Sprintf("wbmux-%s", safeModelSlug(m.ID))
				name := fmt.Sprintf("WorkBuddy / %s", m.Name)
				envConfig := map[string]any{
					"env": map[string]any{
						"ANTHROPIC_BASE_URL":                  baseURL,
						"ANTHROPIC_AUTH_TOKEN":                token,
						"ANTHROPIC_MODEL":                     m.ID,
						"ANTHROPIC_DEFAULT_SONNET_MODEL":      m.ID,
						"ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": m.ID,
						"ANTHROPIC_DEFAULT_OPUS_MODEL":        m.ID,
						"ANTHROPIC_DEFAULT_HAIKU_MODEL":       m.ID,
						"CLAUDE_CODE_SUBAGENT_MODEL":          m.ID,
					},
				}
				rawCfg, _ := json.Marshal(envConfig)
				meta := `{"folder": "WorkBuddy", "endpointAutoSelect": true, "apiFormat": "anthropic"}`

				sql := `INSERT OR REPLACE INTO providers (
					id, app_type, name, settings_config, website_url, category, created_at, sort_index, meta, is_current, in_failover_queue, cost_multiplier
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
				params := []any{
					id, appType, name, string(rawCfg), baseURL, "workbuddy", nowMs, 0, meta, 0, 0, "1.0",
				}
				stmts = append(stmts, migrate.Statement{SQL: sql, Params: params})
			}
		case "opencode":
			modelsMap := make(map[string]any)
			for _, m := range req.Models {
				modelsMap[m.ID] = map[string]string{"name": m.Name}
			}
			openCodeConfig := map[string]any{
				"npm": "@ai-sdk/openai",
				"options": map[string]any{
					"baseURL": baseURL + "/v1",
					"apiKey":  token,
				},
				"models": modelsMap,
			}
			rawCfg, _ := json.Marshal(openCodeConfig)
			meta := `{"folder": "WorkBuddy", "endpointAutoSelect": true}`

			sql := `INSERT OR REPLACE INTO providers (
				id, app_type, name, settings_config, website_url, category, created_at, sort_index, meta, is_current, in_failover_queue, cost_multiplier
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			params := []any{
				"wbmux-opencode", appType, "WorkBuddy (wbmux)", string(rawCfg), baseURL, "workbuddy", nowMs, 0, meta, 0, 0, "1.0",
			}
			stmts = append(stmts, migrate.Statement{SQL: sql, Params: params})

		case "codex":
			firstModel := "auto-free"
			if len(req.Models) > 0 {
				firstModel = req.Models[0].ID
			}
			codexToml := fmt.Sprintf("model_provider = \"custom\"\nmodel = %q\nmodel_reasoning_effort = \"high\"\ndisable_response_storage = true\n\n[model_providers.custom]\nname = \"WorkBuddy\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = true\n", firstModel, baseURL+"/v1")
			codexConfig := map[string]any{
				"auth": map[string]any{
					"OPENAI_API_KEY": token,
				},
				"config": codexToml,
			}
			rawCfg, _ := json.Marshal(codexConfig)
			meta := `{"folder": "WorkBuddy"}`

			sql := `INSERT OR REPLACE INTO providers (
				id, app_type, name, settings_config, website_url, category, created_at, sort_index, meta, is_current, in_failover_queue, cost_multiplier
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
			params := []any{
				"wbmux-codex", appType, "WorkBuddy (wbmux)", string(rawCfg), baseURL, "workbuddy", nowMs, 0, meta, 0, 0, "1.0",
			}
			stmts = append(stmts, migrate.Statement{SQL: sql, Params: params})
		}
	}

	changes, err := migrate.UpsertDB(probe, variant.CN, dbPath, []string{"providers"}, stmts)
	if err != nil {
		return 0, fmt.Errorf("写入 cc-switch 数据库失败: %w", err)
	}

	total := 0
	for _, c := range changes {
		total += c
	}
	if total == 0 && len(stmts) > 0 {
		total = len(stmts)
	}
	return total, nil
}
