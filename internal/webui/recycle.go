package webui

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

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
	autoMu       sync.Mutex
	autoEnabled  bool
	autoLastRun  time.Time
	autoRemoved  int
	autoStopOnce sync.Once
	autoStop     = make(chan struct{})
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
	defer autoMu.Unlock()
	st := AutoCleanState{
		Enabled:     autoEnabled,
		Interval:    autoCleanInterval.String(),
		LastRemoved: autoRemoved,
	}
	if !autoLastRun.IsZero() {
		st.LastRun = autoLastRun.Format(time.RFC3339)
	}
	return st
}

// startAutoClean 起后台循环。重复调用是安全的（只起一个）。
func startAutoClean() {
	autoStopOnce = sync.Once{}
	autoMu.Lock()
	defer autoMu.Unlock()
	// 已经在跑就不重复起
	if autoRunning {
		return
	}
	autoRunning = true
	go func() {
		t := time.NewTicker(autoCleanInterval)
		defer t.Stop()
		for {
			select {
			case <-autoStop:
				autoMu.Lock()
				autoRunning = false
				autoMu.Unlock()
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
	}()
}

var autoRunning bool

// runAutoCleanOnce 执行一次自动清理。只清噪声，不动用户文件。
func runAutoCleanOnce() {
	entries, err := recyclenoise.Scan()
	if err != nil {
		return
	}
	res := recyclenoise.Clean(entries, recyclenoise.CleanOptions{All: false})

	autoMu.Lock()
	autoLastRun = time.Now()
	autoRemoved = res.Removed
	autoMu.Unlock()
}
