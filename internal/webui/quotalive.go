package webui

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
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
	// seen 是"代理亲眼看它成功过"的模型集合。
	//
	// 用它把"确认可用"与"从没验证过"分开：界面上前者显示"可用"、
	// 后者显示"未验证"。没有它的话，一个从没发过请求的模型也会被写成
	// "可用"——那是无根据的断言，用户一试就 429。
	seen map[string]bool
	// seeded 表示"已尝试从日志恢复过"，只做一次。
	seeded bool
}

// quotaSeed 是从日志里恢复出来的两组信息。
type quotaSeed struct {
	// Limited 是"最后一条事件是被 429 拒绝"的模型。
	Limited []ProxyQuota
	// OK 是"最后一条事件是成功"的模型。
	OK []string
}

// 日志里两行关键格式（见 proxylog.go / openai.go）：
//
//	2026-09-29 19:57:13  被国际后端拒绝 model=deepseek-v4.1-flash 上游 HTTP 429: <上游原话>
//	2026-09-29 20:20:54  转发完成 model=hy4-preview-f 经国际后端 …
var logModelRe = regexp.MustCompile(`model=([^\s]+)`)
var logHTTPRe = regexp.MustCompile(`上游 HTTP (\d{3}):\s*(.*)$`)

// seedFromLog 从 gui.log 的尾部恢复限流状态。
//
// # 为什么必须做（2026-09-29 用户反馈"重启后没显示那个限流了"）
//
// 状态本来只在内存里，**进程一重启就全没了**——而那时模型其实还被限着。
// 更糟的是界面把"没有观察数据"渲染成了"都能用"，等于给出一个反着的结论：
// 用户以为能用了，一发请求又 429。
//
// 日志是持久化的，而且写的正是同一批事实（谁被拒了、上游说什么时候恢复），
// 所以启动时拿它恢复既准又不用新开存储。只读尾部若干 KB：日志可能有几 MB。
//
// 幂等，由 rateState.seeded 保证只跑一次。
func (s *Server) seedFromLog() {
	s.rate.mu.Lock()
	defer s.rate.mu.Unlock()
	if s.rate.seeded {
		return
	}
	s.rate.seeded = true

	seed := parseQuotaLog(s.logPath(), time.Now().UnixMilli())
	for _, q := range seed.Limited {
		if s.rate.items == nil {
			s.rate.items = map[string]ProxyQuota{}
		}
		s.rate.items[q.Model] = q
	}
	for _, m := range seed.OK {
		if s.rate.seen == nil {
			s.rate.seen = map[string]bool{}
		}
		s.rate.seen[m] = true
	}
}

// logPath 返回 gui.log 的位置。logPathFn 是测试接缝。
func (s *Server) logPath() string {
	if s.logPathFn != nil {
		return s.logPathFn()
	}
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "gui.log")
}

// logTailBytes 是从日志尾部读多少字节。
//
// 512 KB 够覆盖"最近几千条事件"，而日志本身可能长到几十 MB——全读会拖慢启动。
const logTailBytes = 512 << 10

// parseQuotaLog 从日志尾部算出"每个模型此刻的状态"。
//
// now 传进来是为了可测：解析结果里"已过期的限流"要被丢掉，而那是相对当前
// 时刻判断的。
func parseQuotaLog(path string, now int64) quotaSeed {
	if path == "" {
		return quotaSeed{}
	}
	f, err := os.Open(path)
	if err != nil {
		return quotaSeed{}
	}
	defer func() { _ = f.Close() }()

	// 只在**真的从中间截断**时才丢掉第一行（那半条日志解析出来是错的）。
	// 无条件丢会把正常的小日志的第一条真记录吃掉——写错过一次。
	truncated := false
	if st, err := f.Stat(); err == nil && st.Size() > logTailBytes {
		_, _ = f.Seek(st.Size()-logTailBytes, io.SeekStart)
		truncated = true
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return quotaSeed{}
	}
	lines := strings.Split(string(raw), "\n")
	if truncated && len(lines) > 0 {
		lines = lines[1:]
	}

	// 按时间顺序扫描，后来的事件覆盖先前的：最后一条才代表"此刻"。
	state := map[string]ProxyQuota{}
	ok := map[string]bool{}
	for _, line := range lines {
		m := logModelRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		model := m[1]

		// 成功 → 这个模型此刻是好的，清掉记录并记进"确认可用"
		// （与 observe 的口径一致）。
		if strings.Contains(line, "转发完成") {
			delete(state, model)
			ok[model] = true
			continue
		}
		// 只认"被上游拒绝且是 429"。转发失败（网络）不是配额状态，
		// 「收到请求」是在途，都不能改判断。
		if !strings.Contains(line, "拒绝") {
			continue
		}
		h := logHTTPRe.FindStringSubmatch(line)
		if h == nil || h[1] != "429" {
			continue
		}
		msg := h[2]
		q := ProxyQuota{Model: model, Message: cutStr(compactError(msg), 300)}
		if looksExhausted(msg) {
			q.State = "exhausted"
		} else {
			q.State = "rate_limited"
			q.UntilMs, q.UntilText = parseResetAt(msg)
		}
		q.SeenMs = now
		state[model] = q
	}

	var out quotaSeed
	for _, q := range state {
		// 恢复时刻已过 → 这次限流结束了，不该再报。日志里没有"恢复"事件
		// （恢复是时间到了自然发生），所以只能靠时刻判断。
		if q.State == "rate_limited" && q.UntilMs > 0 && q.UntilMs <= now {
			continue
		}
		out.Limited = append(out.Limited, q)
	}
	for m := range ok {
		// 被限的模型不该同时出现在"确认可用"里（后发生的限流已把它移出
		// ok 的不是——ok 只由"转发完成"写入，而 429 之后不会有完成记录）。
		if _, limited := state[m]; !limited {
			out.OK = append(out.OK, m)
		}
	}
	return out
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
	// 先补上"重启前"的状态，否则下面那句"成功就删记录"会把日志里恢复出来的
	// 限流痕迹抹掉，而那个模型其实还被限着。
	s.seedFromLog()
	s.rate.mu.Lock()
	defer s.rate.mu.Unlock()
	if s.rate.items == nil {
		s.rate.items = map[string]ProxyQuota{}
	}

	if status == http.StatusOK {
		delete(s.rate.items, model)
		if s.rate.seen == nil {
			s.rate.seen = map[string]bool{}
		}
		s.rate.seen[model] = true
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

// quotaOKSnapshot 返回"代理确认可用"的模型（排序后）。
func (s *Server) quotaOKSnapshot() []string {
	s.seedFromLog()
	s.rate.mu.Lock()
	defer s.rate.mu.Unlock()
	out := make([]string, 0, len(s.rate.seen))
	for m := range s.rate.seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// quotaSnapshot 返回当前状态（按模型名排序，界面好对照）。
func (s *Server) quotaSnapshot() []ProxyQuota {
	s.seedFromLog()
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
	// ProxyOK 是代理**亲眼见它成功过**的模型。界面用它把"确认可用"与
	// "从没验证过"分开——后者显示"未验证"，而不是无根据地写"可用"。
	ProxyOK []string `json:"proxyOk"`
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
		ProxyOK:    s.quotaOKSnapshot(),
	})
}
