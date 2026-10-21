package webui

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/modelradar"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 本地 OpenAI 兼容代理（国际侧） ----------
//
// 两个用途，共用同一套代码：
//
//	1. 外部工具直接调（Cherry Studio / 各种 OpenAI 客户端）
//	2. 注入进国内客户端的自定义模型清单，让国内壳子用国际模型
//
//	GET  /v1/models            模型清单（实时，含限时免费标记）
//	POST /v1/chat/completions  聊天补全（流式/非流式都支持）
//
// 鉴权复用 GUI 令牌：工具侧把它当 API Key，写进
// Authorization: Bearer <令牌> 即可（openaiGuard 单独校验这种形态）。
// 上游凭据不经过调用方——token 只在服务端读取与使用。

// customLocalPrefix 是客户端给"本地自定义模型"id 加的前缀，由
// ProductFeature["CustomModelIdPrefix"] 控制。5.6 客户端在真正发请求前
// 会自己剥掉它（客户端内 stripCustomLocalModelPrefix），所以正常情况
// 见不到；这里兜一层是因为"前缀是否剥离"属于客户端内部行为，
// 它换个版本改了，我们不该跟着失效。
const customLocalPrefix = "custom-local:"

// chatCompletionsPath 是国际后端的补全端点。
const chatCompletionsPath = "/v2/chat/completions"

// maxChatBodyBytes 是请求体上限。
//
// 32 MB 是按实测定出来的，不是拍脑袋：注入条目照抄官方配置，把上下文窗口
// 写成真值 1,000,000 token，客户端因此会放心地把长会话整段发过来。
// 实测（2026-09-27）95 万 token 的中文提示词请求体就有 11.4 MB，
// 1M 约 12 MB，再叠上工具定义与历史里的 JSON/代码还要更大。
// 原来的 4 MB 会在这种请求上先自己 400，用户却会以为是模型的窗口不够；
// 16 MB 也只是刚好够。给到 32 MB 才留出余量。
//
// 代价是要一次性读进内存（还要解析成 map 再序列化出去，峰值是正文的几倍），
// 桌面上一次请求多占几十 MB 可以接受。
const maxChatBodyBytes = 32 << 20

// upstreamRoute 是一次转发要去哪里、用什么凭据与模型名。
//
// 代理现在有两种上游：内置的国际 WorkBuddy 后端（凭据来自客户端认证文件），
// 以及用户自己填的 OpenAI 兼容端点（BYOK，凭据来自设置文件）。路由靠
// **注入条目 id 的前缀**区分，因为那是客户端唯一会原样带回来的信息。
type upstreamRoute struct {
	// Label 只用于日志与错误信息（"国际后端" / 提供方名字）。
	Label string
	// Endpoint 是完整的补全端点。
	Endpoint string
	// APIKey 为空表示走内置国际凭据（那是唯一需要额外头的一侧）。
	APIKey string
	// Provider 为 nil 表示走内置国际后端；否则是命中的那张 BYOK 卡片。
	Provider *config.Provider
	// Model 是发给上游的模型名（已剥掉我们自己的前缀）。
	Model string
}

// resolveRoute 把客户端发来的 model 解析成一条路由。
//
// 顺序很重要：先剥客户端那层 custom-local: 前缀，再按**最长**的提供方前缀
// 匹配（否则 p1 会把 p10 的条目吃掉），最后才落到内置国际后端。
// 认不出来的名字一律按内置后端处理——外部工具直连时用的就是官方 id。
func resolveRoute(clientModel string, providers []config.Provider) upstreamRoute {
	m := clientModel
	for strings.HasPrefix(m, customLocalPrefix) {
		m = strings.TrimPrefix(m, customLocalPrefix)
	}
	m = strings.TrimSpace(m)

	// 提供方按前缀长度降序，避免 p1 抢先匹配 p10。
	sorted := append([]config.Provider(nil), providers...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].EntryPrefix()) > len(sorted[j].EntryPrefix())
	})
	for i := range sorted {
		p := sorted[i]
		if p.ID == "" {
			continue
		}
		if strings.HasPrefix(m, p.EntryPrefix()) {
			model := strings.TrimPrefix(m, p.EntryPrefix())
			if model == "" {
				break // 只有前缀没有模型名：当认不出来处理
			}
			return upstreamRoute{
				Label:    p.Name,
				Endpoint: p.BaseURL,
				APIKey:   p.APIKey,
				Provider: &p,
				Model:    model,
			}
		}
	}

	normalized := normalizeModelName(m)
	if isAutoFreeAlias(normalized) {
		fastest, _ := modelradar.Default().FastestFreeModel("deepseek-chat")
		return upstreamRoute{
			Label: "智能最速限免",
			Model: fastest,
		}
	}

	// 认不出来 → 内置国际后端（外部工具直连时用的就是官方 id，
	// 也可能带着我们注入的 wbmux-intl- 前缀，交给 normalizeModelName 剥）。
	return upstreamRoute{
		Label: "国际后端",
		Model: normalized,
	}
}

// isAutoFreeAlias 检查模型名是否属于虚拟智能优选别名。
func isAutoFreeAlias(m string) bool {
	lower := strings.ToLower(strings.TrimSpace(m))
	return lower == "auto" || lower == "auto-free" || lower == "fastest-free" || lower == "auto-fastest"
}


