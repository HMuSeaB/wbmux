// 会话导出的编排：扫描 → 挑出要导的 → 逐个渲染 HTML → 写索引页。
//
// # 为什么与 codexhtml.go 分开
//
// `codexhtml.go` 只管"把一条已读出的会话渲染成 HTML"，是纯函数——
// 给同样的 Turn 一定得到同样的页面，不碰磁盘、不扫描、不知道源在哪。
// 这一层负责编排：去哪找文件、挑哪些、写到哪。分开之后渲染能单测，
// 编排也能在临时目录里跑。
//
// # 目前只支持 Codex
//
// 四种源的正文形态完全不同（见各 adapter 的注释）：Codex 是 rollout jsonl、
// Claude 是 projects/<slug>/<uuid>.jsonl、ZCode 在 SQLite 的 part 表、
// WB 是库行 + projects/<slug>/<id>.jsonl。要全支持得先抽一层"读会话正文"
// 的接口、四家各实现一次。这一版先把已经能跑通的 Codex 接到位——
// 其余源在结果里**明确报"暂不支持"，而不是静默跳过**，免得用户以为导了。
package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ExportOptions 控制导出哪些、导到哪。
type ExportOptions struct {
	// Dir 是输出目录。
	Dir string
	// Vendor 非空时只导这一家。
	Vendor Vendor
	// OnlyID 非空时只导这一个会话。
	OnlyID string
	// OnlyIDs 非空时只导这几个（界面上的"导出整个项目"用）。
	// 与 OnlyID 同时给时，两个条件都要满足——实际上调用方只会给一个。
	OnlyIDs []string
	// Days / Keep 与扫描一致——导出范围要和界面里看到的一致，
	// 否则会出现"界面显示 63 条、导出只有 40 条"这种对不上账。
	Days int
	Keep int
	// Refresh 强制重扫。
	Refresh bool
}

// ExportReport 是导出结果，给 CLI 打印用。
type ExportReport struct {
	Dir      string
	Index    string
	Exported int
	// Skipped 是被跳过的源与原因（比如"claude：暂不支持导出"）。
	Skipped []string
	// Failed 是处理失败的单个会话。
	Failed  []string
	Elapsed time.Duration
}

