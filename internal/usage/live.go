package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 实时账号数据（官方接口，只读） ----------
//
// 用户要求把客户端选择器里那些"好用的东西"（实时倍率、限时免费活动、
// 积分余量）内嵌进 wbmux 界面。数据源与鉴权方式全部来自对客户端/第三方
// 切换器的实测（2026-09-26，changexbc_workbuddy-switch 的 auth_file.rs /
// credits.rs + 本机 app.asar 逆向）：
//
//   - 凭证：%LOCALAPPDATA%/CodeBuddyExtension/Data/Public/auth/
//     workbuddy-desktop.info（国内）/ workbuddy-desktop-ai.info（国际），
//     根下 auth.accessToken + account.uid。**取凭据一律走 internal/credential**，
//     它会顺带处理国内侧的加密信封（2026-09-29 起）。
//   - 请求：Authorization: Bearer <accessToken> + X-User-Id: <uid>。
//   - 端点：GET  {endpoint}/v3/config —— 实时产品配置
//     （models[].credits 倍率、modelPromotions 限时免费活动）；
//     POST {endpoint}/billing/meter/get-user-resource-summary —— 积分包余量。
//
// **本包只发只读请求**。签到（daily-checkin 等改状态的操作）刻意不在这里，
// 见 internal/checkin —— 混在一起的话，一个"刷新面板"的动作就可能把状态改了。

// LiveModel 是官方接口返回的实时模型条目。
type LiveModel struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Rate    float64 `json:"rate"`
	RateRaw string  `json:"rateRaw"`
	// Desc 是官方的中文描述（没有就退英文），界面照客户端样式展示。
	Desc string `json:"desc"`
	// Ctx 是上下文窗口长度（maxInputTokens，token 数）。
	Ctx int `json:"ctx"`
	// MaxOutput 是单次回复上限（maxOutputTokens）。注入国内客户端时要用：
	// 缺了它客户端只能按默认值估，会出现"明明能放 1M 却提前压缩上下文"。
	MaxOutput int `json:"maxOutput,omitempty"`
	// Tools / Images 是官方的能力开关（supportsToolCall / supportsImages）。
	// 注入国内客户端时必须如实带上：客户端按 supportsToolCall 决定要不要
	// 在请求体里带 tools，写 false 就等于把模型的工具能力关掉。
	Tools  bool `json:"tools"`
	Images bool `json:"images"`
	// FreeNow 表示该模型当前被"限时免费"活动覆盖（折扣因子为 0 且在有效期内）。
	FreeNow bool `json:"freeNow"`
	// FreeLabel 是活动徽章原文（"Free now" / "限时免费"），界面照抄不翻译。
	FreeLabel string `json:"freeLabel,omitempty"`
}

// LivePromo 是一条限时优惠活动。
type LivePromo struct {
	Label      string   `json:"label"`
	Color      string   `json:"color"`
	ModelIDs   []string `json:"modelIds"`
	ValidFrom  string   `json:"validFrom"`
	ValidUntil string   `json:"validUntil"`
	TextZh     string   `json:"textZh"`
}

// LivePackage 是一个积分包的周期用量。
type LivePackage struct {
	Code   string  `json:"code"`
	Unit   string  `json:"unit"`
	Total  float64 `json:"total"`
	Remain float64 `json:"remain"`
	Used   float64 `json:"used"`
}

// LiveAccount 是一侧的实时账号数据。OK 为 false 时 Err 说明原因
// （最常见：该侧没登录，或客户端装的位置没探测到）。
type LiveAccount struct {
	OK       bool          `json:"ok"`
	Err      string        `json:"err,omitempty"`
	Models   []LiveModel   `json:"models"`
	Promos   []LivePromo   `json:"promos"`
	Packages []LivePackage `json:"packages"`
	// Nickname 是登录昵称。信封侧解不出来，为空；界面退回显示 UID 前缀。
	Nickname string `json:"nickname,omitempty"`
	// UID 是账号标识，界面只展示前 8 位。
	UID string `json:"uid,omitempty"`
	// CredKind 是凭据形态，取值见 credKindOf：plaintext=明文、
	// unsealed=信封但已借客户端解开、envelope=信封且没解开。
	//
	// 用户问"国内版到底行不行"时，这一个字段就是答案的界面化。
	CredKind string `json:"credKind,omitempty"`
	// UnsealedBy 是解开信封时借用的客户端主程序，供界面如实说明
	// "数据是从哪来的"。为空表示没走解密。
	UnsealedBy string `json:"unsealedBy,omitempty"`
}

