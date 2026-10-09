package webui

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/recyclenoise"
)

// ---------- 回收站噪声 ----------
//
// # 为什么要有这一页
//
// 用户反馈回收站被灌满、打开就卡死。查下来真凶是工具侧每次调用产生的小
// 文件（PowerShell 的策略探测、工具自己的临时目录），而不是用户的文件。
// 详细分析见 internal/recyclenoise 的包注释。
//
// 原本清这个要靠手工跑一个 Python 脚本。太麻烦了——它是个**会持续复发**的
// 问题（每次干活就涨几项），搁在脚本里等于没有。所以收进界面：
// 一个"看"（现在有多少、都是谁在塞）、一个"清"（一键清掉噪声）。
//
// # 自动清理
//
// 支持在后台按间隔自动清噪声，默认关闭。默认关的理由：
//   - 它要删东西，删的是回收站里的条目。这种事默认开着不合适。
//   - 用户自己的文件可能刚删完还想恢复。
//
// 开启后只清"已识别的噪声"，绝不碰用户文件（见 recyclenoise.Clean 的
// All 选项恒为 false）。

// autoCleanInterval 是自动清理的间隔下限。太频繁没意义——
// 噪声的增长速度和"你干了多少活"相关，不是按秒涨的。
const autoCleanInterval = 30 * time.Minute

// AutoCleanState 是自动清理的当前状态，给界面显示。
type AutoCleanState struct {
	Enabled  bool   `json:"enabled"`
	Interval string `json:"interval"`
	// LastRun 是上一次实际执行的时间（RFC3339），没跑过就是空。
	LastRun string `json:"lastRun,omitempty"`
	// LastRemoved 是上一次清掉的条数。
	LastRemoved int `json:"lastRemoved"`
}

var (
	autoMu      sync.Mutex
	autoEnabled bool
	autoLastRun time.Time
	autoRemoved int
	// autoStop 是当前后台循环的停止信号。nil 表示没在跑。
	autoStop chan struct{}
	// autoRunning 只在 autoMu 下读写。
	autoRunning bool
)

// handleRecycleScan 扫描回收站并汇总。
func (s *Server) handleRecycleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	entries, err := recyclenoise.Scan()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"summary": recyclenoise.Summarize(entries),
		"auto":    autoSnapshot(),
	})
}

// handleRecycleClean 清掉已识别的噪声。
//
// 只接受一个可选参数 all=true 来连用户文件一起清（等于清空回收站）。
// 这需要显式传，不设默认——用户自己删的东西可能还想恢复。
func (s *Server) handleRecycleClean(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		All bool `json:"all"`
		// Through 是"清到哪一档"：noise（默认）/ scratch / keep。
		// 界面上的三个按钮分别传这三个值。
		Through string `json:"through"`
	}
	// 允许空请求体：界面点"清理噪声"时不带参数，这时 ContentLength 是 0。
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体无法解析")
			return
		}
	}

	// 认不出来的 through 一律退到最保守的那档。别让一个拼错的值
	// 顺手清到"连用户文件一起删"上去。
	through := recyclenoise.CategoryNoise
	switch req.Through {
	case string(recyclenoise.CategoryScratch):
		through = recyclenoise.CategoryScratch
	case string(recyclenoise.CategoryKeep):
		through = recyclenoise.CategoryKeep
	}

	entries, err := recyclenoise.Scan()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	res := recyclenoise.Clean(entries, recyclenoise.CleanOptions{All: req.All, Through: through})

	// 清完再扫一次，让界面直接拿到新状态，省一次往返。
	after, err := recyclenoise.Scan()
	if err != nil {
		writeJSON(w, map[string]any{"result": res, "auto": autoSnapshot()})
		return
	}
	s.logf("回收站清理：删了 %d 项，失败 %d 项", res.Removed, res.Failed)
	writeJSON(w, map[string]any{
		"result":  res,
		"summary": recyclenoise.Summarize(after),
		"auto":    autoSnapshot(),
	})
}

