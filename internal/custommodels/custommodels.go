// Package custommodels 把 wbmux 代理暴露的国际模型，注入国内客户端的
// 自定义模型清单（~/.workbuddy/models.json），实现"国内壳子里无缝
// 使用国际模型"。
//
// 原理：国内客户端的 CustomModelsProductProvider 读这个文件；条目格式
// 照抄用户已验证可用的自定义模型（vendor=Custom，url 指向完整的
// chat/completions 端点，Bearer 鉴权）。wbmux 代理收到请求后转
// 国际后端、扣国际账号——国内客户端零改动、小程序远程控制不受影响。
//
// # id 前缀与模型名
//
// 条目的 id 带 IDPrefix，是为了同步时能识别并整体替换旧条目
// （绝不动用户自己加的自定义模型）。代价是客户端会把**带前缀的 id**
// 当请求体的 model 发出来，所以代理侧必须剥掉前缀才能转给上游
// （见 webui.normalizeModelName，2026-09-27 那个"一用就 502"就是漏了这步）。
package custommodels

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/usage"
)

// IDPrefix 是"国际免费模型"注入条目的 id 前缀：同步时整体替换旧条目，
// 绝不动用户自己加的自定义模型。
const IDPrefix = "wbmux-intl-"

// ownedPrefixes 是所有属于 wbmux 的条目前缀。
//
// 两个来源（借国际账号的免费模型、自备提供方）用不同前缀区分，代理靠它
// 决定把请求转给谁（见 webui.resolveRoute）。凡是这几个前缀开头的条目，
// 同步时都可以放心重写——它们是我们的，不是用户的。
func ownedPrefixes() []string {
	return []string{IDPrefix, config.ProviderIDPrefix}
}