// liveEndpoint 与两侧产品配置的 endpoint 字段一致（产品身份字段，不改）。
var liveEndpoint = map[variant.ID]string{
	variant.CN:   "https://www.workbuddy.cn",
	variant.Intl: "https://www.workbuddy.ai",
}

// credKindOf 把凭据形态压成一个界面能直接用的取值。
//
// "信封且没解开"与"信封但解开了"必须分开报：前者的对策是去装/修客户端，
// 后者是正常可用状态。含糊地都说成"信封"会让用户以为功能没生效。
func credKindOf(c credential.Credential) string {
	switch {
	case c.Unsealed:
		return "unsealed"
	case c.Sealed:
		return "envelope"
	default:
		return "plaintext"
	}
}

// readCredential 取一侧可用的明文凭据。
//
// 这里只是把 credential 包接进来，不自己再写一遍解析：凭据文件的布局与
// 信封的解法都由它对客户端负责，两处各写一份迟早分叉。
func readCredential(probe *variant.Probe, id variant.ID) (credential.Credential, error) {
	return credential.Resolve(probe, id)
}

// ProxyCredentials 是本地 API 代理层需要的最小凭据集（token+uid）。
type ProxyCredentials struct {
	Token string
	UID   string
}

// IntlProxyCredentials 读国际侧凭据（代理层用）。
//
// 国际侧一直是明文，所以这条链路很轻：读文件 + 解析 + 校验非空。
// 国内侧的信封不走这里——代理只服务国际后端。
func IntlProxyCredentials(probe *variant.Probe) (ProxyCredentials, error) {
	c, err := readCredential(probe, variant.Intl)
	if err != nil {
		return ProxyCredentials{}, err
	}
	if c.Token == "" {
		return ProxyCredentials{}, fmt.Errorf("accessToken 为空")
	}
	return ProxyCredentials{Token: c.Token, UID: c.UID}, nil
}

// IntlEndpoint 返回国际后端地址（与产品配置 endpoint 一致）。
func IntlEndpoint() string { return liveEndpoint[variant.Intl] }

// IntlCommonHeaders 官方接口通用附加头（X-Product / User-Agent）。
func IntlCommonHeaders() map[string]string { return liveCommonHeaders(variant.Intl) }

// liveCacheTTL 内的重复请求直接用缓存：面板每点一次就打一轮官方接口，
// 没必要；60 秒内的重复点击共用一份数据。
const liveCacheTTL = 60 * time.Second

var liveCache = struct {
	sync.Mutex
	data map[variant.ID]*LiveAccount
	at   map[variant.ID]time.Time
}{data: map[variant.ID]*LiveAccount{}, at: map[variant.ID]time.Time{}}

// LiveAccounts 拉取两侧实时账号数据（带缓存）。
// 一侧失败不影响另一侧：失败信息放在该侧的 Err 里，界面照常渲染。
func LiveAccounts(probe *variant.Probe) map[string]*LiveAccount {
	liveCache.Lock()
	defer liveCache.Unlock()
	out := map[string]*LiveAccount{}
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		if at, ok := liveCache.at[id]; ok && time.Since(at) < liveCacheTTL {
			out[string(id)] = liveCache.data[id]
			continue
		}
		acc := fetchLiveAccount(probe, id)
		liveCache.data[id] = acc
		liveCache.at[id] = time.Now()
		out[string(id)] = acc
	}
	return out
}

// fetchLiveAccount 实际拉取一侧的数据。网络错误一律降级成 Err，
// 不让单侧的失败拖垮整个面板。
func fetchLiveAccount(probe *variant.Probe, id variant.ID) *LiveAccount {
	acc := &LiveAccount{}
	cred, err := readCredential(probe, id)
	acc.UID = cred.UID
	acc.Nickname = cred.Nickname
	acc.CredKind = credKindOf(cred)
	acc.UnsealedBy = cred.UnsealedBy
	if err != nil {
		// 这里如实回 credKindOf 的判定：还是信封就是 envelope，
		// 界面据此提示"要装/修客户端"，而不是笼统一句读不到。
		acc.Err = err.Error()
		return acc
	}
	if cred.Token == "" {
		acc.Err = "凭据里没有可用的令牌"
		return acc
	}
	endpoint := liveEndpoint[id]

	models, promos, err := fetchLiveModels(endpoint, cred, id)
	if err != nil {
		acc.Err = "拉模型清单失败：" + err.Error()
		return acc
	}
	acc.Models = models
	acc.Promos = promos
	acc.OK = true

	// 积分余量拿不到不致命：模型与活动已经足够展示。
	if pkgs, err := fetchLivePackages(endpoint, cred); err == nil {
		acc.Packages = pkgs
	}
	return acc
}

