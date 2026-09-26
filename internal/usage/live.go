package usage

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

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
//     根下 auth.accessToken + account.uid。
//   - 请求：Authorization: Bearer <accessToken> + X-User-Id: <uid>。
//   - 端点：GET  {endpoint}/v2/enterprises/personal/models —— 实时产品配置
//     （models[].credits 倍率、modelPromotions 限时免费活动）；
//     POST {endpoint}/billing/meter/get-user-resource-summary —— 积分包余量。
//
// 只发只读请求。daily-checkin（签到）是改状态的操作，**绝不自动调用**；
// 优惠码兑换在官方网页上，这里只展示活动信息。

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
// （最常见：国内版凭据是加密信封，本地解不开，调不了官方接口）。
type LiveAccount struct {
	OK       bool          `json:"ok"`
	Err      string        `json:"err,omitempty"`
	Models   []LiveModel   `json:"models"`
	Promos   []LivePromo   `json:"promos"`
	Packages []LivePackage `json:"packages"`
}

// liveEndpoint 与两侧产品配置的 endpoint 字段一致（产品身份字段，不改）。
var liveEndpoint = map[variant.ID]string{
	variant.CN:   "https://www.workbuddy.cn",
	variant.Intl: "https://www.workbuddy.ai",
}

// liveAuthFile 返回一侧客户端认证文件的路径。
// 布局来自官方客户端（CodeBuddyExtension），按操作系统分目录。
func liveAuthFile(probe *variant.Probe, id variant.ID) string {
	dir := map[string]string{
		"windows": filepath.Join("AppData", "Local", "CodeBuddyExtension", "Data", "Public", "auth"),
		"darwin":  filepath.Join("Library", "Application Support", "CodeBuddyExtension", "Data", "Public", "auth"),
		"linux":   filepath.Join(".local", "share", "CodeBuddyExtension", "Data", "Public", "auth"),
	}[runtime.GOOS]
	name := map[variant.ID]string{
		variant.CN:   "workbuddy-desktop.info",
		variant.Intl: "workbuddy-desktop-ai.info",
	}[id]
	return filepath.Join(probe.Home, dir, name)
}

// liveCredentials 是认证文件里要用的两部分。Token 为空表示不可用。
type liveCredentials struct {
	Token string
	UID   string
	// Envelope 为 true 表示 accessToken 是加密信封（WorkBuddy 5.6+），
	// 客户端用 keyblob 自行解密，第三方拿不到明文就调不了接口。
	Envelope bool
}

// readLiveCredentials 读认证文件并取出 token/uid。
// accessToken 可能是明文字符串，也可能是 {$wbEncrypted:..., envelope:...}
// 加密信封对象——信封要如实上报，绝不能拿空串当 Bearer 发出去。
func readLiveCredentials(path string) (liveCredentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return liveCredentials{}, err
	}
	var root struct {
		Auth struct {
			AccessToken json.RawMessage `json:"accessToken"`
		} `json:"auth"`
		Account struct {
			UID string `json:"uid"`
		} `json:"account"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return liveCredentials{}, err
	}
	c := liveCredentials{UID: root.Account.UID}
	var token string
	if err := json.Unmarshal(root.Auth.AccessToken, &token); err == nil {
		token = strings.TrimSpace(token)
		if token == "" {
			return c, fmt.Errorf("accessToken 为空")
		}
		c.Token = token
		return c, nil
	}
	// 不是字符串——看一眼是不是信封对象（只看键名，不看内容）
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(root.Auth.AccessToken, &obj); err == nil {
		if _, ok := obj["$wbEncrypted"]; ok {
			c.Envelope = true
			return c, fmt.Errorf("凭据是 WorkBuddy 加密信封（5.6+），本地无法解密调用官方接口")
		}
	}
	return c, fmt.Errorf("accessToken 既不是明文也不是已知信封结构")
}

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
	cred, err := readLiveCredentials(liveAuthFile(probe, id))
	if err != nil {
		if cred.Envelope {
			acc.Err = err.Error() // 信封场景的报错本身已是完整说明，别再加前缀
		} else {
			acc.Err = "读认证文件失败：" + err.Error()
		}
		return acc
	}
	if cred.Envelope {
		acc.Err = "凭据是加密信封（WorkBuddy 5.6+），本地无法解密；实时倍率/余额请看客户端或官方账单页"
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
func fetchLiveModels(endpoint string, cred liveCredentials, id variant.ID) ([]LiveModel, []LivePromo, error) {
	hdr := liveCommonHeaders(id)
	raw, err := liveJSON(endpoint, "/v3/config", "GET", cred.Token, cred.UID, nil, hdr)
	if err != nil {
		raw, err = liveJSON(endpoint, "/v2/enterprises/personal/models", "GET", cred.Token, cred.UID, nil, nil)
		if err != nil {
			return nil, nil, err
		}
	}
	var doc struct {
		Models []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Credits        string `json:"credits"`
			DescriptionZh  string `json:"descriptionZh"`
			DescriptionEn  string `json:"descriptionEn"`
			MaxInputTokens int    `json:"maxInputTokens"`
		} `json:"models"`
		Promotions []livePromoRaw `json:"modelPromotions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	byID := map[string]LiveModel{}
	var order []string
	add := func(id, name, credits string) {
		if id == "" {
			return
		}
		if _, ok := byID[id]; ok {
			return
		}
		lm := LiveModel{ID: id, Name: name, RateRaw: credits}
		if mm := creditsRateRe.FindStringSubmatch(credits); mm != nil {
			lm.Rate = parseRateNumber(mm[1])
		}
		byID[id] = lm
		order = append(order, id)
	}
	// 描述就地补齐：同一模型可能出现在多个配置层，先到先得，
	// 但描述/上下文字段允许后层补上（主体层没有这些字段）。
	fill := func(m struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		Credits        string `json:"credits"`
		DescriptionZh  string `json:"descriptionZh"`
		DescriptionEn  string `json:"descriptionEn"`
		MaxInputTokens int    `json:"maxInputTokens"`
	}) {
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
			byID[m.ID] = lm
		}
	}
	for _, m := range doc.Models {
		add(m.ID, m.Name, m.Credits)
		fill(m)
	}
	// v3/config 是完整配置，不再有 include 层；fetchIncludedModels 仅作
	// 回退端点的旧结构保留。
	out := make([]LiveModel, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	applyFreeNow(out, doc.Promotions)
	sortLiveModels(out)
	return out, promosOf(doc.Promotions), nil
}

// fetchIncludedModels 拉 include 引用的配置层，只取 models。
func fetchIncludedModels(url string, cred liveCredentials, id variant.ID) ([]struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Credits        string `json:"credits"`
	DescriptionZh  string `json:"descriptionZh"`
	DescriptionEn  string `json:"descriptionEn"`
	MaxInputTokens int    `json:"maxInputTokens"`
}, error) {
	raw, err := liveJSON(url, "", "GET", cred.Token, cred.UID, nil, liveCommonHeaders(id))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Models []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			Credits        string `json:"credits"`
			DescriptionZh  string `json:"descriptionZh"`
			DescriptionEn  string `json:"descriptionEn"`
			MaxInputTokens int    `json:"maxInputTokens"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc.Models, nil
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
func fetchLivePackages(endpoint string, cred liveCredentials) ([]LivePackage, error) {
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
