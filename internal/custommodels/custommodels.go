// Package custommodels 把 wbmux 代理暴露的国际模型，注入国内客户端的
// 自定义模型清单（~/.workbuddy/models.json），实现"国内壳子里无缝
// 使用国际模型"。
//
// 原理：国内客户端的 CustomModelsProductProvider 读这个文件；条目格式
// 照抄用户已验证可用的自定义模型（vendor=Custom，url 指向完整的
// chat/completions 端点，Bearer 鉴权）。wbmux 代理收到请求后转
// 国际后端、扣国际账号——国内客户端零改动、小程序远程控制不受影响。
package custommodels

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/usage"
)

// IDPrefix 是注入条目的 id 前缀：同步时整体替换旧条目，
// 绝不动用户自己加的自定义模型。
const IDPrefix = "wbmux-intl-"

// Sync 把国际模型写入国内客户端的自定义模型清单。
//
// 规则：
//   - 先剥掉此前注入的旧条目（按 IDPrefix 识别），用户自己的条目原样保留
//   - 只注入限时免费模型（FreeNow）：测试与日常都零成本，不盲发计费模型
//   - 写前备份原文件（models.json.wbmux-bak），写后校验可解析
//
// chatURL 是代理的补全端点（http://127.0.0.1:<port>/v1/chat/completions），
// apiKey 是代理的鉴权令牌（GUI 令牌，仅本机回环有效）。
// 返回注入的条目数。
func Sync(modelsJSONPath, chatURL, apiKey string, intl []usage.LiveModel) (int, error) {
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
		if strings.HasPrefix(id, IDPrefix) {
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
		kept = append(kept, map[string]any{
			"id":                IDPrefix + m.ID,
			"name":              fmt.Sprintf("%s（国际免费）", displayName(m)),
			"vendor":            "Custom",
			"url":               chatURL,
			"apiKey":            apiKey,
			"supportsToolCall":  false,
			"supportsImages":    false,
			"supportsReasoning": false,
			"useCustomProtocol": true,
			"onlyReasoning":     false,
		})
		added++
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
		if strings.HasPrefix(id, IDPrefix) {
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