// liveJSON 发一次官方接口请求（10 秒超时）。只读。
// extra 是额外请求头：/v3/config 必须带 X-Product 与 User-Agent，
// 缺了会被网关 400 拒掉（实测 2026-09-26）。
func liveJSON(endpoint, path, method, token, uid string, body io.Reader, extra map[string]string) ([]byte, error) {
	req, err := http.NewRequest(method, endpoint+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-User-Id", uid)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Code != 0 && envelope.Code != 200 {
		return nil, fmt.Errorf("code %d: %s", envelope.Code, envelope.Msg)
	}
	return envelope.Data, nil
}

// xProductHeader 是产品请求必须带的 X-Product 值（= 各侧 deploymentType，
// 来自两侧 product.json；拦截器对所有产品请求统一注入，/v3/config 缺了不行）。
var xProductHeader = map[variant.ID]string{
	variant.CN:   "workbuddy",
	variant.Intl: "workbuddy-ai",
}

// liveCommonHeaders 各接口通用额外头。User-Agent 缺省值（Python-urllib 等）
// 会被网关 400 拒掉，所以显式带上。
func liveCommonHeaders(id variant.ID) map[string]string {
	return map[string]string{
		"X-Product":  xProductHeader[id],
		"User-Agent": "WorkBuddy/5.6 (wbmux)",
	}
}

