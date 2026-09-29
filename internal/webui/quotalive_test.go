package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newQuotaTestServer 造一个**日志已隔离**的 Server。
//
// # 为什么必须隔离（2026-09-29 自己踩到）
//
// quotaSnapshot 会从 gui.log 恢复限流状态。测试若用 `&Server{}`，
// logPathFn 为 nil → 回落到**用户真实的** ~/.wbmux/gui.log，于是断言结果
// 随开发机上真跑过什么而变（TestQuotaSnapshotOrder 就是这么从 3 条变 4 条的）。
// 这与"测试不许碰真实用户状态"是同一条规矩。
func newQuotaTestServer(t *testing.T) *Server {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "no-such.log") // 故意不存在 → 播种为空
	return &Server{logPathFn: func() string { return missing }}
}

// TestParseResetAt 钉住上游文案里恢复时刻的解析。
//
// 这条值得单测是因为它的失败**不响**：上游改一个字，解析不出来时面板只是
// 少显示一个时间，不会报错——用户看到的仍然是"被限流"，但不知道等到什么时候。
func TestParseResetAt(t *testing.T) {
	// 实测原文（2026-09-29，国际后端对 ds4.1-flash 的 429）。
	real := "usage exceeds frequency limit, but don't worry, your usage will reset at " +
		"2026-09-29 22:18:43 UTC+8, alternatively, you can switch to the other models to continue using it."

	ms, raw := parseResetAt(real)
	if ms == 0 {
		t.Fatalf("没解析出恢复时刻：raw=%q", raw)
	}
	if raw != "2026-09-29 22:18:43" {
		t.Errorf("原文时刻应原样保留，得到 %q", raw)
	}
	want := time.Date(2026, 9, 29, 22, 18, 43, 0, time.FixedZone("", 8*3600)).UnixMilli()
	if ms != want {
		t.Errorf("时刻算错：得到 %d，期望 %d（相差 %d 秒）",
			ms, want, (ms-want)/1000)
	}

	// 时区标注变了要跟着走（UTC-5）。
	ms2, _ := parseResetAt("reset at 2026-09-29 09:18:43 UTC-5")
	want2 := time.Date(2026, 9, 29, 9, 18, 43, 0, time.FixedZone("", -5*3600)).UnixMilli()
	if ms2 != want2 {
		t.Errorf("UTC-5 算错：得到 %d，期望 %d", ms2, want2)
	}

	// 没有时区标注时按 +8（国内侧服务的默认）。
	ms3, _ := parseResetAt("reset at 2026-09-29 22:18:43")
	if ms3 != want {
		t.Errorf("无时区标注时应按 +8，得到 %d，期望 %d", ms3, want)
	}

	// 解析不出来要老实返回 0，而不是给一个假的时刻。
	if ms4, _ := parseResetAt("some other error"); ms4 != 0 {
		t.Errorf("认不出来时应返回 0，得到 %d", ms4)
	}
	// `T` 分隔也要能吃（ISO 风格）。
	if ms5, _ := parseResetAt("reset at 2026-09-29T22:18:43"); ms5 != want {
		t.Errorf("T 分隔没吃住：得到 %d", ms5)
	}
}

// TestLooksExhausted 区分"额度用尽"与"频率超限"——两者的处置完全相反
// （充值 vs 等），混成一句会让人白等一晚。
func TestLooksExhausted(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"Credits exhausted", true},
		{"your credits have been exhausted, please recharge", true},
		// 给了恢复时刻的一律算频率类。
		{"usage exceeds frequency limit, reset at 2026-09-29 22:18:43 UTC+8", false},
		{"Credits exhausted, reset at 2026-09-29 22:18:43", false},
		{"internal server error", false},
	}
	for _, c := range cases {
		if got := looksExhausted(c.msg); got != c.want {
			t.Errorf("looksExhausted(%q) = %v，期望 %v", c.msg, got, c.want)
		}
	}
}

