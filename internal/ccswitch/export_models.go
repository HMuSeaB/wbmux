package ccswitch

import (
	"fmt"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/usage"
)

// ExportModel 是准备导出至 cc-switch 的模型条目。
type ExportModel struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"` // "auto", "free", "intl", "cn", "byok"
	Channel  string `json:"channel"`  // "auto", "intl", "cn", "byok"
	Selected bool   `json:"selected"`
}

// CuratedModelDef 是核心推荐模型的定义元数据。
type CuratedModelDef struct {
	ID       string
	Name     string
	Category string
	Channel  string
	Selected bool
}

// CoreFreeModels 当前限时免费核心款。
var CoreFreeModels = []CuratedModelDef{
	{ID: "hy4free", Name: "Hy4 (官方限免 0.00x · 1M 上下文)", Category: "free", Channel: "intl", Selected: true},
	{ID: "hy3", Name: "Hy3 (官方限免 0.00x · 192k 思考模型)", Category: "free", Channel: "intl", Selected: true},
}

// CoreIntlPaidModels 当前花积分的高频特惠核心款（以官方 GLM-5.3 与 DeepSeek 真实上线版本为主）。
var CoreIntlPaidModels = []CuratedModelDef{
	{ID: "deepseek-v4.1-flash-sg", Name: "DeepSeek-V4.1-Flash (夜间折扣 0.11x · 1M)", Category: "intl", Channel: "intl", Selected: true},
	{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash (订阅优先 0.08x · 1M)", Category: "intl", Channel: "intl", Selected: true},
	{ID: "glm-5.3", Name: "GLM-5.3 (订阅优先 0.69x · 1M)", Category: "intl", Channel: "intl", Selected: true},
}

// CoreCNModels 国内专享核心款（全面升级为 GLM-5.3 系列）。
var CoreCNModels = []CuratedModelDef{
	{ID: "minimax-m2.5", Name: "MiniMax-M2.5 (国内包月)", Category: "cn", Channel: "cn", Selected: true},
	{ID: "glm-5.3", Name: "GLM-5.3 (国内包月)", Category: "cn", Channel: "cn", Selected: true},
	{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash (国内专享)", Category: "cn", Channel: "cn", Selected: true},
	{ID: "kimi-k2.6", Name: "Kimi-K2.6 (国内包月)", Category: "cn", Channel: "cn", Selected: true},
	{ID: "hy3", Name: "混元 3.0 (国内包月 · 极速)", Category: "cn", Channel: "cn", Selected: true},
	{ID: "deepseek-v4-flash", Name: "DeepSeek-V4-Flash (国内专享)", Category: "cn", Channel: "cn", Selected: true},
	{ID: "hunyuan-chat", Name: "混元聊天 (国内包月)", Category: "cn", Channel: "cn", Selected: true},
}

// ListExportableModels 根据当前配置、实时可用模型与选择模式（merged 或 split），
// 生成导出到 cc-switch 的候选模型列表。
//
// mode 可取值：
//   - "merged" (默认): 自动去重合并，消灭重复同名模型，输出单一精简列表；
//   - "split": 按通道严格分开，国内通道与国际通道独立并存，便于精细控制物理节点。
func ListExportableModels(cfg *config.Config, accounts map[string]*usage.LiveAccount, mode ...string) []ExportModel {
	m := "merged"
	if len(mode) > 0 && strings.ToLower(strings.TrimSpace(mode[0])) == "split" {
		m = "split"
	}

	if m == "split" {
		return listExportableModelsSplit(cfg, accounts)
	}
	return listExportableModelsMerged(cfg, accounts)
}

// listExportableModelsMerged 聚合极简模式：自动去重合并，避免双胞胎混淆。
func listExportableModelsMerged(cfg *config.Config, accounts map[string]*usage.LiveAccount) []ExportModel {
	var list []ExportModel
	addedMap := make(map[string]bool)

	// 1. 智能免积分优选
	list = append(list, ExportModel{
		ID:       "auto-free",
		Name:     "智能免积分优选 (Auto-Free · 最速限免)",
		Category: "auto",
		Channel:  "auto",
		Selected: true,
	})
	addedMap["auto-free"] = true

	// 2. 官方限免核心款 (Hy4, Hy3)
	for _, m := range CoreFreeModels {
		list = append(list, ExportModel{
			ID:       m.ID,
			Name:     m.Name,
			Category: m.Category,
			Channel:  m.Channel,
			Selected: m.Selected,
		})
		addedMap[strings.ToLower(m.ID)] = true
	}

	// 3. 动态追加实时探测中发现的其他限免模型
	if accounts != nil {
		if intlAcc, ok := accounts["intl"]; ok && intlAcc != nil {
			for _, m := range intlAcc.Models {
				if m.FreeNow && !addedMap[strings.ToLower(m.ID)] {
					list = append(list, ExportModel{
						ID:       m.ID,
						Name:     fmt.Sprintf("%s (官方限免 0.00x)", m.Name),
						Category: "free",
						Channel:  "intl",
						Selected: true,
					})
					addedMap[strings.ToLower(m.ID)] = true
				}
			}
		}
	}

	// 4. 官方高频特惠核心款 (DeepSeek 0.11x, GLM-5.3-Flash 0.08x, GLM-5.3 0.69x)
	for _, m := range CoreIntlPaidModels {
		if !addedMap[strings.ToLower(m.ID)] {
			list = append(list, ExportModel{
				ID:       m.ID,
				Name:     m.Name,
				Category: m.Category,
				Channel:  m.Channel,
				Selected: m.Selected,
			})
			addedMap[strings.ToLower(m.ID)] = true
		}
	}

	// 用户在配置中自选的其他国际模型
	if cfg != nil && len(cfg.SelectedIntlModels) > 0 {
		for _, customIntl := range cfg.SelectedIntlModels {
			low := strings.ToLower(customIntl)
			if !addedMap[low] {
				list = append(list, ExportModel{
					ID:       customIntl,
					Name:     fmt.Sprintf("%s (国际自选)", customIntl),
					Category: "intl",
					Channel:  "intl",
					Selected: true,
				})
				addedMap[low] = true
			}
		}
	}

	// 5. 国内专享核心款（聚合模式下自动排除同名重叠项如 hy3、glm-5.3，保留国内真正独占的模型）
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

	// 聚合模式下国内独占款：MiniMax, Kimi, DeepSeek-V4-Flash, 混元聊天
	exclusiveCNModels := []struct {
		id   string
		name string
	}{
		{"minimax-m2.5", "MiniMax-M2.5 (国内包月)"},
		{"kimi-k2.6", "Kimi-K2.6 (国内包月)"},
		{"deepseek-v4-flash", "DeepSeek-V4-Flash (国内专享)"},
		{"hunyuan-chat", "混元聊天 (国内包月)"},
	}
	for _, item := range exclusiveCNModels {
		sel := curatedSet[strings.ToLower(item.id)]
		list = append(list, ExportModel{
			ID:       custommodels.IDPrefixCN + item.id,
			Name:     item.name,
			Category: "cn",
			Channel:  "cn",
			Selected: sel,
		})
	}

	// 6. 用户自备 BYOK
	list = appendBYOKModels(list, cfg)
	return list
}

// listExportableModelsSplit 分端独立模式：双轨并立，明确区分国内物理节点与国际物理节点。
func listExportableModelsSplit(cfg *config.Config, accounts map[string]*usage.LiveAccount) []ExportModel {
	var list []ExportModel
	addedMap := make(map[string]bool)

	// 1. 智能免积分优选
	list = append(list, ExportModel{
		ID:       "auto-free",
		Name:     "智能免积分优选 (Auto-Free · 最速限免)",
		Category: "auto",
		Channel:  "auto",
		Selected: true,
	})

	// 2. 国内专享通道 (全量呈现国内精选模型)
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
	for _, m := range CoreCNModels {
		sel := curatedSet[strings.ToLower(m.ID)]
		list = append(list, ExportModel{
			ID:       custommodels.IDPrefixCN + m.ID,
			Name:     m.Name,
			Category: "cn",
			Channel:  "cn",
			Selected: sel,
		})
	}

	// 3. 国际通道：限免核心款
	for _, m := range CoreFreeModels {
		list = append(list, ExportModel{
			ID:       m.ID,
			Name:     m.Name,
			Category: m.Category,
			Channel:  m.Channel,
			Selected: m.Selected,
		})
		addedMap[strings.ToLower(m.ID)] = true
	}

	// 动态追加实时探测中发现的其他国际限免
	if accounts != nil {
		if intlAcc, ok := accounts["intl"]; ok && intlAcc != nil {
			for _, m := range intlAcc.Models {
				if m.FreeNow && !addedMap[strings.ToLower(m.ID)] {
					list = append(list, ExportModel{
						ID:       m.ID,
						Name:     fmt.Sprintf("%s (国际限免 0.00x)", m.Name),
						Category: "free",
						Channel:  "intl",
						Selected: true,
					})
					addedMap[strings.ToLower(m.ID)] = true
				}
			}
		}
	}

	// 4. 国际通道：花积分特惠款
	for _, m := range CoreIntlPaidModels {
		if !addedMap[strings.ToLower(m.ID)] {
			list = append(list, ExportModel{
				ID:       m.ID,
				Name:     m.Name,
				Category: m.Category,
				Channel:  m.Channel,
				Selected: m.Selected,
			})
			addedMap[strings.ToLower(m.ID)] = true
		}
	}

	// 用户在配置中自选的其他国际模型
	if cfg != nil && len(cfg.SelectedIntlModels) > 0 {
		for _, customIntl := range cfg.SelectedIntlModels {
			low := strings.ToLower(customIntl)
			if !addedMap[low] {
				list = append(list, ExportModel{
					ID:       customIntl,
					Name:     fmt.Sprintf("%s (国际自选)", customIntl),
					Category: "intl",
					Channel:  "intl",
					Selected: true,
				})
				addedMap[low] = true
			}
		}
	}

	// 5. 用户自备 BYOK
	list = appendBYOKModels(list, cfg)
	return list
}

// appendBYOKModels 追加自备 BYOK 模型。
func appendBYOKModels(list []ExportModel, cfg *config.Config) []ExportModel {
	if cfg == nil {
		return list
	}
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
				Channel:  "byok",
				Selected: false,
			})
		}
	}
	return list
}