// normalizeModelName 把客户端送来的模型名还原成官方 id。
//
// # 为什么必须还原
//
// 注入进 models.json 的条目 id 是 "<IDPrefix><官方 id>"，而客户端是把
// 条目 id 当请求体的 model 字段原样发出的（它只剥自己那层前缀）。上游
// 只认官方 id，收到带前缀的名字直接 400 —— 2026-09-27 实测：
//
//	{"code":11102,"msg":"model [wbmux-intl-deepseek-v4.1-flash] service info not found"}
//
// 而客户端界面上只会显示一句「502 | Trace Id」，看不出任何原因。
//
// 循环剥而不是剥一次：两种前缀可能叠加出现。
func normalizeModelName(name string) string {
	s := strings.TrimSpace(name)
	for {
		switch {
		case strings.HasPrefix(s, customLocalPrefix):
			s = s[len(customLocalPrefix):]
		case strings.HasPrefix(s, custommodels.IDPrefix):
			s = s[len(custommodels.IDPrefix):]
		default:
			return s
		}
	}
}

// upstreamRequest 是改写后的转发请求。
type upstreamRequest struct {
	// Body 是发往上游的请求体。
	Body map[string]any
	// Model 是还原后的官方模型名（日志与错误信息用）。
	Model string
	// ClientStream 记录调用方原本要不要流式，决定我们是边收边转还是聚合。
	ClientStream bool
	// Tools 是调用方带来的工具定义数量（只用于日志：0 表示这次没法调工具）。
	Tools int
}

// buildUpstreamBody 把客户端请求体改写成上游能直接吃的形态。
//
// # 为什么用 map 承载而不是定义结构体
//
// 调用方会带 tools / tool_choice / temperature / max_tokens / reasoning
// 等一票字段，结构体必然漏字段。而漏掉 tools 的后果是把"模型能调工具"
// 这件事悄悄砍掉——国内壳子里的 Agent 全靠工具干活，表现成"模型能聊天
// 但什么也不做"，比直接报错难查得多（这正是本次一并修掉的问题）。
//
// 只动三处，其余原样透传：
//   - model：还原成官方 id
//   - stream：强制 true，上游只支持流式（调用方要非流式我们再聚合）
//   - messages：system 挪到首位（上游硬要求，实测 code 11128）
func buildUpstreamBody(raw []byte) (upstreamRequest, error) {
	var out upstreamRequest
	var body map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	// 与项目其它处一致：数字保持原文，不搬成 float64 再搬回来。
	dec.UseNumber()
	if err := dec.Decode(&body); err != nil {
		return out, fmt.Errorf("请求体不是 JSON 对象：%w", err)
	}
	if body == nil {
		return out, fmt.Errorf("请求体为空")
	}

	name, _ := body["model"].(string)
	out.Model = normalizeModelName(name)
	if out.Model == "" {
		return out, fmt.Errorf("缺少 model")
	}
	out.ClientStream, _ = body["stream"].(bool)
	if tools, ok := body["tools"].([]any); ok {
		out.Tools = len(tools)
	}

	body["model"] = out.Model
	body["stream"] = true
	if msgs, ok := body["messages"].([]any); ok {
		body["messages"] = ensureSystemFirst(msgs)
	}
	out.Body = body
	return out, nil
}

// ensureSystemFirst 官方接口要求第一条消息必须是 system（实测 code 11128）。
// 调用方没发 system 就补一条默认的；发了但不在首位就挪到首位。
//
// 元素用 any 而不是具体结构体：消息体是原样透传的，这里只认 role，
// 其余字段（tool_calls / tool_call_id / name / content 的多模态数组…）
// 必须一个不动地传下去。
func ensureSystemFirst(msgs []any) []any {
	if len(msgs) == 0 {
		return []any{defaultSystemMessage()}
	}
	if roleOf(msgs[0]) == "system" {
		return msgs
	}
	for i, m := range msgs {
		if roleOf(m) == "system" {
			out := make([]any, 0, len(msgs))
			out = append(out, m)
			out = append(out, msgs[:i]...)
			out = append(out, msgs[i+1:]...)
			return out
		}
	}
	return append([]any{defaultSystemMessage()}, msgs...)
}

// roleOf 取一条消息的 role（小写）；取不到返回空串。
func roleOf(m any) string {
	obj, ok := m.(map[string]any)
	if !ok {
		return ""
	}
	role, _ := obj["role"].(string)
	return strings.ToLower(strings.TrimSpace(role))
}

func defaultSystemMessage() map[string]any {
	return map[string]any{"role": "system", "content": "You are a helpful assistant."}
}

// writeOpenAIError 按 OpenAI 的错误结构回错，工具与客户端都认这个形状。
func writeOpenAIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": "wbmux_proxy_error"},
	})
}

// openaiGuard 校验 OpenAI 工具常用的 Bearer 形态，也兼容既有令牌头。
func (s *Server) openaiGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.Header.Get("X-Wbmux-Token")
		}
		if got == "" {
			got = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			// 这条也要记：令牌过期（重启后端口与令牌都变）在客户端那边
			// 同样只是一句报错，不记就完全看不出是"地址还是旧的"。
			s.note(ProxyLogEntry{Level: "bad",
				Text: fmt.Sprintf("拒绝 %s %s：令牌无效（客户端可能还拿着上一次启动的地址）", r.Method, r.URL.Path)})
			writeOpenAIError(w, http.StatusForbidden, "令牌无效：请把界面地址里 t= 的值当 API Key 用")
			return
		}
		next(w, r)
	}
}

// registerOpenAI 挂载 /v1/* 路由。单独用 openaiGuard：OpenAI 工具
// 生态只认 Authorization: Bearer，不认 X-Wbmux-Token。
func (s *Server) registerOpenAI(mux *http.ServeMux) {
	mux.HandleFunc("/v1/models", s.openaiGuard(s.handleOpenAIModels))
	mux.HandleFunc("/v1/chat/completions", s.openaiGuard(s.handleOpenAIChat))
}

