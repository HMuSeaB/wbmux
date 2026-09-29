// Package credential 读出一侧客户端**可用**的登录凭据。
//
// # 为什么单独成包
//
// 凭据在磁盘上有两种形态：
//
//   - 明文：直接就是 token，谁都能用；
//   - 加密信封（WorkBuddy 5.6+ 的国内侧）：形如
//     {"$wbEncrypted":1,"envelope":"<base64>"}，密钥由客户端的原生模块持有。
//
// 信封**只有客户端自己的运行时能解开**，所以本包要启动一次客户端主程序。
// 「怎么拿到明文」和「拿明文去查什么」是两件事——usage 与 checkin 都要前者，
// 各自只关心后者。混在一个包里会让两条链路互相牵连（改签到会动到用量面板）。
//
// # 一条硬规矩
//
// 这里拿到的 Token 等价于用户的登录态。**绝不允许出现在日志、错误信息、
// 界面回传或任何磁盘文件里**。往外传之前先过 Credential.Redact。
package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ErrSealed 表示凭据在磁盘上是加密信封，光读文件取不到明文。
//
// 单独做成哨兵错误，是为了让调用方能区分"读不到"和"要解密"：
// 前者是真的没法用，后者换个入口（Resolve）就行。
var ErrSealed = errors.New("凭据是加密信封（WorkBuddy 5.6+）")

// Credential 是一侧客户端登录态的可读形态。
type Credential struct {
	// Token 是明文访问令牌。信封未被解开时为空。
	Token string `json:"-"`
	// UID 是账号标识，官方接口要求放进 X-User-Id 头。
	UID string `json:"uid,omitempty"`
	// Nickname 是登录昵称；信封侧常常也是信封，取不到就为空。
	Nickname string `json:"nickname,omitempty"`
	// Domain 是登录域（实测国内 www.codebuddy.cn、国际 www.workbuddy.ai）。
	//
	// 注意它**不是**接口地址：凭据文件里没有 endpoint 字段，接口域名要另取。
	Domain string `json:"domain,omitempty"`
	// ExpiresAtMs / RefreshExpiresAtMs 是 auth 里的到期时间戳（毫秒）。
	//
	// 实测与"access 7 天 / refresh 14 天"的二手说法**对不上**，
	// 所以这里只如实带出来、由调用方自行判断，不做任何推算。
	ExpiresAtMs        int64 `json:"expiresAt,omitempty"`
	RefreshExpiresAtMs int64 `json:"refreshExpiresAt,omitempty"`

	// Sealed 表示磁盘上原本是加密信封。
	Sealed bool `json:"sealed,omitempty"`
	// Unsealed 表示信封是被我们借客户端运行时当场解开的（不是文件里就是明文）。
	Unsealed bool `json:"unsealed,omitempty"`
	// UnsealedBy 记录借用了哪个客户端主程序，供界面与排错如实展示。
	UnsealedBy string `json:"unsealedBy,omitempty"`
}

// Redact 把文本里出现的令牌替换成占位符。
//
// 所有要写进日志或回报给界面的字符串都必须先过这一道。令牌太短时不做替换，
// 免得把正常文本里的短串误伤成占位符。
func (c Credential) Redact(s string) string {
	if len(c.Token) < 8 {
		return s
	}
	return strings.ReplaceAll(s, c.Token, "[REDACTED]")
}

// authFileCandidates 返回一侧凭据文件的候选路径，按优先级排列。
//
// 目录布局来自客户端本体（CodeBuddyExtension），按操作系统分目录。
// 文件名取 Backend.AuthID 加 .info——与实测一致（workbuddy-desktop /
// workbuddy-desktop-ai），不要再硬编码一份。
//
// 第二个候选是 Linux 的 CodeBuddy CLI 写出来的名字：那里没有桌面端，
// 入口是 CLI，它用的文件名不同（实测 Tencent-Cloud.coding-copilot.info），
// 但 JSON 结构与桌面端一致。多试一个不存在的路径只花一次 stat。
func authFileCandidates(p *variant.Probe, id variant.ID) []string {
	b, err := variant.Get(id)
	if err != nil {
		return nil
	}
	rel := map[string]string{
		"windows": "AppData/Local/CodeBuddyExtension/Data/Public/auth",
		"darwin":  "Library/Application Support/CodeBuddyExtension/Data/Public/auth",
		"linux":   ".local/share/CodeBuddyExtension/Data/Public/auth",
	}[p.GOOS]
	if rel == "" {
		rel = ".local/share/CodeBuddyExtension/Data/Public/auth"
	}
	dir := filepath.Join(p.Home, filepath.FromSlash(rel))

	out := []string{filepath.Join(dir, b.AuthID+".info")}
	if cli := filepath.Join(dir, "Tencent-Cloud.coding-copilot.info"); cli != out[0] {
		out = append(out, cli)
	}
	return out
}

