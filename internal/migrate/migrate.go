// Package migrate 把一侧数据目录里的会话、技能与记忆搬到另一侧。
//
// # 为什么需要它
//
// 两套后端的账号体系不互通（国内走 PIPL、国际走 GDPR 口径），数据目录也不同
// （~/.workbuddy 与 ~/.workbuddy-ai），官方没有任何同步途径。于是在国际版里
// 干过的活，切回国内版就看不见了。
//
// # 为什么不能只复制文件
//
// 会话列表读的是数据目录下 workbuddy.db 的 sessions 表，不是 projects/ 目录。
// 客户端确实有一个「从 projects/ 重建索引」的函数
// （workbuddy-server 的 database-service.ts · rebuildSessionIndexFromHistory），
// 但挖开调用点可以看到它**只在数据库损坏自愈时**才跑——正常启动不会执行。
// 所以只把 jsonl 复制过去，列表里什么都不会出现。必须同时写入 sessions 行，
// 这正是本包不能退化成一次文件复制的原因。
//
// # 为什么写数据库不用第三方驱动
//
// 本项目 Go 侧零第三方依赖，而 Go 标准库没有 SQLite 驱动。解法是借用
// **客户端自己捆的** better-sqlite3：Electron 主程序带 ELECTRON_RUN_AS_NODE=1
// 就是一个普通 Node 运行时，再把它 node_modules 里的 better_sqlite3.node
// 交给 better-sqlite3 的 JS 封装（走 nativeBinding 选项，绕开打包时留在
// app.asar 里、拿不到的 bindings 模块）。
//
// 这样既不引入依赖，也天然与客户端同一次构建、同 ABI。
//
// # 安全约束
//
//   - 只新增，绝不覆盖。目标已有同名文件就跳过；已有同 id 会话行就跳过。
//   - 改目标数据库前先整份备份（连 -wal / -shm），备份目录随报告返回。
//   - 只给「jsonl 确实落在目标目录」的会话写索引行，避免造出打不开的列表项。
//   - 目标客户端在运行时给出警告但不阻断：WAL 模式下多进程写同一个库是安全的，
//     实际影响只是它需要重启才会刷新列表。
package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Kind 是要搬运的东西的种类。
type Kind string

const (
	// KindSessions 是会话历史，唯一需要同时写索引的一种。
	KindSessions Kind = "sessions"
	// KindSkills 是技能目录，纯文件复制。
	KindSkills Kind = "skills"
	// KindMemory 是账号级的用户记忆文件。
	KindMemory Kind = "memory"
)

// Options 指定搬运方向。
type Options struct {
	Source variant.ID
	Target variant.ID
	// Probe 可注入，便于测试。
	Probe *variant.Probe
}