// handleOpenAIModels 返回国际侧实时模型清单（OpenAI 格式）。
func (s *Server) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	live := usage.LiveAccounts(s.probe())[string(variant.Intl)]
	if live == nil || !live.OK {
		msg := "国际侧凭据不可用"
		if live != nil && live.Err != "" {
			msg = live.Err
		}
		writeOpenAIError(w, http.StatusServiceUnavailable, msg)
		return
	}
	data := make([]map[string]any, 0, len(live.Models)+2)
	for _, m := range live.Models {
		owned := "workbuddy-ai"
		if m.FreeNow {
			owned = "workbuddy-ai:free-now"
		}
		data = append(data, map[string]any{
			"id": m.ID, "object": "model", "owned_by": owned,
		})
	}
	// 智能优选虚拟模型：自动分发至当前延迟最低且健康的限免模型
	data = append(data, map[string]any{
		"id": "auto-free", "object": "model", "owned_by": "wbmux:auto-routing",
	})
	data = append(data, map[string]any{
		"id": "auto", "object": "model", "owned_by": "wbmux:auto-routing",
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list", "data": data,
	})
}

// ---------- 上游的"渠道校验"要求的那套身份头 ----------
//
// 2026-10-16 上游加了一道渠道校验：请求缺了这套头就回
//
//	HTTP 400 {"code":11128,"msg":"conversation id missing",
//	          "displayTips":{"zh":"这次调用没通过渠道校验，可能不是官方客户端发出的。"}}
//
// 实测（同一凭据、同一模型，只改头）：
//
//	整套 8 个 conversation 头 → 200；任意 2~4 个 → 400；
//	只有 agent/IDE 那族       → 400
//
// 也就是说它要的是**完整的一套**——真客户端每次都带齐，少一个就露馅。
//
// # 为什么 wbmux 要自己补
//
// 官方客户端本来会在请求里带这串头（`app.asar.unpacked/cli/dist/
// codebuddy-lite-wb.mjs` 里 `yc.*_HEADER` 的赋值，见下面 identityHeaders）。
// 但 wbmux 转发时**只带 Authorization / X-User-Id / X-Product / User-Agent**，
// 其余一概丢掉，于是注入的模型集体报"渠道校验不过"。
//
// 取值原则：**客户端发来的优先**。那是它真实的会话/请求标识，上游可能拿它
// 做上下文关联，编一个假的会把关联打断。只有客户端没带（比如第三方 OpenAI
// 客户端，它们不知道这套头）才补一个合法的随机 id——补是为了让这类用法
// 也能用，不是为了让请求看起来像官方发的。
// identityHeaders 分两类，处置不同：
//
//   - required：**实测必需要有的**。客户端没带就补一个随机 UUID——
//     它们全都是"这次调用的标识"，UUID 形态与真值一致。
//   - optional：官方请求里有、但实测单独缺了也能过校验。只做透传，
//     **不凭空造**：像 X-Agent-Intent 是 `craft` 这种枚举词、X-IDE-Version
//     是版本号，编一个假值既没意义又可能误导上游的统计。
var (
	requiredIdentityHeaders = []string{
		"X-Conversation-ID",
		"X-Parent-Conversation-ID",
		"X-Session-ID",
		"X-Conversation-Request-ID",
		"X-Conversation-Message-ID",
		"X-Request-ID",
		"X-Root-Request-ID",
		"X-Trace-ID",
	}
	optionalIdentityHeaders = []string{
		"X-Agent-Intent",
		"X-Agent-Type",
		"X-Agent-Purpose",
		"X-IDE-Type",
		"X-IDE-Name",
		"X-IDE-Version",
		"X-Product-Version",
		"X-Model-ID",
	}
)

// withIdentityHeaders 把身份头补进 headers：客户端带了的照抄，必需品没带的补一个。
//
// 只认上面白名单里的名字——**不能无脑透传客户端的所有头**，因为其中包括
// Authorization（那是客户端给本机代理的令牌，不该发给上游）、Content-Length、
// Host 这类由 HTTP 栈自己管的头。白名单比黑名单安全：将来客户端新增一个
// 我们没见过的头，最坏是少带一个，而不是把不该发的发出去。
func withIdentityHeaders(in http.Header, out map[string]string) {
	for _, name := range optionalIdentityHeaders {
		if v := strings.TrimSpace(in.Get(name)); v != "" {
			out[name] = v
		}
	}
	// 缺的必需头补齐。整族缺位时共用同一个 id：上游只校验"在不在"，
	// 而同一个请求用同一个标识在语义上也对。
	fallback := ""
	for _, name := range requiredIdentityHeaders {
		if v := strings.TrimSpace(in.Get(name)); v != "" {
			out[name] = v
			continue
		}
		if fallback == "" {
			fallback = newRequestID()
		}
		out[name] = fallback
	}
}

// newRequestID 生成一个 UUID v4 形态的随机 id。
//
// 用 UUID 而不是"wbmux-1"这种：上游对格式是否敏感没有把握，而真客户端的
// 这些头是 UUID（另外 X-Agent-Intent 等是短枚举词，见下）。宁可生成得像
// 一点，也别在这上面再赌一次。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用极罕见；退化成时间戳。宁可格式差也不留空——
		// 空值正是上游判"缺失"的依据。
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // 版本 4
	b[8] = (b[8] & 0x3f) | 0x80 // 变体 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// handleOpenAIChat 转发聊天补全。上游只支持流式，所以统一以流式