// File 返回一侧凭据文件的实际路径；一个都不存在时返回首选路径
// （让报错信息里能看到"本该在哪"）。
func File(p *variant.Probe, id variant.ID) string {
	cands := authFileCandidates(p, id)
	if len(cands) == 0 {
		return ""
	}
	for _, c := range cands {
		if p.Exists(c) {
			return c
		}
	}
	return cands[0]
}

// Read 读凭据文件并解析出**文件中已有的**信息，不做任何解密。
//
// 遇到加密信封时返回 ErrSealed，同时把 Sealed/UID/Domain/到期时间一并带出——
// 界面仍然能显示"是哪一侧、什么时候到期"，只是没有明文令牌可用。
// 要明文请用 Resolve。
func Read(p *variant.Probe, id variant.ID) (Credential, error) {
	path := File(p, id)
	if path == "" {
		return Credential{}, fmt.Errorf("无法确定凭据文件路径")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credential{}, fmt.Errorf("没找到凭据文件 %s（先在客户端里登录一次）", path)
		}
		return Credential{}, fmt.Errorf("读取凭据文件失败: %w", err)
	}
	c, err := parse(raw)
	if err != nil {
		// 带上 c：信封场景下调用方还要用 UID/Domain/到期时间，
		// 这里回零值会让界面连"是哪一侧"都显示不出来。
		return c, err
	}
	if c.Sealed {
		return c, ErrSealed
	}
	return c, nil
}

// doc 是凭据文件里我们用到的字段。
//
// 全部用 json.RawMessage 而不是具体类型：accessToken 既可能是字符串也可能是
// 信封对象，nickname 同理，直接 Unmarshal 成 string 会在信封上直接失败。
type doc struct {
	Auth struct {
		AccessToken      json.RawMessage `json:"accessToken"`
		Domain           string          `json:"domain"`
		ExpiresAt        int64           `json:"expiresAt"`
		RefreshExpiresAt int64           `json:"refreshExpiresAt"`
	} `json:"auth"`
	Account struct {
		UID      json.RawMessage `json:"uid"`
		Nickname json.RawMessage `json:"nickname"`
	} `json:"account"`
}

// parse 解析凭据文件内容。信封不报错，而是置 Sealed 并返回 ErrSealed，
// 让上层能同时拿到"是信封"和"其余元数据"。
func parse(raw []byte) (Credential, error) {
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return Credential{}, fmt.Errorf("凭据文件不是合法 JSON: %w", err)
	}
	c := Credential{
		Domain:             d.Auth.Domain,
		ExpiresAtMs:        d.Auth.ExpiresAt,
		RefreshExpiresAtMs: d.Auth.RefreshExpiresAt,
	}
	// uid / nickname 只收字符串，信封对象一律当"取不到"（不报错）。
	_ = json.Unmarshal(d.Account.UID, &c.UID)
	_ = json.Unmarshal(d.Account.Nickname, &c.Nickname)

	var token string
	if err := json.Unmarshal(d.Auth.AccessToken, &token); err == nil {
		token = strings.TrimSpace(token)
		if token == "" {
			return c, fmt.Errorf("凭据文件里 accessToken 是空串")
		}
		c.Token = token
		return c, nil
	}

	// 不是字符串。只认我们已知的信封外壳；其余形态如实报错，
	// 不要猜——猜错会把"客户端换了格式"误诊成"解密失败"。
	var envelope struct {
		Mark     json.RawMessage `json:"$wbEncrypted"`
		Envelope string          `json:"envelope"`
	}
	if err := json.Unmarshal(d.Auth.AccessToken, &envelope); err != nil ||
		len(envelope.Mark) == 0 || envelope.Envelope == "" {
		return c, fmt.Errorf("凭据文件里 accessToken 既不是明文也不是已知的信封结构")
	}
	c.Sealed = true
	return c, ErrSealed
}