// TestObserveTracksOnlyRateLimit 只有 429 才算"被限"，成功要清零。
func TestObserveTracksOnlyRateLimit(t *testing.T) {
	s := newQuotaTestServer(t)

	// 429 + 恢复时刻 → rate_limited 且带 UntilMs
	s.observe("deepseek-v4.1-flash", http.StatusTooManyRequests,
		"usage exceeds frequency limit, will reset at 2026-09-29 22:18:43 UTC+8")
	snap := s.quotaSnapshot()
	if len(snap) != 1 {
		t.Fatalf("应有 1 条状态，得到 %d", len(snap))
	}
	if snap[0].State != "rate_limited" || snap[0].UntilMs == 0 {
		t.Errorf("状态不对：%+v", snap[0])
	}

	// 另一个模型不受影响（限流按模型单独计）
	s.observe("hy4-preview-f", http.StatusTooManyRequests, "Credits exhausted")
	snap = s.quotaSnapshot()
	if len(snap) != 2 {
		t.Fatalf("应有 2 条状态，得到 %d", len(snap))
	}
	// 额度用尽的排后面，且没有恢复时刻
	if snap[1].State != "exhausted" || snap[1].UntilMs != 0 {
		t.Errorf("额度用尽那条不对：%+v", snap[1])
	}

	// 非 429 的错误不该被当成限流（400 模型名不对、500 上游故障）
	s.observe("gpt-6-astra", http.StatusBadRequest, "当前模型不可用")
	s.observe("gpt-6-astra", http.StatusInternalServerError, "boom")
	if len(s.quotaSnapshot()) != 2 {
		t.Errorf("非 429 不该被记为限流：%+v", s.quotaSnapshot())
	}

	// 成功要清掉状态——否则模型恢复了面板还一直报限流。
	s.observe("deepseek-v4.1-flash", http.StatusOK, "")
	snap = s.quotaSnapshot()
	if len(snap) != 1 || snap[0].Model != "hy4-preview-f" {
		t.Errorf("成功后应只剩 hy4 那条，得到 %+v", snap)
	}

	// 空模型名不该留下一堆空键。
	s.observe("", http.StatusTooManyRequests, "x")
	if len(s.quotaSnapshot()) != 1 {
		t.Errorf("空模型名不该记账：%+v", s.quotaSnapshot())
	}
}

// TestQuotaSnapshotOrder 限流中的排前面，且按恢复时刻由近到远。
func TestQuotaSnapshotOrder(t *testing.T) {
	s := newQuotaTestServer(t)
	s.observe("b-model", http.StatusTooManyRequests, "reset at 2026-09-29 23:00:00 UTC+8")
	s.observe("a-model", http.StatusTooManyRequests, "reset at 2026-09-29 22:00:00 UTC+8")
	s.observe("z-model", http.StatusTooManyRequests, "Credits exhausted")

	snap := s.quotaSnapshot()
	if len(snap) != 3 {
		t.Fatalf("应有 3 条，得到 %d", len(snap))
	}
	// 先按恢复时刻近的在前，额度用尽的垫底
	if snap[0].Model != "a-model" || snap[1].Model != "b-model" || snap[2].Model != "z-model" {
		got := []string{snap[0].Model, snap[1].Model, snap[2].Model}
		t.Errorf("排序不对：得到 %v，期望 [a-model b-model z-model]", got)
	}
}

