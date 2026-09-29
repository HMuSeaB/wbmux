package credential

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/variant/varianttest"
)

// sealedDoc 是一份国内侧形态的凭据文件：accessToken / nickname 都是信封。
const sealedDoc = `{
  "auth": {
    "accessToken": {"$wbEncrypted": 1, "envelope": "ZW52ZWxvcGU="},
    "domain": "www.codebuddy.cn",
    "expiresAt": 1795435750747,
    "refreshExpiresAt": 1795867750747
  },
  "account": {
    "uid": "e1ebf7aa11223344",
    "nickname": {"$wbEncrypted": 1, "envelope": "bmljaw=="}
  }
}`

// plainDoc 是一份国际侧形态的凭据文件：全部明文。
const plainDoc = `{
  "auth": {
    "accessToken": "eyJhbGciOi-plaintext-token",
    "domain": "www.workbuddy.ai",
    "expiresAt": 1821072372619
  },
  "account": {"uid": "fd46b8cc55667788", "nickname": "seabhmu"}
}`

// fakeExe 是主程序的占位路径。测试里从不真的执行它。
const fakeExe = `C:\fake\WorkBuddy.exe`

// newEnv 造一个最小环境：一个临时主目录，外加把「定位主程序」钉死成 fakeExe。
//
// # 为什么不直接用 varianttest.New
//
// 那会铺出一整套假安装（7 个文件、8 层目录）。本机建/删一个临时文件要几百
// 毫秒（杀软扫描），铺满之后光环境搭建就占掉这个包大半的测试时间。
// 本包绝大多数用例只关心凭据文件本身，"主程序在哪"由 variant 自己的测试覆盖。
// 需要真链路的单列一条（见 TestResolveUsesRealInstallDetection）。
func newEnv(t *testing.T) *variant.Probe {
	t.Helper()
	p := varianttest.Probe(t.TempDir())

	prev := findExe
	t.Cleanup(func() { findExe = prev })
	findExe = func(*variant.Probe, variant.ID, string) (string, error) { return fakeExe, nil }

	ClearCache()
	t.Cleanup(ClearCache)
	return p
}

// preferredPath 是某一档位**桌面端**凭据应在的位置（不看文件在不在）。
//
// 单独取出来是因为 File() 会退到 CLI 名字上：测试里想"往桌面端那个位置写"
// 时用 File() 会被已有文件带偏。
func preferredPath(p *variant.Probe, id variant.ID) string {
	cands := authFileCandidates(p, id)
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}

