package embedstats

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want Freshness
	}{
		{0, FreshnessFresh},
		{29*time.Minute + 59*time.Second, FreshnessFresh},
		{30 * time.Minute, FreshnessAged},
		{119 * time.Minute, FreshnessAged},
		{120 * time.Minute, FreshnessIdle},
		{24 * time.Hour, FreshnessIdle},
	}
	for _, c := range cases {
		if got := Classify(c.age); got != c.want {
			t.Errorf("Classify(%v) = %q, want %q", c.age, got, c.want)
		}
	}
}

func TestLevel(t *testing.T) {
	cases := []struct {
		tokens int
		f      Freshness
		want   string
	}{
		{1000, FreshnessFresh, ""},
		{255999, FreshnessFresh, ""},
		{256000, FreshnessFresh, "warn"},
		{511999, FreshnessFresh, "warn"},
		{512000, FreshnessFresh, "bad"},
		{900000, FreshnessFresh, "bad"},
		// 过期档强制不着色：旧值再大也不报警。
		{900000, FreshnessAged, ""},
		{900000, FreshnessIdle, ""},
	}
	for _, c := range cases {
		if got := Level(c.tokens, c.f); got != c.want {
			t.Errorf("Level(%d, %q) = %q, want %q", c.tokens, c.f, got, c.want)
		}
	}
}

func TestQuotaLevel(t *testing.T) {
	cases := []struct {
		pct  float64
		want string
	}{
		{100, ""},
		{62.5, ""},
		{20, ""}, // 边界：20% 本身不黄
		{19.9, "warn"},
		{10, "warn"}, // 边界：10% 本身不红
		{9.9, "bad"},
		{0, "bad"}, // 0% 本身是红的；调用方按"用尽档不参与"先过滤
	}
	for _, c := range cases {
		if got := QuotaLevel(c.pct); got != c.want {
			t.Errorf("QuotaLevel(%v) = %q, want %q", c.pct, got, c.want)
		}
	}
}

func TestQuotaPace(t *testing.T) {
	cases := []struct {
		remaining, timeLeft float64
		want                string
	}{
		{62, 80, "ahead"}, // headroom −18：烧得比时间快
		{70, 80, "ahead"}, // headroom −10：边界也算 ahead
		{71, 80, "on"},    // headroom −9：还不预警
		{62, 55, "on"},    // headroom +7：还不够省
		{62, 54, "under"}, // headroom +8：边界算 under
		{90, 70, "under"}, // headroom +20：省着用
		{50, 50, "on"},
	}
	for _, c := range cases {
		if got := QuotaPace(c.remaining, c.timeLeft); got != c.want {
			t.Errorf("QuotaPace(%v, %v) = %q, want %q", c.remaining, c.timeLeft, got, c.want)
		}
	}
}

func TestAgeMinutes(t *testing.T) {
	if got := AgeMinutes(42*time.Minute + 30*time.Second); got != 42 {
		t.Errorf("AgeMinutes = %d, want 42", got)
	}
}

func TestLastHold(t *testing.T) {
	now := time.Now()
	h := NewLastHold[int]()

	// 两边都没有 → ok=false
	if _, _, ok := h.Serve(nil, time.Time{}); ok {
		t.Error("空兜底 + nil candidate 应返回 ok=false")
	}

	// 新记录存入并给出
	v1 := 61000
	if got, _, ok := h.Serve(&v1, now); !ok || got == nil || *got != 61000 {
		t.Errorf("第一次 Serve 应给出 61000，得到 %+v/ok=%v", got, ok)
	}

	// nil candidate → 给兜底（同一份）
	if got, at, ok := h.Serve(nil, time.Time{}); !ok || got == nil || *got != 61000 || !at.Equal(now) {
		t.Errorf("nil candidate 应给兜底 61000，得到 %+v/%v/ok=%v", got, at, ok)
	}

	// 更旧的 candidate 不替换兜底
	vOld := 48000
	if got, _, ok := h.Serve(&vOld, now.Add(-10*time.Minute)); !ok || got == nil || *got != 61000 {
		t.Errorf("旧 candidate 不应替换兜底，得到 %+v/ok=%v", got, ok)
	}

	// 更新的 candidate 替换兜底
	v2 := 62000
	if got, _, ok := h.Serve(&v2, now.Add(time.Minute)); !ok || got == nil || *got != 62000 {
		t.Errorf("新 candidate 应替换兜底，得到 %+v/ok=%v", got, ok)
	}

	// 兜底的时间戳原样保留：调用方按它重算年龄——120 分钟后应判 idle
	hOld := NewLastHold[int]()
	v3 := 61000
	hOld.Serve(&v3, now.Add(-121*time.Minute))
	if _, at, ok := hOld.Serve(nil, time.Time{}); !ok || Classify(time.Since(at)) != FreshnessIdle {
		t.Errorf("121 分钟前的兜底应判 idle，得到 at=%v/ok=%v", at, ok)
	}
	// 40 分钟前 → aged，自然老化不断档
	hAged := NewLastHold[int]()
	hAged.Serve(&v3, now.Add(-40*time.Minute))
	if _, at, ok := hAged.Serve(nil, time.Time{}); !ok || Classify(time.Since(at)) != FreshnessAged {
		t.Errorf("40 分钟前的兜底应判 aged，得到 at=%v/ok=%v", at, ok)
	}
}

func TestLastHoldNilReceiver(t *testing.T) {
	// holder 为 nil（测试直接拼的结构体）时等价于无兜底
	var h *LastHold[int]
	v := 100
	if got, _, ok := h.Serve(&v, time.Now()); !ok || got == nil || *got != 100 {
		t.Errorf("nil holder 应直接返回 candidate，得到 %+v/ok=%v", got, ok)
	}
	if _, _, ok := h.Serve(nil, time.Time{}); ok {
		t.Error("nil holder + nil candidate 应返回 ok=false")
	}
}
