package webui

import (
	"net/http"
	"testing"
	"time"
)

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
	s := &Server{}

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
	s := &Server{}
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