// writeAuth 把一份凭据写到该档位桌面端应在的位置，返回路径。
func writeAuth(t *testing.T, p *variant.Probe, id variant.ID, body string) string {
	t.Helper()
	path := preferredPath(p, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// stubHelper 替换 execHelper，记录调用参数并回一个固定应答。
//
// 返回调用计数器，测试用它断言"该解的才解、该缓存的才缓存"。
func stubHelper(t *testing.T, reply string, gotPayload *[]byte) *int {
	t.Helper()
	calls := 0
	prev := execHelper
	t.Cleanup(func() { execHelper = prev })
	execHelper = func(exe string, payload []byte) ([]byte, error) {
		calls++
		if gotPayload != nil {
			*gotPayload = append([]byte(nil), payload...)
		}
		return []byte(reply), nil
	}
	return &calls
}

func TestFilePrefersDesktopNameOverCLIName(t *testing.T) {
	p := newEnv(t)
	desktop := writeAuth(t, p, variant.CN, plainDoc)
	// 再放一份 CLI 名字的：两个都在时，必须选桌面端那个。
	cli := filepath.Join(filepath.Dir(desktop), "Tencent-Cloud.coding-copilot.info")
	if err := os.WriteFile(cli, []byte(plainDoc), 0o600); err != nil {
		t.Fatalf("写 CLI 凭据: %v", err)
	}

	if got := File(p, variant.CN); got != desktop {
		t.Fatalf("应优先桌面端文件名\n得到 %s\n期望 %s", got, desktop)
	}
}

func TestFileFallsBackToCLIName(t *testing.T) {
	p := newEnv(t)
	// 只有 CLI 名字存在（Linux 无桌面端的情形）。
	dir := filepath.Dir(preferredPath(p, variant.CN))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cli := filepath.Join(dir, "Tencent-Cloud.coding-copilot.info")
	if err := os.WriteFile(cli, []byte(plainDoc), 0o600); err != nil {
		t.Fatalf("写 CLI 凭据: %v", err)
	}

	if got := File(p, variant.CN); got != cli {
		t.Fatalf("应回退到 CLI 文件名，得到 %s", got)
	}
}

func TestFileReturnsPreferredPathWhenNothingExists(t *testing.T) {
	p := newEnv(t)
	got := File(p, variant.CN)
	// 一个都不存在时也要给出"本该在哪"，否则报错信息帮不上忙。
	if !strings.HasSuffix(got, "workbuddy-desktop.info") {
		t.Fatalf("应返回首选路径，得到 %s", got)
	}
}

func TestReadPlaintext(t *testing.T) {
	p := newEnv(t)
	writeAuth(t, p, variant.Intl, plainDoc)

	c, err := Read(p, variant.Intl)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if c.Token != "eyJhbGciOi-plaintext-token" {
		t.Fatalf("令牌不对: %q", c.Token)
	}
	if c.Sealed || c.Unsealed {
		t.Fatalf("明文不该带信封标记: sealed=%v unsealed=%v", c.Sealed, c.Unsealed)
	}
	if c.UID != "fd46b8cc55667788" || c.Nickname != "seabhmu" || c.Domain != "www.workbuddy.ai" {
		t.Fatalf("元数据解析不对: %+v", c)
	}
	if c.ExpiresAtMs != 1821072372619 {
		t.Fatalf("到期时间不对: %d", c.ExpiresAtMs)
	}
}

func TestReadSealedReportsErrSealedAndKeepsMetadata(t *testing.T) {
	p := newEnv(t)
	writeAuth(t, p, variant.CN, sealedDoc)

	c, err := Read(p, variant.CN)
	if !errors.Is(err, ErrSealed) {
		t.Fatalf("应回 ErrSealed，得到 %v", err)
	}
	// 关键：即使解不开，也要让上层能显示"是哪一侧、何时到期"。
	if c.Token != "" {
		t.Fatalf("信封未解开时不该有令牌: %q", c.Token)
	}
	if !c.Sealed {
		t.Fatal("应标记 Sealed")
	}
	if c.UID != "e1ebf7aa11223344" || c.Domain != "www.codebuddy.cn" {
		t.Fatalf("UID/Domain 应当仍可读出: %+v", c)
	}
	// nickname 也是信封，取不到就留空，不能报错。
	if c.Nickname != "" {
		t.Fatalf("信封形态的 nickname 应为空，得到 %q", c.Nickname)
	}
	if c.RefreshExpiresAtMs != 1795867750747 {
		t.Fatalf("refresh 到期时间不对: %d", c.RefreshExpiresAtMs)
	}
}

func TestReadRejectsUnknownTokenShape(t *testing.T) {
	p := newEnv(t)
	// 既不是字符串、也不是已知信封：必须如实报错，不能猜。
	writeAuth(t, p, variant.CN, `{"auth":{"accessToken":{"weird":true}},"account":{}}`)

	_, err := Read(p, variant.CN)
	if err == nil || errors.Is(err, ErrSealed) {
		t.Fatalf("应报告「不是已知结构」，得到 %v", err)
	}
	if !strings.Contains(err.Error(), "信封结构") {
		t.Fatalf("错误信息应说明形态不认识: %v", err)
	}
}

func TestReadEmptyTokenIsAnError(t *testing.T) {
	p := newEnv(t)
	writeAuth(t, p, variant.CN, `{"auth":{"accessToken":"   "},"account":{}}`)

	if _, err := Read(p, variant.CN); err == nil {
		t.Fatal("空令牌应当报错")
	}
}

func TestReadMissingFileNamesThePath(t *testing.T) {
	p := newEnv(t)
	// 一个凭据文件都不写。
	_, err := Read(p, variant.CN)
	if err == nil {
		t.Fatal("文件不存在应当报错")
	}
	if !strings.Contains(err.Error(), "workbuddy-desktop.info") {
		t.Fatalf("报错应带上应放位置: %v", err)
	}
	if !strings.Contains(err.Error(), "登录一次") {
		t.Fatalf("报错应给出可执行的动作: %v", err)
	}
}

func TestResolveUnsealsSealedCredential(t *testing.T) {
	p := newEnv(t)
	writeAuth(t, p, variant.CN, sealedDoc)

	var payload []byte
	calls := stubHelper(t, `{"ok":true,"accessToken":"unsealed-token-xyz"}`, &payload)

	c, err := Resolve(p, variant.CN)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("helper 应被调用 1 次，实际 %d", *calls)
	}
	if c.Token != "unsealed-token-xyz" {
		t.Fatalf("令牌不对: %q", c.Token)
	}
	if !c.Sealed || !c.Unsealed {
		t.Fatalf("应标记 sealed+unsealed: %+v", c)
	}
	if c.UnsealedBy != fakeExe {
		t.Fatalf("应记录借用了哪个主程序，得到 %q", c.UnsealedBy)
	}
	// 元数据不能因为在解信封的路上丢掉。
	if c.UID != "e1ebf7aa11223344" || c.Domain != "www.codebuddy.cn" {
		t.Fatalf("元数据丢了: %+v", c)
	}

	// 载荷里必须是信封**原文**，Go 侧不得重建（重建会漏字段）。
	var req struct {
		Version   int             `json:"version"`
		Operation string          `json:"operation"`
		Value     json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("载荷不是合法 JSON: %v", err)
	}
	if req.Version != 1 || req.Operation != "decrypt" {
		t.Fatalf("载荷版本/操作不对: %+v", req)
	}
	if !strings.Contains(string(req.Value), "$wbEncrypted") ||
		!strings.Contains(string(req.Value), "ZW52ZWxvcGU=") {
		t.Fatalf("载荷里应是信封原文，得到 %s", req.Value)
	}
}

func TestResolveCachesUntilFileChanges(t *testing.T) {
	p := newEnv(t)
	path := writeAuth(t, p, variant.CN, sealedDoc)
	calls := stubHelper(t, `{"ok":true,"accessToken":"token-1"}`, nil)

	if _, err := Resolve(p, variant.CN); err != nil {
		t.Fatalf("第一次 Resolve: %v", err)
	}
	if _, err := Resolve(p, variant.CN); err != nil {
		t.Fatalf("第二次 Resolve: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("同样的文件不该重复解密，helper 调用了 %d 次", *calls)
	}

	// 客户端刷新令牌会重写文件：指纹一变就必须重解。
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	if _, err := Resolve(p, variant.CN); err != nil {
		t.Fatalf("第三次 Resolve: %v", err)
	}
	if *calls != 2 {
		t.Fatalf("文件变了应重新解密，helper 调用了 %d 次", *calls)
	}
}

func TestResolvePlaintextNeverRunsHelper(t *testing.T) {
	p := newEnv(t)
	writeAuth(t, p, variant.Intl, plainDoc)

	prev := execHelper
	t.Cleanup(func() { execHelper = prev })
	execHelper = func(string, []byte) ([]byte, error) {
		t.Fatal("明文凭据不该启动客户端进程")
		return nil, nil
	}

	c, err := Resolve(p, variant.Intl)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if c.Token != "eyJhbGciOi-plaintext-token" {
		t.Fatalf("令牌不对: %q", c.Token)
	}
}

func TestResolveTranslatesHelperReasons(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"UNSUPPORTED_ENVELOPE", "升级 wbmux"},
		{"RUNTIME_UNAVAILABLE", "客户端运行时"},
		{"KEY_MISMATCH", "另一档位"},
		{"DECRYPT_FAILED", "已损坏"},
		{"INVALID_FORMAT", "格式不对"},
		// 未知码要原样带出，不能吞掉真实原因。
		{"SOMETHING_NEW", "SOMETHING_NEW"},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			p := newEnv(t)
			writeAuth(t, p, variant.CN, sealedDoc)
			stubHelper(t, `{"ok":false,"reason":"`+tc.reason+`"}`, nil)

			_, err := Resolve(p, variant.CN)
			if err == nil {
				t.Fatal("应报错")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，得到 %v", tc.want, err)
			}
			// 令牌绝不能出现在错误信息里。
			if strings.Contains(err.Error(), "eyJ") {
				t.Fatalf("错误信息疑似夹带令牌: %v", err)
			}
		})
	}
}

