// Package models 是 wbmux 的中央官方模型注册中心（Single Source of Truth, SSOT）。
//
// 本包为纯叶子包，不依赖任何业务逻辑包（不依赖 config、ccswitch、webui），
// 集中管理官方大模型的唯一标识、中文显示名、倍率标签、上下文窗口大小、
// 物理通道归属、高频别名解析以及默认精选推荐白名单。
package models

import (
	"strings"
)

// ModelDef 是官方大模型的全维度元数据定义。
type ModelDef struct {
	ID          string   // 官方标准唯一 ID，如 "hy4free", "deepseek-v4.1-flash", "glm-5.3"
	Name        string   // 纯净官方显示名，如 "Hy4", "GLM-5.3", "DeepSeek-V4.1-Flash"
	Label       string   // 带说明完整展示名，如 "Hy4 (官方限免 0.00x · 1M 上下文)"
	Category    string   // 分类: "free" (限免), "intl" (国际特惠), "cn" (国内专享/包月)
	Channel     string   // 主归属通道: "intl" 或 "cn"
	CtxTokens   int      // 上下文窗口 tokens, 如 1000000 (1M), 192000 (192k), 256000
	Rate        float64  // 费率倍率, 如 0.0, 0.11, 0.08, 0.69, 0.79
	RateTag     string   // 费率说明标签, 如 "官方限免 0.00x", "夜间折扣 0.11x", "订阅优先 0.08x"
	Aliases     []string // 高频简写/别名列表, 如 ["hy4", "hy-4"], ["ds4.1flash", "ds4.1"]
	CuratedCN   bool     // 是否默认属于国内端精选推荐白名单
	CuratedIntl bool     // 是否默认属于国际端精选推荐
	ExclusiveCN bool     // 聚合导出模式下是否作为国内独占款保留
	Selected    bool     // 导出至外部工具（cc-switch）时默认是否勾选
}

// Registry 官方大模型全量知识库。
//
// 维护法则：
// 当 WorkBuddy 官方客户端增减限免、升级模型或调整折扣时，
// 开发者【只需维护本切片这一处】，网关路由别名、反向注入、cc-switch 导出、
// 上下文阶梯分配和 WebUI 呈现将全自动级联同步生效。
var Registry = []ModelDef{
	// ---------- 1. 官方核心限免款 (0.00x) ----------
	{
		ID:          "hy4free",
		Name:        "Hy4",
		Label:       "Hy4 (官方限免 0.00x · 1M 上下文)",
		Category:    "free",
		Channel:     "intl",
		CtxTokens:   1000000,
		Rate:        0.0,
		RateTag:     "官方限免 0.00x",
		Aliases:     []string{"hy4", "hy-4", "hy4free"},
		CuratedCN:   true,
		CuratedIntl: true,
		Selected:    true,
	},
	{
		ID:          "hy3",
		Name:        "Hy3",
		Label:       "Hy3 (官方限免 0.00x · 192k 思考模型)",
		Category:    "free",
		Channel:     "intl",
		CtxTokens:   192000,
		Rate:        0.0,
		RateTag:     "官方限免 0.00x",
		Aliases:     []string{"hy3", "hy-3", "混元3", "混元3.0"},
		CuratedCN:   true,
		CuratedIntl: true,
		Selected:    true,
	},

	// ---------- 2. 国际特惠与核心高频款 ----------
	{
		ID:          "deepseek-v4.1-flash-sg",
		Name:        "DeepSeek-V4.1-Flash",
		Label:       "DeepSeek-V4.1-Flash (夜间折扣 0.11x · 1M)",
		Category:    "intl",
		Channel:     "intl",
		CtxTokens:   1000000,
		Rate:        0.11,
		RateTag:     "夜间折扣 0.11x",
		Aliases:     []string{"ds4.1flash", "ds4.1-flash", "ds4.1", "deepseek-v4.1-flash-sg"},
		CuratedIntl: true,
		Selected:    true,
	},
	{
		ID:          "glm-5.3-flash",
		Name:        "GLM-5.3-Flash",
		Label:       "GLM-5.3-Flash (订阅优先 0.08x · 1M)",
		Category:    "intl",
		Channel:     "intl",
		CtxTokens:   1000000,
		Rate:        0.08,
		RateTag:     "订阅优先 0.08x",
		Aliases:     []string{"glm5.3flash", "glm5.3-flash", "glm-5.3flash"},
		CuratedIntl: true,
		CuratedCN:   true,
		Selected:    true,
	},
	{
		ID:          "glm-5.3",
		Name:        "GLM-5.3",
		Label:       "GLM-5.3 (订阅优先 0.69x · 1M)",
		Category:    "intl",
		Channel:     "intl",
		CtxTokens:   1000000,
		Rate:        0.69,
		RateTag:     "订阅优先 0.69x",
		Aliases:     []string{"glm5.3", "glm-5.3"},
		CuratedIntl: true,
		CuratedCN:   true,
		Selected:    true,
	},
	{
		ID:        "glm-5.2",
		Name:      "GLM-5.2",
		Label:     "GLM-5.2 (夜间折扣 0.79x · 1M)",
		Category:  "intl",
		Channel:   "intl",
		CtxTokens: 1000000,
		Rate:      0.79,
		RateTag:   "夜间折扣 0.79x",
		Aliases:   []string{"glm5.2", "glm-5.2"},
		CuratedCN: true,
		Selected:  false,
	},

	// ---------- 3. 国内专属与包月核心款 ----------
	{
		ID:          "minimax-m2.5",
		Name:        "MiniMax-M2.5",
		Label:       "MiniMax-M2.5 (国内包月)",
		Category:    "cn",
		Channel:     "cn",
		CtxTokens:   256000,
		RateTag:     "国内包月",
		Aliases:     []string{"minimax", "minimax-m2.5"},
		CuratedCN:   true,
		ExclusiveCN: true,
		Selected:    true,
	},
	{
		ID:          "kimi-k2.6",
		Name:        "Kimi-K2.6",
		Label:       "Kimi-K2.6 (国内包月)",
		Category:    "cn",
		Channel:     "cn",
		CtxTokens:   256000,
		RateTag:     "国内包月",
		Aliases:     []string{"kimi", "kimi-k2.6"},
		CuratedCN:   true,
		ExclusiveCN: true,
		Selected:    true,
	},
	{
		ID:        "deepseek-v4.1-flash",
		Name:      "DeepSeek-V4.1-Flash",
		Label:     "DeepSeek-V4.1-Flash (夜间折扣 0.11x · 1M)",
		Category:  "cn",
		Channel:   "cn",
		CtxTokens: 1000000,
		Rate:      0.11,
		RateTag:   "夜间折扣 0.11x",
		Aliases:   []string{"deepseek-v4-flash", "deepseek-v4.1-flash"},
		CuratedCN: true,
		Selected:  true,
	},
	{
		ID:        "hunyuan-chat",
		Name:      "混元聊天",
		Label:     "混元聊天 (国内包月)",
		Category:  "cn",
		Channel:   "cn",
		CtxTokens: 128000,
		RateTag:   "国内包月",
		CuratedCN: true,
		Selected:  false,
	},
}

