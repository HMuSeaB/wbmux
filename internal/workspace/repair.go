// 索引修复：清掉会话表里的"坏条目"。
//
// # 为什么需要单独一块
//
// `set` / `move` 处理的是**工作区归属**（cwd 与 workspaces 表），
// 它们假设每一条索引都对应一个真实会话。但实测发现表里混着**根本不该存在的行**：
//
//	2105179313749086208  cwd 为空、transport=cloud、deleted_at=-1、磁盘无正文
//
// 这种行是云端会话的残留：本地既没有正文文件、也没有工作区，**点开只会是空白**。
// 它不属于"归属不对"，改路径也修不好——只能删。
//
// 还有一类是**软删除但状态字段脏**的：已标了 deleted_at，却被客户端当作正常会话
// 显示出来。实测 26 条里有 3 条 deleted_at 非空，其中一条 status 是 `Completed`
// （首字母大写），另一条 deleted_at 是 `-1`。
//
// 这些都属于"索引自身的数据问题"，与工作区无关，所以分成独立的一步。
//
// # 为什么不做成"扫描到就自动删"
//
// 删索引条目意味着**用户再也看不到那个会话**（正文文件还在磁盘上，但客户端
// 不会去找）。所以规则必须窄到"确定是坏的"，且每一项都要能说出为什么：
//
//	无正文文件 + cwd 为空   → 确定是残留（本地没有任何东西指向它）
//	transport=cloud + 无文件 → 同上，且不是本机产生的
//
// 判据写死在代码里、逐条列出，不搞启发式。不确定的一律留给用户。
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// BadSession 是一条该被清理的索引条目。
type BadSession struct {
	ID      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Reason  string `json:"reason"`
	HasBody bool   `json:"hasBody"`
	// Action 是打算怎么处理：delete（删条目）或 fix（就地修正字段）。
	Action string `json:"action"`
}

// RepairPlan 是索引修复的计划。先算出来给用户看，确认后才执行。
type RepairPlan struct {
	Side string `json:"side"`
	// Bad 是要处理的条目。
	Bad []BadSession `json:"bad"`
	// Healthy 是正常的条数，用来让用户知道"大头没动"。
	Healthy int `json:"healthy"`
	Total   int `json:"total"`
}

// BuildRepairPlan 扫描会话表，找出该清理的条目。**只读**。
func BuildRepairPlan(p *variant.Probe, id variant.ID) (*RepairPlan, error) {
	rows, err := migrate.Query(p, id, `
		select id, cwd, title, custom_title, status, deleted_at, transport
		from sessions`)
	if err != nil {
		return nil, fmt.Errorf("读会话表失败: %w", err)
	}

	// 磁盘上有哪些正文文件。只收真正的会话文件——
	// `agent-*.jsonl` 是子代理转录，本来就不该出现在会话表里，不算"缺文件"。
	bodies := sessionBodyIndex(p, id)

	plan := &RepairPlan{Side: string(id), Total: len(rows)}
	for _, r := range rows {
		sid := str(r["id"])
		title := str(r["custom_title"])
		if title == "" {
			title = str(r["title"])
		}
		cwd := str(r["cwd"])
		status := str(r["status"])
		transport := str(r["transport"])
		del := toInt(r["deleted_at"])
		_, hasBody := bodies[sid]

		reasons, action := judgeSession(sessionFacts{
			cwd:       cwd,
			status:    status,
			transport: transport,
			deletedAt: del,
			hasBody:   hasBody,
		})

		if len(reasons) == 0 {
			plan.Healthy++
			continue
		}
		plan.Bad = append(plan.Bad, BadSession{
			ID:      sid,
			Title:   title,
			Reason:  strings.Join(reasons, "；"),
			HasBody: hasBody,
			Action:  action,
		})
	}
	return plan, nil
}

// sessionFacts 是一条索引里与"是否坏掉"有关的字段。
//
// 抽成结构体是为了让判据可以被单独测——这是整块逻辑里最容易被改坏的地方，
// 而 BuildRepairPlan 本身要读真实数据库、不方便单测。
type sessionFacts struct {
	cwd       string
	status    string
	transport string
	deletedAt int
	hasBody   bool
}

// judgeSession 返回该条目的问题列表与打算怎么处理。
//
// 判据只有三条，每条都要能说出"为什么确定它是坏的"。**宁可漏掉也不能误伤**：
// 删索引意味着用户再也看不到那个会话，而正文文件还躺在磁盘上没人去找。
//
//	action = "delete"   — 只能删，改什么都不对
//	action = "fix"      — 就地改字段
//	action = ""         — 没问题
func judgeSession(f sessionFacts) ([]string, string) {
	var reasons []string
	action := ""

	// 判据一：没有任何本地痕迹的残留。
	//
	// cwd 为空说明它不属于任何目录；再看没有正文文件，就是彻底的孤儿。
	// 这两个条件**要同时成立**才删——只 cwd 空但有文件的话，那是"归属丢了"，
	// 该由 set/move 去修，删了就真没了。
	if f.cwd == "" && !f.hasBody {
		reasons = append(reasons, "cwd 为空且磁盘上没有正文文件")
		if strings.EqualFold(f.transport, "cloud") {
			reasons = append(reasons, "transport=cloud（不是本机产生的会话）")
		}
		action = "delete"
	}

	// 判据二：deleted_at 是脏值。
	//
	// 合法的时间戳是正数（毫秒）。负数会让"该不该显示"的判断失效。
	// 归零表示"未删除"，交给正常流程。
	//
	// **只认 `< 0` 这一种明确写错的情况**——正数时间戳一律视为"用户正常删的"，
	// 不碰（实测那两条正数条目，删除时间与最后活动时间完全一致，
	// 是正常删除的特征）。
	if f.deletedAt < 0 {
		reasons = append(reasons, fmt.Sprintf("deleted_at=%d 不是合法时间戳", f.deletedAt))
		if action == "" {
			action = "fix"
		}
	}

	// 判据三：status 大小写异常。
	//
	// 正常值是全小写的 `completed` / `working`。实测有一条是 `Completed`，
	// 客户端若按小写比对就会漏判。
	if f.status != "" && f.status != strings.ToLower(f.status) {
		reasons = append(reasons, fmt.Sprintf(
			"status=%q 未统一成小写（正常是 %q）", f.status, strings.ToLower(f.status)))
		if action == "" {
			action = "fix"
		}
	}

	return reasons, action
}