func TestResolveRejectsGarbageHelperOutput(t *testing.T) {
	p := newEnv(t)
	writeAuth(t, p, variant.CN, sealedDoc)
	stubHelper(t, "这不是 JSON", nil)

	_, err := Resolve(p, variant.CN)
	if err == nil {
		t.Fatal("应报错")
	}
	// 关键：原始输出不得被回显（那是外部进程的输出，可能含敏感内容）。
	if strings.Contains(err.Error(), "这不是 JSON") {
		t.Fatalf("不该回显 helper 原文: %v", err)
	}
}

// TestResolveWithHonorsExplicitClientPath 确认 --exe 钉的位置被原样传下去。
//
// 这条重要的原因是：显式路径必须是权威的，绝不能被自动探测顶掉——
// 探测到另一处安装会启动一个"看起来对、其实连的是另一个后端"的客户端。
func TestResolveWithHonorsExplicitClientPath(t *testing.T) {
	p := newEnv(t)
	writeAuth(t, p, variant.CN, sealedDoc)

	const pinned = `D:\pinned\WorkBuddy.exe`
	var gotExe string
	prev := findExe
	t.Cleanup(func() { findExe = prev })
	findExe = func(_ *variant.Probe, _ variant.ID, exe string) (string, error) {
		gotExe = exe
		if exe == "" {
			return fakeExe, nil
		}
		return exe, nil
	}
	stubHelper(t, `{"ok":true,"accessToken":"tok-from-pinned"}`, nil)

	c, err := ResolveWith(p, variant.CN, pinned)
	if err != nil {
		t.Fatalf("ResolveWith: %v", err)
	}
	if gotExe != pinned {
		t.Fatalf("显式路径应被原样传下去，得到 %q", gotExe)
	}
	if c.UnsealedBy != pinned {
		t.Fatalf("应用钉住的那个主程序，得到 %q", c.UnsealedBy)
	}
}

