package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
func TestObserveTracksRateLimitAndUpstreamFault(t *testing.T) {
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

	// 400（模型名不对）不该被当成限流——那是调用方的错，报成限流会把排查带偏。
	s.observe("gpt-6-astra", http.StatusBadRequest, "当前模型不可用")
	if len(s.quotaSnapshot()) != 2 {
		t.Errorf("400 不该被记为限流：%+v", s.quotaSnapshot())
	}

	// 但 5xx **要**单独记一笔（upstream_error）：那是上游自己挂了，
	// 跟用户的配置/额度都无关。之前这里一律忽略，导致上游抽风在面板上
	// 毫无痕迹（2026-10-15 用户连撞 4 次 step-5-preview 的 500 才发现）。
	s.observe("step-5-preview", http.StatusInternalServerError, "服务暂时异常，请稍后重试。")
	snap = s.quotaSnapshot()
	var fault *ProxyQuota
	for i := range snap {
		if snap[i].Model == "step-5-preview" {
			fault = &snap[i]
		}
	}
	if fault == nil {
		t.Fatalf("上游 5xx 应留下状态：%+v", snap)
	}
	if fault.State != "upstream_error" || fault.UntilMs != 0 {
		t.Errorf("5xx 状态应为 upstream_error 且没有恢复时刻：%+v", *fault)
	}
	if len(snap) != 3 {
		t.Errorf("应有 3 条状态（2 限流 + 1 上游故障），得到 %d", len(snap))
	}

	// 成功要清掉状态——否则模型恢复了面板还一直报限流。
	s.observe("deepseek-v4.1-flash", http.StatusOK, "")
	s.observe("step-5-preview", http.StatusOK, "")
	snap = s.quotaSnapshot()
	if len(snap) != 1 || snap[0].Model != "hy4-preview-f" {
		t.Errorf("成功后应只剩 hy4 那条，得到 %+v", snap)
	}

	// 空模型名不该留下一堆空键。
	s.observe("", http.StatusTooManyRequests, "x")
	s.observe("", http.StatusInternalServerError, "x")
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
		// 上游故障：文案是"上游故障"而不是"拒绝"（2026-10-15 起区分），
		// 恢复时也要认出来——重启不该把"它刚挂过"这件事丢掉。
		`2026-09-29 19:03:00  被国际后端上游故障 model=step-5-preview 上游 HTTP 500: 服务暂时异常，请稍后重试。`,
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
	// 但 5xx 要恢复成 upstream_error（没有任何恢复时刻）。
	if sf, ok := got["step-5-preview"]; !ok {
		t.Error("上游 5xx 应从日志恢复成 upstream_error")
	} else if sf.State != "upstream_error" || sf.UntilMs != 0 {
		t.Errorf("step-5-preview 状态不对：%+v", sf)
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

// TestQuotaForNilWhenHealthy 确认"没记录"与"限流中"分得开。
//
// 这是注入过滤的前提：返回 nil 表示**可以注入**。
// 若把"没记录"也返回成零值，调用方会把所有模型都当成有问题，
// 一个都注入不进去。
func TestQuotaForNilWhenHealthy(t *testing.T) {
	s := newQuotaTestServer(t)
	if q := s.quotaFor("deepseek-v4.1-flash"); q != nil {
		t.Errorf("没有记录的模型应返回 nil，实际 %+v", q)
	}
	if q := s.quotaFor(""); q != nil {
		t.Errorf("空模型名应返回 nil，实际 %+v", q)
	}
}

// TestQuotaForExpiredIsUsable 到了恢复时刻就该重新可用。
//
// 少了这一步，一个模型一旦被限流就会**永远**被挡在外面——
// 哪怕它的额度早就恢复了。
func TestQuotaForExpiredIsUsable(t *testing.T) {
	s := newQuotaTestServer(t)

	past := time.Now().Add(-time.Minute).UnixMilli()
	s.rate.items = map[string]ProxyQuota{
		"hy4-preview-f": {Model: "hy4-preview-f", State: "rate_limited", UntilMs: past},
	}
	if q := s.quotaFor("hy4-preview-f"); q != nil {
		t.Error("恢复时刻已过，应判定为可用（返回 nil）")
	}

	future := time.Now().Add(time.Hour).UnixMilli()
	s.rate.items["hy4-preview-f"] = ProxyQuota{
		Model: "hy4-preview-f", State: "rate_limited", UntilMs: future,
		UntilText: "2026-10-05 02:13:38",
	}
	q := s.quotaFor("hy4-preview-f")
	if q == nil {
		t.Fatal("还在限流窗口内，应返回状态而不是 nil")
	}
	if q.UntilText != "2026-10-05 02:13:38" {
		t.Errorf("恢复时刻文案没带出来：%q", q.UntilText)
	}
}

// TestQuotaForExhaustedStaysBlocked 额度用尽（没有恢复时刻）要一直挡着。
//
// 与 rate_limited 的区别：那个有 UntilMs 会自己恢复，这个不会。别混为一谈。
func TestQuotaForExhaustedStaysBlocked(t *testing.T) {
	s := newQuotaTestServer(t)
	s.rate.items = map[string]ProxyQuota{
		"some-model": {Model: "some-model", State: "exhausted"},
	}
	q := s.quotaFor("some-model")
	if q == nil {
		t.Fatal("额度已尽应一直挡住")
	}
	if q.State != "exhausted" {
		t.Errorf("状态不对：%q", q.State)
	}
}

// TestQuotaForReturnsCopy 返回副本，调用方改它不该污染内部状态。
func TestQuotaForReturnsCopy(t *testing.T) {
	s := newQuotaTestServer(t)
	future := time.Now().Add(time.Hour).UnixMilli()
	s.rate.items = map[string]ProxyQuota{
		"m": {Model: "m", State: "rate_limited", UntilMs: future},
	}
	q := s.quotaFor("m")
	if q == nil {
		t.Fatal("应返回状态")
	}
	q.State = "tampered"
	if s.rate.items["m"].State != "rate_limited" {
		t.Error("返回的是引用，调用方能改坏内部状态")
	}
}

// TestQuotaWarnTextExplainsConsequence 提醒的重点不是"你被限流了"，
// 而是"客户端接下来会做什么"。
//
// 只说前半句没用——客户端自己也会弹限流提示。真正容易被坑的是后半句：
// 它拿到 429 会**自己换模型**继续跑，而国内客户端那边的备选常常是付费模型，
// 这一下不走 wbmux，用户看不到。所以文案里必须有"付费"和"自动切换"两个意思。
func TestQuotaWarnTextExplainsConsequence(t *testing.T) {
	got := quotaWarnText([]ProxyQuota{
		{Model: "hy4-preview-f", State: "rate_limited", UntilText: "2026-10-05 02:13:38"},
	})
	if got == "" {
		t.Fatal("有限流记录时应该有提醒")
	}
	for _, want := range []string{"hy4-preview-f", "2026-10-05 02:13:38", "付费", "自动"} {
		if !strings.Contains(got, want) {
			t.Errorf("提醒里缺 %q：\n  %s", want, got)
		}
	}

	// 没有记录 → 不该凭空造一句提醒
	if quotaWarnText(nil) != "" {
		t.Error("没有限流记录时不该有提醒")
	}
}

// TestQuotaWarnTextExhaustedHasNoTime 额度已尽没有恢复时刻，
// 文案不能写出一个空的时间。
func TestQuotaWarnTextExhaustedHasNoTime(t *testing.T) {
	got := quotaWarnText([]ProxyQuota{{Model: "m", State: "exhausted"}})
	if strings.Contains(got, "（）") || strings.Contains(got, "()") {
		t.Errorf("额度已尽时写出了空的恢复时刻：%s", got)
	}
	if !strings.Contains(got, "m") {
		t.Errorf("模型名没写进去：%s", got)
	}
}

// TestBlocksInjectionNeverBlocks 钉住"不再阻止注入"这条用户控制权界线。
// 即使发生限流，客户端也必须保有模型入口，网关层由 Auto-Failover 自动兜底。
func TestBlocksInjectionNeverBlocks(t *testing.T) {
	for _, state := range []string{"rate_limited", "exhausted", "upstream_error", "ok"} {
		if got := (ProxyQuota{State: state}).BlocksInjection(); got {
			t.Errorf("State=%q BlocksInjection() 期望为 false（不阻止注入），实际为 %v", state, got)
		}
	}
}

// TestQuotaForStillReturnsUpstreamFault 上游故障仍要能被单点查询到——
// 界面靠它显示"上游故障，可重试"，不能因为"不阻止注入"就把它藏起来。
func TestQuotaForStillReturnsUpstreamFault(t *testing.T) {
	s := newQuotaTestServer(t)
	s.observe("step-5-preview", http.StatusInternalServerError, "服务暂时异常")
	q := s.quotaFor("step-5-preview")
	if q == nil {
		t.Fatal("上游故障应能被 quotaFor 查到")
	}
	if q.State != "upstream_error" {
		t.Errorf("状态不对：%+v", *q)
	}
	if q.BlocksInjection() {
		t.Error("上游故障不该阻止注入")
	}
}

// TestQuotaWarnTextSeparatesUpstreamFault 上游故障的措辞必须与"被限"分开。
//
// 混在一起会把人引到错误方向：被限是"等重置/充值"，上游故障是"重试就好"。
func TestQuotaWarnTextSeparatesUpstreamFault(t *testing.T) {
	// 只有上游故障：不该出现"收到拒绝后可能自动切到付费模型"那套限流措辞。
	only := quotaWarnText([]ProxyQuota{{Model: "step-5-preview", State: "upstream_error"}})
	if only == "" {
		t.Fatal("上游故障也该有提示")
	}
	if strings.Contains(only, "被限") || strings.Contains(only, "付费模型") {
		t.Errorf("上游故障的文案不该套用限流措辞：%s", only)
	}
	if !strings.Contains(only, "5xx") || !strings.Contains(only, "重试") {
		t.Errorf("应说明是 5xx 且重试即可：%s", only)
	}

	// 两类同时存在：两段都要在，且各自说各自的。
	both := quotaWarnText([]ProxyQuota{
		{Model: "deepseek-v4.1-flash", State: "rate_limited", UntilText: "2026-10-15 23:00:00"},
		{Model: "step-5-preview", State: "upstream_error"},
	})
	if !strings.Contains(both, "deepseek-v4.1-flash") || !strings.Contains(both, "step-5-preview") {
		t.Errorf("两类都要提到：%s", both)
	}
	if !strings.Contains(both, "付费模型") {
		t.Errorf("限流那段仍要说清故障转移到付费模型的风险：%s", both)
	}
}

// TestUsageEndpointsFastAndHistory 验证 /api/usage（瘦身后首屏）与 /api/usage/history（按需历史）接口
func TestUsageEndpointsFastAndHistory(t *testing.T) {
	s := newQuotaTestServer(t)

	// 1. /api/usage 应该返回 JSON，且 Costs 默认不查（nil 或空）
	reqFast := httptest.NewRequest(http.MethodGet, "/api/usage", nil)
	recFast := httptest.NewRecorder()
	s.handleUsage(recFast, reqFast)

	if recFast.Code != http.StatusOK {
		t.Fatalf("handleUsage 应返回 200，得到 %d", recFast.Code)
	}
	var fastResp usageResponse
	if err := json.Unmarshal(recFast.Body.Bytes(), &fastResp); err != nil {
		t.Fatalf("handleUsage 响应无法反序列化为 usageResponse: %v", err)
	}
	if len(fastResp.Costs) != 0 {
		t.Errorf("首屏轻量 handleUsage 不应扫描 Costs 会话，得到 %d 条", len(fastResp.Costs))
	}

	// 2. /api/usage/history 应该返回完整的历史分析（即便本地无会话也应正常返回 200 JSON）
	reqHist := httptest.NewRequest(http.MethodGet, "/api/usage/history", nil)
	recHist := httptest.NewRecorder()
	s.handleUsageHistory(recHist, reqHist)

	if recHist.Code != http.StatusOK {
		t.Fatalf("handleUsageHistory 应返回 200，得到 %d", recHist.Code)
	}
	var histResp struct {
		Limits   any `json:"limits"`
		Costs    any `json:"costs"`
		Warnings any `json:"warnings"`
	}
	if err := json.Unmarshal(recHist.Body.Bytes(), &histResp); err != nil {
		t.Fatalf("handleUsageHistory 响应无法解析: %v", err)
	}
}