// TestParseQuotaLog 从日志尾部恢复限流状态。
//
// # 这条为什么必须存在（2026-09-29 用户反馈"重启后没显示那个限流了"）
//
// 状态原先只在内存里，**进程一重启就没了**——而那时模型其实还被限着，
// 界面于是显示"都能用"（把"没有数据"当成了"没问题"）。用户以为能用了，
// 一发请求又 429。日志是持久化的同一批事实，启动时拿它恢复。
func TestParseQuotaLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gui.log")
	now := time.Date(2026, 9, 29, 21, 0, 0, 0, time.FixedZone("", 8*3600)).UnixMilli()

	// 用真实日志的格式（含两条空格分隔、`model=` 位置、上游原话）。
	body := strings.Join([]string{
		`2026-09-29 19:56:53  转发完成 model=deepseek-v4.1-flash 经国际后端 tools=23 stream=true 回给客户端 119.9 KB 用时 5.49s`,
		`2026-09-29 19:57:13  被国际后端拒绝 model=deepseek-v4.1-flash 上游 HTTP 429: usage exceeds frequency limit, but don't worry, your usage will reset at 2026-09-29 22:18:43 UTC+8, alternatively, you can switch to the other models to continue using it.`,
		`2026-09-29 20:20:33  收到请求 model=hy4-preview-f 经国际后端 tools=23 stream=true 正文 1.7 MB`,
		`2026-09-29 20:20:54  转发完成 model=hy4-preview-f 经国际后端 tools=23 stream=true 回给客户端 66.5 KB 用时 21.881s`,
		`2026-09-29 19:00:00  被国际后端拒绝 model=gpt-6-astra 上游 HTTP 429: Credits exhausted`,
		`2026-09-29 19:01:00  被国际后端拒绝 model=glm-5.3 上游 HTTP 400: 当前模型不可用`,
		`2026-09-29 19:02:00  转发失败 model=kimi-k3 经国际后端：dial tcp: timeout`,
		"",
	}, "\n")
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got := map[string]ProxyQuota{}
	for _, q := range parseQuotaLog(logPath, now).Limited {
		got[q.Model] = q
	}

	// ds4.1：最后一条是 429 且恢复时刻（22:18）还没到 → 应报限流。
	ds, ok := got["deepseek-v4.1-flash"]
	if !ok {
		t.Fatal("ds4.1 应被恢复成限流状态")
	}
	if ds.State != "rate_limited" || ds.UntilMs == 0 {
		t.Errorf("ds4.1 状态不对：%+v", ds)
	}
	if ds.UntilText != "2026-09-29 22:18:43" {
		t.Errorf("恢复时刻原文应保留，得到 %q", ds.UntilText)
	}

	// hy4：最后一条是「转发完成」→ 不该在限流表里。
	if _, ok := got["hy4-preview-f"]; ok {
		t.Errorf("hy4 最后一次是成功，不该报限流：%+v", got["hy4-preview-f"])
	}
	// 额度用尽要记下来（它不会自愈，也就没有过期一说）。
	if g := got["gpt-6-astra"]; g.State != "exhausted" {
		t.Errorf("gpt-6-astra 应记为 exhausted，得到 %+v", g)
	}
	// 非 429 的拒绝与网络失败都不算限流。
	for _, m := range []string{"glm-5.3", "kimi-k3"} {
		if _, ok := got[m]; ok {
			t.Errorf("%s 不该被当成限流：%+v", m, got[m])
		}
	}
}

// TestParseQuotaLogDropsExpired 恢复时刻已过的限流不该再报。
//
// 日志里没有"恢复"事件（恢复是时间到了自然发生），只能靠时刻判断——
// 不做这一步的话，重启后会把几天前的旧限流一直报下去。
func TestParseQuotaLogDropsExpired(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gui.log")
	body := `2026-09-29 19:57:13  被国际后端拒绝 model=deepseek-v4.1-flash 上游 HTTP 429: reset at 2026-09-29 20:00:00 UTC+8`
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	after := time.Date(2026, 9, 29, 21, 0, 0, 0, time.FixedZone("", 8*3600)).UnixMilli()
	if got := parseQuotaLog(logPath, after).Limited; len(got) != 0 {
		t.Errorf("恢复时刻已过，应返回空，得到 %+v", got)
	}
	// 同一个文件、把"现在"调到恢复之前 → 应当报出来。
	before := time.Date(2026, 9, 29, 19, 59, 0, 0, time.FixedZone("", 8*3600)).UnixMilli()
	if got := parseQuotaLog(logPath, before).Limited; len(got) != 1 {
		t.Errorf("恢复时刻未到时应报限流，得到 %+v", got)
	}
}

// TestParseQuotaLogMissingFile 日志不存在时要安静返回空，不能崩。
func TestParseQuotaLogMissingFile(t *testing.T) {
	if got := parseQuotaLog(filepath.Join(t.TempDir(), "nope.log"), 0).Limited; len(got) != 0 {
		t.Errorf("读不到日志应返回空，得到 %+v", got)
	}
	if got := parseQuotaLog("", 0).Limited; len(got) != 0 {
		t.Errorf("空路径应返回空，得到 %+v", got)
	}
}

// TestSeedFromLogOnce 播种只跑一次，且不覆盖启动后新观察到的事实。
func TestSeedFromLogOnce(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "gui.log")
	body := `2026-09-29 19:57:13  被国际后端拒绝 model=deepseek-v4.1-flash 上游 HTTP 429: reset at 2099-01-01 00:00:00 UTC+8`
	if err := os.WriteFile(logPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Server{logPathFn: func() string { return logPath }}

	if snap := s.quotaSnapshot(); len(snap) != 1 {
		t.Fatalf("应从日志恢复 1 条，得到 %+v", snap)
	}
	// 启动后这个模型成功了一次 → 状态清掉；再查不该被日志里的旧记录复活。
	s.observe("deepseek-v4.1-flash", http.StatusOK, "")
	if snap := s.quotaSnapshot(); len(snap) != 0 {
		t.Errorf("成功后不该被日志里的旧记录复活，得到 %+v", snap)
	}
}