// TestResolveUsesRealInstallDetection 走一次真链路：不替换 findExe，
// 让它从假安装里把主程序找出来。其余用例都把定位钉死，只有这里验它确实接上了。
func TestResolveUsesRealInstallDetection(t *testing.T) {
	env := varianttest.New(t)
	ClearCache()
	t.Cleanup(ClearCache)
	writeAuth(t, env.Probe, variant.CN, sealedDoc)
	stubHelper(t, `{"ok":true,"accessToken":"token-from-real-detect"}`, nil)

	c, err := Resolve(env.Probe, variant.CN)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// 必须真的用了假安装里那个主程序，而不是别处碰巧找到的。
	if filepath.Clean(c.UnsealedBy) != filepath.Clean(env.Exe[variant.CN]) {
		t.Fatalf("应借用假安装里的主程序\n得到 %s\n期望 %s", c.UnsealedBy, env.Exe[variant.CN])
	}
}

func TestResolveReportsMissingClientWithoutRunningAnything(t *testing.T) {
	// 主目录里没有任何安装：必须给出"没装客户端"这种可行动的说明。
	p := varianttest.Probe(t.TempDir())
	ClearCache()
	t.Cleanup(ClearCache)
	writeAuth(t, p, variant.CN, sealedDoc)

	prev := execHelper
	t.Cleanup(func() { execHelper = prev })
	execHelper = func(string, []byte) ([]byte, error) {
		t.Fatal("定位不到主程序时不该启动进程")
		return nil, nil
	}

	_, err := Resolve(p, variant.CN)
	if err == nil {
		t.Fatal("应报错")
	}
	if !strings.Contains(err.Error(), "主程序") {
		t.Fatalf("应说明找不到主程序: %v", err)
	}
}