// ExportSessions 执行导出。**只读**：只读会话文件、只写输出目录。
func ExportSessions(probe *variant.Probe, opts ExportOptions) (*ExportReport, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, fmt.Errorf("导出目录不能为空")
	}
	t0 := time.Now()

	// **导出一律走重扫**，不读缓存。
	//
	// 实测过的坑：会话文件在磁盘上被增删之后，索引缓存还留着旧快照。
	// 直接拿缓存导出会"少导"——用户刚还原回来的 40 个会话一个都不在里面，
	// 而命令却报成功。导出的语义是"把我现在磁盘上有的都给我"，
	// 拿旧快照答不了这个问题。
	//
	// 代价是一次几秒的重扫（实测 63 个会话约 8 秒），换"导出的就是现状"，
	// 值得。opts.Refresh 保留只是为了表达"就算有缓存也别用"，行为一致。
	idx, err := Scan(probe, Options{
		Refresh:        true,
		KeepDays:       opts.Days,
		KeepPerProject: opts.Keep,
	})
	if err != nil {
		return nil, err
	}

	rep := &ExportReport{Dir: opts.Dir}

	// 按来源分组：只导支持的那些，其余如实说明。
	byVendor := map[Vendor][]Session{}
	for _, s := range idx.Sessions {
		if opts.Vendor != "" && s.Vendor != opts.Vendor {
			continue
		}
		if opts.OnlyID != "" && s.ID != opts.OnlyID {
			continue
		}
		if len(opts.OnlyIDs) > 0 && !containsStr(opts.OnlyIDs, s.ID) {
			continue
		}
		byVendor[s.Vendor] = append(byVendor[s.Vendor], s)
	}

	// 用户点名了来源、但那家没会话：说清楚，别让他对着空目录猜。
	if opts.Vendor != "" && len(byVendor[opts.Vendor]) == 0 {
		return nil, fmt.Errorf("没有找到 %s 的会话（可能本机没装，或 --id 没对上）", opts.Vendor.Label())
	}

	var files []string
	var cursorList []Session
	for v, list := range byVendor {
		if v == VendorCursor {
			// Cursor 走单独的分支：正文在共用库里（不在独立文件），
			// 产物是 **Markdown handoff**（问答 + 文件清单 + 待办），
			// 与 Codex 的 HTML 归档互补——见 cursorhandoff.go 的定位说明。
			cursorList = append(cursorList, list...)
			continue
		}
		if !canExport(v) {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf(
				"%s：暂不支持导出（它的正文不在独立文件里，需要单独的读取实现），共 %d 条",
				v.Label(), len(list)))
			continue
		}
		for _, s := range list {
			p := s.Source.Path
			if p == "" {
				rep.Failed = append(rep.Failed, s.ID+"：没有正文文件")
				continue
			}
			if _, err := os.Stat(p); err != nil {
				rep.Failed = append(rep.Failed, fmt.Sprintf("%s：正文文件不在了（%s）", s.ID, p))
				continue
			}
			files = append(files, p)
		}
	}

	// Cursor 分支：逐条提炼 Markdown handoff。
	// 放在 Codex 文件流之前建目录：cursor-only 导出也要有落点。
	if len(cursorList) > 0 {
		if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
			return nil, err
		}
	}
	for _, s := range cursorList {
		md, err := RenderCursorHandoff(probe, s.Source.Path, s.ID, s.Title)
		if err != nil {
			rep.Failed = append(rep.Failed, "cursor "+shortID(s.ID)+"："+err.Error())
			continue
		}
		name := fmt.Sprintf("cursor_%s_%s.md", shortID(s.ID), exportSlug(s.Title))
		if err := os.WriteFile(filepath.Join(opts.Dir, name), md, 0o644); err != nil {
			rep.Failed = append(rep.Failed, s.ID+"："+err.Error())
			continue
		}
		rep.Exported++
	}

	if len(files) == 0 {
		// 一个都导不出来时也要建目录并写索引页，否则用户面对的是
		// "命令成功了但什么都没有"。
		if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
			return nil, err
		}
		rep.Elapsed = time.Since(t0)
		return rep, nil
	}

	// 去重：同一个文件可能被两条索引指向（极少见，但别赌）。
	seen := map[string]bool{}
	uniq := files[:0]
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			uniq = append(uniq, f)
		}
	}
	sort.Strings(uniq)

	idxPath, err := ExportCodexDir(uniq, opts.Dir, "Codex 会话归档", CodexHTMLOptions{
		WithTools:     true,
		WithReasoning: true,
		// 单图上限 8 MB：超过就不内嵌。极端情况下一张图几 MB、
		// 一个页面几十张会把浏览器直接卡死，留个说明比卡死强。
		MaxImageBytes: 8 << 20,
	})
	if err != nil {
		return nil, err
	}
	rep.Index = idxPath
	rep.Exported = len(uniq)
	rep.Elapsed = time.Since(t0)
	return rep, nil
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// CanRenderInline 说明某一家现在能不能在界面里直接看。
//
// 与 canExport 是**不同的判断**：能不能"看"取决于有没有读取正文的实现，
// 能不能"导出"还取决于那个实现是否适合落盘。现在两者恰好相同（都只有
// Codex），但以后 Claude 只读实现写完时，很可能先能看、再考虑导出。
// 所以两个函数分开，而不是让一个去调另一个。
func CanRenderInline(v Vendor) bool {
	return v == VendorCodex || v == VendorCursor
}

// canExport 说明某一家现在能不能导出。
//
// 单独一个函数而不是散在 if 里：以后给 Claude 写好读取实现时，
// 只要在这里把它挪到"能"，编排逻辑一行不用动。
func canExport(v Vendor) bool {
	return v == VendorCodex
}
