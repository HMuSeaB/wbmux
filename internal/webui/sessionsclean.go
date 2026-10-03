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
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/sessions"
	"github.com/HMuSeaB/wbmux/internal/variant"
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

// rowView 是"删库行"的计划。
type rowView struct {
	Side     string                   `json:"side"`
	Targets  []rowTargetView          `json:"targets"`
	Total    int                      `json:"total"`
	Executed bool                     `json:"executed"`
	Result   *sessions.RowCleanResult `json:"result,omitempty"`
}

// rowTargetView 是其中一条（对外只需要这几个字段）。
type rowTargetView struct {
	ID       string `json:"id"`
	Vendor   string `json:"vendor"`
	Title    string `json:"title,omitempty"`
	DBPath   string `json:"dbPath"`
	Tables   int    `json:"tables"`
	BodyPath string `json:"bodyPath,omitempty"`
	BodySize int64  `json:"bodySize"`
}

// handleSessionsCleanRows 删掉"存在数据库里"的候选会话。
//
// **不可逆**（不像移回收站能还原），所以要的确认比那边更强：
//   - 必须 POST；
//   - 必须 confirm=true 且 dryRun=false；
//   - 必须显式给 table，且只能是对应的那张表名——
//     这是为了拦住"手滑对着 WB 的库发了 ZCode 的表清单"这类错配。
func (s *Server) handleSessionsCleanRows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
		return
	}

	q := r.URL.Query()
	days := intParam(q.Get("days"), 30)
	keep := intParam(q.Get("keep"), 1)
	side := checkinTarget(q.Get("host"))

	scanMu.Lock()
	idx, err := sessions.Scan(s.probe(), sessions.Options{KeepDays: days, KeepPerProject: keep})
	scanMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	plan := sessions.BuildRowPlan(idx)

	view := rowView{Side: string(side), Total: len(plan.Targets)}
	for _, t := range plan.Targets {
		v := rowTargetView{
			ID: t.ID, Vendor: string(t.Vendor), Title: t.Title,
			DBPath: t.DBPath, Tables: len(t.Tables),
		}
		// WB 那种"库行 + 独立文件"：把正文文件也列出来，用户要看到"配着删的是什么"。
		if t.Vendor == sessions.VendorWBCN || t.Vendor == sessions.VendorWBIntl {
			if bodies := sessions.FindBodyFiles(s.dataDirOf(t.Vendor), t.ID); len(bodies) > 0 {
				v.BodyPath = bodies[0]
				if fi, err := os.Stat(bodies[0]); err == nil {
					v.BodySize = fi.Size()
				}
			}
		}
		view.Targets = append(view.Targets, v)
	}

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
		execute = req.Confirm && !req.DryRun
	}

	if !execute {
		writeJSON(w, view)
		return
	}

	// 执行前先备份数据库。删行不可逆，没有备份就没有退路。
	for _, backupPath := range distinctDBPaths(plan) {
		if err := s.backupDBFile(backupPath); err != nil {
			writeErr(w, http.StatusInternalServerError,
				"备份数据库失败，已中止（没有备份就不删）："+err.Error())
			return
		}
	}

	res, err := sessions.CleanRows(plan, sessions.RowCleanDeps{
		DataDirOf: s.dataDirOf,
		DeleteRows: func(dbPath string, tables []string, stmts []sessions.SQLStatement) ([]int, error) {
			ms := make([]migrate.Statement, 0, len(stmts))
			for _, st := range stmts {
				ms = append(ms, migrate.Statement{SQL: st.SQL, Params: st.Params})
			}
			return migrate.DeleteRows(s.probe(), variant.CN, dbPath, tables, ms)
		},
	}, sessions.CleanOptions{})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	view.Executed = true
	view.Result = &res
	s.logf("会话清理（库行）：删 %d 条会话 / %d 行，正文移入回收站 %d 个（%.0f MB）",
		res.Sessions, res.RowsDeleted, res.BodiesMoved, float64(res.FreedBytes)/1048576)
	sessions.InvalidateCache()
	writeJSON(w, view)
}