func TestHelperEnvStripsClientStartupKnobs(t *testing.T) {
	got := helperEnv([]string{
		"PATH=/usr/bin",
		"ELECTRON_RUN_AS_NODE=0",
		"electron_enable_logging=1",
		"NODE_OPTIONS=--require evil",
		"WORKBUDDY_TOKEN=leak",
		"HOME=/root",
	})
	joined := strings.Join(got, "\n")

	for _, bad := range []string{"evil", "leak", "ELECTRON_RUN_AS_NODE=0", "enable_logging"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("应剥掉 %q，实际环境:\n%s", bad, joined)
		}
	}
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/root"} {
		if !strings.Contains(joined, keep) {
			t.Fatalf("不该动无关变量 %q，实际环境:\n%s", keep, joined)
		}
	}
	// 必须恰好设一次，且为 1。
	if strings.Count(joined, "ELECTRON_RUN_AS_NODE=") != 1 ||
		!strings.Contains(joined, "ELECTRON_RUN_AS_NODE=1") {
		t.Fatalf("应以 Node 模式启动且只设一次:\n%s", joined)
	}
}

func TestCapWriterTruncatesAndReportsFullConsumption(t *testing.T) {
	w := &capWriter{limit: 4}
	n, err := w.Write([]byte("abcdef"))
	if err != nil || n != 6 {
		// 必须报"全收下"：报少了会让子进程以为写失败而挂死。
		t.Fatalf("应报告全部消费，得到 n=%d err=%v", n, err)
	}
	if w.buf.String() != "abcd" || !w.over {
		t.Fatalf("应截断到上限并置 over，得到 %q over=%v", w.buf.String(), w.over)
	}

	// 第二次写：已经满了，内容全丢但仍报成功。
	if n, _ := w.Write([]byte("xyz")); n != 3 {
		t.Fatalf("满了也要报全部消费，得到 %d", n)
	}
	if w.buf.String() != "abcd" {
		t.Fatalf("满后不该再增长: %q", w.buf.String())
	}
}

func TestCapWriterUnderLimitKeepsEverything(t *testing.T) {
	w := &capWriter{limit: 16}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.buf.String() != "hello" || w.over {
		t.Fatalf("未超限应原样保留: %q over=%v", w.buf.String(), w.over)
	}
}

func TestRedact(t *testing.T) {
	c := Credential{Token: "supersecrettoken"}
	got := c.Redact("调用失败，token=supersecrettoken 已失效")
	if strings.Contains(got, "supersecrettoken") {
		t.Fatalf("应替换掉令牌: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("应留下占位符: %s", got)
	}

	// 短令牌不做替换，免得把正常文本误伤。
	short := Credential{Token: "ab"}
	if got := short.Redact("about ab"); got != "about ab" {
		t.Fatalf("短令牌不该替换: %s", got)
	}
}

// TestHelperJSCarriesTheVerifiedCryptoShape 钉住 helper 里几处「写错就看不出原因」
// 的细节。这些常量一旦被误改，症状只是"解密失败"，没有别的地方能拦住。
func TestHelperJSCarriesTheVerifiedCryptoShape(t *testing.T) {
	must := map[string]string{
		// 借运行时的方式
		"electron_browser_workbuddy_storage": "原生模块名",
		"loggerGet":                          "取密钥的入口",
		"aes-256-gcm":                        "信封算法",
		"authTagLength: 16":                  "GCM 标签长度",
		// AAD 前缀必须是 7 字节（含 NUL）：写成 'WB-AAD' 会静默得到 DECRYPT_FAILED
		`'WB-AAD\0'`:    "AAD 前缀",
		"lp('WBEV1')":   "AAD 版本段",
		"lp('sym-v1')":  "AAD 套件段",
		"writeUInt32BE": "长度前缀必须是 big-endian",
		// 密钥派生哈希的是 base64 字符串本身，不是解码后的字节——最容易写反的一处
		"createHash('sha256').update(payload.atRestSecretKey, 'utf8')": "密钥派生方式",
	}
	for frag, why := range must {
		if !strings.Contains(helperJS, frag) {
			t.Fatalf("helper 里缺少%s（%q）——改动它会静默导致解密失败", why, frag)
		}
	}
	// keyId 必须参与 AAD，否则 AAD 构造不完整。
	if !strings.Contains(helperJS, "lp(envelope.keyId)") {
		t.Fatal("AAD 里必须包含 keyId")
	}
	// 载荷与命令都必须写死在 Go 侧；这里只确认脚本本身不读任何外部输入。
	if strings.Contains(helperJS, "process.argv") {
		t.Fatal("helper 不该从命令行读输入——载荷只走 stdin")
	}
}
