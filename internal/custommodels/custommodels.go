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

// IDPrefix 是"国际免费/自选模型"注入国内客户端的条目 id 前缀：同步时整体替换旧条目，
// 绝不动用户自己加的自定义模型。
const IDPrefix = "wbmux-intl-"

// IDPrefixCN 是"国内模型"反向注入国际版客户端或本地网关代理的条目 id 前缀。
const IDPrefixCN = "wbmux-cn-"

// modelsDoc 是 models.json 的**新版**文档形状。
//
// 客户端 5.8.3 起，解析器要求顶层是对象：
//
//	parseModelsJson = t => (!t || typeof t !== "object" || Array.isArray(t))
//	    ? {models: []} : {models: Array.isArray(t.models) ? t.models : []}
//
// 也就是说**裸数组会被当成空清单**。后果不是"少几条模型"，而是
// `contextWindow` / `maxInputTokens` 一起丢，客户端回落到内置默认上下文
// （实测约 300k），用户看到"ds4.1 的 1M 都到不了"、长会话被提前压缩。
//
// 这里只用 Models 一个字段：`availableModels` 是客户端运行时的状态，
// 由它自己填，不是我们该写的配置项。
type modelsDoc struct {
	Models []map[string]any `json:"models"`
}

// ownedPrefixes 是所有属于 wbmux 的条目前缀。
//
// 两个来源（借国际账号的免费模型、自备提供方）用不同前缀区分，代理靠它
// 决定把请求转给谁（见 webui.resolveRoute）。凡是这几个前缀开头的条目，
// 同步时都可以放心重写——它们是我们的，不是用户的。
func ownedPrefixes() []string {
	return []string{IDPrefix, IDPrefixCN, config.ProviderIDPrefix}
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
// Sync 把 wbmux 提供的模型写入国内客户端的自定义模型清单。
//
// 来源，代理按条目前缀分流：
//   - 国际限时免费模型（借国际账号凭据，前缀 IDPrefix，默认自动注入）
//   - 用户自选的国际付费高级模型（借国际账号凭据，前缀 IDPrefix，由 selectedIntl 传入）
//   - 用户自备的提供方（BYOK，前缀 config.ProviderIDPrefix）
//
// 规则：
//   - 先剥掉此前注入的旧条目（按 ownedPrefixes 识别），用户自己的条目原样保留
//   - 国际侧默认注入限时免费模型（FreeNow），并同时注入用户自选的付费模型（带对应费率倍率后缀）
//   - 写前备份原文件（models.json.wbmux-bak），写后校验可解析
//   - **本轮全部来源都拿不到东西时，一个字节都不写**
//
// chatURL 是代理的补全端点（http://127.0.0.1:<port>/v1/chat/completions），
// apiKey 是代理的鉴权令牌（GUI 令牌，仅本机回环有效）。
// 返回注入的条目数。
func Sync(modelsJSONPath, chatURL, apiKey string, intl []usage.LiveModel, providers []config.Provider, selectedIntl ...[]string) (int, error) {
	// 目录不存在时**不**替客户端创建：凭空生成一个空的 ~/.workbuddy 会让
	// 客户端自己以为"这是已有档位"，而里面没有登录态。宁可明确报错，
	// 让人先启动一次客户端——顺带把操作系统那句"找不到路径"翻译成人话。
	if dir := filepath.Dir(modelsJSONPath); dir != "" {
		if _, err := os.Stat(dir); err != nil {
			return 0, fmt.Errorf("国内客户端的数据目录还不存在（%s）：先启动一次客户端，再点注入", dir)
		}
	}

	var selectedList []string
	if len(selectedIntl) > 0 {
		selectedList = selectedIntl[0]
	}
	selectedSet := make(map[string]bool, len(selectedList))
	for _, id := range selectedList {
		if clean := strings.ToLower(strings.TrimSpace(id)); clean != "" {
			selectedSet[clean] = true
		}
	}

	// 先算清"这一轮到底有没有东西可注入"，再决定碰不碰文件。
	// 只要有一个来源可用，就该照常剥旧重加（用户删了卡片、模型下线都要立刻反映）。
	if !hasAnythingToInject(intl, providers, selectedSet) {
		return 0, nil
	}

	// 读旧清单。两种格式都认（新版对象 / 旧版裸数组）——见 parseModelsJSON。
	//
	// 读不出清单时（空文件、`null`、乱码）留一份原文 `.wbmux-corrupt` 供事后
	// 查看，然后当作"没有旧清单"继续：注入照做，用户条目由客户端自己的
	// 那份文件兜着。
	var list []map[string]any
	var rawOld []byte
	if raw, err := os.ReadFile(modelsJSONPath); err == nil {
		rawOld = raw
		list, _ = parseModelsJSON(raw)
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

	// 注入国际模型：限时免费模型自动注入；自选付费模型（在 selectedSet 中）同时注入。
	added := 0
	injectedIDs := make(map[string]bool)

	// 若用户勾选了 auto / auto-free 虚拟模型，注入智能最速限免条目
	if selectedSet["auto"] || selectedSet["auto-free"] {
		entry := map[string]any{
			"id":                IDPrefix + "auto",
			"name":              "⚡ 最速限免智能优选 (Auto-Free)",
			"vendor":            "Custom",
			"url":               chatURL,
			"apiKey":            apiKey,
			"useCustomProtocol": true,
			"onlyReasoning":     false,
			"supportsToolCall":  true,
			"supportsImages":    true,
			"contextWindow":     buildContextWindowDoc(1000000),
			"maxInputTokens":    1000000,
			"maxOutputTokens":   64000,
		}
		kept = append(kept, entry)
		injectedIDs["auto"] = true
		added++
	}

	for _, m := range intl {
		isFree := m.FreeNow
		isSelected := selectedSet[strings.ToLower(m.ID)]
		if !isFree && !isSelected {
			continue
		}
		if injectedIDs[strings.ToLower(m.ID)] {
			continue
		}
		injectedIDs[strings.ToLower(m.ID)] = true

		displayNameStr := displayName(m)
		var label string
		if isFree {
			label = fmt.Sprintf("%s（国际免费）", displayNameStr)
		} else if m.Locked {
			badge := m.TierBadge
			if badge == "" {
				badge = "需更高档位"
			}
			label = fmt.Sprintf("%s（国际 · %s）", displayNameStr, badge)
		} else if m.Rate > 0 {
			label = fmt.Sprintf("%s（国际 · %.2fx）", displayNameStr, m.Rate)
		} else {
			label = fmt.Sprintf("%s（国际）", displayNameStr)
		}

		ctxTokens := m.Ctx
		if ctxTokens <= 0 {
			ctxTokens = 1000000
		}

		entry := map[string]any{
			"id":     IDPrefix + m.ID,
			"name":   label,
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
		// 上限缺省就别写：客户端对缺失字段有自己的兜底，而写个 0 会被当成"上限为零"。
		// 当官方有明确上下文上限时（如 deepseek 的 1M），注入结构化阶梯 contextWindow 对象。
		// 客户端 (resolveEffectiveContextBudget) 严格要求 contextWindow 包含 supportedLengths（且长度 >= 2）
		// 与 defaultLength，否则用户在客户端切换上下文长度（Context）时会被判定为非法值并立刻静默重置为 null（导致"调了跟没调一样"）。
		if m.Ctx > 0 {
			entry["maxInputTokens"] = m.Ctx
			entry["contextWindow"] = buildContextWindowDoc(m.Ctx)
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
			displayName := model
			if p.Name != "" && !strings.EqualFold(p.Name, model) {
				displayName = fmt.Sprintf("%s · %s", p.Name, model)
			}
			entry := map[string]any{
				"id":                p.EntryPrefix() + model,
				"name":              displayName,
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
			inTokens := p.MaxInputTokens
			if inTokens <= 0 {
				inTokens = 1000000
			}
			entry["maxInputTokens"] = inTokens
			entry["contextWindow"] = buildContextWindowDoc(inTokens)
			if p.MaxOutputTokens > 0 {
				entry["maxOutputTokens"] = p.MaxOutputTokens
			} else {
				entry["maxOutputTokens"] = 64000
			}
			kept = append(kept, entry)
			added++
		}
	}

	// 写出去的格式是**对象** `{"models":[...]}`：客户端 5.8.3 起只认这个
	// （裸数组会被它的解析器当成空清单扔掉，见上面读旧清单的注释）。
	// 对象格式对旧版客户端也读得动（旧版直接读 `.models` 或整份当数组与否
	// 都不影响——实测旧版就是按数组读的，所以这里两头兼顾靠的是"读时都认、
	// 写时用新格式"）。
	doc := modelsDoc{Models: kept}
	if doc.Models == nil {
		// 空清单也写成 `[]` 而不是 `null`：客户端对 `null` 的容忍度未知，
		// 而空数组是明确的"没有自定义模型"。
		doc.Models = []map[string]any{}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return 0, err
	}
	// 落盘前先自检：Marshal 出来的东西必须能解析回 `{"models":[...]}`。
	// 这是对**输出**的体检，不是对磁盘的体检，但能挡住"kept 里混进
	// 不可序列化值"这类编程错误——真出这种事，宁可不写也不能写坏清单。
	var check modelsDoc
	if err := json.Unmarshal(out, &check); err != nil {
		return 0, fmt.Errorf("拒绝写入：生成的清单无法解析回 JSON（客户端会丢整个自定义清单）：%w", err)
	}
	if len(check.Models) != len(kept) {
		return 0, fmt.Errorf("拒绝写入：写前 %d 条、回读 %d 条，数量对不上", len(kept), len(check.Models))
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
// 的分叉——那正是清空事故的成因。国际侧看 FreeNow 或在 selectedSet 中，
// 自备侧看是否有非空模型名。
func hasAnythingToInject(intl []usage.LiveModel, providers []config.Provider, selectedSet map[string]bool) bool {
	if selectedSet != nil && (selectedSet["auto"] || selectedSet["auto-free"]) {
		return true
	}
	for _, m := range intl {
		if m.FreeNow || (selectedSet != nil && selectedSet[strings.ToLower(m.ID)]) {
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
	list, ok := parseModelsJSON(raw)
	if !ok {
		// 解析不动就不动它——回滚操作没有"顺手覆盖用户文件"的授权。
		return 0, fmt.Errorf("清单无法解析，未做改动")
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
	if kept == nil {
		kept = []map[string]any{}
	}
	out, err := json.MarshalIndent(modelsDoc{Models: kept}, "", "  ")
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

// parseModelsJSON 解析 models.json，**两种格式都认**：
//
//	{"models":[...]}  客户端 5.8.3 起的格式（写出去一律用这个）
//	[...]             旧格式，老客户端与旧文件
//
// 返回 ok=false 表示这份内容不是可用的清单（空文件、`null`、乱码、对象但
// models 不是数组）。调用方据此决定"当空清单重建"还是"别碰它"——两条路径
// 的取舍不同，所以这里不替调用方决定，只如实回答"能不能解析出清单"。
//
// `null` 是个陷阱：它能 Unmarshal 成 nil 切片**且不报错**，所以判据不能只
// 看 err，得先看首字节。
func parseModelsJSON(raw []byte) ([]map[string]any, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false
	}
	switch trimmed[0] {
	case '[':
		var list []map[string]any
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, false
		}
		return list, true
	case '{':
		var doc modelsDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, false
		}
		if doc.Models == nil {
			return nil, false
		}
		return doc.Models, true
	}
	return nil, false
}

// hasAnyFreeModel 检查当前国际模型列表中是否存在至少一个 0.00x 限免模型。
func hasAnyFreeModel(intl []usage.LiveModel) bool {
	for _, m := range intl {
		if m.FreeNow {
			return true
		}
	}
	return false
}

// buildContextWindowDoc 根据模型的最大输入 token 数量生成 WorkBuddy 客户端合规的
// contextWindow 阶梯档位对象。
//
// 客户端在解析会话预算 (resolveEffectiveContextBudget) 时，要求 contextWindow 是包含
// supportedLengths 与 defaultLength 的对象。
// 更关键的是，客户端前端渲染进程中的分段滑动选择器组件 (ContextWindowSegmented)
// 内部 hardcode 严格限制了档位数量：
//
//	resolveContextUsageWindowTiers = e => {
//	    let r = ...filter(...).sort((e, t) => e - t);
//	    return r.length === 2 || r.length === 3 ? r : void 0;
//	}
//	resolveContextWindowTiers = e =>
//	    e.length === 2 ? [{tier: 'default'}, {tier: 'extraLong'}]
//	  : e.length === 3 ? [{tier: 'default'}, {tier: 'moderate'}, {tier: 'extraLong'}]
//	  : [];
//
// 若档位数量超过 3（如以前的 6 档），前端直接返回 undefined，分段滑动开关完全不渲染或失效！
// 此外，客户端格式化函数 formatContextWindow 要求数值必须是 1000 的整数倍
// (t % 1000 == 0 ? `${t/1000}K` : String(t))，否则会被渲染成丑陋且不友好的纯数字字符串 (如 "32768")。
func buildContextWindowDoc(maxInputTokens int) map[string]any {
	if maxInputTokens <= 0 {
		maxInputTokens = 1000000
	}

	var supported []int
	switch {
	case maxInputTokens >= 1000000:
		// 1M 级别（如 DeepSeek-V4.1-Flash、Hy4 等）：
		// 严格 3 档（128K 日常轻快 / 200K 适中扩展 / 1M 满血超长），
		// 对应前端 default / moderate / extraLong
		supported = []int{128000, 200000, maxInputTokens}
	case maxInputTokens >= 300000:
		// 300k~500k 级别
		supported = []int{128000, 200000, maxInputTokens}
	case maxInputTokens >= 190000:
		// 192k~256k 级别（如 Hy3 的 192k）
		supported = []int{64000, 128000, maxInputTokens}
	case maxInputTokens >= 100000:
		// 100k~160k 级别
		supported = []int{32000, 64000, maxInputTokens}
	default:
		// 小于 100k 的小窗口模型，凑足 2 档（整千对齐）
		half := (maxInputTokens / 2 / 1000) * 1000
		if half <= 0 {
			half = maxInputTokens / 2
		}
		if half > 0 && half < maxInputTokens {
			supported = []int{half, maxInputTokens}
		} else {
			supported = []int{maxInputTokens}
		}
	}

	// 兜底保障：若档位数量不足 2 档且上限允许，硬编码凑齐 2 档以激活客户端 UI 选择器
	if len(supported) < 2 {
		lower := (maxInputTokens / 2 / 1000) * 1000
		if lower > 0 {
			supported = []int{lower, maxInputTokens}
		} else {
			supported = []int{16000, maxInputTokens}
		}
	}
	// 兜底保障：绝对不能超过 3 档，超过则截取 [首档, 次高档, 尾档]
	if len(supported) > 3 {
		supported = []int{supported[0], supported[len(supported)-2], supported[len(supported)-1]}
	}

	return map[string]any{
		"supportedLengths": supported,
		"defaultLength":   maxInputTokens,
	}
}

// SyncCNIntoIntl 把国内版提供的可用模型反向注入到国际版客户端的自定义模型清单（~/.workbuddy-ai/models.json）。
//
// 规则：
//   - 校验国际客户端数据目录存在性，不存在时不盲目创建
//   - 剥掉此前的旧条目（包含 IDPrefixCN 与 BYOK），用户条目原样保留
//   - 过滤纯多媒体模型（生图/生视频），只注入语言交互与编码模型
//   - 注入前缀 IDPrefixCN ("wbmux-cn-")，供代理路由回国内后端
//   - 注入严格 2~3 档整千阶梯 contextWindow 与 maxInputTokens、maxOutputTokens
//   - 写前备份、原子写盘与结构校验
func SyncCNIntoIntl(modelsJSONPath, chatURL, apiKey string, cnModels []usage.LiveModel, providers []config.Provider) (int, error) {
	if dir := filepath.Dir(modelsJSONPath); dir != "" {
		if _, err := os.Stat(dir); err != nil {
			return 0, fmt.Errorf("国际客户端的数据目录还不存在（%s）：先启动一次国际客户端，再点注入", dir)
		}
	}

	// 过滤有效模型：排除生图、生视频
	var validModels []usage.LiveModel
	for _, m := range cnModels {
		if strings.TrimSpace(m.ID) == "" || IsMediaModel(m.ID) {
			continue
		}
		validModels = append(validModels, m)
	}

	if len(validModels) == 0 && len(providers) == 0 {
		return 0, nil
	}

	var list []map[string]any
	var rawOld []byte
	if raw, err := os.ReadFile(modelsJSONPath); err == nil {
		rawOld = raw
		list, _ = parseModelsJSON(raw)
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	if list == nil && len(rawOld) > 0 {
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

	added := 0
	injectedIDs := make(map[string]bool)

	for _, m := range validModels {
		lowerID := strings.ToLower(m.ID)
		if injectedIDs[lowerID] {
			continue
		}
		injectedIDs[lowerID] = true

		displayNameStr := displayName(m)
		label := fmt.Sprintf("%s（国内专享）", displayNameStr)

		ctxTokens := m.Ctx
		if ctxTokens <= 0 {
			ctxTokens = 128000
		}

		entry := map[string]any{
			"id":                IDPrefixCN + m.ID,
			"name":              label,
			"vendor":            "Custom",
			"url":               chatURL,
			"apiKey":            apiKey,
			"useCustomProtocol": true,
			"onlyReasoning":     false,
			"supportsToolCall":  m.Tools,
			"supportsImages":    m.Images,
			"supportsReasoning": false,
			"maxInputTokens":    ctxTokens,
			"contextWindow":     buildContextWindowDoc(ctxTokens),
		}
		if m.MaxOutput > 0 {
			entry["maxOutputTokens"] = m.MaxOutput
		} else {
			entry["maxOutputTokens"] = 64000
		}
		kept = append(kept, entry)
		added++
	}

	// 同时注入用户自备提供方（BYOK）
	for _, p := range providers {
		for _, model := range p.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			displayName := model
			if p.Name != "" && !strings.EqualFold(p.Name, model) {
				displayName = fmt.Sprintf("%s · %s", p.Name, model)
			}
			entry := map[string]any{
				"id":                p.EntryPrefix() + model,
				"name":              displayName,
				"vendor":            "Custom",
				"url":               chatURL,
				"apiKey":            apiKey,
				"useCustomProtocol": true,
				"onlyReasoning":     false,
				"supportsToolCall":  p.ToolsSupported(),
				"supportsImages":    p.ImagesSupported(),
				"supportsReasoning": false,
			}
			inTokens := p.MaxInputTokens
			if inTokens <= 0 {
				inTokens = 1000000
			}
			entry["maxInputTokens"] = inTokens
			entry["contextWindow"] = buildContextWindowDoc(inTokens)
			if p.MaxOutputTokens > 0 {
				entry["maxOutputTokens"] = p.MaxOutputTokens
			} else {
				entry["maxOutputTokens"] = 64000
			}
			kept = append(kept, entry)
			added++
		}
	}

	doc := modelsDoc{Models: kept}
	if doc.Models == nil {
		doc.Models = []map[string]any{}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return 0, err
	}
	var check modelsDoc
	if err := json.Unmarshal(out, &check); err != nil {
		return 0, fmt.Errorf("拒绝写入：生成的清单无法解析回 JSON：%w", err)
	}

	if raw, err := os.ReadFile(modelsJSONPath); err == nil {
		_ = os.WriteFile(modelsJSONPath+".wbmux-bak", raw, 0o644)
	}

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

// IsMediaModel 检查模型 ID 是否属于纯生图或生视频等多媒体模型。
func IsMediaModel(id string) bool {
	lower := strings.ToLower(id)
	return strings.Contains(lower, "image") || strings.Contains(lower, "video") || strings.Contains(lower, "kling")
}