// distinctDBPaths 列出计划里涉及的数据库，用于备份。
func distinctDBPaths(plan *sessions.RowPlan) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range plan.Targets {
		if t.DBPath != "" && !seen[t.DBPath] {
			seen[t.DBPath] = true
			out = append(out, t.DBPath)
		}
	}
	return out
}

// handleSessionsExport 把一个会话导出成可浏览的 HTML，返回文件路径。
//
// 与清理那两个入口分开：这是**只读**的（只读会话文件、只往导出目录写），
// 所以不需要"客户端在跑就拒绝"那道闸，也不需要预演/确认两步。
// 写的目标是用户自己指定的目录，不是别人的数据目录。
func (s *Server) handleSessionsExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		ID  string   `json:"id"`
		IDs []string `json:"ids"`
		Dir string   `json:"dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		// 没给目录就用默认位置：放在用户主目录下一个好找的地方，
		// 而不是当前工作目录——界面是从托盘/快捷方式起的，工作目录不可预期。
		// 目录名不带厂商：导出现在有 Codex（HTML）与 Cursor（Markdown handoff）两家。
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, "WorkBuddy AI", "会话导出")
	}

	rep, err := sessions.ExportSessions(s.probe(), sessions.ExportOptions{
		Dir:     dir,
		OnlyID:  strings.TrimSpace(req.ID),
		OnlyIDs: req.IDs,
		// 界面上的筛选参数要带上，否则"界面上看到这个、导出成另一个"。
		Days: intParam(r.URL.Query().Get("days"), 30),
		Keep: intParam(r.URL.Query().Get("keep"), 1),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 什么都没导出时**必须给出一句话说明**。
	//
	// 否则界面收到 exported=0 且 failed/skipped 全空，只能显示"没导出任何东西"
	// ——用户不知道是"这个会话不存在"、"源不支持"还是"功能坏了"。
	if rep.Exported == 0 && len(rep.Failed) == 0 && len(rep.Skipped) == 0 {
		who := strings.TrimSpace(req.ID)
		if who == "" && len(req.IDs) > 0 {
			who = fmt.Sprintf("这 %d 个会话", len(req.IDs))
		}
		if who == "" {
			rep.Failed = append(rep.Failed, "没有找到可导出的会话（本机的 Codex 会话目录可能是空的）")
		} else {
			rep.Failed = append(rep.Failed,
				fmt.Sprintf("没找到 %s —— 它可能不在当前扫描范围里（试试在界面上点「重新扫描」），或者本机没有 Codex 会话", who))
		}
	}

	writeJSON(w, map[string]any{
		"dir":      rep.Dir,
		"index":    rep.Index,
		"exported": rep.Exported,
		"skipped":  rep.Skipped,
		"failed":   rep.Failed,
		"elapsed":  rep.Elapsed.Milliseconds(),
	})
}

// dataDirOf 返回某一家档位的数据目录。
//
// 只对 WB 两档有意义（它的正文是"库行 + projects/<slug>/<id>.jsonl"，
// 要找那个文件得先知道数据目录在哪）。ZCode 的正文在库里，返回空即可。
func (s *Server) dataDirOf(v sessions.Vendor) string {
	switch v {
	case sessions.VendorWBCN:
		return s.probe().DataDir(variant.CN)
	case sessions.VendorWBIntl:
		return s.probe().DataDir(variant.Intl)
	}
	return ""
}

// backupDBFile 备份任意一个数据库文件（不限于 workbuddy.db）。
//
// 与 workspace 的那份分开：那个写死了 workbuddy.db，而这里还要处理
// ZCode 的 db.sqlite。备份是删行前**唯一**的退路，所以它失败时必须
// 让整次操作中止——调用方就是这么用的。
func (s *Server) backupDBFile(dbPath string) error {
	raw, err := os.ReadFile(dbPath)
	if err != nil {
		return fmt.Errorf("读取 %s 失败: %w", dbPath, err)
	}
	dir := filepath.Join(filepath.Dir(dbPath), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := strings.TrimSuffix(filepath.Base(dbPath), filepath.Ext(dbPath))
	dst := filepath.Join(dir, fmt.Sprintf("%s-%s.db", base, time.Now().Format("20060102-150405")))
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return fmt.Errorf("写备份失败: %w", err)
	}
	return nil
}
