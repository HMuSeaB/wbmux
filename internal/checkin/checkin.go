// Package checkin 查签到的状态、领当天的积分。
//
// # 为什么独立于 usage
//
// usage 里那份实时账号数据（倍率、余额、限时免费）**只发只读请求**，
// 它有一条明确的纪律：面板刷新这种无害动作不许改变账号状态。
// 签到正相反——daily-checkin 是**写操作**，它会真的发积分。
// 两条链路的约束不同，混在一个包里，"刷一下面板"和"领一次积分"就会
// 共用同一段代码，迟早有人在只读路径上顺手调一次写接口。
//
// # 实测接口（2026-09-29）
//
//	POST {endpoint}/v2/billing/meter/checkin-activity-status   查询，幂等
//	POST {endpoint}/v2/billing/meter/daily-checkin             领取
//
// 两个都是 POST，但前者语义上是查询、重复调用无副作用，因此查询路径可以
// 随便刷（界面加载时打一次没问题）；只有后者是真的改状态。
//
// # 只有国内侧
//
// 国际侧同一个 status 接口返回 200 但 active=false（没开这个活动），
// 成长中心的 /v2/activity/growth/* 在国际侧是 404。所以界面不该给国际侧
// 摆一个永远空的签到页。
//
// # 幂等是设计的一部分
//
// "今天已签"是**正常**结果而不是失败：重复调用不会多领，这正是"早晚各跑一次、
// 晚的那次当作补签"这种用法的前提。因此它与真失败在返回值上是分开的
// （Result.AlreadyCheckedIn），不靠解析错误文案来区分。
package checkin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Deps 是一次签到调用所需的外部依赖。零值表示全部走真实实现。
//
// 用显式结构体而不是包级变量，是为了让"打真实后端"这件事在测试里**不可能**
// 意外发生：测试必须主动把 Endpoint 指到假服务器。
type Deps struct {
	// Probe 提供凭据文件位置与客户端安装位置。
	Probe *variant.Probe
	// Endpoint 覆盖接口根地址；留空时按档位的产品配置端点推导。
	Endpoint string
	// Cred 覆盖凭据来源；留空时走 credential.Resolve。
	Cred func(*variant.Probe, variant.ID) (credential.Credential, error)
	// HTTP 覆盖 HTTP 客户端；留空时用 20 秒超时的默认客户端。
	HTTP *http.Client
}

func (d Deps) endpoint(id variant.ID) (string, error) {
	if d.Endpoint != "" {
		return strings.TrimRight(d.Endpoint, "/"), nil
	}
	// 取产品配置里的 endpoint，而不是在这里再抄一份域名。
	//
	// 实测国内侧三个域名（copilot.tencent.com / www.codebuddy.cn /
	// www.workbuddy.cn）指向同一服务、checkin 接口都返回 200，所以用哪个都行；
	// 用产品配置的那个是为了跟"当前连的是哪个后端"保持一致。
	b, err := variant.Get(id)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(b.Endpoint, "/"), nil
}

func (d Deps) cred(p *variant.Probe, id variant.ID) (credential.Credential, error) {
	if d.Cred != nil {
		return d.Cred(p, id)
	}
	return credential.Resolve(p, id)
}

func (d Deps) probe() *variant.Probe {
	if d.Probe != nil {
		return d.Probe
	}
	return variant.DefaultProbe()
}

func (d Deps) http() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Status 是签到活动的状态快照。
type Status struct {
	// Active 表示该档位是否开了签到活动。国际侧实测为 false。
	Active bool `json:"active"`
	// TodayCheckedIn 表示今天是否已签。
	TodayCheckedIn bool `json:"todayCheckedIn"`
	// StreakDays 是当前连签天数。
	StreakDays int `json:"streakDays"`
	// DailyCredit / TodayCredit 是每日积分与今天已得积分。
	DailyCredit int `json:"dailyCredit"`
	TodayCredit int `json:"todayCredit"`
	// NextStreakDay / StreakBonusDays / StreakBonusCredit 描述连签奖励：
	// 还差几天到下一档、奖励送几天、送多少分。都为 0 表示当前不在奖励周期上。
	NextStreakDay     int `json:"nextStreakDay"`
	StreakBonusDays   int `json:"streakBonusDays"`
	StreakBonusCredit int `json:"streakBonusCredit"`
	// CheckinDates 是已签到的日期，后端的顺序即"最近的在前"。
	CheckinDates []string `json:"checkinDates,omitempty"`
	// WeekCheckinDays 是本周已签天数。
	WeekCheckinDays int `json:"weekCheckinDays"`
}