// ---------- 借客户端运行时解开信封 ----------

// helperTimeout 是单次解密的超时。
//
// 客户端主程序约 200 MB，冷启动到这个 native binding 可用需要一点时间；
// 30 秒对"偶尔解一次"足够了，同时不至于在客户端卡死时把界面拖住。
const helperTimeout = 30 * time.Second

// maxHelperBytes 是 helper 输出的上限。
//
// 外部进程的输出不可信：不设上限时一个失控的 helper 能把内存吃光。
// 正常回话是几十到几千字节（令牌约 1.3 KB）。
const maxHelperBytes = 1 << 20

// helperRequest / helperResponse 是 Go 与 helper JS 之间的协议。
//
// 请求只带信封本身，**不带任何路径或命令**：载荷必须是在这里写死的常量与
// 从凭据文件读到的数据，绝不能拼接用户输入，否则等于给了一个任意执行入口。
type helperRequest struct {
	Version   int             `json:"version"`
	Operation string          `json:"operation"`
	Value     json.RawMessage `json:"value"`
}

type helperResponse struct {
	OK          bool   `json:"ok"`
	Reason      string `json:"reason,omitempty"`
	AccessToken string `json:"accessToken,omitempty"`
}

// helperReasonText 把 helper 的机器码翻成人话。
//
// 分开报的原因：这几种失败的对策完全不同——UNSUPPORTED_ENVELOPE 要升级
// wbmux，RUNTIME_UNAVAILABLE 要装/修客户端，KEY_MISMATCH 是读错了文件。
// 一律说"解密失败"会让用户无从下手。
var helperReasonText = map[string]string{
	"INVALID_FORMAT":        "凭据信封格式不对（可能是客户端版本变了）",
	"UNSUPPORTED_ENVELOPE":  "信封版本比本程序认识的更新（suite 非 1），请升级 wbmux",
	"RUNTIME_UNAVAILABLE":   "拿不到客户端运行时（可能没装该档位客户端，或版本不支持）",
	"KEY_MISMATCH":          "信封不是这份客户端的密钥加密的（可能读到了另一档位或另一账号的文件）",
	"DECRYPT_FAILED":        "解密失败（信封可能已损坏，或客户端正在重写它）",
	"HELPER_PROTOCOL":       "客户端运行时没有按约定回话",
	"HELPER_UNAVAILABLE":    "无法启动客户端主程序",
	"HELPER_TIMEOUT":        "客户端运行时超时未回话",
	"HELPER_OUTPUT_TOO_BIG": "客户端运行时输出了异常大的内容，已中止",
}

func helperReason(code string) string {
	if t, ok := helperReasonText[code]; ok {
		return t
	}
	// 未知码原样带出：宁可露出一个陌生代号，也不要把真实原因吞掉。
	if code == "" {
		code = "HELPER_PROTOCOL"
	}
	return "客户端运行时回报了未知错误 " + code
}

// execHelper 是运行 helper 的实现，测试里替换它即可脱离真实客户端。
var execHelper = execHelperReal

// findExe 是定位客户端主程序的实现，测试里替换它即可脱离真实安装。
//
// exe 非空表示用户用 --exe / 设置文件钉死了位置：那时它是权威的，
// 探测不能悄悄换一个（Detect 已经把这条规矩实现好了）。
var findExe = findExeReal

// findExeReal 复用安装探测，而不是自己再写一套"主程序在哪"。
//
// 探测顺序（数据目录线索 → 注册表 → 常见位置 → PATH）已经在 variant 里
// 调过很多轮，重写一份迟早与它分叉。
func findExeReal(p *variant.Probe, id variant.ID, exe string) (string, error) {
	inst := p.Detect(id, exe)
	if !inst.Found || inst.Executable == "" {
		return "", fmt.Errorf("没找到%s客户端主程序，无法解开凭据信封%s",
			displayName(id), problemsSuffix(inst.Problems))
	}
	return inst.Executable, nil
}

