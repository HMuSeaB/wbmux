// 删掉"存在数据库里"的会话。
//
// # 为什么与 clean.go 分开
//
// `clean.go` 处理的是**独占文件**（1 文件 = 1 会话），删法是把文件移进回收站。
// 这里处理的是**数据库里的行**，删法是 SQL —— 两件事的风险面完全不同：
//
//	移回收站   可恢复。后悔了右键还原。
//	删库行     不可逆。而且**必须一次删干净**：只删 session 行、留下 message/part，
//	           那些正文就变成永远读不到的孤儿（占着空间、谁也找不到）。
//
// 所以拆成两块，各自的判据与护栏独立测。
//
// # 两种库的结构不同（这决定了删几张表）
//
//	WB (workbuddy.db)   sessions 一行 + projects/<slug>/<id>.jsonl 一个文件
//	                   → 两处都要动，只删一处会得到"点开空白"或"文件成孤儿"
//	ZCode (db.sqlite)   正文全在库里，挂 6 张表
//	                   → session / message / part / todo / session_entry / session_input
//
// # 最要紧的一条：先删文件、再删行
//
// 反过来的话，行删掉了、文件还在 → 用户看不到它，文件变成谁也认不出的垃圾。
// 而先删文件后删行，中间失败最多是"文件没了但行还在"，那是**看得见**的
// （界面上点开会空白），比"悄悄漏下一个文件"好。
package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// rowTarget 是一条"存在数据库里"的待删会话。
type rowTarget struct {
	ID     string `json:"id"`
	Vendor Vendor `json:"vendor"`
	Title  string `json:"title,omitempty"`
	// DBPath 是那个库的路径。
	DBPath string `json:"dbPath"`
	// BodyPath 是 WB 那种"库行 + 独立文件"里的文件；ZCode 为空（正文在库里）。
	BodyPath string `json:"bodyPath,omitempty"`
	// BodySize 是正文文件大小；ZCode 那条按 0 算（空间在库里，删行不会让 db 变小）。
	BodySize int64 `json:"bodySize"`
	// Tables 是要删的表与条件（只给 ZCode 这类多表的结构用）。
	Tables []rowTable `json:"tables,omitempty"`
}

type rowTable struct {
	Name string `json:"name"`
	Col  string `json:"col"`
}

// RowPlan 是"删库行"的计划。
type RowPlan struct {
	Targets []rowTarget `json:"targets"`
	// TotalBody 是配着删掉的正文文件字节合计。
	TotalBody int64 `json:"totalBodyBytes"`
}

// BuildRowPlan 从扫描结果里挑出"候选且在数据库里"的那些。
//
// 与 BuildCleanPlan 互补：那个挑"独占文件"、这个挑"共用数据库"。两者不重叠。
func BuildRowPlan(idx *Index) *RowPlan {
	if idx == nil {
		return &RowPlan{}
	}
	// 每个 path 被几条会话共用 —— 与 BuildCleanPlan 用同一判据，方向相反。
	shared := map[string]int{}
	for _, s := range idx.Sessions {
		shared[s.Source.Path]++
	}

	plan := &RowPlan{}
	for _, s := range idx.Sessions {
		if !s.Candidate || shared[s.Source.Path] <= 1 {
			continue
		}
		// 行级删除只对已实现的三家开放：ZCode（六张表）、WB 双档（正文
		// 文件 + 库行）。Cursor/Qoder 的行结构/加密不同，还没实现——
		// 不进这张计划，免得界面显示"可清理"实际却一个字都没动。
		if s.Vendor != VendorZCode && s.Vendor != VendorWBCN && s.Vendor != VendorWBIntl {
			continue
		}
		t := rowTarget{
			ID:     s.ID,
			Vendor: s.Vendor,
			Title:  s.Title,
			DBPath: s.Source.Path,
		}
		if s.Vendor == VendorZCode {
			// ZCode 的正文全在库里，删这 6 张表。
			//
			// 顺序有意义：先删子表、最后删主表。反过来的话，主表行没了、
			// 子表还在（虽然外键多半不强制），中途失败会留下孤儿正文。
			t.Tables = []rowTable{
				{"part", "session_id"},
				{"message", "session_id"},
				{"todo", "session_id"},
				{"session_entry", "session_id"},
				{"session_input", "session_id"},
				{"session", "id"},
			}
		}
		plan.Targets = append(plan.Targets, t)
	}

	sort.Slice(plan.Targets, func(i, j int) bool {
		return plan.Targets[i].ID < plan.Targets[j].ID
	})
	return plan
}