// Result 是一次领取的结果。
type Result struct {
	// AlreadyCheckedIn 表示今天已经签过了。
	//
	// 这是**正常**结果，不是失败：接口幂等，重复调用不会多领。
	// 与真失败分开成两个字段，而不是靠匹配错误文案来区分。
	AlreadyCheckedIn bool `json:"alreadyCheckedIn"`
	// Credit 是本次领到的积分；已签过时为 0。
	Credit int `json:"credit"`
	// Message 是后端给的原话（已签过时就是"今天已签到，请明天再来"）。
	Message string `json:"message,omitempty"`
	// Status 是领取之后查到的状态快照，尽力而为：
	// 查不到为 nil，不影响"领到了 / 已签过"这个结论。
	// 两条分支都补（含"已签过"那条）——连签天数是使用者最想知道的那个数。
	Status *Status `json:"status,omitempty"`
}

// ---------- 接口回话 ----------

// envelope 是官方接口的统一外壳（实测：响应外层包着 code/msg/data/requestId）。
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
	// Credit 是领取接口可能放在**顶层**的积分字段（与放在 data 里两种都可能）。
	Credit *int `json:"credit"`
}

// alreadyCheckedInCode 是"今天已签到"的业务码。
//
// 后端的形态不止一种：可能是 HTTP 400 + 这个码，也可能是 HTTP 200 + data:null，
// 甚至是 HTTP 200 + 这个码。三种都按"已签"处理。
const alreadyCheckedInCode = 10001

// statusRaw 是状态接口 data 的原文结构。
//
// 字段名照抄后端（snake_case），与对外的 Status 分开：以后端视角命名的结构
// 一旦被界面直用，后端改名就会连带改前端。
type statusRaw struct {
	Active            bool     `json:"active"`
	TodayCheckedIn    bool     `json:"today_checked_in"`
	StreakDays        int      `json:"streak_days"`
	DailyCredit       int      `json:"daily_credit"`
	TodayCredit       int      `json:"today_credit"`
	NextStreakDay     int      `json:"next_streak_day"`
	StreakBonusDays   int      `json:"streak_bonus_days"`
	StreakBonusCredit int      `json:"streak_bonus_credit"`
	CheckinDates      []string `json:"checkin_dates"`
	WeekCheckinDays   int      `json:"week_checkin_days"`
}

func (r statusRaw) toStatus() Status {
	return Status{
		Active:            r.Active,
		TodayCheckedIn:    r.TodayCheckedIn,
		StreakDays:        r.StreakDays,
		DailyCredit:       r.DailyCredit,
		TodayCredit:       r.TodayCredit,
		NextStreakDay:     r.NextStreakDay,
		StreakBonusDays:   r.StreakBonusDays,
		StreakBonusCredit: r.StreakBonusCredit,
		CheckinDates:      r.CheckinDates,
		WeekCheckinDays:   r.WeekCheckinDays,
	}
}

// Query 查一次签到状态。只读、可重复调用。
func Query(d Deps, id variant.ID) (Status, error) {
	env, err := post(d, id, "/v2/billing/meter/checkin-activity-status")
	if err != nil {
		return Status{}, err
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		// 状态接口回 null 说明这个后端没开这个活动（国际侧就是如此）。
		return Status{Active: false}, nil
	}
	var raw statusRaw
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		return Status{}, fmt.Errorf("签到状态回话看不懂（接口可能变了）: %w", err)
	}
	return raw.toStatus(), nil
}

// Claim 领取今天的积分。
//
// 已签过不算失败，见 Result.AlreadyCheckedIn。
func Claim(d Deps, id variant.ID) (Result, error) {
	env, err := post(d, id, "/v2/billing/meter/daily-checkin")
	if err != nil {
		// "今天已签到"会以业务错误的形式回来，在这里翻译掉——
		// 对使用者来说它不是错误。
		var biz *BizError
		if asBizError(err, &biz) && biz.Code == alreadyCheckedInCode {
			return attachStatus(d, id, Result{AlreadyCheckedIn: true, Message: biz.Message}), nil
		}
		return Result{}, err
	}

	res := Result{Message: strings.TrimSpace(env.Msg)}

	// 积分可能放在 data.credit，也可能直接放在顶层。两种都收。
	credit, ok := creditOf(env)
	switch {
	case ok:
		res.Credit = credit
	case len(env.Data) == 0 || string(env.Data) == "null":
		// 实测"今日已签"的一种形态就是 HTTP 200 + data:null。
		res.AlreadyCheckedIn = true
	default:
		// 回话形状既不是成功也没有 credit：如实报错，**不能**猜成"已签"——
		// 猜错的话用户以为签了、其实没领到，而且永远不会再查。
		return Result{}, fmt.Errorf("领取回话里既没有 credit 也不是空（接口可能变了）：%s", summarize(env.Data))
	}

	return attachStatus(d, id, res), nil
}