// displayName 返回档位的中文名；取不到就退回 id 本身。
func displayName(id variant.ID) string {
	if b, err := variant.Get(id); err == nil {
		return b.DisplayName
	}
	return string(id)
}

func problemsSuffix(problems []string) string {
	if len(problems) == 0 {
		return ""
	}
	return "（" + strings.Join(problems, "；") + "）"
}

// Resolve 返回**可用**的一侧凭据。
//
// 明文：直接回。
// 信封：借客户端运行时解开，并按文件指纹缓存结果。
//
// # 为什么要缓存
//
// 解开一次要启动一个约 200 MB 的客户端进程。用量面板是 60 秒一轮的读操作，
// 每次都现解的话，光是进程启动就够把界面拖卡。客户端刷新令牌时会重写文件，
// 指纹（路径 + 修改时间 + 大小）随之变化，缓存自动失效并重解一次。
//
// 客户端位置靠自动探测。用户用 --exe 钉过位置的用 ResolveWith。
func Resolve(p *variant.Probe, id variant.ID) (Credential, error) {
	return ResolveWith(p, id, "")
}

// ResolveWith 与 Resolve 相同，但允许显式指定客户端主程序。
//
// exe 来自用户的 --exe / 设置文件。为它单开一个入口而不是加个"全局提示"：
// 显式路径必须是权威的，探测绝不能悄悄换成另一处安装——那会连到非预期的
// 后端（variant.Detect 对这条有完整说明）。
func ResolveWith(p *variant.Probe, id variant.ID, exe string) (Credential, error) {
	path := File(p, id)
	if path == "" {
		return Credential{}, fmt.Errorf("无法确定凭据文件路径")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credential{}, fmt.Errorf("没找到凭据文件 %s（先在客户端里登录一次）", path)
		}
		return Credential{}, fmt.Errorf("读取凭据文件失败: %w", err)
	}

	c, err := Read(p, id)
	if err == nil {
		// 明文：顺手让缓存失效，免得下次文件变回信封时命中旧值。
		cacheForget(id)
		return c, nil
	}
	if !errors.Is(err, ErrSealed) {
		return Credential{}, err
	}

	fingerprint := fmt.Sprintf("%s|%d|%d", path, info.ModTime().UnixNano(), info.Size())
	if hit, ok := cacheLookup(id, fingerprint); ok {
		return hit, nil
	}

	token, usedExe, err := Unseal(p, id, path, exe)
	if err != nil {
		return c, err
	}
	c.Token = token
	c.Unsealed = true
	c.UnsealedBy = usedExe
	cacheStore(id, fingerprint, c)
	return c, nil
}

// Unseal 借客户端运行时解开 path 里的 accessToken，返回明文令牌与被借用的主程序。
//
// 单独导出是为了让"诊断"类功能（doctor）能在不进入缓存的前提下试一次，
// 从而把真实原因回给用户。
func Unseal(p *variant.Probe, id variant.ID, path, exe string) (token, usedExe string, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("读取凭据文件失败: %w", err)
	}
	// 从原文里把信封对象原样抠出来交给 helper：不在 Go 里重建它，
	// 免得漏字段（信封结构属于客户端内部实现，会变）。
	var d doc
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", "", fmt.Errorf("凭据文件不是合法 JSON: %w", err)
	}
	if len(d.Auth.AccessToken) == 0 {
		return "", "", fmt.Errorf("凭据文件里没有 accessToken")
	}

	usedExe, err = findExe(p, id, exe)
	if err != nil {
		return "", "", err
	}

	payload, err := json.Marshal(helperRequest{
		Version:   1,
		Operation: "decrypt",
		Value:     d.Auth.AccessToken,
	})
	if err != nil {
		return "", "", fmt.Errorf("准备解密请求失败: %w", err)
	}

	out, err := execHelper(usedExe, payload)
	if err != nil {
		return "", usedExe, fmt.Errorf("借客户端运行时解密失败: %w", err)
	}

	var resp helperResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		// 不回显 out：那是客户端进程的原始输出，可能包含敏感内容。
		return "", usedExe, fmt.Errorf("无法解析客户端运行时的回话: %w", err)
	}
	if !resp.OK {
		return "", usedExe, errors.New(helperReason(resp.Reason))
	}
	token = strings.TrimSpace(resp.AccessToken)
	if token == "" {
		return "", usedExe, fmt.Errorf("客户端运行时回报成功但没给出令牌")
	}
	return token, usedExe, nil
}