// Candidate 是界面上的一条可搬运项。ID 对会话是会话 id，对技能是技能名。
type Candidate struct {
	ID       string `json:"id"`
	Kind     Kind   `json:"kind"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Size     int64  `json:"size"`
	// State 取 new / exists / blocked，决定界面默认勾不勾、能不能勾。
	State string `json:"state"`
	Note  string `json:"note"`
}

// SurveyResult 是「先看看有什么」的结果，全程只读。
type SurveyResult struct {
	Source     variant.ID `json:"source"`
	Target     variant.ID `json:"target"`
	SourceName string     `json:"sourceName"`
	TargetName string     `json:"targetName"`

	SourceDataDir string `json:"sourceDataDir"`
	TargetDataDir string `json:"targetDataDir"`

	Sessions []Candidate `json:"sessions"`
	Content  []Candidate `json:"content"`
	Assets   []Candidate `json:"assets"`
	Skills   []Candidate `json:"skills"`
	Memory   []Candidate `json:"memory"`

	// TargetRunning 为目标端客户端是否在运行；界面据此提示需要重启。
	TargetRunning bool     `json:"targetRunning"`
	Warnings      []string `json:"warnings"`
}

// Selection 是「决定搬哪些」。
//
// 两个字段都为空表示"全部"——界面的"全选"按钮走这条；正常勾选时
// 前端总是送显式列表，因此不存在"取消了所有项"被误判成全选的歧义。
type Selection struct {
	Kinds      []Kind   `json:"kinds"`
	SessionIDs []string `json:"sessionIds"`
}

// Report 是搬运结果。
type Report struct {
	CopiedFiles  int `json:"copiedFiles"`
	SkippedFiles int `json:"skippedFiles"`
	// SkippedItems 是因为"目标端已有"而整项没动的数量，
	// 与 SkippedFiles 不同：后者是项内个别文件撞名，前者是整个条目被跳过。
	SkippedItems int `json:"skippedItems"`
	RowsInserted int `json:"rowsInserted"`
	RowsSkipped  int `json:"rowsSkipped"`
	// PathRewrites 是被改写掉的附件路径引用数。
	//
	// 会显示出来是因为它意味着"搬过去的会话文件与来源不再逐字节一致"，
	// 用户有权知道这件事发生了什么、发生了多少次。
	PathRewrites int      `json:"pathRewrites"`
	BackupDir    string   `json:"backupDir"`
	Details      []string `json:"details"`
	Warnings     []string `json:"warnings"`
}

// workItem 是一件真正要干的活。
//
// 界面看到的是它的摘要（Candidate），执行用的是它本身，两者由同一个函数
// buildWork 生成——这样"预览"与"实际执行"不可能不一致。
type workItem struct {
	kind  Kind
	id    string
	title string
	note  string
	size  int64

	files []fileCopy
	trees []treeCopy

	// fill 非空表示要把来源端的记忆正文填进目标端的空模板里。
	// 它是本包唯一允许改写已有文件的操作，边界见 fillEmptyMemory。
	fill *memoryFill

	// row 非空表示这条会话还需要写一行索引。
	row *Row
	// skip 表示整项无需处理。
	skip    bool
	skipWhy string
}

type fileCopy struct {
	src, dst string

	// replace 非空时，写入目标前按顺序做字符串替换。
	//
	// 目前只用于会话文件：把里面引用的附件绝对路径从来源端数据目录改到
	// 目标端。不改写的话，图片会一直依赖来源端目录存在，迁移就不算完成。
	// 见 content.go 的 pathRewrites。
	replace [][2]string
}

type treeCopy struct{ srcDir, dstDir string }

// memoryFill 描述一次"填空模板"操作。
type memoryFill struct {
	src    string
	dst    string
	srcUID string
	dstUID string
}

// errExists 表示目标已存在，调用方按"跳过"而非"失败"处理。
//
// 之所以做成错误而不是先检查再写：先检查再写存在竞态窗口，
// 而真正要守住的约束是"绝不覆盖"，交给 O_EXCL 由内核保证最稳妥。
var errExists = errors.New("目标已存在")

// ---------- 只读侦察 ----------

// Survey 列出可以搬运的东西，不做任何改动。
func Survey(opts Options) (SurveyResult, error) {
	probe := opts.Probe
	if probe == nil {
		probe = variant.DefaultProbe()
	}
	src, err := variant.Get(opts.Source)
	if err != nil {
		return SurveyResult{}, err
	}
	dst, err := variant.Get(opts.Target)
	if err != nil {
		return SurveyResult{}, err
	}
	if src.ID == dst.ID {
		return SurveyResult{}, errors.New("来源与目标不能是同一套后端")
	}

	out := SurveyResult{
		Source:        src.ID,
		Target:        dst.ID,
		SourceName:    src.DisplayName,
		TargetName:    dst.DisplayName,
		SourceDataDir: probe.DataDir(src.ID),
		TargetDataDir: probe.DataDir(dst.ID),
		TargetRunning: IsRunning(dst.ID),
	}

	items, warns, err := buildWork(opts, Selection{})
	if err != nil {
		return SurveyResult{}, err
	}
	out.Warnings = warns

	for _, it := range items {
		c := Candidate{
			ID:       it.id,
			Kind:     it.kind,
			Title:    it.title,
			Subtitle: it.note,
			Size:     it.size,
			State:    "new",
		}
		if it.skip {
			c.State = "exists"
			c.Note = it.skipWhy
		}
		switch it.kind {
		case KindSessions:
			out.Sessions = append(out.Sessions, c)
		case KindContent:
			out.Content = append(out.Content, c)
		case KindAssets:
			out.Assets = append(out.Assets, c)
		case KindSkills:
			out.Skills = append(out.Skills, c)
		case KindMemory:
			out.Memory = append(out.Memory, c)
		}
	}
	return out, nil
}

// ---------- 生成待办 ----------

// buildWork 把"要搬什么"翻译成一件件具体的活。全程只读。
func buildWork(opts Options, sel Selection) ([]workItem, []string, error) {
	probe := opts.Probe
	if probe == nil {
		probe = variant.DefaultProbe()
	}
	if _, err := variant.Get(opts.Source); err != nil {
		return nil, nil, err
	}
	if _, err := variant.Get(opts.Target); err != nil {
		return nil, nil, err
	}
	if opts.Source == opts.Target {
		return nil, nil, errors.New("来源与目标不能是同一套后端")
	}

	srcDir := probe.DataDir(opts.Source)
	dstDir := probe.DataDir(opts.Target)

	var warns []string
	var items []workItem

	want := map[Kind]bool{}
	for _, k := range sel.Kinds {
		want[k] = true
	}
	all := len(sel.Kinds) == 0
	incl := func(k Kind) bool { return all || want[k] }

	// projects 目录只扫一次：会话与附件两项都要用它的结果，
	// 各扫一遍是纯粹重复的 IO（会话多时是几百 MB）。
	var srcSessions map[string]sessionFiles
	if incl(KindSessions) || incl(KindAssets) {
		got, err := scanSessions(srcDir)
		if err != nil {
			return nil, nil, fmt.Errorf("扫描来源端会话失败: %w", err)
		}
		srcSessions = got
	}

	if incl(KindSessions) {
		// 会话的"全选"看的是 SessionIDs 空不空，不是 Kinds 空不空。
		// 早先把两者混用一个布尔量，导致"只要了会话这一类但没列具体 id"
		// 会被当成"一条都不要"——正是这个 bug 被 TestBuildWorkSessions 抓住。
		got, w, err := planSessions(probe, opts, srcSessions, srcDir, dstDir, sel, len(sel.SessionIDs) == 0)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, got...)
		warns = append(warns, w...)
	}
	if incl(KindContent) {
		got, w := planContent(srcDir, dstDir)
		items = append(items, got...)
		warns = append(warns, w...)
	}
	if incl(KindAssets) {
		got, w := planAssets(srcDir, dstDir, srcSessions)
		items = append(items, got...)
		warns = append(warns, w...)
	}
	if incl(KindSkills) {
		got, w := planSkills(srcDir, dstDir)
		items = append(items, got...)
		warns = append(warns, w...)
	}
	if incl(KindMemory) {
		got, w := planMemory(srcDir, dstDir)
		items = append(items, got...)
		warns = append(warns, w...)
	}
	return items, warns, nil
}

// planSessions 规划会话搬运。src 由调用方扫好传入，避免重复扫描。
func planSessions(probe *variant.Probe, opts Options, src map[string]sessionFiles, srcDir, dstDir string, sel Selection, all bool) ([]workItem, []string, error) {
	var warns []string

	dst, err := scanSessions(dstDir)
	if err != nil {
		return nil, nil, fmt.Errorf("扫描目标端会话失败: %w", err)
	}

	// 目标端已有索引行也一并收齐：判断"这条是不是已经在对方列表里了"
	// 得看数据库，光看文件会得出相反结论。
	existing := map[string]bool{}
	rt, rtErr := detectRuntime(probe, opts.Target)
	if rtErr == nil {
		if rows, err := readRows(rt, dbPathFor(dstDir)); err == nil {
			for _, r := range rows {
				existing[r.ID] = true
			}
		} else {
			warns = append(warns, "读取目标端会话索引失败，可能重复搬运："+err.Error())
		}
	} else {
		// 读不出索引不影响"能搬什么"的判断，只影响"目标端是否已有"的准确性。
		warns = append(warns, "无法读取会话索引（"+rtErr.Error()+"），会话列表将不会显示搬运来的历史")
	}

	// 标题以数据库里的为准——那才是用户在列表里看到的名字；
	// 拿不到就退回文件头部的 ai-title。
	rowsByID := map[string]Row{}
	if rtErr == nil {
		if rows, err := readRows(rt, dbPathFor(srcDir)); err == nil {
			for _, r := range rows {
				rowsByID[r.ID] = r
			}
		}
	}

	ids := make([]string, 0, len(src))
	for id := range src {
		if !all && !contains(sel.SessionIDs, id) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]workItem, 0, len(ids))
	for _, id := range ids {
		sf := src[id]

		slug := CompressCwd(sf.Head.Cwd)
		title := firstNonEmpty(sf.Head.Title, id)
		if r, ok := rowsByID[id]; ok {
			if r.Title != nil && *r.Title != "" {
				title = *r.Title
			}
		}

		it := workItem{
			kind:  KindSessions,
			id:    id,
			title: title,
			size:  sf.Size,
			note:  sf.Head.Cwd,
		}

		// 没有 cwd 就定不了该放进哪个工作区目录。宁可跳过并说明，
		// 也不要凭猜建一个目录——那会生出一个用户从没见过的"空间"。
		if slug == "" {
			it.skip = true
			it.skipWhy = "会话文件里读不到工作目录，无法确定归属"
			out = append(out, it)
			continue
		}

		targetDir := filepath.Join(dstDir, "projects", slug)
		jsonl := filepath.Join(targetDir, id+".jsonl")
		// 会话正文里带的是来源端的附件绝对路径，落盘时要改写到目标端，
		// 否则图片会一直依赖来源端数据目录存在。
		it.files = append(it.files, fileCopy{
			src:     sf.JSONL,
			dst:     jsonl,
			replace: pathRewrites(srcDir, dstDir),
		})
		if sf.Meta != "" {
			it.files = append(it.files, fileCopy{src: sf.Meta, dst: filepath.Join(targetDir, id+".meta.json")})
		}
		if sf.Rollback != "" {
			it.files = append(it.files, fileCopy{src: sf.Rollback, dst: filepath.Join(targetDir, id+".file-rollback.ndjson")})
		}

		if existing[id] {
			it.skip = true
			it.skipWhy = "目标端列表中已有这条会话"
			out = append(out, it)
			continue
		}

		if _, fileThere := dst[id]; fileThere {
			// 目标端有文件却没有索引行——这正是"文件早先被拷过去了、列表却
			// 什么都不显示"的典型症状。这种情况只需要补一行索引就能修好，
			// 所以不跳过，但要让用户知道这一步做了什么。
			it.note = sf.Head.Cwd + "　（目标端已有文件，只需补索引）"
		}

		// 索引行的 user_id 置空串，不是 NULL：该列 NOT NULL，
		// 而客户端的 getSessions 用 `user_id IS NULL OR user_id = ''`
		// 让这类记录对当前登录用户可见。填目标端 uid 是错的——那是别人的会话。
		it.row = &Row{
			ID:             id,
			Cwd:            firstNonEmpty(sf.Head.Cwd, ""),
			UserID:         "",
			Title:          strPtr(title),
			Status:         strPtr("completed"),
			CreatedAt:      time.Now().UnixMilli(),
			UpdatedAt:      time.Now().UnixMilli(),
			LastActivityAt: int64Ptr(time.Now().UnixMilli()),
			IsPlayground:   0,
		}
		if r, ok := rowsByID[id]; ok {
			// 有源端的精确索引就照抄，时间戳与状态都保持原样，
			// 免得搬运后列表里所有会话都挤在同一秒。
			it.row.Cwd = r.Cwd
			it.row.Title = r.Title
			it.row.CustomTitle = r.CustomTitle
			it.row.Status = r.Status
			it.row.CreatedAt = r.CreatedAt
			it.row.UpdatedAt = r.UpdatedAt
			it.row.LastActivityAt = r.LastActivityAt
			it.row.IsPlayground = r.IsPlayground
			it.row.SourceMode = r.SourceMode
			it.row.Mode = r.Mode
			it.row.Model = r.Model
			it.row.PermissionMode = r.PermissionMode
			it.row.UseSandboxCLI = r.UseSandboxCLI
			it.row.AddonSelection = r.AddonSelection
			it.row.ContextWindow = r.ContextWindow
			it.row.ThoughtLevel = r.ThoughtLevel
		}
		out = append(out, it)
	}
	return out, warns, nil
}

// planSkills 规划技能搬运。
func planSkills(srcDir, dstDir string) ([]workItem, []string) {
	var items []workItem
	var warns []string

	entries, err := os.ReadDir(filepath.Join(srcDir, "skills"))
	if err != nil {
		if !os.IsNotExist(err) {
			warns = append(warns, "读取来源端技能目录失败："+err.Error())
		}
		return items, warns
	}

	for _, e := range entries {
		// 只认目录。该目录下还躺着 _bm_skillid_migration.json 之类的
		// 管理用文件，照搬过去没有意义。
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		src := filepath.Join(srcDir, "skills", name)
		dst := filepath.Join(dstDir, "skills", name)

		it := workItem{
			kind:  KindSkills,
			id:    name,
			title: name,
			size:  dirSize(src),
			note:  "技能",
			trees: []treeCopy{{srcDir: src, dstDir: dst}},
		}
		if dirExists(dst) {
			it.skip = true
			it.skipWhy = "目标端已有同名技能"
		}
		items = append(items, it)
	}
	return items, warns
}

// planMemory 规划用户记忆搬运。
//
// 记忆文件名是 <uid>_memory.md，所以跨账号搬运必须换成目标端自己的 uid，
// 否则客户端根本不会读它。
func planMemory(srcDir, dstDir string) ([]workItem, []string) {
	var items []workItem
	var warns []string

	srcUID, dstUID := memoryUID(srcDir), memoryUID(dstDir)
	if srcUID == "" || dstUID == "" {
		// 两端任一侧还没有记忆文件，就没有可搬的东西。
		return items, warns
	}

	srcPath := filepath.Join(srcDir, "memory", srcUID+"_memory.md")
	dstPath := filepath.Join(dstDir, "memory", dstUID+"_memory.md")

	srcBlock, _ := memoryBlock(srcPath)
	dstBlock, _ := memoryBlock(dstPath)

	it := workItem{
		kind:  KindMemory,
		id:    "memory",
		title: "用户记忆",
		size:  fileSize(srcPath),
		note:  srcUID + "_memory.md",
	}
	switch {
	case strings.TrimSpace(srcBlock) == "":
		// 来源端记忆是空的，搬过去等于把对方的空模板覆盖一遍，纯属添乱。
		it.skip = true
		it.skipWhy = "来源端记忆为空，无需搬运"
	case strings.TrimSpace(dstBlock) != "":
		// 两端都有内容时不做任何合并：记忆是模型自己维护的文本，
		// 机械拼接会产出它读不懂的东西，比不搬更糟。
		it.skip = true
		it.skipWhy = "目标端记忆已有内容，不覆盖"
	default:
		it.fill = &memoryFill{src: srcPath, dst: dstPath, srcUID: srcUID, dstUID: dstUID}
	}
	items = append(items, it)
	return items, warns
}

// ---------- 执行 ----------

// Apply 执行搬运。
func Apply(opts Options, sel Selection) (Report, error) {
	probe := opts.Probe
	if probe == nil {
		probe = variant.DefaultProbe()
	}
	dstDir := probe.DataDir(opts.Target)

	items, warns, err := buildWork(opts, sel)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Warnings: warns}

	// 要写索引就必须先有可用的 runtime，且必须先备份。
	// 这两件事都在任何改动之前完成——做不到就整批中止，
	// 绝不能出现"文件复制完了但索引没写进去"的半成品状态。
	needDB := false
	for _, it := range items {
		if it.kind == KindSessions && it.row != nil && !it.skip {
			needDB = true
			break
		}
	}

	var rt runtimePaths
	if needDB {
		rt, err = detectRuntime(probe, opts.Target)
		if err != nil {
			return Report{}, err
		}
		dbPath := dbPathFor(dstDir)
		if !fileExists(dbPath) {
			// 目标端从没登录过就没有数据库，也就没有列表可写。
			return Report{}, fmt.Errorf("目标端还没有会话索引（%s 不存在），请先启动一次该档位的客户端并登录", dbPath)
		}
		backup, err := backupDB(dbPath)
		if err != nil {
			return Report{}, fmt.Errorf("备份目标端数据库失败，已中止: %w", err)
		}
		rep.BackupDir = backup
	}

	// 第一阶段：复制文件。
	var rows []Row
	for _, it := range items {
		if it.skip {
			rep.SkippedItems++
			continue
		}
		for _, f := range it.files {
			if len(f.replace) == 0 {
				switch err := copyFile(f.src, f.dst); {
				case errors.Is(err, errExists):
					rep.SkippedFiles++
				case err != nil:
					rep.Warnings = append(rep.Warnings, fmt.Sprintf("复制 %s 失败：%v", filepath.Base(f.src), err))
				default:
					rep.CopiedFiles++
				}
				continue
			}
			// 需要改写内容的（目前只有会话文件），走另一条路径。
			changed, err := copyFileWithRewrites(f.src, f.dst, f.replace)
			switch {
			case errors.Is(err, errExists):
				rep.SkippedFiles++
			case err != nil:
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("复制 %s 失败：%v", filepath.Base(f.src), err))
			default:
				rep.CopiedFiles++
				rep.PathRewrites += changed
			}
		}
		for _, t := range it.trees {
			copied, skipped, err := copyTree(t.srcDir, t.dstDir)
			if err != nil {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("复制技能 %s 失败：%v", filepath.Base(t.srcDir), err))
			}
			rep.CopiedFiles += copied
			rep.SkippedFiles += skipped
		}
		if it.fill != nil {
			if err := fillEmptyMemory(*it.fill); err != nil {
				rep.Warnings = append(rep.Warnings, "搬运记忆失败："+err.Error())
			} else {
				rep.CopiedFiles++
			}
		}
		if it.kind == KindSessions {
			rep.Details = append(rep.Details, "复制会话文件："+it.title)
		}
	}

	// 第二阶段：写索引。
	//
	// 只在 jsonl 确实落在目标目录时才写——否则会造出一条点开就报错的列表项，
	// 那比不显示更让人困惑。
	for _, it := range items {
		if it.skip || it.row == nil || len(it.files) == 0 {
			continue
		}
		if !fileExists(it.files[0].dst) {
			rep.Warnings = append(rep.Warnings, "会话文件未能落到目标目录，已跳过其索引："+it.title)
			continue
		}
		rows = append(rows, *it.row)
	}
	if len(rows) > 0 {
		inserted, skipped, err := writeRows(rt, dbPathFor(dstDir), rows)
		if err != nil {
			return rep, fmt.Errorf("写会话索引失败（文件已复制，索引未写入）: %w", err)
		}
		rep.RowsInserted = len(inserted)
		rep.RowsSkipped = len(skipped)
	}

	if IsRunning(opts.Target) {
		rep.Warnings = append(rep.Warnings, "目标端客户端当前正在运行，会话列表要重启后才会出现搬运来的历史。")
	}
	return rep, nil
}

// ---------- 文件操作 ----------

// copyFile 复制单个文件。目标已存在时返回 errExists，绝不覆盖。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	// O_EXCL：目标已存在就报错。这是"绝不覆盖"最可靠的实现方式，
	// 比"先 stat 再写"少了竞态窗口。
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return errExists
		}
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		// 只删自己刚建的半个文件，不碰别的。
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// copyTree 递归复制目录，已存在的文件逐个跳过。返回复制与跳过的文件数。
func copyTree(srcDir, dstDir string) (copied, skipped int, err error) {
	err = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dstDir, rel)

		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		switch e := copyFile(p, dst); {
		case errors.Is(e, errExists):
			skipped++
		case e != nil:
			return e
		default:
			copied++
		}
		return nil
	})
	return copied, skipped, err
}

// fillEmptyMemory 把来源端的记忆正文填进目标端那个空模板里。
//
// 这是本包唯一一处会改写已有文件的地方，边界刻意收得很紧：
//
//   - 只在**目标端正文为空**时才会被调用，因此没有任何内容会被丢掉；
//   - 做法是把来源文件里的 uid 换成目标端 uid 后整份写出，所以目标文件的
//     结构就是客户端自己写的那一套，不会造出它读不懂的格式。
//
// 之所以不能套用"目标已存在就跳过"的通用规则：记忆文件名固定是
// <uid>_memory.md，目标端只要登录过一次就必然有这个文件，一律跳过
// 等于这个功能永远不生效。
func fillEmptyMemory(f memoryFill) error {
	raw, err := os.ReadFile(f.src)
	if err != nil {
		return err
	}

	// uid 在文件里出现两处（RAW_JSON 的 uid 字段，以及 <uid>_memory.md
	// 这个文件名本身），两处都要换。只换文件名不够：客户端按 uid 字段校验。
	text := string(raw)
	if f.srcUID != "" && f.dstUID != "" {
		text = strings.ReplaceAll(text, f.srcUID, f.dstUID)
	}

	if err := os.MkdirAll(filepath.Dir(f.dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f.dst, []byte(text), 0o644)
}

// backupDB 在改动数据库前把它整份备份，返回备份目录。
//
// 必须连 -wal / -shm 一起拷：WAL 模式下最近的写入还留在 -wal 里，
// 只拷 .db 会得到一个缺数据的备份，等真要回滚时才发现少了东西。
func backupDB(dbPath string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	// 备份放在 wbmux 自己的目录下，而不是客户端数据目录里：
	// 后者可能被客户端自己的清理逻辑扫到。
	dest := filepath.Join(dir, "backup", time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}

	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := dbPath + suffix
		if !fileExists(src) {
			continue
		}
		if err := copyFile(src, filepath.Join(dest, filepath.Base(dbPath)+suffix)); err != nil {
			return "", err
		}
	}
	return dest, nil
}

// ---------- 小工具 ----------

func dbPathFor(dataDir string) string { return filepath.Join(dataDir, "workbuddy.db") }

// memoryUID 从数据目录的 memory/ 里取账号 uid。
//
// 该目录下的文件都叫 <uid>_memory.md；取第一个即可，
// 一个数据目录只对应一个登录账号。
func memoryUID(dataDir string) string {
	entries, err := os.ReadDir(filepath.Join(dataDir, "memory"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if uid, ok := strings.CutSuffix(e.Name(), "_memory.md"); ok && uid != "" {
			return uid
		}
	}
	return ""
}

// memoryBlock 取出记忆文件里 RAW_JSON 块中的 memoryBlock 正文。
//
// 该文件是人可读的 Markdown 包一段 JSON，真正的内容在 JSON 里；
// 直接拿整个文件比大小会把"两个空模板"误判成"都有内容"。
func memoryBlock(path string) (string, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	s := string(raw)
	_, rest, ok := strings.Cut(s, "RAW_JSON_START")
	if !ok {
		return "", false
	}
	body, _, ok := strings.Cut(rest, "RAW_JSON_END")
	if !ok {
		return "", false
	}
	var doc struct {
		MemoryBlock string `json:"memoryBlock"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &doc); err != nil {
		return "", false
	}
	return doc.MemoryBlock, true
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func fileSize(p string) int64 {
	info, err := os.Stat(p)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

// dirSize 累加目录下所有普通文件的大小，用于界面展示。
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func strPtr(s string) *string { return &s }

func int64Ptr(n int64) *int64 { return &n }