// attachStatus 尽力补一份状态快照。
//
// "已签过"这条分支同样要补：对使用者最有用的信息恰恰是"现在连签几天了"，
// 而那只能从状态接口拿。查不到不影响"领到了 / 已签过"这个结论。
func attachStatus(d Deps, id variant.ID, res Result) Result {
	if st, err := Query(d, id); err == nil {
		res.Status = &st
	}
	return res
}

// creditOf 从回话里取本次领到的积分。
//
// 两个位置都认，是因为实测只确认过"含 credit 字段"这一条，
// 没确认它一定在 data 里；两种都试比猜一种稳。
func creditOf(env envelope) (int, bool) {
	if env.Credit != nil {
		return *env.Credit, true
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return 0, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &obj); err != nil {
		return 0, false
	}
	raw, ok := obj["credit"]
	if !ok {
		return 0, false
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	return n, true
}

// ---------- 错误 ----------

// BizError 是后端明确回了一个业务错误码的情况。
//
// 单独成类型是为了让调用方能按码分支（"已签到"就是靠它识别），
// 而不是去匹配错误文案。
type BizError struct {
	Code    int
	Message string
}

func (e *BizError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("后端返回错误码 %d", e.Code)
	}
	return fmt.Sprintf("后端返回错误码 %d：%s", e.Code, e.Message)
}

func asBizError(err error, target **BizError) bool {
	e, ok := err.(*BizError)
	if ok {
		*target = e
	}
	return ok
}

// post 发一次签到接口请求并拆掉外壳，返回外壳本身。
//
// 请求体是空的：实测（2026-09-29）两个接口都不需要任何参数，
// 带 `{}` 反而与已验证的形态不一致。
func post(d Deps, id variant.ID, path string) (envelope, error) {
	endpoint, err := d.endpoint(id)
	if err != nil {
		return envelope{}, err
	}
	cred, err := d.cred(d.probe(), id)
	if err != nil {
		return envelope{}, err
	}
	if cred.Token == "" {
		return envelope{}, fmt.Errorf("凭据里没有可用的令牌")
	}

	req, err := http.NewRequest(http.MethodPost, endpoint+path, bytes.NewReader(nil))
	if err != nil {
		return envelope{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	req.Header.Set("X-User-Id", cred.UID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	// 网关会把缺省 UA（Python-urllib 之类）当爬虫拒掉，必须显式带一个。
	req.Header.Set("User-Agent", "WorkBuddy")

	resp, err := d.http().Do(req)
	if err != nil {
		// 错误里可能带上请求，而 URL 不带令牌、头才带，所以这里只报 err 本身，
		// 但保险起见还是过一遍替换（Credential.Redact 对空令牌是空操作）。
		return envelope{}, fmt.Errorf("请求签到接口失败：%s", cred.Redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return envelope{}, fmt.Errorf("读取签到接口回话失败：%w", err)
	}

	// 401 是登录态失效，与"业务拒绝"对策完全不同，单独说清楚。
	// 403 只作为权限/业务拒绝，**不能**据此判断登录过期。
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return envelope{}, fmt.Errorf("登录态已失效（HTTP 401），去客户端重新登录一次")
	case http.StatusForbidden:
		return envelope{}, fmt.Errorf("后端拒绝了这个请求（HTTP 403）：%s", summarize(body))
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		// 只带状态码与截断后的正文，不带令牌（正文本来也不该含令牌）。
		return envelope{}, fmt.Errorf("签到接口回了 HTTP %d，且不是合法 JSON：%s",
			resp.StatusCode, summarize(body))
	}

	// 200/0 都算成功态：官方接口不同版本用过两套码（与 usage 的 liveJSON 一致）。
	if resp.StatusCode == http.StatusOK && (env.Code == 0 || env.Code == 200) {
		return env, nil
	}
	if env.Code != 0 {
		return envelope{}, &BizError{Code: env.Code, Message: strings.TrimSpace(env.Msg)}
	}
	return envelope{}, fmt.Errorf("签到接口回了 HTTP %d：%s", resp.StatusCode, summarize(body))
}

// summarize 截断一段正文用于错误信息，并顺手把可能的令牌形状抹掉。
//
// 错误信息会被写进日志和界面，正文来自外部，不能原样长驱直入。
func summarize(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	if s == "" {
		return "（空正文）"
	}
	return s
}