// aliasMap 缓存规范化后的别名映射表。
var aliasMap map[string]string

func init() {
	aliasMap = make(map[string]string)
	for _, m := range Registry {
		idLower := strings.ToLower(m.ID)
		aliasMap[idLower] = m.ID
		for _, a := range m.Aliases {
			clean := cleanAlias(a)
			if clean != "" {
				aliasMap[clean] = m.ID
			}
		}
	}
	// 兼容历史过渡旧别名
	aliasMap["glm-5.1"] = "glm-5.3"
	aliasMap["glm5.1"] = "glm-5.3"
}

func cleanAlias(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "_", "-")
}

// ResolveAlias 将客户端或第三方工具可能传入的高频别名（如 hy4、ds4.1flash、glm5.3）
// 精准还原为官方规范 ID。若无法匹配则原样返回。
func ResolveAlias(raw string) string {
	clean := cleanAlias(raw)
	if target, ok := aliasMap[clean]; ok {
		return target
	}
	return raw
}

// FindByID 根据模型 ID 查找模型元数据定义（不区分大小写），未找到返回 nil。
func FindByID(id string) *ModelDef {
	lower := strings.ToLower(strings.TrimSpace(id))
	for i := range Registry {
		if strings.ToLower(Registry[i].ID) == lower {
			return &Registry[i]
		}
	}
	return nil
}

// DefaultCuratedCNIDs 返回国内端默认精选推荐反向注入的模型 ID 白名单。
func DefaultCuratedCNIDs() []string {
	var list []string
	for _, m := range Registry {
		if m.CuratedCN {
			list = append(list, m.ID)
		}
	}
	// 追加旧名兼容
	list = append(list, "deepseek-v4-flash", "glm-5.1")
	return list
}

// IsCuratedCN 判断一个模型 ID 是否属于国内精选白名单。
func IsCuratedCN(id string) bool {
	lower := strings.ToLower(strings.TrimSpace(id))
	for _, cid := range DefaultCuratedCNIDs() {
		if strings.ToLower(cid) == lower {
			return true
		}
	}
	return false
}

// IsFree 判断一个模型 ID 是否属于当前官方限免模型（0.00x）。
func IsFree(id string) bool {
	m := FindByID(id)
	if m != nil && m.Category == "free" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(id))
	return lower == "hy4free" || lower == "hy4" || lower == "hy3"
}

// ContextWindowFor 返回指定模型 ID 的默认上下文窗口 token 长度。
// 未找到时默认返回安全兜底 128000 (128k)。
func ContextWindowFor(id string) int {
	m := FindByID(id)
	if m != nil && m.CtxTokens > 0 {
		return m.CtxTokens
	}
	return 128000
}

// CoreFreeList 返回当前官方限免核心款模型列表（Hy4, Hy3）。
func CoreFreeList() []ModelDef {
	var list []ModelDef
	for _, m := range Registry {
		if m.Category == "free" {
			list = append(list, m)
		}
	}
	return list
}

// CoreIntlPaidList 返回国际高频特惠核心款模型列表（DeepSeek-V4.1-Flash, GLM-5.3-Flash, GLM-5.3）。
func CoreIntlPaidList() []ModelDef {
	var list []ModelDef
	for _, m := range Registry {
		if m.Category == "intl" && m.CuratedIntl {
			list = append(list, m)
		}
	}
	return list
}

// CoreCNList 返回国内专享通道全量精选核心款列表。
func CoreCNList() []ModelDef {
	var list []ModelDef
	// 国内限免款
	for _, m := range Registry {
		if m.Category == "free" {
			cnFree := m
			cnFree.Channel = "cn"
			list = append(list, cnFree)
		}
	}
	// 国内特惠/包月款
	for _, m := range Registry {
		if m.CuratedCN && m.Category != "free" {
			list = append(list, m)
		}
	}
	return list
}

// ExclusiveCNList 返回聚合模式下需要保留的国内真正独占款（MiniMax, Kimi）。
func ExclusiveCNList() []ModelDef {
	var list []ModelDef
	for _, m := range Registry {
		if m.ExclusiveCN {
			list = append(list, m)
		}
	}
	return list
}
