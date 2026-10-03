// 会话清理：把"候选"接到实际动作上。
//
// # 现状与边界
//
// `sessions.Scan` 只**标记**候选（超期且不在项目保底名次内），从不执行。
// 那块的红线是"只读"，包注释里写得很明白。所以真正动手的代码放在这里，
// 与索引扫描分开——**读与写不能混在一条路径上**，这是本项目一贯的分工
// （usage 只读、checkin 单独、workspace 的 repair 单独）。
//
// # 最要紧的一条：按"位置"分两种，删法完全不同
//
// 会话的 source.path 有两种形态：
//
//	独占文件     1 个文件 = 1 条会话（Codex 的 rollout-*.jsonl）
//	共用数据库   N 条会话共用一个 .db（WB 的 workbuddy.db、ZCode 的 db.sqlite）
//
// **共用数据库的绝不能按文件删。** 实测本机 `~/.workbuddy/workbuddy.db` 里
// 有 24 条会话、其中只有 3 条是候选——按文件删会连另外 21 条一起没。
// 所以默认只处理"独占文件"那批；数据库里的行要走 SQL 删（本轮不做）。
//
// # 为什么默认移回收站而不是直接删
//
// 一次动的量不小（实测 40 条 / 1187 MB）。移到回收站是可恢复的，
// 而直接删没有退路。用户事后后悔的代价远大于回收站占的那点空间。
package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CleanPlan 是一次清理的计划。**只读算出来的**，先给用户看。
type CleanPlan struct {
	// Targets 是可以按文件安全删除的候选。
	Targets []CleanTarget `json:"targets"`
	// TotalBytes 是 Targets 的字节合计。
	TotalBytes int64 `json:"totalBytes"`
	// Refused 是被拒绝处理的条目（共用数据库里的行）。
	//
	// 单独列出来而不是悄悄跳过：用户会问"为什么候选有 44 条、只清了 40 条"，
	// 这份列表就是答案。
	Refused []CleanRefusal `json:"refused,omitempty"`
}