// isOwned 判断一条条目是不是 wbmux 注入的。
func isOwned(id string) bool {
	for _, p := range ownedPrefixes() {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// Sync 把 wbmux 提供的模型写入国内客户端的自定义模型清单。
//
// 两个来源，代理按条目前缀分流：
//   - 国际限时免费模型（借国际账号凭据，前缀 IDPrefix）
//   - 用户自备的提供方（BYOK，前缀 config.ProviderIDPrefix）
//
// 规则：
//   - 先剥掉此前注入的旧条目（按 ownedPrefixes 识别），用户自己的条目原样保留
//   - 国际侧只注入限时免费模型（FreeNow）：测试与日常都零成本，不盲发计费模型
//   - 写前备份原文件（models.json.wbmux-bak），写后校验可解析
//
// chatURL 是代理的补全端点（http://127.0.0.1:<port>/v1/chat/completions），
// apiKey 是代理的鉴权令牌（GUI 令牌，仅本机回环有效）。
// 返回注入的条目数。
func Sync(modelsJSONPath, chatURL, apiKey string, intl []usage.LiveModel, providers []config.Provider) (int, error) {
	// 目录不存在时**不**替客户端创建：凭空生成一个空的 ~/.workbuddy 会让
	// 客户端自己以为"这是已有档位"，而里面没有登录态。宁可明确报错，
	// 让人先启动一次客户端——顺带把操作系统那句"找不到路径"翻译成人话。
	if dir := filepath.Dir(modelsJSONPath); dir != "" {
		if _, err := os.Stat(dir); err != nil {
			return 0, fmt.Errorf("国内客户端的数据目录还不存在（%s）：先启动一次客户端，再点注入", dir)
		}
	}

	var list []map[string]any
	if raw, err := os.ReadFile(modelsJSONPath); err == nil {
		if err := json.Unmarshal(raw, &list); err != nil {
			// 文件损坏/格式变化：不覆盖用户数据，先备份再重来
			_ = os.Rename(modelsJSONPath, modelsJSONPath+".wbmux-corrupt")
			list = nil
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}

	// 剥旧 + 保留用户条目
	var kept []map[string]any
	for _, m := range list {
		id, _ := m["id"].(string)
		if isOwned(id) {
			continue
		}
		kept = append(kept, m)
	}

	// 只注入限时免费模型：测试与日常都零成本，不盲发计费模型
	added := 0
	for _, m := range intl {
		if !m.FreeNow {
			continue
		}
		entry := map[string]any{
			"id":     IDPrefix + m.ID,
			"name":   fmt.Sprintf("%s（国际免费）", displayName(m)),
			"vendor": "Custom",
			"url":    chatURL,
			"apiKey": apiKey,
			// URL 已是完整的补全端点，别再让客户端去补 /chat/completions
			"useCustomProtocol": true,
			"onlyReasoning":     false,

			// 能力与上限照抄官方配置（LiveModel 来自 /v3/config）。
			//
			// supportsToolCall 必须是真值：客户端见到 false 会把请求体里的
			// tools/tool_choice 删掉（客户端 ProductFeature
			// "SkipToolCallSupportCheck" 的注释写明了这条规则），
			// 而国内壳子里的 Agent 全靠工具干活——写 false 的结果是
			// 模型只能聊天，什么也做不了。实测官方配置里这几个免费模型
			// 都是 supportsToolCall=true。
			"supportsToolCall": m.Tools,
			"supportsImages":   m.Images,
			// 思考类开关官方配置里没有对应字段，不猜，一律 false。
			"supportsReasoning": false,
		}
		// 上限缺省就别写：客户端对缺失字段有自己的兜底，
		// 而写个 0 会被当成"上限为零"。
		if m.Ctx > 0 {
			entry["maxInputTokens"] = m.Ctx
		}
		if m.MaxOutput > 0 {
			entry["maxOutputTokens"] = m.MaxOutput
		}
		kept = append(kept, entry)
		added++
	}

	// 自备提供方（BYOK）：每张卡片按它的模型清单各生成一条。
	//
	// 这些条目的"上游"是用户自己的端点，注入条目本身仍然指向本机代理
	// （凭据是我们自己的令牌），所以 key 不会落到客户端目录里。
	for _, p := range providers {
		for _, model := range p.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			entry := map[string]any{
				"id":                p.EntryPrefix() + model,
				"name":              fmt.Sprintf("%s · %s", p.Name, model),
				"vendor":            "Custom",
				"url":               chatURL,
				"apiKey":            apiKey,
				"useCustomProtocol": true,
				"onlyReasoning":     false,
				// 能力缺省按"支持工具、不支持图片"：写 false 的代价是客户端
				// 把请求体里的 tools 删掉，Agent 直接废掉（见上面国际侧注释）。
				"supportsToolCall":  p.ToolsSupported(),
				"supportsImages":    p.ImagesSupported(),
				"supportsReasoning": false,
			}
			if p.MaxInputTokens > 0 {
				entry["maxInputTokens"] = p.MaxInputTokens
			}
			if p.MaxOutputTokens > 0 {
				entry["maxOutputTokens"] = p.MaxOutputTokens
			}
			kept = append(kept, entry)
			added++
		}
	}

	// 写前备份
	if raw, err := os.ReadFile(modelsJSONPath); err == nil {
		_ = os.WriteFile(modelsJSONPath+".wbmux-bak", raw, 0o644)
	}
	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(modelsJSONPath, out, 0o644); err != nil {
		return 0, err
	}

	// 写后校验：必须能原样解析回来，否则客户端会丢整个自定义清单
	var check []map[string]any
	if err := json.Unmarshal(out, &check); err != nil {
		return 0, fmt.Errorf("写后校验失败（已恢复备份）：%w", err)
	}
	return added, nil
}

// displayName 官方 name 优先（如 "Deepseek-V4.1-Flash"），退回 id。
func displayName(m usage.LiveModel) string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID
}

// Remove 移除全部注入条目（回滚用），返回删除的条目数。
func Remove(modelsJSONPath string) (int, error) {
	raw, err := os.ReadFile(modelsJSONPath)
	if err != nil {
		return 0, err
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		return 0, err
	}
	var kept []map[string]any
	removed := 0
	for _, m := range list {
		id, _ := m["id"].(string)
		if isOwned(id) {
			removed++
			continue
		}
		kept = append(kept, m)
	}
	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return 0, err
	}
	return removed, os.WriteFile(modelsJSONPath, out, 0o644)
}
