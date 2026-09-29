package webui

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProbeRealLog 拿**真实 gui.log** 跑一遍恢复逻辑，看会恢复出什么。
//
// 默认跳过（要用 WBMUX_QUOTA_PROBE=1 开）：它读的是开发机上的真实状态，
// 结果随那台机器跑过什么而变，不适合当断言。但它的价值在于**能回答
// "用户重启后到底会看到什么"**——这比任何构造数据都可信。
func TestProbeRealLog(t *testing.T) {
	if os.Getenv("WBMUX_QUOTA_PROBE") == "" {
		t.Skip("需要 WBMUX_QUOTA_PROBE=1")
	}
	p := filepath.Join(os.Getenv("USERPROFILE"), ".wbmux", "gui.log")
	st, err := os.Stat(p)
	if err != nil {
		t.Skipf("没有日志：%v", err)
	}
	t.Logf("日志 %s（%.1f KB）", p, float64(st.Size())/1024)

	seed := parseQuotaLog(p, time.Now().UnixMilli())
	t.Logf("恢复出 %d 条限流、%d 个确认可用", len(seed.Limited), len(seed.OK))
	for _, q := range seed.Limited {
		t.Logf("  限流  %-26s state=%-13s until=%s", q.Model, q.State, q.UntilText)
	}
	for _, m := range seed.OK {
		t.Logf("  可用  %s", m)
	}
}
