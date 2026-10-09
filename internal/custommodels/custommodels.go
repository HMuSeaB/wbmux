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
	"bytes"
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
//   - **本轮两个来源都拿不到东西时，一个字节都不写**
//
// # 最后那条为什么是硬规则（2026-10-09 真实事故）
//
// 原实现无条件走"剥旧 → 重加 → 覆盖写"。国际侧凭据在一次客户端升级/重启的
// 瞬间不可用，`intl` 为空；用户又没建过 BYOK 卡片，`providers` 也为空。
// 于是"剥旧"把 ~/.workbuddy/models.json 里全部 wbmux-intl-* 条目删掉，"重加"
// 一次都没进循环、什么都没补回来，最后照样覆盖写盘——用户的自定义模型清单
// 被原地清空，客户端里表现为"升级完模型全没了"。
//
// 当时的"写前备份 + 写后校验"给了一种虚假的安全感：备份只是把即将被覆盖的
// 旧文件复制一份（对"内容变空"毫无帮助），而写后校验 Unmarshal 的是我们自己
// Marshal 出来的字节，恒为真、永不触发。两处都拦不住这次事故。
//
// 所以现在的判据很直白：**没东西可注入就什么都不做**。保留旧文件里已有的
// 注入条目，比清空它们正确得多——凭据是暂时的，条目是持久的。
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

	// 先算清"这一轮到底有没有东西可注入"，再决定碰不碰文件。
	// 只要有一个来源可用，就该照常剥旧重加（用户删了卡片、模型下线都要立刻反映）。
	if !hasAnythingToInject(intl, providers) {
		return 0, nil
	}

	// 读旧清单。
	//
	// 三种情况要分开对待：
	//   1. 正常数组 → 原样解析，用户条目保留；
	//   2. 存在但没有可用的条目（`null`、对象、乱码、空文件）→ 客户端升级期
	//      实测会把文件短暂写成 `null`。此时保留原文一份 .wbmux-corrupt 供
	//      事后查看，然后只在其上追加注入条目；
	//   3. 文件不存在 → 新客户端，正常从空清单建。
	//
	// 注意 `null` 是个陷阱：它 Unmarshal 成 nil 切片**且不报错**，所以判据
	// 不能只看 err，得显式确认"是一个 JSON 数组"（探测首个非空白字节）。
	var list []map[string]any
	var rawOld []byte
	if raw, err := os.ReadFile(modelsJSONPath); err == nil {
		rawOld = raw
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			if err := json.Unmarshal(raw, &list); err != nil {
				list = nil
			}
		}
	} else if !os.IsNotExist(err) {
		// 权限、IO 之类的真错误：报出去，别猜。
		return 0, err
	}
	if list == nil && len(rawOld) > 0 {
		// 原文留档，不参与重建——用户自己的条目因此不丢。
		_ = os.WriteFile(modelsJSONPath+".wbmux-corrupt", rawOld, 0o644)
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

	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return 0, err
	}
	// 落盘前先自检：Marshal 出来的东西必须能解析回 JSON 数组。
	// 这是对**输出**的体检，不是对磁盘的体检，但能挡住"kept 里混进
	// 不可序列化值"这类编程错误——真出这种事，宁可不写也不能写坏清单。
	var check []map[string]any
	if err := json.Unmarshal(out, &check); err != nil {
		return 0, fmt.Errorf("拒绝写入：生成的清单无法解析回 JSON（客户端会丢整个自定义清单）：%w", err)
	}

	// 写前备份：把**当前磁盘上的**原文件留一份（不是即将写入的 out）。
	if raw, err := os.ReadFile(modelsJSONPath); err == nil {
		_ = os.WriteFile(modelsJSONPath+".wbmux-bak", raw, 0o644)
	}

	// 原子写：先写同目录临时文件，再 rename 覆盖。
	// 直接把 out 写进 models.json 的话，写到一半崩溃/断电会留下半截 JSON，
	// 客户端读不动就等于自定义模型全丢。tmp+rename 保证"要么旧的、要么新的"。
	// （本项目 config.Save、instance.Claim 都是这个模式，这里跟上。）
	tmp := modelsJSONPath + ".wbmux-tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, modelsJSONPath); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return added, nil
}

// hasAnythingToInject 报告这一轮是否有东西可注入。
//
// 判据必须与 Sync 的循环保持一致，否则会出现"它说有、但循环一条都没加"
// 的分叉——那正是清空事故的成因。国际侧看 FreeNow（Sync 只收限时免费），
// 自备侧看是否有非空模型名。
func hasAnythingToInject(intl []usage.LiveModel, providers []config.Provider) bool {
	for _, m := range intl {
		if m.FreeNow {
			return true
		}
	}
	for _, p := range providers {
		for _, model := range p.Models {
			if strings.TrimSpace(model) != "" {
				return true
			}
		}
	}
	return false
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
		// 解析不动就不动它——回滚操作没有"顺手覆盖用户文件"的授权。
		return 0, fmt.Errorf("清单无法解析，未做改动：%w", err)
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
	if removed == 0 {
		return 0, nil
	}
	out, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return 0, err
	}
	// 与 Sync 同样的原子写：半截文件会让客户端丢整个清单。
	tmp := modelsJSONPath + ".wbmux-tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, modelsJSONPath); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return removed, nil
}