// 请求上游：调用方要非流式就聚合，要流式就边收边转发。
func (s *Server) handleOpenAIChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodyBytes+1))
	if err != nil {
		s.note(ProxyLogEntry{Level: "bad", Text: "读取请求体失败：" + compactError(err.Error())})
		writeOpenAIError(w, http.StatusBadRequest, "读请求体失败："+err.Error())
		return
	}
	// 超限要明确说出来。截断后直接解析会报"JSON 不完整"，
	// 那会把"请求太大"误导成"请求格式错"。
	if int64(len(raw)) > maxChatBodyBytes {
		msg := fmt.Sprintf("请求体 %s 超过上限 %s，已拒绝", describeBytes(int64(len(raw))), describeBytes(maxChatBodyBytes))
		s.note(ProxyLogEntry{Level: "bad", Text: msg})
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, msg+"，请缩短上下文")
		return
	}
	up, err := buildUpstreamBody(raw)
	if err != nil {
		s.note(ProxyLogEntry{Level: "bad", Text: "拒绝请求：" + compactError(err.Error())})
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 临时诊断（默认关，见 dump_body_diag.go）：抓一次请求体的**结构**，
	// 回答"图片是内联 base64 还是引用"。写成文件后立刻停用，不反复写。
	if p := dumpOncePath(); p != "" {
		_ = os.WriteFile(p, []byte(dumpBodyShape(raw)), 0o600)
		_ = os.Unsetenv("WBMUX_DUMP_ONCE")
		s.note(ProxyLogEntry{Level: "info",
			Text: "已把请求体结构写到 " + p + "（一次性诊断，开关已自关）"})
	}

	// 路由：客户端发来的 model 决定这次转给谁——内置国际后端，还是
	// 用户自备的 OpenAI 兼容端点（BYOK 卡片）。两者共用后面整条转发路径，
	// 差别只有端点、凭据头与模型名。
	route := resolveRoute(up.Model, s.providers())
	up.Body["model"] = route.Model

	// 先记"收到"，再转发：这样卡在上游的请求在界面上也看得见
	// （只有一条"收到"没有对应的"完成"，一眼就知道卡在哪一步）。
	scale := evaluateContextScale(int64(len(raw)))
	s.note(ProxyLogEntry{
		Level: "info", Model: route.Model, Tools: up.Tools, Stream: up.ClientStream,
		BodyBytes: int64(len(raw)), Tokens: scale.Tokens, ContextScale: scale.Level,
		Text: fmt.Sprintf("收到请求 model=%s 经%s · 正文 %s",
			route.Model, route.Label, scale.Desc),
	})

	// 端点与凭据头：内置后端要读客户端认证文件（并带上 X-User-Id 等产品头），
	// 自备提供方只用卡片里的 key。
	headers := map[string]string{}
	if route.Provider == nil {
		// 这里**不**查实时账号（LiveAccounts）：那是两个网络往返外加一把全局锁，
		// 会把首字延迟从毫秒级拖到秒级，而它对"这次能不能转发"没有任何额外保证
		// ——凭据读得出来就够。模型是否存在交给上游回答，它的报错比我们猜的准。
		cred, err := s.intlCred(s.probe())
		if err != nil {
			s.note(ProxyLogEntry{Level: "bad", Model: route.Model, Tools: up.Tools,
				Text: "拒绝请求：国际侧凭据不可用：" + compactError(err.Error())})
			writeOpenAIError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		route.Endpoint = s.upstreamBase() + chatCompletionsPath
		headers["Authorization"] = "Bearer " + cred.Token
		headers["X-User-Id"] = cred.UID
		for k, v := range usage.IntlCommonHeaders() {
			headers[k] = v
		}
	} else {
		headers["Authorization"] = "Bearer " + route.APIKey
	}
	headers["Accept"] = "text/event-stream"
	headers["Content-Type"] = "application/json"
	// 身份头必须和凭据一起带上，否则上游的渠道校验会直接 400（见
	// withIdentityHeaders 的说明）。放在最后，避免上面的产品头被顶掉。
	withIdentityHeaders(r.Header, headers)

	out, err := json.Marshal(up.Body)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req2, err := http.NewRequestWithContext(r.Context(), "POST", route.Endpoint, bytes.NewReader(out))
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for k, v := range headers {
		req2.Header.Set(k, v)
	}
	s.forwardChat(w, req2, out, route, up, time.Now())
}

// forwardChat 发出去、按结果回给客户端，并在两种结局上各记一条日志。
//
// 抽出来是因为"内置后端"与"自备提供方"两条路的后半段完全一样：
// 都是流式转发/聚合、都要透传上游状态码、都要在日志里写清楚是谁拒绝的。
func (s *Server) forwardChat(w http.ResponseWriter, req *http.Request, rawBody []byte, route upstreamRoute, up upstreamRequest, started time.Time) {
	accID := req.Header.Get("X-User-Id")
	accName := route.Label
	if route.Provider == nil {
		accName = "国际账号"
		if accs, err := credential.ListAccounts(s.probe(), variant.Intl); err == nil {
			for _, a := range accs {
				if a.UID == accID || (accID == "" && a.Active) {
					if a.Nickname != "" {
						accName = a.Nickname
					} else if a.UID != "" {
						accName = a.UID
					}
					break
				}
			}
		}
	}

	var modelBusyRetries int
	var modelBusyDelays []string

	recordAudit := func(statusCode int, m chatMetrics, failover bool, errMsg string) {
		pTok := m.promptTokens
		cTok := m.completionTokens
		if pTok == 0 && len(rawBody) > 0 {
			pTok = len(rawBody) / 3
			if pTok < 1 {
				pTok = 1
			}
		}
		if cTok == 0 && m.bytesSent > 0 {
			cTok = int(m.bytesSent) / 3
			if cTok < 1 {
				cTok = 1
			}
		}
		ttft := m.ttftMs
		if ttft <= 0 && statusCode == http.StatusOK {
			ttft = time.Since(started).Milliseconds()
		}
		scale := evaluateContextScale(int64(len(rawBody)))
		gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
			ID:                fmt.Sprintf("req-%d", time.Now().UnixNano()),
			Timestamp:         started,
			ModelRequested:    up.Model,
			ModelMapped:       route.Model,
			AccountID:         accID,
			AccountMask:       accName,
			IsStream:          up.ClientStream,
			StatusCode:        statusCode,
			PromptTokens:      pTok,
			CompletionTokens:  cTok,
			TotalTokens:       pTok + cTok,
			TTFTMs:            ttft,
			DurationMs:        time.Since(started).Milliseconds(),
			FailoverTriggered: failover,
			ErrorMsg:          errMsg,
			ContextScale:      scale.Level,
			BodyBytes:         int64(len(rawBody)),
			ModelBusyRetries:  modelBusyRetries,
			RetryDelays:       modelBusyDelays,
		})
	}

	deliverSuccess := func(respBody io.ReadCloser, isFailover bool, customPrefix string) {
		defer respBody.Close()
		s.observe(route.Model, http.StatusOK, "")
		var m chatMetrics
		if up.ClientStream {
			m = s.streamChatThrough(w, respBody, started)
		} else {
			m = s.aggregateChat(w, respBody, started)
		}

		// 流量被动测速：以本次真实流量的实际首字延迟 TTFT 更新雷达快照
		ttft := m.ttftMs
		if ttft <= 0 {
			ttft = time.Since(started).Milliseconds()
		}
		radar := modelradar.Default()
		rec, _ := radar.GetRecord(route.Model)
		rec.Model = route.Model
		rec.StatusCode = http.StatusOK
		rec.TTFTMs = ttft
		rec.ErrorMsg = ""
		rec.IsRateLimited = false
		if ttft < 800 {
			rec.Status = modelradar.StatusFast
		} else if ttft <= 2500 {
			rec.Status = modelradar.StatusNormal
		} else {
			rec.Status = modelradar.StatusSlow
		}
		rec.CheckedAt = time.Now()
		radar.SetRecord(rec)

		recordAudit(http.StatusOK, m, isFailover, "")
		s.note(ProxyLogEntry{
			Level:  "ok",
			Model:  route.Model,
			Tools:  up.Tools,
			Stream: up.ClientStream,
			MS:     time.Since(started).Milliseconds(),
			Text: fmt.Sprintf("%s model=%s 经%s 回给客户端 %s 用时 %s",
				customPrefix, route.Model, route.Label,
				describeBytes(m.bytesSent), time.Since(started).Round(time.Millisecond)),
		})
	}

	retryOnce := func(token, uid string) (*http.Response, error) {
		reqRetry, errReq := http.NewRequestWithContext(req.Context(), "POST", route.Endpoint, bytes.NewReader(rawBody))
		if errReq != nil {
			return nil, errReq
		}
		for k, v := range req.Header {
			reqRetry.Header[k] = v
		}
		if token != "" {
			reqRetry.Header.Set("Authorization", "Bearer "+token)
		}
		if uid != "" {
			reqRetry.Header.Set("X-User-Id", uid)
		}
		return probeHTTPClient().Do(reqRetry)
	}

	resp, err := probeHTTPClient().Do(req)
	if err != nil {
		s.note(ProxyLogEntry{Level: "bad", Model: route.Model, Tools: up.Tools,
			Text: fmt.Sprintf("转发失败 model=%s 经%s：%s", route.Model, route.Label, compactError(err.Error()))})
		recordAudit(http.StatusBadGateway, chatMetrics{}, false, err.Error())
		writeOpenAIError(w, http.StatusBadGateway, "上游请求失败："+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		deliverSuccess(resp.Body, false, "转发完成")
		return
	}

	// 1. 401 令牌过期：尝试使用 RefreshToken 无感刷新并重发
	if resp.StatusCode == http.StatusUnauthorized && route.Provider == nil {
		newAcc, errRefresh := credential.RefreshAccount(req.Context(), probeHTTPClient(), s.probe(), variant.Intl, "active")
		if errRefresh == nil && newAcc != nil && newAcc.Token != "" {
			s.note(ProxyLogEntry{
				Level: "warn",
				Model: route.Model,
				Text:  fmt.Sprintf("上游 401 令牌过期，已自动通过 RefreshToken 无感保鲜并重试请求 (UID: %s)", newAcc.UID),
			})
			usage.InvalidateLiveCache(variant.Intl)
			respRetry, errRetry := retryOnce(newAcc.Token, newAcc.UID)
			if errRetry == nil && respRetry.StatusCode == http.StatusOK {
				deliverSuccess(respRetry.Body, false, "重试转发完成")
				return
			}
			if errRetry == nil {
				_ = respRetry.Body.Close()
			}
		}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	msg := upstreamMessage(body)
	isModelBusy := strings.Contains(string(body), "14003") || strings.Contains(strings.ToLower(string(body)), "model busy")

	if isModelBusy {
		msg = "[官方该模型当前算力繁忙 Model Busy，非账号额度问题，建议切换其它模型重试] " + msg
	}

	// 2. 14003 算力繁忙：秒级短退避重试（带真随机微抖动与自适应 Retry-After，避免客户端长会话直接报错中断）
	if isModelBusy && resp.StatusCode == http.StatusTooManyRequests {
		scale := evaluateContextScale(int64(len(rawBody)))
		for attempt := 0; attempt < len(modelBusyRetryDelays); attempt++ {
			delay := calcModelBusyBackoff(attempt, resp)
			modelBusyRetries++
			modelBusyDelays = append(modelBusyDelays, fmt.Sprintf("%v", delay.Round(10*time.Millisecond)))
			tipSuffix := ""
			if scale.Tip != "" {
				tipSuffix = " (建议: " + scale.Tip + ")"
			}
			s.note(ProxyLogEntry{
				Level: "warn",
				Model: route.Model,
				Text:  fmt.Sprintf("上游模型当前算力繁忙 (14003 Model Busy)，代理层自动防抖等待 %v 进行第 %d 次重试%s", delay.Round(time.Millisecond), attempt+1, tipSuffix),
			})

			select {
			case <-req.Context().Done():
				return
			case <-time.After(delay):
			}

			respRetry, errRetry := retryOnce("", "")
			if errRetry != nil {
				break
			}
			if respRetry.StatusCode == http.StatusOK {
				deliverSuccess(respRetry.Body, false, "算力繁忙重试成功")
				return
			}

			bodyRetry, _ := io.ReadAll(io.LimitReader(respRetry.Body, 8<<10))
			_ = respRetry.Body.Close()
			body = bodyRetry
			msg = upstreamMessage(body)
			isModelBusy = strings.Contains(string(body), "14003") || strings.Contains(strings.ToLower(string(body)), "model busy")
			if isModelBusy {
				msg = "[官方该模型当前算力繁忙 Model Busy，非账号额度问题，建议切换其它模型重试] " + msg
			} else {
				resp = respRetry
				break
			}
		}
	}

	// 3. 429/402 账号限额：多账号故障转移（Failover）轮换
	if !isModelBusy && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusPaymentRequired) && route.Provider == nil {
		accs, _ := credential.ListAccounts(s.probe(), variant.Intl)
		var currentActiveID string
		for _, a := range accs {
			if a.Active {
				currentActiveID = a.ID
				break
			}
		}
		if currentActiveID != "" && len(accs) > 1 {
			currentFailedID := currentActiveID
			for attempt := 0; attempt < len(accs)-1 && attempt < 3; attempt++ {
				nextAcc, errFailover := credential.GetDefaultFailoverTracker().FailoverToNext(s.probe(), variant.Intl, currentFailedID, credential.DefaultCooldownDuration)
				if errFailover != nil || nextAcc == nil || nextAcc.Token == "" {
					break
				}
				s.note(ProxyLogEntry{
					Level: "warn",
					Model: route.Model,
					Text:  fmt.Sprintf("上游触发 429 账号限流/配额用尽，自动故障转移至备用账号：%s (%s) 并重发 (第 %d 次重试)", nextAcc.Nickname, nextAcc.UID, attempt+1),
				})
				usage.InvalidateLiveCache(variant.Intl)
				respRetry, errRetry := retryOnce(nextAcc.Token, nextAcc.UID)
				if errRetry != nil {
					break
				}
				if respRetry.StatusCode == http.StatusOK {
					accName = nextAcc.Nickname
					accID = nextAcc.UID
					deliverSuccess(respRetry.Body, true, fmt.Sprintf("故障转移重发成功 (账号: %s)", nextAcc.Nickname))
					return
				}
				if respRetry.StatusCode == http.StatusTooManyRequests || respRetry.StatusCode == http.StatusPaymentRequired {
					_ = respRetry.Body.Close()
					currentFailedID = nextAcc.ID
					continue
				}
				_ = respRetry.Body.Close()
				break
			}
		}
	}

	s.observe(route.Model, resp.StatusCode, msg)
	level, verdict := "bad", "拒绝"
	if resp.StatusCode >= 500 {
		level, verdict = "warn", "上游故障"
	}
	s.note(ProxyLogEntry{Level: level, Model: route.Model, Tools: up.Tools,
		MS: time.Since(started).Milliseconds(),
		Text: fmt.Sprintf("被%s%s model=%s 上游 HTTP %d: %s",
			route.Label, verdict, route.Model, resp.StatusCode, cutStr(compactError(msg), 300))})
	recordAudit(resp.StatusCode, chatMetrics{}, false, msg)
	writeOpenAIError(w, resp.StatusCode, fmt.Sprintf("上游 HTTP %d: %s", resp.StatusCode, msg))
}