// fetchLiveModels 拉实时模型清单与活动。
//
// 首选 GET /v3/config——渲染层模型选择器用的就是这份：26 个模型
// （含 DeepSeek 系）、中文描述、上下文窗口、限时免费活动全在里面。
// 失败时回退到 /v2/enterprises/personal/models（少 DeepSeek 层）。
func fetchLiveModels(endpoint string, cred credential.Credential, id variant.ID) ([]LiveModel, []LivePromo, error) {
	hdr := liveCommonHeaders(id)
	raw, err := liveJSON(endpoint, "/v3/config", "GET", cred.Token, cred.UID, nil, hdr)
	if err != nil {
		raw, err = liveJSON(endpoint, "/v2/enterprises/personal/models", "GET", cred.Token, cred.UID, nil, nil)
		if err != nil {
			return nil, nil, err
		}
	}
	var doc struct {
		Models     []liveModelRaw `json:"models"`
		Promotions []livePromoRaw `json:"modelPromotions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	byID := map[string]LiveModel{}
	var order []string
	add := func(m liveModelRaw) {
		if m.ID == "" {
			return
		}
		if _, ok := byID[m.ID]; ok {
			return
		}
		lm := LiveModel{ID: m.ID, Name: m.Name, RateRaw: m.Credits}
		if mm := creditsRateRe.FindStringSubmatch(m.Credits); mm != nil {
			lm.Rate = parseRateNumber(mm[1])
		}
		byID[m.ID] = lm
		order = append(order, m.ID)
	}
	// 同一模型可能出现在多个配置层，字段补齐规则：
	//   - 文本类（描述）：先到先得，后层只补空
	//   - 数值类（上下文/输出上限）：为 0 才补，避免后层的 0 覆盖真实值
	//   - 能力开关：取或。缺字段被解析成 false，若按"覆盖"处理，
	//     后一层没写这个键就会把前一层给的能力抹掉。这些键只会被
	//     厂商用来授予能力，不会用来显式收回，取或才是安全语义。
	fill := func(m liveModelRaw) {
		if lm, ok := byID[m.ID]; ok {
			if lm.Desc == "" {
				lm.Desc = m.DescriptionZh
				if lm.Desc == "" {
					lm.Desc = m.DescriptionEn
				}
			}
			if lm.Ctx == 0 {
				lm.Ctx = m.MaxInputTokens
			}
			if lm.MaxOutput == 0 {
				lm.MaxOutput = m.MaxOutputTokens
			}
			lm.Tools = lm.Tools || m.SupportsToolCall
			lm.Images = lm.Images || m.SupportsImages
			byID[m.ID] = lm
		}
	}
	for _, m := range doc.Models {
		add(m)
		fill(m)
	}
	out := make([]LiveModel, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	applyFreeNow(out, doc.Promotions)
	sortLiveModels(out)
	return out, promosOf(doc.Promotions), nil
}

// liveModelRaw 是配置里一条模型的原文结构。
//
// 抽成具名类型而不是就地写匿名结构：这份字段表原先在三处各写了一遍
// （v3/config、include 层、补齐逻辑），加字段时漏掉一处就会静默丢数据。
type liveModelRaw struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Credits         string `json:"credits"`
	DescriptionZh   string `json:"descriptionZh"`
	DescriptionEn   string `json:"descriptionEn"`
	MaxInputTokens  int    `json:"maxInputTokens"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	// 能力开关。官方配置里确实带这两个键（实测 2026-09-27：
	// deepseek-v4.1-flash 与两个限时免费混元模型都是 true），
	// 是"这个后端模型支不支持工具/图片"的权威来源，别靠猜。
	SupportsToolCall bool `json:"supportsToolCall"`
	SupportsImages   bool `json:"supportsImages"`
}

// livePromoRaw 对应 modelPromotions 的原文结构（只取界面要用的字段）。
type livePromoRaw struct {
	Badge struct {
		Label string `json:"label"`
		Color string `json:"color"`
	} `json:"badge"`
	Discount struct {
		Factor float64 `json:"factor"`
	} `json:"discount"`
	Enabled bool `json:"enabled"`
	Hover   struct {
		TextZh string `json:"textZh"`
	} `json:"hover"`
	ModelIDs []string `json:"modelIds"`
	Priority int      `json:"priority"`
	Schedule struct {
		ValidFrom  string `json:"validFrom"`
		ValidUntil string `json:"validUntil"`
	} `json:"schedule"`
}

// promosOf 从原文活动列表里挑出界面要展示的：enabled 且带 badge 的。
// 时间字段原样透传，不解析——界面显示"至 2026-09-30"足够，
// 解析错了反而误导（与限流重置时间同一原则）。
func promosOf(raw []livePromoRaw) []LivePromo {
	var out []LivePromo
	for _, p := range raw {
		if !p.Enabled {
			continue
		}
		lp := LivePromo{
			Label:      p.Badge.Label,
			Color:      p.Badge.Color,
			ModelIDs:   p.ModelIDs,
			ValidFrom:  p.Schedule.ValidFrom,
			ValidUntil: p.Schedule.ValidUntil,
			TextZh:     p.Hover.TextZh,
		}
		out = append(out, lp)
	}
	return out
}

// applyFreeNow 给被"折扣因子为 0 且已启用"的活动覆盖的模型打上 FreeNow。
// 时间窗只在能解析时校验，解析不了的活动按仍有效处理——宁可多显示
// 一个"免费"标签，也不把还在期的活动静默吞掉。
func applyFreeNow(models []LiveModel, promos []livePromoRaw) {
	now := time.Now()
	for _, p := range promos {
		if !p.Enabled || p.Discount.Factor != 0 {
			continue
		}
		if p.Schedule.ValidUntil != "" {
			if until, err := time.Parse(time.RFC3339, p.Schedule.ValidUntil); err == nil && until.Before(now) {
				continue
			}
		}
		if p.Schedule.ValidFrom != "" {
			if from, err := time.Parse(time.RFC3339, p.Schedule.ValidFrom); err == nil && from.After(now) {
				continue
			}
		}
		for i := range models {
			for _, id := range p.ModelIDs {
				if models[i].ID == id {
					models[i].FreeNow = true
					models[i].FreeLabel = p.Badge.Label
				}
			}
		}
	}
}

func sortLiveModels(models []LiveModel) {
	sort.SliceStable(models, func(i, j int) bool { return models[i].Rate > models[j].Rate })
}

// fetchLivePackages 拉积分包余量。字段来自实测响应：
// Packages[].Cycle{Total,Remain,Used,Frozen}Capacity + CapacityUnit。
func fetchLivePackages(endpoint string, cred credential.Credential) ([]LivePackage, error) {
	raw, err := liveJSON(endpoint, "/billing/meter/get-user-resource-summary", "POST", cred.Token, cred.UID, strings.NewReader("{}"), nil)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Packages []struct {
			PackageCode  string      `json:"PackageCode"`
			Total        json.Number `json:"CycleTotalCapacity"`
			Remain       json.Number `json:"CycleRemainCapacity"`
			Used         json.Number `json:"CycleUsedCapacity"`
			CapacityUnit string      `json:"CapacityUnit"`
		} `json:"Packages"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []LivePackage
	for _, p := range doc.Packages {
		lc := LivePackage{Code: p.PackageCode, Unit: p.CapacityUnit}
		lc.Total, _ = p.Total.Float64()
		lc.Remain, _ = p.Remain.Float64()
		lc.Used, _ = p.Used.Float64()
		out = append(out, lc)
	}
	return out, nil
}

// parseRateNumber 从 "2.00" 这类片段解析倍数；解析不了返回 0。
func parseRateNumber(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%g", &f)
	return f
}