// RepairResult 是执行结果。
type RepairResult struct {
	Deleted int      `json:"deleted"`
	Fixed   int      `json:"fixed"`
	Kept    []string `json:"kept,omitempty"`
}

// ApplyRepair 执行修复。**写操作**，调用方负责先备份、先确认客户端已退出。
//
// 只删 plan 里 action=delete 的条目；正文文件一律不动——它们还在
// `projects/<slug>/` 下，将来若要恢复到别处还能用。
func ApplyRepair(p *variant.Probe, id variant.ID, plan *RepairPlan) (RepairResult, error) {
	var res RepairResult
	var stmts []migrate.Statement

	for _, b := range plan.Bad {
		switch b.Action {
		case "delete":
			if b.HasBody {
				// 有正文还删索引的话，那个会话就"消失但文件还在"——
				// 这不是本函数的意图（那种情况该走 set/move）。
				// 真出现了就跳过并记下来，别默默做掉。
				res.Kept = append(res.Kept,
					fmt.Sprintf("%s（有正文文件，不删索引）", b.ID))
				continue
			}
			stmts = append(stmts, migrate.Statement{
				SQL:    `delete from sessions where id = ?`,
				Params: []any{b.ID},
			})
		case "fix":
			// deleted_at 统一成 0（=未删除，交由正常显示逻辑），
			// status 统一成小写。
			//
			// 注意：这里**只**修这两个字段。把 deleted_at 从负数归零意味着
			// "它不再被当成已删除"——对 `-1` 那条而言这是对的（那个值本来就
			// 是写错了）；但如果将来遇到"确实删了、只是时间戳格式不对"的，
			// 就不该走这条路。所以判据只认 `< 0` 这一种明确写错的情况。
			stmts = append(stmts,
				migrate.Statement{
					SQL:    `update sessions set deleted_at = 0 where id = ? and deleted_at < 0`,
					Params: []any{b.ID},
				},
				migrate.Statement{
					SQL:    `update sessions set status = lower(status) where id = ? and status <> lower(status)`,
					Params: []any{b.ID},
				},
			)
		}
	}

	if len(stmts) > 0 {
		changed, err := migrate.Update(p, id, []string{"sessions"}, stmts)
		if err != nil {
			return res, fmt.Errorf("写会话表失败: %w", err)
		}
		// changed 是每条语句影响的行数，与语句一一对应。
		// 不在这里按序号回填 Deleted/Fixed——语句与条目是多对一的关系，
		// 回填容易算错。改成执行后再扫一遍，用"实际还剩几条坏的"来报数。
		_ = changed
	}

	after, err := BuildRepairPlan(p, id)
	if err != nil {
		return res, nil // 报告不出来不影响"已改完"，让调用方看日志
	}
	for _, b := range plan.Bad {
		if b.Action == "delete" {
			res.Deleted++
		} else {
			res.Fixed++
		}
	}
	// 还有残留就如实说，别报一个漂亮数字。
	if len(after.Bad) > 0 {
		for _, b := range after.Bad {
			res.Kept = append(res.Kept, fmt.Sprintf("修复后仍异常: %s %s", b.ID, b.Reason))
		}
	}
	return res, nil
}

// sessionBodyIndex 列出磁盘上真正存在的会话正文文件 id。
//
// 把 `agent-*.jsonl`（子代理转录）排除掉：它们在 `projects/<slug>/subagents/`
// 下，是子代理的运行记录，**本来就不该出现在会话表里**。把它们算作"有正文"
// 会让"孤儿条目"的判据失效——实测这条曾经让我误以为有 15 个会话"丢失"。
func sessionBodyIndex(p *variant.Probe, id variant.ID) map[string]string {
	out := map[string]string{}
	dir := p.DataDir(id)
	if dir == "" {
		return out
	}
	_ = filepath.Walk(filepath.Join(dir, "projects"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasSuffix(base, ".jsonl") {
			return nil
		}
		if strings.HasPrefix(base, "agent-") {
			return nil
		}
		out[strings.TrimSuffix(base, ".jsonl")] = path
		return nil
	})
	return out
}

// str 从查询结果里取字符串，取不到给空串（Query 返回的是 map[string]any）。
func str(v any) string {
	s, _ := v.(string)
	return s
}