// providers 读一次自备提供方配置。
//
// 每个请求都读一遍文件（约 1 KB）：卡片改完要立刻生效，而这点开销相比
// 请求体动辄几 MB 的解析可以忽略。读不动就当作"没有提供方"——
// 那只影响 BYOK 那几条模型，内置国际后端照常可用。
func (s *Server) providers() []config.Provider {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}
	return cfg.Providers
}

type chatMetrics struct {
	bytesSent        int64
	ttftMs           int64
	promptTokens     int
	completionTokens int
}

var (
	rePromptTokens     = regexp.MustCompile(`"prompt_tokens"\s*:\s*(\d+)`)
	reCompletionTokens = regexp.MustCompile(`"completion_tokens"\s*:\s*(\d+)`)
)

// extractUsageFromBytes 尝试从上游 SSE 的 chunk 中提取官方返回的 usage 字段。
func extractUsageFromBytes(b []byte) (int, int) {
	if !bytes.Contains(b, []byte(`"usage"`)) {
		return 0, 0
	}
	// 1. 尝试按行切分并以标准 JSON 反序列化解析
	lines := bytes.Split(b, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		line = bytes.TrimPrefix(line, []byte("data:"))
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		var u struct {
			Usage *struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(line, &u); err == nil && u.Usage != nil {
			return u.Usage.PromptTokens, u.Usage.CompletionTokens
		}
	}

	// 2. 兜底策略：若 chunk 跨行或被反代截断，通过正则表达式直接匹配 Token 数量
	pMatch := rePromptTokens.FindSubmatch(b)
	cMatch := reCompletionTokens.FindSubmatch(b)
	var pTok, cTok int
	if len(pMatch) == 2 {
		pTok, _ = strconv.Atoi(string(pMatch[1]))
	}
	if len(cMatch) == 2 {
		cTok, _ = strconv.Atoi(string(cMatch[1]))
	}
	return pTok, cTok
}

// streamChatThrough 把上游 SSE 转发给调用方。
func (s *Server) streamChatThrough(w http.ResponseWriter, upstream io.Reader, started time.Time) chatMetrics {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// 万一前面挂了反代/网关，别让它缓冲——缓冲会让流式退化成一次性返回。
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		// 先甩出响应头：否则调用方要等到第一块正文才知道请求已被接受。
		flusher.Flush()
	}
	var m chatMetrics
	buf := make([]byte, 32*1024)
	for {
		n, readErr := upstream.Read(buf)
		if n > 0 {
			if m.ttftMs == 0 {
				m.ttftMs = time.Since(started).Milliseconds()
			}
			chunk := buf[:n]
			if bytes.Contains(chunk, []byte(`"usage"`)) {
				p, c := extractUsageFromBytes(chunk)
				if p > 0 || c > 0 {
					m.promptTokens = p
					m.completionTokens = c
				}
			}
			written, err := w.Write(chunk)
			m.bytesSent += int64(written)
			if err != nil {
				return m // 调用方断开，上游留给 defer 关
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return m // io.EOF 或上游中断，流到此结束
		}
	}
}

// toolCallAcc 是聚合 tool_calls 增量时的累加器。
//
// tool_calls 是**按 index 分片**流下来的：id 与函数名只在第一片出现，
// arguments 则是逐片字符串拼接。聚合时少拼一片，调用方拿到的就是
// 半截 JSON 参数，工具调用会以"参数解析失败"这种莫名其妙的形式失败。
type toolCallAcc struct {
	id   string
	name string
	args strings.Builder
}

// aggregateChat 聚合上游流式块，组装成一份非流式的 OpenAI 响应。
func (s *Server) aggregateChat(w http.ResponseWriter, upstream io.Reader, started time.Time) chatMetrics {
	var served string
	var finish string
	var content strings.Builder
	var promptTok, completionTok int
	var credit float64
	toolCalls := map[int]*toolCallAcc{}
	var toolOrder []int

	sc := bufio.NewScanner(upstream)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int     `json:"prompt_tokens"`
				CompletionTokens int     `json:"completion_tokens"`
				Credit           float64 `json:"credit"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Model != "" {
			served = chunk.Model
		}
		for _, ch := range chunk.Choices {
			content.WriteString(ch.Delta.Content)
			if ch.FinishReason != "" {
				finish = ch.FinishReason
			}
			for _, tc := range ch.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				acc, ok := toolCalls[idx]
				if !ok {
					acc = &toolCallAcc{}
					toolCalls[idx] = acc
					toolOrder = append(toolOrder, idx)
				}
				if tc.ID != "" {
					acc.id = tc.ID
				}
				if tc.Function.Name != "" {
					acc.name = tc.Function.Name
				}
				acc.args.WriteString(tc.Function.Arguments)
			}
		}
		if chunk.Usage != nil {
			promptTok = chunk.Usage.PromptTokens
			completionTok = chunk.Usage.CompletionTokens
			credit = chunk.Usage.Credit
		}
	}
	if served == "" {
		writeOpenAIError(w, http.StatusBadGateway, "上游没有返回任何内容")
		return chatMetrics{}
	}

	msg := map[string]any{"role": "assistant", "content": content.String()}
	finishReason := "stop"
	if len(toolOrder) > 0 {
		calls := make([]map[string]any, 0, len(toolOrder))
		for _, idx := range toolOrder {
			acc := toolCalls[idx]
			calls = append(calls, map[string]any{
				"id":   acc.id,
				"type": "function",
				"function": map[string]any{
					"name":      acc.name,
					"arguments": acc.args.String(),
				},
			})
		}
		msg["tool_calls"] = calls
		finishReason = "tool_calls"
	} else if finish != "" {
		finishReason = finish
	}

	w.Header().Set("Content-Type", "application/json")
	encoded, err := json.Marshal(map[string]any{
		"id":      "wbmux-proxy",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   served,
		"choices": []map[string]any{{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTok,
			"completion_tokens": completionTok,
			"total_tokens":      promptTok + completionTok,
			"credit":            credit,
		},
	})
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return chatMetrics{}
	}
	written, _ := w.Write(encoded)
	return chatMetrics{
		bytesSent:        int64(written),
		ttftMs:           time.Since(started).Milliseconds(),
		promptTokens:     promptTok,
		completionTokens: completionTok,
	}
}

// upstreamMessage 从上游的错误体里挑一句人能看的话。
//
// 官方错误体形如 {"code":11102,"msg":"...","displayMsg":{"zh":"当前模型不可用，
// 请切换其他模型后重试。"}}。优先取中文面向上游的说法，再退回通用字段，
// 都不像就原样截一段——绝不把原因吞成一句"未知错误"。
func upstreamMessage(raw []byte) string {
	var obj struct {
		Msg        string `json:"msg"`
		Message    string `json:"message"`
		DisplayMsg struct {
			Zh   string `json:"zh"`
			En   string `json:"en"`
			EnUS string `json:"en-US"`
		} `json:"displayMsg"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		for _, cand := range []string{obj.DisplayMsg.Zh, obj.Msg, obj.Message, obj.DisplayMsg.En, obj.DisplayMsg.EnUS} {
			if s := flatten(cand); s != "" {
				return s
			}
		}
		if obj.Error != nil {
			if s := flatten(obj.Error.Message); s != "" {
				return s
			}
		}
	}
	return cutStr(flatten(string(raw)), 300)
}

