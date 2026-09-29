package webui

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/usage"
)

// ---------- 代理亲眼见到的限流状态 ----------
//
// # 为什么需要它（2026-09-29）
//
// 用户原话："这个提示完全看不了啊"。查下来问题不在排版，而在**答错了问题**：
// 额度面板原本读的是客户端会话文件里的历史错误记录，于是它显示的是
// **过去 7 天被限过几次**，而用户要问的是"**我现在能不能用、什么时候能用**"。
// 那次面板显示"限制已过，可以继续用（2026-09-27 重置）"，同时用户正在被
// 09-29 的 429 挡着——数据是旧的，结论自然也是错的。
//
// 唯一真正跟上游说过话的是本机代理。它每次收到非 200 都看得见状态码和上游
// 原话（含"will reset at <时刻>"），所以**把这份观察记下来**就是权威的当前
// 状态。这也顺带解决了"客户端那边只给一句 429 + Trace Id"的问题。
//
// # 与 usage 包的分工
//
// usage 包管"账单与历史"（读客户端数据），这里管"此刻的可用性"（代理自己的
// 观察）。两者都往额度面板上送，但**结论以这里为准**。

// ProxyQuota 是一个模型此刻的可用状态（由代理观察到）。
type ProxyQuota struct {
	// Model 是还原后的官方模型名（如 deepseek-v4.1-flash）。
	Model string `json:"model"`
	// State：ok / rate_limited / exhausted。
	//
	//   - rate_limited：频率/用量超限，**等到 UntilMs 就恢复**；
	//   - exhausted：额度用尽，**不会自己恢复**，要充值（上游不给我们恢复时刻）。
	State string `json:"state"`
	// UntilMs 是预计恢复时刻（毫秒）。仅 rate_limited 且上游给了时刻时非 0。
	UntilMs int64 `json:"untilMs,omitempty"`
	// UntilText 是上游原文里的恢复时刻，原样保留给人看（如
	// "2026-09-29 22:18:43 UTC+8"）——它比我们换算出来的更可信。
	UntilText string `json:"untilText,omitempty"`
	// SeenMs 是这条状态被观察到的时刻（毫秒）。
	SeenMs int64 `json:"seenMs"`
	// Message 是上游原话（截断），排错时有用。
	Message string `json:"message,omitempty"`
}

// resetAtRe 抓上游原话里的恢复时刻。
//
// 实测原文（2026-09-29）：
//
//	usage exceeds frequency limit, but don't worry, your usage will reset at
//	2026-09-29 22:18:43 UTC+8, alternatively, you can switch to the other models
//
// 只抓"reset at <时间>"这一段；后面的时区单独看。
var resetAtRe = regexp.MustCompile(`reset at\s+(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2})`)

// resetTZRe 抓时区标注（`UTC+8` / `UTC-5`）。上游没给就按 +8 算——
// 这是国内侧服务，默认本地时区最合理。
var resetTZRe = regexp.MustCompile(`UTC([+-]\d{1,2})`)

// parseResetAt 从上游原话里解析恢复时刻，返回毫秒时间戳（0 表示没解析出来）。
//
// 单独抽出来是因为这段逻辑值得单测：上游文案一变，这里是最先坏的地方，
// 而坏掉的表现是"面板显示不了恢复时间"——不会崩，只会静默变差。
func parseResetAt(msg string) (int64, string) {
	m := resetAtRe.FindStringSubmatch(msg)
	if m == nil {
		return 0, ""
	}
	raw := strings.Replace(m[1], "T", " ", 1)

	// 时区：优先取原文里的标注，缺省 +8。
	//
	// 注意别用 time.Parse("Z07", "UTC-5")——Z07 只认 `-05` 这种形态，
	// 带 "UTC" 字面的会解析失败并**静默退回默认值**（第一次就是这么写错的，
	// 结果 UTC-5 被当成 +8，差了 13 小时）。所以直接把数字抠出来。
	off := 8
	if tz := resetTZRe.FindStringSubmatch(msg); tz != nil {
		if n, err := strconv.Atoi(tz[1]); err == nil && n >= -12 && n <= 14 {
			off = n
		}
	}
	loc := time.FixedZone("", off*3600)
	t, err := time.ParseInLocation("2006-01-02 15:04:05", raw, loc)
	if err != nil {
		return 0, raw
	}
	return t.UnixMilli(), raw
}

