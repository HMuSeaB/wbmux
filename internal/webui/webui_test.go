package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/variant/varianttest"
)

// ---------- 脚手架 ----------
//
// 测试不碰真实机器：安装探测走注入的 Probe（临时目录里的假安装），
// 设置目录走 config.SetRoot（临时目录）。否则在装了两套客户端的开发机上
// 断言会随机器状态漂移，而 CI 上又会因为什么都没装而失败。

type testEnv struct {
	srv  *Server
	root string // 假的 ~/.wbmux
	home string
}

func newTestEnv(t *testing.T, opts Options) *testEnv {
	t.Helper()

	root := t.TempDir()
	t.Cleanup(config.SetRoot(root))

	env := varianttest.New(t)
	if opts.Probe == nil {
		opts.Probe = env.Probe
	}

	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Shutdown)

	return &testEnv{srv: srv, root: root, home: env.Home}
}

// ---------- 请求辅助 ----------

type response struct {
	code int
	body []byte
}

func (r response) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("解析响应失败: %v（原文 %s）", err, r.body)
	}
}

func (e *testEnv) do(t *testing.T, method, path, body string, withToken bool) response {
	t.Helper()

	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, "http://"+e.srv.Addr()+path, rdr)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if withToken {
		req.Header.Set("X-Wbmux-Token", e.srv.token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, path, err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return response{code: resp.StatusCode, body: buf.Bytes()}
}

// ---------- 令牌与访问控制 ----------

func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: ""},
		{in: "127.0.0.1:8080", want: "127.0.0.1:8080"},
		{in: "localhost:8080", want: "localhost:8080"},
		{in: "[::1]:8080", want: "[::1]:8080"},
		{in: "127.0.0.1", want: "127.0.0.1"},

		// 这个服务能启动本机进程，非回环地址必须直接拒绝而不是警告放行。
		{in: "0.0.0.0:8080", wantErr: true},
		{in: "192.168.1.5:8080", wantErr: true},
		{in: ":8080", wantErr: true},
		{in: "example.com:80", wantErr: true},
		{in: "8.8.8.8:53", wantErr: true},
	}

	for _, c := range cases {
		got, err := NormalizeAddr(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizeAddr(%q) 应当报错，却返回 %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeAddr(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeAddr(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestGuardRejectsMissingAndWrongToken(t *testing.T) {
	e := newTestEnv(t, Options{})

	if got := e.do(t, http.MethodGet, "/api/ping", "", false).code; got != http.StatusForbidden {
		t.Errorf("无令牌访问 /api/ping = %d，期望 403", got)
	}
	if got := e.do(t, http.MethodGet, "/api/state", "", false).code; got != http.StatusForbidden {
		t.Errorf("无令牌访问 /api/state = %d，期望 403", got)
	}
	if got := e.do(t, http.MethodGet, "/api/ping", "", true).code; got != http.StatusOK {
		t.Errorf("带令牌访问 /api/ping = %d，期望 200", got)
	}
}

func TestGuardAcceptsTokenInQuery(t *testing.T) {
	e := newTestEnv(t, Options{})

	// 令牌写在查询串里是给"首次打开页面"用的——浏览器没法给地址栏
	// 里的请求加自定义头。后续 API 调用才走 X-Wbmux-Token。
	got := e.do(t, http.MethodGet, "/api/ping?t="+e.srv.token, "", false)
	if got.code != http.StatusOK {
		t.Errorf("查询串令牌 = %d，期望 200", got.code)
	}
}

func TestIndexRequiresTokenAndServesPage(t *testing.T) {
	e := newTestEnv(t, Options{Version: "1.2.3"})

	if got := e.do(t, http.MethodGet, "/", "", false).code; got != http.StatusForbidden {
		t.Errorf("无令牌访问首页 = %d，期望 403（否则任何网页都能把它嵌进 iframe）", got)
	}

	got := e.do(t, http.MethodGet, "/?t="+e.srv.token, "", false)
	if got.code != http.StatusOK {
		t.Fatalf("带令牌访问首页 = %d，期望 200", got.code)
	}
	if len(got.body) == 0 {
		t.Fatal("首页内容为空，embed 可能没生效")
	}
	page := string(got.body)
	// 界面靠这几个 id 挂载，缺一个就会白屏。
	for _, want := range []string{`id="backends"`, `id="installs"`, `id="doctor-body"`} {
		if !strings.Contains(page, want) {
			t.Errorf("首页缺少 %s", want)
		}
	}
}

func TestUnknownPathIsNotFound(t *testing.T) {
	e := newTestEnv(t, Options{})

	got := e.do(t, http.MethodGet, "/nope?t="+e.srv.token, "", false)
	if got.code != http.StatusNotFound {
		t.Errorf("未知路径 = %d，期望 404", got.code)
	}
}

// ---------- 状态与预览 ----------

func TestStateReportsBothInstalls(t *testing.T) {
	e := newTestEnv(t, Options{Version: "9.9.9"})

	var st State
	e.do(t, http.MethodGet, "/api/state", "", true).decode(t, &st)

	if st.Version != "9.9.9" {
		t.Errorf("version = %q", st.Version)
	}
	if len(st.Installs) != 2 {
		t.Fatalf("installs 数量 = %d，期望 2", len(st.Installs))
	}
	for _, inst := range st.Installs {
		if !inst.Found {
			t.Errorf("%s 未被探测到：%v", inst.ID, inst.Problems)
		}
		if inst.Executable == "" {
			t.Errorf("%s 缺少主程序路径", inst.ID)
		}
	}
	if len(st.Backends) != 2 {
		t.Fatalf("backends 数量 = %d，期望 2", len(st.Backends))
	}
	// 宿主默认解析到国内版，两套后端都应当可以启动
	// （覆盖模式不要求目标档位自己有安装）。
	if st.HostID != variant.CN {
		t.Errorf("hostId = %q，期望 %q", st.HostID, variant.CN)
	}
	for _, b := range st.Backends {
		if !st.CanRun[string(b.ID)] {
			t.Errorf("%s 应当可以启动", b.ID)
		}
	}
	if st.Settings.Path == "" {
		t.Error("settings.path 为空，设置目录接缝可能没生效")
	}
	// 设置目录必须落在临时目录里，否则测试写进了用户的 ~/.wbmux。
	if !strings.HasPrefix(st.Settings.Path, e.root) {
		t.Errorf("settings.path = %q，不在临时目录 %q 内", st.Settings.Path, e.root)
	}
}

func TestPreviewRejectsBadRequest(t *testing.T) {
	e := newTestEnv(t, Options{})

	if got := e.do(t, http.MethodGet, "/api/preview", "", true).code; got != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/preview = %d，期望 405", got)
	}
	if got := e.do(t, http.MethodPost, "/api/preview", "not json", true).code; got != http.StatusBadRequest {
		t.Errorf("非法 JSON = %d，期望 400", got)
	}
	if got := e.do(t, http.MethodPost, "/api/preview", `{"target":"nope"}`, true).code; got != http.StatusBadRequest {
		t.Errorf("未知目标 = %d，期望 400", got)
	}
	// 未带令牌的请求不该走到解析逻辑。
	if got := e.do(t, http.MethodPost, "/api/preview", `{"target":"intl"}`, false).code; got != http.StatusForbidden {
		t.Errorf("无令牌 = %d，期望 403", got)
	}
}

func TestPreviewDescribesRewritesAndLeavesRestAlone(t *testing.T) {
	e := newTestEnv(t, Options{})

	var pv PreviewView
	e.do(t, http.MethodPost, "/api/preview", `{"target":"intl"}`, true).decode(t, &pv)

	if pv.TargetID != variant.Intl {
		t.Fatalf("targetId = %q", pv.TargetID)
	}
	if pv.HostID != variant.CN {
		t.Fatalf("hostId = %q，期望宿主仍是国内版", pv.HostID)
	}
	if pv.Native {
		t.Error("未请求原生模式，native 却为 true")
	}
	// 数据目录必须跟着目标后端走：两套后端账号体系不互通，
	// 共用 profile 会互相冲掉登录态。
	if !strings.Contains(pv.DataDir, variant.Intl.String()) && !strings.Contains(pv.DataDir, ".workbuddy-ai") {
		t.Errorf("dataDir = %q，应当跟随国际版的数据目录", pv.DataDir)
	}
	if pv.ConfigPath == "" {
		t.Error("configPath 为空")
	}
	if pv.CommandLine == "" {
		t.Error("commandLine 为空")
	}

	joined := strings.Join(pv.Changes, "\n")
	for _, want := range []string{"endpoint", "dataFolderName", "officialEndpoints", "isOversea"} {
		if !strings.Contains(joined, want) {
			t.Errorf("改写清单里缺少 %s：\n%s", want, joined)
		}
	}
	// 这几项绝不能动：改了会让客户端自己的更新与产品标识错乱。
	for _, forbidden := range []string{"productName", "updates", "smhHost", "productFeatures"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("改写清单里不该出现 %s：\n%s", forbidden, joined)
		}
	}
}

func TestPreviewNativeModeSkipsRewrites(t *testing.T) {
	e := newTestEnv(t, Options{})

	var pv PreviewView
	e.do(t, http.MethodPost, "/api/preview", `{"target":"intl","native":true}`, true).decode(t, &pv)

	if !pv.Native {
		t.Fatal("native 应为 true")
	}
	if len(pv.Changes) != 0 {
		t.Errorf("原生模式不该有改写项，却得到 %v", pv.Changes)
	}
	if pv.ConfigPath != "" {
		t.Errorf("原生模式不该生成配置，却得到 %q", pv.ConfigPath)
	}
	// 原生模式用目标档位自己的安装，因此宿主就是国际版。
	if pv.HostID != variant.Intl {
		t.Errorf("hostId = %q，期望国际版", pv.HostID)
	}
}

func TestDoctorReportsChecks(t *testing.T) {
	e := newTestEnv(t, Options{})

	var dv DoctorView
	e.do(t, http.MethodGet, "/api/doctor?target=intl", "", true).decode(t, &dv)

	if len(dv.Checks) == 0 {
		t.Fatal("体检没有任何检查项")
	}
	for _, c := range dv.Checks {
		if c.Name == "" || c.Level == "" {
			t.Errorf("检查项字段不全: %+v", c)
		}
	}
	if dv.Summary == "" {
		t.Error("体检缺少结论")
	}
	if got := e.do(t, http.MethodGet, "/api/doctor?target=nope", "", true).code; got != http.StatusBadRequest {
		t.Errorf("非法 target = %d，期望 400", got)
	}
}

// ---------- 生命周期 ----------

func TestQuitStopsServer(t *testing.T) {
	e := newTestEnv(t, Options{})

	got := e.do(t, http.MethodPost, "/api/quit", "", true)
	if got.code != http.StatusOK {
		t.Fatalf("/api/quit = %d，期望 200", got.code)
	}

	select {
	case <-e.srv.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("请求关闭后服务没有停下")
	}
}

func TestIdleShutdownWhenUnattended(t *testing.T) {
	e := newTestEnv(t, Options{IdleTimeout: 150 * time.Millisecond})

	select {
	case <-e.srv.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("空闲超时没有生效，残留进程会一直占着端口")
	}
}

func TestActivityPostponesIdleShutdown(t *testing.T) {
	// 页面每 60 秒发一次心跳，因此"标签页还开着"不该被算成空闲。
	// 这里把超时压到 300ms、心跳间隔压到 80ms，用同样的比例验证这条规则。
	e := newTestEnv(t, Options{IdleTimeout: 300 * time.Millisecond})

	deadline := time.Now().Add(900 * time.Millisecond)
	for time.Now().Before(deadline) {
		if got := e.do(t, http.MethodGet, "/api/ping", "", true); got.code != http.StatusOK {
			t.Fatalf("心跳返回 %d", got.code)
		}
		select {
		case <-e.srv.Done():
			t.Fatal("有活动时服务不该退出")
		case <-time.After(80 * time.Millisecond):
		}
	}

	// 停止心跳后应当自行退出。
	select {
	case <-e.srv.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("心跳停止后服务没有退出")
	}
}

func TestIdleDisabledKeepsServerAlive(t *testing.T) {
	e := newTestEnv(t, Options{IdleTimeout: 0})

	select {
	case <-e.srv.Done():
		t.Fatal("IdleTimeout 为 0 时不该自动退出")
	case <-time.After(500 * time.Millisecond):
	}
}

// ---------- 纯函数 ----------

func TestShortBuild(t *testing.T) {
	cases := map[string]string{
		"":             "",
		"37a65c0b":     "37a65c0b",
		"37a65c0b1234": "37a65c0b",
	}
	for in, want := range cases {
		if got := shortBuild(in); got != want {
			t.Errorf("shortBuild(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestNewGeneratesDistinctTokens(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		s, err := New(Options{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if len(s.token) != 32 {
			t.Fatalf("令牌长度 = %d，期望 32", len(s.token))
		}
		if seen[s.token] {
			t.Fatal("两次启动拿到了相同令牌")
		}
		seen[s.token] = true
	}
}

func TestURLContainsToken(t *testing.T) {
	e := newTestEnv(t, Options{})

	u := e.srv.URL()
	if !strings.HasPrefix(u, "http://127.0.0.1:") {
		t.Errorf("URL 不是回环地址: %q", u)
	}
	if !strings.Contains(u, "t="+e.srv.token) {
		t.Errorf("URL 未带令牌: %q", u)
	}
}