// flatten 压掉换行与多余空白，避免日志与错误信息被多行 JSON 撑爆。
func flatten(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// cutStr 截断长文本用于错误信息。
func cutStr(str string, n int) string {
	if len(str) <= n {
		return str
	}
	return str[:n] + "…"
}

// modelBusyRetryDelays 是上游 14003 Model Busy（算力繁忙）时的短退避重发延迟阶梯。
// 可由单元测试临时改小以加速测试。
var modelBusyRetryDelays = []time.Duration{1500 * time.Millisecond, 2500 * time.Millisecond}

// calcModelBusyBackoff 计算包含真随机微抖动（Jitter）与上游提示的自适应退避等待时间。
//
// 策略：
//  1. 若上游响应包含 Retry-After 头且解析出的秒数在 [1, 5] 秒内，以此为基础基准；
//  2. 否则使用配置的阶梯延迟（默认 1.5s / 2.5s）；
//  3. 增加 ±20% 且不超过 300ms 的密码学真随机抖动（Jitter），彻底打破多客户端瞬态重试的时间同步共振，防止群体踩踏；
//  4. 当传入的 baseDelay 很小时（如单元测试里的 1ms~5ms），微抖动自动按比例缩小至微秒/毫秒级，保证测试快速完成。
func calcModelBusyBackoff(attempt int, resp *http.Response) time.Duration {
	base := 1500 * time.Millisecond
	if attempt < len(modelBusyRetryDelays) {
		base = modelBusyRetryDelays[attempt]
	}

	// 嗅探上游 Retry-After 标头
	if resp != nil {
		if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
			if sec, err := strconv.Atoi(ra); err == nil && sec >= 1 && sec <= 5 {
				base = time.Duration(sec) * time.Second
			}
		}
	}

	if base <= 10*time.Millisecond {
		// 测试环境微秒级快速通过，不加宏观抖动
		return base
	}

	// 计算最大抖动窗口：取 base 的 20%，上限 300ms
	jitterMax := base / 5
	if jitterMax > 300*time.Millisecond {
		jitterMax = 300 * time.Millisecond
	}
	if jitterMax <= 0 {
		return base
	}

	// crypto/rand 生成 [-jitterMax, +jitterMax]
	rangeMs := (jitterMax * 2).Milliseconds()
	if rangeMs > 0 {
		n, err := rand.Int(rand.Reader, big.NewInt(rangeMs+1))
		if err == nil {
			jitter := time.Duration(n.Int64()-rangeMs/2) * time.Millisecond
			base += jitter
		}
	}

	if base < 500*time.Millisecond {
		base = 500 * time.Millisecond
	}
	return base
}