// looksExhausted 判断这条 429 是不是"额度用尽"（不可自愈）。
//
// 上游对额度用尽的措辞与频率超限不同（实测 2026-09-28 那次是
// "Credits exhausted"），且**不会给恢复时刻**。两者的处置完全相反
// （等 vs 充值），所以必须分开——混成一句"被限了"会让人白等一晚。
func looksExhausted(msg string) bool {
	if resetAtRe.MatchString(msg) {
		return false // 给了恢复时刻 → 是频率类
	}
	l := strings.ToLower(msg)
	return strings.Contains(l, "credit") && strings.Contains(l, "exhaust")
}

// rateState 是 Server 上的限流状态表（model → 状态）。
//
// 用 model 作键：限流是**按模型单独计**的（实测同一次里 ds4.1 被拒而
// hy4-preview-f 照常可用），所以状态天然是"每个模型一条"。
type rateState struct {
	mu    sync.Mutex
	items map[string]ProxyQuota
}

// observe 记下代理刚看到的结局，并返回是否改变了对该模型的判断。
//
// 成功要**清掉**状态：不能只记坏消息，否则模型恢复了面板还一直报限流，
// 用户会以为没恢复（这正是原来那个面板的毛病之一——它读的历史记录只增不减）。
func (s *Server) observe(model string, status int, msg string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	s.rate.mu.Lock()
	defer s.rate.mu.Unlock()
	if s.rate.items == nil {
		s.rate.items = map[string]ProxyQuota{}
	}

	if status == http.StatusOK {
		delete(s.rate.items, model)
		return
	}
	// 只有 429 才是"限流"；其它错误（400 模型名不对、500 上游故障）
	// 报成限流会把排查带偏。
	if status != http.StatusTooManyRequests {
		return
	}

	q := ProxyQuota{Model: model, SeenMs: time.Now().UnixMilli(),
		Message: cutStr(compactError(msg), 300)}
	if looksExhausted(msg) {
		q.State = "exhausted"
	} else {
		q.State = "rate_limited"
		q.UntilMs, q.UntilText = parseResetAt(msg)
	}
	s.rate.items[model] = q
}

// quotaSnapshot 返回当前状态（按模型名排序，界面好对照）。
func (s *Server) quotaSnapshot() []ProxyQuota {
	s.rate.mu.Lock()
	defer s.rate.mu.Unlock()
	out := make([]ProxyQuota, 0, len(s.rate.items))
	for _, q := range s.rate.items {
		out = append(out, q)
	}
	// 排序：限流中的在前，然后按恢复时刻。界面不用再排一次。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && lessQuota(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func lessQuota(a, b ProxyQuota) bool {
	ra, rb := a.State == "rate_limited", b.State == "rate_limited"
	if ra != rb {
		return ra // 限流中的排前面
	}
	if a.UntilMs != b.UntilMs {
		return a.UntilMs < b.UntilMs
	}
	return a.Model < b.Model
}

// usageResponse 在 usage.Survey 上挂一块"代理亲眼见到的状态"。
//
// 用内嵌而不是改 usage 包：那份状态是**本机代理的观察**，与"读客户端账单"
// 是两回事，不该混进同一个包（usage 包里没有、也不该有 HTTP 上游的概念）。
type usageResponse struct {
	usage.Survey
	// ProxyQuota 是代理观察到的当前限流状态（按模型）。
	//
	// **可用性以它为准**：Survey 里的 byModel/limits 是客户端落盘的历史记录，
	// 只增不减，无法反映"已经恢复"。
	ProxyQuota []ProxyQuota `json:"proxyQuota"`
}

// handleUsage 返回额度账单 + 代理观察到的当前可用性。
//
// 账单部分（Survey）读的是客户端落盘的历史记录，量的是"花了多少"；
// ProxyQuota 是代理自己的观察，答的是"现在能不能用"。两块都在一个响应里
// 是因为界面要一起渲染——但**结论以 ProxyQuota 为准**。
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, usageResponse{
		Survey:     usage.Build(s.probe()),
		ProxyQuota: s.quotaSnapshot(),
	})
}
