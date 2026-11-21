package usage

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 国际端"模拟活跃"每天只该跑一次，官方次日按日发放 +30 积分。
//
// 记录放在这里而不是 cmd/wbmux：定时任务要写、面板要读，两边都得能拿到。
// 文件内容是日期（2006-01-02），比时间戳好查也好认。
const (
	intlSimDayFile = "intl-sim-day"
	intlSimOffFile = "intl-sim-off"
)

var intlSimMu sync.Mutex

func intlSimDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".wbmux")
}

// IntlSimLastDay 返回最近一次成功模拟活跃的日期；没有记录返回空串。
func IntlSimLastDay() string {
	dir := intlSimDir()
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, intlSimDayFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// MarkIntlSimDay 记下今天已经跑过。
func MarkIntlSimDay(day string) {
	dir := intlSimDir()
	if dir == "" || day == "" {
		return
	}
	intlSimMu.Lock()
	defer intlSimMu.Unlock()
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, intlSimDayFile), []byte(day), 0o644)
}

// IntlSimDisabled 表示用户放了开关文件，要求停掉自动模拟。
func IntlSimDisabled() bool {
	dir := intlSimDir()
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, intlSimOffFile))
	return err == nil
}

// IntlSimStatus 是面板要展示的状态。
type IntlSimStatus struct {
	// LastDay 是最近成功日期（空串表示从未成功）。
	LastDay string `json:"lastDay,omitempty"`
	// Today 表示今天已经跑过。
	Today bool `json:"today"`
	// Auto 表示自动任务是否开启（放开关文件即关闭）。
	Auto bool `json:"auto"`
	// NextAt 是下一次自动尝试的时刻（今天已跑过则指向明天 08:00）。
	NextAt string `json:"nextAt,omitempty"`
}

// QueryIntlSimStatus 读出当前状态。只读，不发网络请求。
func QueryIntlSimStatus() IntlSimStatus {
	now := time.Now()
	last := IntlSimLastDay()
	today := last == now.Format("2006-01-02")
	eight := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
	// 今天已经跑过 → 明天 8 点；还没跑且已过 8 点 → 本轮就会跑；
	// 还没跑但没到 8 点 → 今天 8 点。
	next := eight
	switch {
	case today:
		next = eight.Add(24 * time.Hour)
	case now.Before(eight):
		next = eight
	}
	return IntlSimStatus{
		LastDay: last,
		Today:   today,
		Auto:    !IntlSimDisabled(),
		NextAt:  next.Format("2006-01-02 15:04"),
	}
}
