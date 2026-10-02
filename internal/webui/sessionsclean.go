// 会话清理的界面接口。
//
// # 为什么单独一个文件、两个入口
//
// 清理是**写操作**（会把文件移进回收站），而 /api/sessions 是只读的。
// 沿用本项目一贯的分工：读与写不共用一个入口，这样"谁在什么条件下动了
// 文件"是可追溯的（界面上的按钮、日志里的行，都能对上）。
//
//	GET  /api/sessions/clean   预演。只算计划，不动任何文件。
//	POST /api/sessions/clean   执行。移入回收站（可恢复）。
//
// # 两道确认
//
// 执行时要求请求体里带 dryRun=false **且** confirm=true。两个字段都要，
// 是因为这个动作会动用户的历史会话——一个布尔值太容易被"顺手传上去"，
// 两个字段强制调用方明确表达意图（界面上的按钮也是两步：先预演，再执行）。
package webui

import (
	"encoding/json"
	"net/http"

	"github.com/HMuSeaB/wbmux/internal/sessions"
)

// cleanView 是回给界面的清理计划。
type cleanView struct {
	Side       string                  `json:"side"`
	Targets    []sessions.CleanTarget  `json:"targets"`
	Refused    []sessions.CleanRefusal `json:"refused,omitempty"`
	TotalBytes int64                   `json:"totalBytes"`
	// Executed 为真表示这次真的动了文件；false 是预演。
	Executed bool                  `json:"executed"`
	Result   *sessions.CleanResult `json:"result,omitempty"`
}

// handleSessionsClean 预演或执行清理。
func (s *Server) handleSessionsClean(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
		return
	}

	// 参数与扫描一致：候选规则由它们决定，所以清理也必须用同一套参数算，
	// 否则会出现"界面上看到的是候选、清理时按另一套规则算"的错位。
	q := r.URL.Query()
	days := intParam(q.Get("days"), 30)
	keep := intParam(q.Get("keep"), 1)
	side := checkinTarget(q.Get("host")) // 复用"认不出就退回国内侧"的既有行为

	scanMu.Lock()
	idx, err := sessions.Scan(s.probe(), sessions.Options{
		KeepDays: days, KeepPerProject: keep,
	})
	scanMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	plan := sessions.BuildCleanPlan(idx)

	// 默认预演。只有 POST 且显式 confirm 才真执行。
	execute := false
	if r.Method == http.MethodPost {
		var req struct {
			DryRun  bool `json:"dryRun"`
			Confirm bool `json:"confirm"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
				writeErr(w, http.StatusBadRequest, "请求体无法解析")
				return
			}
		}
		// 两个字段都要：dryRun 必须**明确为 false**，且 confirm 为 true。
		execute = req.Confirm && !req.DryRun
	}

	view := cleanView{
		Side:       string(side),
		Targets:    plan.Targets,
		Refused:    plan.Refused,
		TotalBytes: plan.TotalBytes,
	}

	if execute {
		res, err := sessions.CleanTargets(plan, sessions.CleanOptions{})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		view.Executed = true
		view.Result = &res

		// 改动了用户的历史数据，落一行日志。
		s.logf("会话清理：%d 个文件移入回收站，释放 %.0f MB（失败 %d，跳过 %d）",
			res.Moved, float64(res.Freed)/1048576, len(res.Failed), len(res.Skipped))

		// 清理改了磁盘上的会话文件，缓存的索引已经不准了 → 丢掉它。
		// 不丢的话下次打开会话中心会看到刚清掉的那些还在列表里。
		sessions.InvalidateCache()
	} else {
		// 预演也过一次 CleanTargets（DryRun），把"哪些会被跳过"提前暴露出来，
		// 免得用户点了执行才发现有文件已经不在。
		if res, err := sessions.CleanTargets(plan, sessions.CleanOptions{DryRun: true}); err == nil {
			view.Result = &res
		}
	}

	writeJSON(w, view)
}