// execHelperReal 启动客户端主程序，以 Node 模式执行 helper JS。
//
// 关键点是 ELECTRON_RUN_AS_NODE=1：不设它，客户端会当普通 GUI 启动
// （弹出一个窗口、不读 stdin、也不回话）。
func execHelperReal(exe string, payload []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "-e", helperJS)
	cmd.Env = helperEnv(os.Environ())
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Dir = filepath.Dir(exe)

	stdout := &capWriter{limit: maxHelperBytes}
	stderr := &capWriter{limit: 8 << 10}
	cmd.Stdout = stdout
	// Windows 上必须同时读 stderr，否则管道写满会把子进程挂死。
	cmd.Stderr = stderr
	hideWindow(cmd)

	err := cmd.Run()
	if stdout.over {
		return nil, errors.New(helperReason("HELPER_OUTPUT_TOO_BIG"))
	}
	if ctx.Err() == context.DeadlineExceeded {
		return nil, errors.New(helperReason("HELPER_TIMEOUT"))
	}
	if err != nil {
		// 只带退出状态与 stderr 的**长度**，不带内容：helper 的 stderr
		// 属于外部进程输出，不能原样进日志或界面。
		if stderr.buf.Len() > 0 {
			return nil, fmt.Errorf("%s（客户端运行时退出异常：%v）",
				helperReason("HELPER_UNAVAILABLE"), err)
		}
		return nil, fmt.Errorf("%s：%v", helperReason("HELPER_UNAVAILABLE"), err)
	}
	return stdout.buf.Bytes(), nil
}

// helperEnv 构造子进程环境：先剔掉所有会改变客户端启动方式的变量，
// 再显式打开 Node 模式。
//
// NODE_ / ELECTRON_ 开头的一律剥掉是必须的——父进程里若残留
// ELECTRON_RUN_AS_NODE=0 或 NODE_OPTIONS，行为会与预期不符。大小写不敏感：
// Windows 环境变量名不区分大小写，而 os.Environ 保留原始大小写。
func helperEnv(parent []string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, entry := range parent {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		up := strings.ToUpper(key)
		if strings.HasPrefix(up, "NODE_") || strings.HasPrefix(up, "ELECTRON_") ||
			strings.HasPrefix(up, "WORKBUDDY_") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "ELECTRON_RUN_AS_NODE=1")
}

// capWriter 限制最多写入 limit 字节，超出部分丢弃并置 over。
//
// 永远返回 len(p)：报告"已全部消费"能避免子进程因写失败而挂死或提前退出，
// 我们只是想别把内存吃光，不是要中断它。
type capWriter struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if rem := w.limit - w.buf.Len(); rem > 0 {
		if len(p) > rem {
			_, _ = w.buf.Write(p[:rem])
			w.over = true
		} else {
			_, _ = w.buf.Write(p)
		}
	} else {
		w.over = true
	}
	return len(p), nil
}

// ---------- 解密结果缓存 ----------

// cacheEntry 按凭据文件指纹缓存已解开的凭据。
//
// 只缓存"解出来的结果"，键里带路径 + 修改时间 + 大小：客户端刷新令牌会重写
// 文件，指纹一变就自动重解，不会把过期令牌一直用下去。
type cacheEntry struct {
	fingerprint string
	cred        Credential
}

var (
	cacheMu sync.Mutex
	cache   = map[variant.ID]cacheEntry{}
)

func cacheLookup(id variant.ID, fingerprint string) (Credential, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	e, ok := cache[id]
	if !ok || e.fingerprint != fingerprint {
		return Credential{}, false
	}
	return e.cred, true
}

func cacheStore(id variant.ID, fingerprint string, c Credential) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache[id] = cacheEntry{fingerprint: fingerprint, cred: c}
}

func cacheForget(id variant.ID) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	delete(cache, id)
}

// ClearCache 清空解密缓存。测试用，避免用例之间互相污染。
func ClearCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = map[variant.ID]cacheEntry{}
}