// FindBodyFiles 找出 WB 这类"库行 + 独立文件"的正文文件。
//
// 为什么不能靠猜路径拼出来：目录名是 cwd 推导的 slug，规则耦合在客户端里
// （见 WorkBuddy 会话那节），拼错了就会"以为没文件、于是不删"，留下孤儿。
// 所以走**遍历 projects/ 找 <id>.jsonl**，认 id 不认目录名。
//
// 找不到不算错：会话可能本来就没正文（云端会话残留就是如此）。
func FindBodyFiles(dataDir, id string) []string {
	root := filepath.Join(dataDir, "projects")
	var out []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if filepath.Base(path) == id+".jsonl" {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// RowCleanResult 是执行结果。
type RowCleanResult struct {
	// RowsDeleted 是删掉的数据库行数（含子表）。
	RowsDeleted int `json:"rowsDeleted"`
	// Sessions 是删掉的会话条数。
	Sessions int `json:"sessions"`
	// BodiesMoved 是移进回收站的正文文件数。
	BodiesMoved int `json:"bodiesMoved"`
	// FreedBytes 是正文文件释放的字节（数据库不会因此变小，不计入）。
	FreedBytes int64    `json:"freedBytes"`
	Failed     []string `json:"failed,omitempty"`
	DryRun     bool     `json:"dryRun"`
}

// RowCleanDeps 把外部动作抽出来，让测试能在临时目录里跑。
type RowCleanDeps struct {
	// DataDirOf 返回某个档位的数据目录（用来找 WB 的正文文件）；测试注入。
	DataDirOf func(vendor Vendor) string
	// DeleteRows 执行 SQL 删除，返回每条语句影响的行数。
	DeleteRows func(dbPath string, tables []string, stmts []SQLStatement) ([]int, error)
	// Trash 把正文文件移进回收站。
	Trash func(path string) error
}

// SQLStatement 是给 DeleteRows 的一条参数化语句。
type SQLStatement struct {
	SQL    string
	Params []any
}

// CleanRows 执行"删库行"。**不可逆。**
//
// 顺序：**先移正文文件、再删库行**（理由见文件头）。
func CleanRows(plan *RowPlan, deps RowCleanDeps, opts CleanOptions) (RowCleanResult, error) {
	res := RowCleanResult{DryRun: opts.DryRun}
	if plan == nil {
		return res, nil
	}
	trash := opts.Trash
	if trash == nil {
		trash = deps.Trash
	}
	if trash == nil && !opts.Delete {
		trash = moveToTrash
	}

	for _, t := range plan.Targets {
		// 1) 先处理正文文件（WB 那种）。ZCode 的 BodyPath 为空，跳过。
		var bodies []string
		if t.BodyPath != "" {
			bodies = []string{t.BodyPath}
		} else if dir := deps.dataDirOf(t.Vendor); dir != "" {
			bodies = FindBodyFiles(dir, t.ID)
		}

		if !opts.DryRun {
			for _, b := range bodies {
				info, err := os.Stat(b)
				if err != nil {
					continue // 已经不在了，不算失败
				}
				if opts.Delete {
					err = os.Remove(b)
				} else {
					err = trash(b)
				}
				if err != nil {
					res.Failed = append(res.Failed,
						fmt.Sprintf("%s 的正文移入回收站失败：%v（**库行未删**）", t.ID, err))
					// 正文没处理掉就**不删库行**：删了会留下谁也认不出的文件。
					goto next
				}
				res.BodiesMoved++
				res.FreedBytes += info.Size()
			}
		} else {
			for _, b := range bodies {
				if info, err := os.Stat(b); err == nil {
					res.FreedBytes += info.Size()
				}
			}
			res.BodiesMoved += len(bodies)
		}

		// 2) 再删库行。
		if len(t.Tables) > 0 {
			stmts := make([]SQLStatement, 0, len(t.Tables))
			tables := make([]string, 0, len(t.Tables))
			for _, tb := range t.Tables {
				stmts = append(stmts, SQLStatement{
					SQL:    fmt.Sprintf("delete from %s where %s = ?", tb.Name, tb.Col),
					Params: []any{t.ID},
				})
				tables = append(tables, tb.Name)
			}
			if !opts.DryRun {
				changed, err := deps.DeleteRows(t.DBPath, tables, stmts)
				if err != nil {
					res.Failed = append(res.Failed,
						fmt.Sprintf("%s 删库行失败：%v", t.ID, err))
					continue
				}
				for _, n := range changed {
					res.RowsDeleted += n
				}
			}
		}
		res.Sessions++

	next:
	}
	return res, nil
}

// dataDirOf 取某个厂商的数据目录。
func (d RowCleanDeps) dataDirOf(vendor Vendor) string {
	if d.DataDirOf == nil {
		return ""
	}
	return d.DataDirOf(vendor)
}