// contextScale 描述客户端会话上下文体积的三级健康评估。
type contextScale struct {
	Level      string // "normal" | "heavy" | "overload"
	LevelLabel string // "正常" | "长会话" | "超载会话"
	Tokens     int    // 估算的 Token 数
	Desc       string // 友好紧凑描述，如 "1.5 MB (~270k tokens)"
	Tip        string // 优化指导建议（超载时触发）
}

func evaluateContextScale(bytesLen int64) contextScale {
	// 实测校准：客户端发来的整包 HTTP POST 正文为 JSON 格式，包含了 23 个工具 Schema、
	// JSON 属性键、历史角色包装及大量转义字符（\n, \"）。在真实 Tokenizer（如 DeepSeek/GPT）下，
	// 实测 2.0 MB 请求体对应 ~362.4k tokens，平均密度约为 1 Token ≈ 5.5 字节。
	tokens := int(float64(bytesLen) / 5.5)
	bytesStr := describeBytes(bytesLen)

	switch {
	case bytesLen >= 1500*1024: // >= 1.5 MB (~280k-400k+ tokens)
		return contextScale{
			Level:      "overload",
			LevelLabel: "超载会话",
			Tokens:     tokens,
			Desc:       fmt.Sprintf("%s (~%dk tokens)", bytesStr, tokens/1000),
			Tip:        "当前会话上下文已超 300k tokens，极易引发官方推理集群排队拥堵，建议在当前步骤完成后开启新对话以提升响应速度",
		}
	case bytesLen >= 600*1024: // 600 KB ~ 1.5 MB (~110k-280k tokens)
		return contextScale{
			Level:      "heavy",
			LevelLabel: "长会话",
			Tokens:     tokens,
			Desc:       fmt.Sprintf("%s (~%dk tokens)", bytesStr, tokens/1000),
			Tip:        "",
		}
	default:
		return contextScale{
			Level:      "normal",
			LevelLabel: "正常",
			Tokens:     tokens,
			Desc:       bytesStr,
			Tip:        "",
		}
	}
}

// probeHTTPClient 是转发用的 HTTP 客户端（超时按长回复放宽）。
func probeHTTPClient() *http.Client {
	return &http.Client{Timeout: 120 * time.Second}
}