// handleRecycleAuto 开关自动清理。
func (s *Server) handleRecycleAuto(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}

	// 落盘。少了这一步，用户勾上开关、关掉界面再打开，发现自己被悄悄关掉了
	// ——而他的原意正是"别再往回收站里塞"。设置自己会消失比没这个设置更糟。
	// 走 updateConfig：这是"读-改-写"，与别的设置写入口并发时会互相覆盖。
	on := req.Enabled
	if err := s.updateConfig(func(c *config.Config) error {
		c.AutoCleanRecycle = &on
		return nil
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}

	autoMu.Lock()
	autoEnabled = req.Enabled
	autoMu.Unlock()

	if req.Enabled {
		startAutoClean()
		s.logf("回收站自动清理已开启，间隔 %s", autoCleanInterval)
	} else {
		s.logf("回收站自动清理已关闭")
	}
	writeJSON(w, autoSnapshot())
}

// logf 往 gui.log 写一行。logFn 没接上时静默跳过——
// 清理是后台动作，不该因为日志没配就报错。
func (s *Server) logf(format string, args ...any) {
	if s.logFn != nil {
		s.logFn(format, args...)
	}
}

// autoSnapshot 取当前自动清理状态。
func autoSnapshot() AutoCleanState {
	autoMu.Lock()
	enabled, removed, last := autoEnabled, autoRemoved, autoLastRun
	autoMu.Unlock()

	st := AutoCleanState{
		Enabled:     enabled,
		Interval:    autoCleanInterval.String(),
		LastRemoved: removed,
	}
	if !last.IsZero() {
		st.LastRun = last.Format(time.RFC3339)
	}
	return st
}

// autoCleanOn 读配置里的"自动清理"开关。默认关闭——它会删东西。
//
// 读不出配置时按**关闭**处理：这个方向是刻意的，环境有问题时不该还在
// 定时删用户的回收站。
func autoCleanOn() bool {
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return cfg.AutoCleanRecycle != nil && *cfg.AutoCleanRecycle
}

// resumeAutoClean 在服务启动时按配置把循环拉起来。
//
// 没有这一步，开关就是个摆设：用户勾上、关掉界面、再打开，发现它自己
// 灭了，得再勾一次——而他勾它的理由正是"别再往回收站里塞"。
func (s *Server) resumeAutoClean() {
	if !autoCleanOn() {
		return
	}
	autoMu.Lock()
	autoEnabled = true
	autoMu.Unlock()
	startAutoClean()
	s.logf("回收站自动清理已按上次设置恢复开启，间隔 %s", autoCleanInterval)
}

// startAutoClean 起后台循环。重复调用是安全的（只起一个）。
//
// 循环会在 stopAutoClean 时退出。原来它靠一个**永远不关闭**的 autoStop
// 通道停不掉，等于每个进程都留一个空转的 goroutine；顺带 autoStopOnce
// 声明了却从没调用过 .Do，是废代码。两样都清掉了。
func startAutoClean() {
	autoMu.Lock()
	defer autoMu.Unlock()
	// 已经在跑就别再起一个。两个循环同时扫盘，而 Clean 是成对删 $I/$R 的，
	// 并发下去可能删到一半留下幽灵条目。
	if autoRunning && autoStop != nil {
		return
	}
	autoStop = make(chan struct{})
	autoRunning = true
	go autoCleanLoop(autoStop)
}

// autoCleanLoop 是后台循环本体。
func autoCleanLoop(stop <-chan struct{}) {
	t := time.NewTicker(autoCleanInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			autoMu.Lock()
			on := autoEnabled
			autoMu.Unlock()
			if !on {
				continue
			}
			runAutoCleanOnce()
		}
	}
}

// stopAutoClean 停掉后台循环。服务关闭时调用——否则它在服务没了之后
// 还继续扫盘清理，那是没人要求的行为。
func stopAutoClean() {
	autoMu.Lock()
	stop, running := autoStop, autoRunning
	autoStop, autoRunning = nil, false
	autoMu.Unlock()
	if running && stop != nil {
		close(stop)
	}
}

// runAutoCleanOnce 执行一次自动清理。只清噪声档，不动草稿与用户文件。
func runAutoCleanOnce() {
	entries, err := recyclenoise.Scan()
	if err != nil {
		return
	}
	// 不显式给 Through：默认就是最保守的噪声档。写出来是为了让读代码的人
	// 一眼看出"这里只碰最安全的一档"，而不是靠默认值兜着。
	res := recyclenoise.Clean(entries, recyclenoise.CleanOptions{
		Through: recyclenoise.CategoryNoise,
	})

	autoMu.Lock()
	autoLastRun = time.Now()
	autoRemoved = res.Removed
	autoMu.Unlock()
}
