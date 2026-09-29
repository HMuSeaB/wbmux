package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/workspace"
)

// ---------- 会话工作区 ----------
//
// # 为什么要有这一页
//
// 客户端的"工作区"（项目目录）散在三处：workspaces 表的清单、每条会话的 cwd、
// 以及 projects/ 下由 cwd 推导出的目录名。用户想让工作区换个地方，随手改一处
// 就会得到"清单里有、点进去没有会话"或者"会话还在、归错项目"。
//
// 这一页把三处一起处理，并且**先把要改什么、动多少行列清楚**，再让用户按一下。
//
// # 只读与写操作分得很开
//
//	GET  /api/workspace        只读：现状 + 体检。进页面就打一次。
//	POST /api/workspace/move   写：迁移到新目录。**必须点两下**（先看计划再确认）。
//
// 写操作有三道闸，缺一不可：
//   1. 客户端必须在退出状态（否则改动会被它的内存缓存回写覆盖）；
//   2. 执行前自动备份数据库；
//   3. 迁移是"先复制文件、后改库"，任何一步失败都不会丢数据，源目录也不删。
//
// 三道闸都不是保守，是这类操作出错的代价太大：写坏了就是"历史会话全消失"。

// workspaceRunning 判断客户端在不在跑。
//
// 这是工作区这一页最要紧的状态：不在跑才能改库。复用搬运那边的进程检测，
// 不另写一套"怎么算在跑"的判据（两套判据迟早不一致）。
func workspaceRunning(id variant.ID) bool {
	return migrate.IsRunning(id)
}

// handleWorkspace 返回工作区现状与体检结果。只读。
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	id := workspaceTarget(r.URL.Query().Get("host"))
	ov, err := workspace.Inspect(s.probe(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// 客户端在不在跑，是这页最要紧的一个状态：不在跑才能动手。
	writeJSON(w, map[string]any{
		"overview": ov,
		"running":  workspaceRunning(id),
		"suggest":  suggestTarget(ov),
	})
}

// suggestTarget 给界面一个"搬去哪"的默认值。
//
// 不猜具体目录，而是给出方向：如果当前工作区在系统盘的用户目录下，建议搬到
// 非系统盘。理由是用户的真实诉求通常是"别把工作区堆在 C 盘用户目录里"，
// 而不是"搬到我脑子里已经想好的某个路径"——留一个可编辑的输入框，比给一个
// 猜错的路径更省事。
func suggestTarget(ov *workspace.Overview) string {
	// 找出会话最多的那个非系统盘工作区，作为摆放位置的参照。
	for _, sp := range ov.Spaces {
		if !sp.Exists || sp.Sessions == 0 {
			continue
		}
		drv := strings.ToUpper(sp.Path[:1])
		if drv != "C" {
			parent := sp.Path
			if i := strings.LastIndexAny(parent, `\/`); i > 2 {
				parent = parent[:i]
			}
			return parent
		}
	}
	return ""
}

// handleWorkspacePlan 只算不做的试运行：把计划摆给用户看。
func (s *Server) handleWorkspacePlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Host string `json:"host"`
		From string `json:"from"`
		To   string `json:"to"`
		Kind string `json:"kind"`
	}
	if err := decodeSmallJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := workspaceTarget(req.Host)

	pl, err := buildPlan(s.probe(), id, req.Kind, req.From, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"plan":     pl,
		"summary":  pl.Summarize(),
		"ready":    pl.Ready(),
		"running":  workspaceRunning(id),
		"blockers": pl.Blockers,
	})
}

// handleWorkspaceApply 真正执行。
func (s *Server) handleWorkspaceApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Host    string `json:"host"`
		From    string `json:"from"`
		To      string `json:"to"`
		Kind    string `json:"kind"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeSmallJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Confirm {
		// 前端必须显式带上确认标记。多这一道是为了挡住"误触一次就搬了家"
		// ——页面上的按钮是两步的，接口这层也要认这个约定。
		writeErr(w, http.StatusBadRequest, "缺少确认标记（这个操作会改工作区，需要显式确认）")
		return
	}
	id := workspaceTarget(req.Host)
	p := s.probe()

	pl, err := buildPlan(p, id, req.Kind, req.From, req.To)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !pl.Ready() {
		writeErr(w, http.StatusConflict, strings.Join(pl.Blockers, "；"))
		return
	}

	// 执行前后各记一行日志：这类动作事后必须能回答"什么时候谁点的"。
	s.logf("工作区改写开始：%s → %s（%s）", pl.From, pl.To, pl.Kind)
	var execErr error
	if req.Kind == "move" {
		execErr = pl.CommitMove(p, id)
	} else {
		execErr = pl.Commit(p, id)
	}
	if execErr != nil {
		s.logf("工作区改写失败：%v", execErr)
		writeErr(w, http.StatusInternalServerError, execErr.Error())
		return
	}
	s.logf("工作区改写完成：%s → %s", pl.From, pl.To)

	// 改完立刻回读一次现状，界面直接拿到新状态。
	ov, err := workspace.Inspect(p, id)
	if err != nil {
		writeJSON(w, map[string]any{"ok": true, "note": "已完成，但回读现状失败：" + err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "overview": ov})
}

// buildPlan 按 kind 选一个计划构造器。
func buildPlan(p *variant.Probe, id variant.ID, kind, from, to string) (*workspace.Plan, error) {
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
		return nil, errBadInput("源路径与目标路径都要填")
	}
	if kind == "move" {
		return workspace.BuildMovePlan(p, id, from, to)
	}
	return workspace.BuildSetPlan(p, id, from, to)
}

// workspaceTarget 档位选择。与签到同样的理由：默认国内侧。
func workspaceTarget(host string) variant.ID {
	if strings.EqualFold(strings.TrimSpace(host), string(variant.Intl)) {
		return variant.Intl
	}
	return variant.CN
}

func decodeSmallJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.ContentLength <= 0 {
		return errBadInput("缺少请求体")
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(v); err != nil {
		return errBadInput("请求体无法解析")
	}
	return nil
}

type badInput string

func (e badInput) Error() string { return string(e) }

func errBadInput(msg string) error { return badInput(msg) }