// CleanTarget 是一个待清理的文件。
type CleanTarget struct {
	Path    string `json:"path"`
	Vendor  Vendor `json:"vendor"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
	Size    int64  `json:"size"`
	Updated int64  `json:"updatedMs,omitempty"`
}

// CleanRefusal 是一条被拒绝处理的候选。
type CleanRefusal struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	// SharedBy 是共用这个位置的会话数（含自己）。
	SharedBy int `json:"sharedBy"`
}

// CleanResult 是执行结果。
type CleanResult struct {
	Moved   int      `json:"moved"`
	Freed   int64    `json:"freedBytes"`
	Failed  []string `json:"failed,omitempty"`
	DryRun  bool     `json:"dryRun"`
	Skipped []string `json:"skipped,omitempty"`
}

// BuildCleanPlan 从一次扫描结果里算出清理计划。**只读。**
//
// 判据只有两条，都要硬性成立：
//
//  1. 该条被标为候选（规则见 sessions.Scan，不在这里重算）；
//  2. 它的位置是"独占文件"——同一次扫描里没有别的会话指向同一个 path。
//     这一条是安全阀：共用的库哪怕只有一条候选也不能按文件删。
func BuildCleanPlan(idx *Index) *CleanPlan {
	if idx == nil {
		return &CleanPlan{}
	}

	// 先数每个 path 被多少条会话指向。
	shared := map[string]int{}
	for _, s := range idx.Sessions {
		shared[s.Source.Path]++
	}

	plan := &CleanPlan{}
	for _, s := range idx.Sessions {
		if !s.Candidate {
			continue
		}
		p := s.Source.Path
		// 第一道闸按**来源种类**：sqlite 型（WB/ZCode/Cursor/Qoder）的会话
		// 存在共用库里，无论几条都不能按文件删——尤其"这个库里只有 1 条
		// 候选"时，shared 计数是 1，纯计数的安全阀会放行，把整个库
		// （含全部其他数据）送进回收站。Cursor 的 state.vscdb 有 706MB。
		if s.Source.Kind != "file" {
			plan.Refused = append(plan.Refused, CleanRefusal{
				ID:       s.ID,
				Path:     p,
				Reason:   "会话存在数据库里，删它要删库里的行，不能按文件删",
				SharedBy: shared[p],
			})
			continue
		}
		if shared[p] > 1 {
			plan.Refused = append(plan.Refused, CleanRefusal{
				ID:       s.ID,
				Path:     p,
				Reason:   "会话存在数据库里，删它要删库里的行，不能按文件删",
				SharedBy: shared[p],
			})
			continue
		}
		if p == "" {
			plan.Refused = append(plan.Refused, CleanRefusal{
				ID: s.ID, Reason: "没有位置信息", SharedBy: 1,
			})
			continue
		}
		plan.Targets = append(plan.Targets, CleanTarget{
			Path:    p,
			Vendor:  s.Vendor,
			Title:   s.Title,
			Project: s.Project,
			Size:    s.SizeBytes,
			Updated: s.UpdatedMs,
		})
		plan.TotalBytes += s.SizeBytes
	}

	// 路径去重（同一个文件理论上只会出现一次，但别赌）。
	seen := map[string]bool{}
	uniq := plan.Targets[:0]
	for _, t := range plan.Targets {
		if seen[t.Path] {
			continue
		}
		seen[t.Path] = true
		uniq = append(uniq, t)
	}
	plan.Targets = uniq

	sort.Slice(plan.Targets, func(i, j int) bool {
		return plan.Targets[i].Updated > plan.Targets[j].Updated
	})
	return plan
}

// RefineCopy 把一份计划收窄到某几个厂商。空切片表示不过滤（全部）。
//
// 为什么需要：用户可能只想清某一家的（"cc 的先别动"），
// 全量执行会把不想动的也带上。
func (p *CleanPlan) RefineVendors(only []Vendor) *CleanPlan {
	if len(only) == 0 {
		return p
	}
	allow := map[Vendor]bool{}
	for _, v := range only {
		allow[v] = true
	}
	out := &CleanPlan{}
	for _, t := range p.Targets {
		if allow[t.Vendor] {
			out.Targets = append(out.Targets, t)
			out.TotalBytes += t.Size
		}
	}
	for _, r := range p.Refused {
		out.Refused = append(out.Refused, r)
	}
	return out
}

// CleanOptions 控制执行行为。
type CleanOptions struct {
	// DryRun 为真时只检查、不动任何文件。
	DryRun bool
	// Delete 为真时直接删；否则移到回收站（默认）。
	Delete bool
	// Trash 是移入回收站的实现，nil 时用平台默认（Windows 走 shell32）。
	// 抽成函数是为了让测试能在临时目录里跑，不碰真实回收站。
	Trash func(path string) error
}

// CleanTargets 执行清理。
//
// 无论 DryRun 与否，**每个路径都要重新验一遍**：计划是上一次扫描算出来的，
// 而扫描结果可能已经过期（文件被挪走、被别的工具改过）。所以这里不信任
// 传进来的 Size 字段，而是当场 stat。
func CleanTargets(plan *CleanPlan, opts CleanOptions) (CleanResult, error) {
	res := CleanResult{DryRun: opts.DryRun}
	if plan == nil {
		return res, nil
	}
	trash := opts.Trash
	if trash == nil && !opts.Delete {
		trash = moveToTrash
	}

	for _, t := range plan.Targets {
		info, err := os.Stat(t.Path)
		if err != nil {
			// 文件已经不在了：不算失败。可能是上次清过、或用户自己删了。
			res.Skipped = append(res.Skipped, t.Path+"（已不存在）")
			continue
		}
		if info.IsDir() {
			// 会话永远不该是目录。真出现了就跳过并记下来，别递归删下去。
			res.Skipped = append(res.Skipped,
				fmt.Sprintf("%s（是目录，跳过——本工具只删单个会话文件）", t.Path))
			continue
		}
		if !looksLikeSessionFile(t.Path) {
			res.Skipped = append(res.Skipped,
				fmt.Sprintf("%s（文件名不像会话文件，跳过）", t.Path))
			continue
		}

		if opts.DryRun {
			continue
		}

		size := info.Size()
		if opts.Delete {
			err = os.Remove(t.Path)
		} else {
			err = trash(t.Path)
		}
		if err != nil {
			res.Failed = append(res.Failed, t.Path+"："+err.Error())
			continue
		}
		res.Moved++
		res.Freed += size
	}
	return res, nil
}

// looksLikeSessionFile 是删之前的最后一道外形检查。
//
// 它不判断"这是不是候选"（那是扫描的职责），只判断"它看起来是个会话文件"。
// 目的是挡住调用方传错东西进来——比如把某个数据库路径误当成会话文件。
// 这是个便宜的兜底，真正的安全阀在 BuildCleanPlan 的"独占文件"判据。
func looksLikeSessionFile(p string) bool {
	base := strings.ToLower(filepath.Base(p))
	if !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	// 影子转录（子代理）不是会话本身，别删。
	if strings.HasPrefix(base, "agent-") {
		return false
	}
	return true
}
