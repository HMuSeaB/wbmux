// Package workspace 维护 WorkBuddy 的「会话工作区」。
//
// # 它管的是什么
//
// 客户端把"项目目录"记在两个地方，两处合起来才完整：
//
//	workbuddy.db 的 workspaces 表  —— 工作区清单（一行一个目录，path 是主键）
//	workbuddy.db 的 sessions.cwd   —— 每条会话归到哪个目录
//	<数据目录>/projects/<slug>/    —— 该目录下会话的正文文件
//
// 于是"改工作区"从来不是改一个字段：清单、每条会话的 cwd、以及正文所在目录名
// 都得跟着动，否则会出现"清单里有这个目录、点进去没有会话"，或者"会话还在、
// 但归到另一个项目下"。
//
// # slug 的规则
//
// projects/ 下的目录名由 cwd 推导：**只压掉路径分隔符与冒号，其余原样保留**。
// 空格要留（`C:\Users\36230\WorkBuddy AI` → `c-Users-36230-WorkBuddy AI`）。
// 这条是硬事实，写错就会得到一堆打不开的会话。
//
// # 为什么库里会有两种写法
//
// 实测同一目录同时存在 `C:/Users/…` 与 `C:\Users\…`，客户端把它们当两个项目，
// 于是同一个项目在列表里出现两次。所以任何改写都要先有一个规范形式（Unify）。
//
// # 读写约束
//
// 查看类（List / Inspect）只发只读查询，随时可跑。
// 写类（Plan.Commit / Plan.CommitMove）必须先满足两个前提：**客户端已退出**、
// **数据库已备份**。前者不是因为保守——客户端握着会话列表的内存缓存，边跑边改
// 会被它回写覆盖；后者是因为改的是用户的历史会话索引，写坏了就是"历史全消失"。
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Space 是一个工作区。
type Space struct {
	Path string `json:"path"`
	// OpenedAt 是最后打开时间（毫秒）。
	OpenedAt int64 `json:"openedAt"`
	// Sessions 是归到这个目录的会话数（含所有写法）。
	Sessions int `json:"sessions"`
	// Variants 是库里实际出现的写法。多于一个说明被拆成了多个入口。
	Variants []string `json:"variants,omitempty"`
	// Exists 表示目录当前是否存在。
	Exists bool `json:"exists"`
	// InList 表示它是否在工作区清单（workspaces 表）里。
	InList bool `json:"inList"`
	// LastOpen 是给人看的时间串。
	LastOpen string `json:"lastOpen,omitempty"`
}

// Overview 是"看一眼现状"的完整结果。
type Overview struct {
	Side        string  `json:"side"`
	DataDir     string  `json:"dataDir"`
	Spaces      []Space `json:"spaces"`
	TotalSess   int     `json:"totalSessions"`
	Problems    []Issue `json:"problems"`
	ProjectDirs int     `json:"projectDirs"`
}

// Issue 是一处体检发现的问题。
type Issue struct {
	Kind string `json:"kind"` // missing | noncanonical | duplicate
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
	// Variants 只在 duplicate 时有值。
	Variants []string `json:"variants,omitempty"`
}

// readWorkspaces 读工作区清单。
func readWorkspaces(p *variant.Probe, id variant.ID) ([]Space, error) {
	rows, err := migrate.Query(p, id,
		`select path, last_opened_at from workspaces order by last_opened_at desc`)
	if err != nil {
		return nil, err
	}
	out := make([]Space, 0, len(rows))
	for _, r := range rows {
		path, _ := r["path"].(string)
		if path == "" {
			continue
		}
		sp := Space{Path: path, OpenedAt: int64(toInt(r["last_opened_at"])), InList: true}
		sp.Exists = dirExists(path)
		if sp.OpenedAt > 0 {
			sp.LastOpen = time.UnixMilli(sp.OpenedAt).Format("2006-01-02 15:04")
		}
		out = append(out, sp)
	}
	return out, nil
}

// sessionCounts 返回 cwd 原文 → 会话数（不含已删除的）。
func sessionCounts(p *variant.Probe, id variant.ID) (map[string]int, error) {
	rows, err := migrate.Query(p, id,
		`select cwd, count(*) as n from sessions where deleted_at is null group by cwd`)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		cwd, _ := r["cwd"].(string)
		out[cwd] = toInt(r["n"])
	}
	return out, nil
}

// Inspect 汇总体检结果：工作区清单 + 会话分布 + 问题列表。全程只读。
func Inspect(p *variant.Probe, id variant.ID) (*Overview, error) {
	list, err := readWorkspaces(p, id)
	if err != nil {
		return nil, err
	}
	counts, err := sessionCounts(p, id)
	if err != nil {
		return nil, err
	}

	// 按规范形式聚合，好看出"一个目录被拆成几条"。
	byNorm := map[string]int{}
	variants := map[string][]string{}
	total := 0
	for cwd, n := range counts {
		k := Unify(cwd)
		byNorm[k] += n
		variants[k] = append(variants[k], cwd)
		total += n
	}

	ov := &Overview{
		Side:      string(id),
		DataDir:   p.DataDir(id),
		TotalSess: total,
	}

	// 清单里的工作区，补上会话数与写法。
	seenNorm := map[string]bool{}
	for _, sp := range list {
		k := Unify(sp.Path)
		seenNorm[k] = true
		sp.Sessions = byNorm[k]
		sp.Variants = sortedKeys(variants[k])
		ov.Spaces = append(ov.Spaces, sp)

		if !sp.Exists {
			ov.Problems = append(ov.Problems, Issue{
				Kind: "missing", Path: sp.Path, Note: "目录不存在（清单里还留着）"})
		}
		if strings.ContainsRune(sp.Path, '/') {
			ov.Problems = append(ov.Problems, Issue{
				Kind: "noncanonical", Path: sp.Path, Note: "写法非规范，规范形式是 " + k})
		}
	}
	// 有会话、但不在清单里的目录——"实际用过、列表里却看不到"。
	for k, n := range byNorm {
		if seenNorm[k] {
			continue
		}
		ov.Spaces = append(ov.Spaces, Space{
			Path: k, Sessions: n, Variants: sortedKeys(variants[k]),
			Exists: dirExists(k), InList: false,
		})
	}
	sort.Slice(ov.Spaces, func(i, j int) bool {
		if ov.Spaces[i].Sessions != ov.Spaces[j].Sessions {
			return ov.Spaces[i].Sessions > ov.Spaces[j].Sessions
		}
		return ov.Spaces[i].Path < ov.Spaces[j].Path
	})

	// 重复入口：同一规范路径有多种写法，且会话确实分散在多种写法上。
	for k, vs := range variants {
		if len(vs) > 1 {
			ov.Problems = append(ov.Problems, Issue{
				Kind: "duplicate", Path: k,
				Note:     "被拆成多个入口，合并后只剩一个",
				Variants: sortedKeys(vs),
			})
		}
	}
	sort.Slice(ov.Problems, func(i, j int) bool {
		if ov.Problems[i].Kind != ov.Problems[j].Kind {
			return ov.Problems[i].Kind < ov.Problems[j].Kind
		}
		return ov.Problems[i].Path < ov.Problems[j].Path
	})

	if ents, err := os.ReadDir(filepath.Join(p.DataDir(id), "projects")); err == nil {
		ov.ProjectDirs = len(ents)
	}
	return ov, nil
}

func sortedKeys(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// Unify 把路径统一成规范写法：盘符小写 + 反斜杠 + 去掉结尾分隔符。
//
// 只做"写法"层面的事：除盘符外一律原样保留。Windows 路径大小写不敏感，
// 但目录名里可能有必须保留的大小写（`WorkBuddy AI`），所以不动。
func Unify(p string) string {
	s := strings.TrimSpace(p)
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "/", `\`)
	if len(s) >= 2 && s[1] == ':' {
		s = strings.ToLower(s[:1]) + s[1:]
	}
	s = strings.TrimRight(s, `\`)
	if len(s) == 2 && s[1] == ':' {
		s += `\`
	}
	return s
}

// SamePath 按 Windows 语义比较两个路径（大小写不敏感）。
func SamePath(a, b string) bool {
	return strings.EqualFold(Unify(a), Unify(b))
}

// Slug 复刻客户端把 cwd 变成 projects 下目录名的规则。
func Slug(path string) string {
	r := strings.NewReplacer(`\`, "-", `/`, "-", `:`, "-")
	return strings.ToLower(r.Replace(Unify(path)))
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ---------- 改写计划 ----------

// Plan 是一次改写的全貌：先算清楚要改什么、动多少行，再决定执行。
//
// 先算后做是刻意的：这类改写影响的是"历史会话还能不能看见"，
// 让用户在动手前看到行数与文件数，比事后解释有效得多。
type Plan struct {
	Kind string `json:"kind"` // set | move
	From string `json:"from"`
	To   string `json:"to"`

	// CwdVariants 是库里那些"规范化之后等于 from"的原始写法。
	CwdVariants []string `json:"cwdVariants"`
	CwdRows     int      `json:"cwdRows"`
	// WsVariants 是 workspaces 表里的原始写法。
	WsVariants []string `json:"wsVariants"`

	// 以下只在 move 时有意义。
	FileCount    int      `json:"fileCount"`
	TotalBytes   int64    `json:"totalBytes"`
	RefFiles     []string `json:"refFiles,omitempty"`
	ProjectSlugs []string `json:"projectSlugs,omitempty"`

	// Blockers 是阻止执行的原因（客户端在跑、目标非空等）。
	Blockers []string `json:"blockers,omitempty"`
}

// Try 表示"可以做"还是"有拦路的原因"。
func (pl *Plan) Ready() bool { return len(pl.Blockers) == 0 }

// Summarize 生成一段给人看的说明。
func (pl *Plan) Summarize() string {
	var b strings.Builder
	fmt.Fprintf(&b, "从 %s\n到 %s\n\n", pl.From, pl.To)
	fmt.Fprintf(&b, "会话记录：%d 条（%d 种写法）\n", pl.CwdRows, len(pl.CwdVariants))
	for _, v := range pl.CwdVariants {
		fmt.Fprintf(&b, "    %s\n", v)
	}
	if len(pl.WsVariants) > 0 {
		fmt.Fprintf(&b, "工作区清单：%d 条\n", len(pl.WsVariants))
	} else {
		b.WriteString("工作区清单：该路径不在清单里\n")
	}
	if pl.Kind == "move" {
		fmt.Fprintf(&b, "\n要搬的文件：%d 个，%s\n", pl.FileCount, humanBytes(pl.TotalBytes))
		fmt.Fprintf(&b, "会话正文含旧路径、需要改写：%d 个\n", len(pl.RefFiles))
		if len(pl.ProjectSlugs) > 0 {
			fmt.Fprintf(&b, "要改名的会话目录：%s\n", strings.Join(pl.ProjectSlugs, ", "))
		}
	}
	return b.String()
}

// BuildSetPlan 造一个"只改路径、不搬文件"的计划。
//
// 返回的计划**不含拦路原因**，由调用方决定要不要收（BuildMovePlan 会转调本函数，
// 若这里也收就会出现同一条原因列两遍）。
func BuildSetPlan(p *variant.Probe, id variant.ID, from, to string) (*Plan, error) {
	counts, err := sessionCounts(p, id)
	if err != nil {
		return nil, err
	}
	list, err := readWorkspaces(p, id)
	if err != nil {
		return nil, err
	}

	pl := &Plan{Kind: "set", From: Unify(from), To: Unify(to)}
	for cwd, n := range counts {
		if SamePath(cwd, from) {
			pl.CwdVariants = append(pl.CwdVariants, cwd)
			pl.CwdRows += n
		}
	}
	for _, sp := range list {
		if SamePath(sp.Path, from) {
			pl.WsVariants = append(pl.WsVariants, sp.Path)
		}
	}
	sort.Strings(pl.CwdVariants)
	sort.Strings(pl.WsVariants)

	if pl.CwdRows == 0 && len(pl.WsVariants) == 0 {
		return nil, fmt.Errorf("库里没有 %s 的任何记录", from)
	}
	if SamePath(from, to) && len(pl.CwdVariants) < 2 {
		// 目标与来源相同且只有一种写法：没什么可做的，说清楚而不是假装成功。
		return nil, fmt.Errorf("路径已经规范且只有一种写法，无需改动")
	}
	// 拦路原因由调用方在最后统一收集一次。这里不收：BuildMovePlan 会转调
	// BuildSetPlan，两处都收就会出现同一条原因列两遍（实测踩到）。
	return pl, nil
}

// BuildMovePlan 造一个"搬到别处"的计划。
func BuildMovePlan(p *variant.Probe, id variant.ID, from, to string) (*Plan, error) {
	pl, err := BuildSetPlan(p, id, from, to)
	if err != nil {
		return nil, err
	}
	pl.Kind = "move"

	if dirExists(to) {
		ents, _ := os.ReadDir(to)
		if len(ents) > 0 {
			return nil, fmt.Errorf("目标目录已存在且不为空：%s（迁移只接受不存在或空的目标，避免把两份内容混在一起）", to)
		}
	}
	if !dirExists(from) {
		return nil, fmt.Errorf("源目录不存在：%s", from)
	}

	_ = filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == from || d.IsDir() {
			return nil
		}
		if info, e := d.Info(); e == nil {
			pl.FileCount++
			pl.TotalBytes += info.Size()
		}
		return nil
	})

	// 会话正文里可能记着绝对路径（图片、文件引用），一并找出来。
	projDir := filepath.Join(p.DataDir(id), "projects")
	_ = filepath.WalkDir(projDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".jsonl") {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return nil
		}
		if containsPath(string(raw), from) {
			if rel, e := filepath.Rel(projDir, path); e == nil {
				pl.RefFiles = append(pl.RefFiles, rel)
			}
		}
		return nil
	})
	sort.Strings(pl.RefFiles)

	// projects 目录里哪些要改名（新旧 slug 不同才列）。
	newSlug := Slug(pl.To)
	seen := map[string]bool{}
	for _, a := range append([]string{pl.From}, pl.CwdVariants...) {
		if s := Slug(a); s != newSlug && !seen[s] {
			if dirExists(filepath.Join(projDir, s)) {
				seen[s] = true
				pl.ProjectSlugs = append(pl.ProjectSlugs, s)
			}
		}
	}
	sort.Strings(pl.ProjectSlugs)

	if err := pl.ensureNoDuplicateBlockers(); err != nil {
		return nil, err
	}
	pl.collectBlockers(id, true)
	return pl, nil
}

// ensureNoDuplicateBlockers 兜住"同一原因列两遍"这类汇总错误。
//
// 拦路原因是给用户看的，重复列出会让人以为有两个不同的问题。
func (pl *Plan) ensureNoDuplicateBlockers() error {
	seen := map[string]bool{}
	out := pl.Blockers[:0]
	for _, b := range pl.Blockers {
		if seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	pl.Blockers = out
	return nil
}

// collectBlockers 收集"现在还不能做"的原因。
//
// 客户端在跑是硬阻断而不是警告：它握着会话列表的内存缓存，边跑边改会被回写覆盖，
// 表现为"改了没生效"，或更糟——索引与正文对不上，列表里点开是空白。
func (pl *Plan) collectBlockers(id variant.ID, needSource bool) {
	if migrate.IsRunning(id) {
		pl.Blockers = append(pl.Blockers,
			fmt.Sprintf("%s 客户端正在运行，先完全退出它（否则改动会被回写覆盖）", id))
	}
	if needSource && !dirExists(pl.From) {
		pl.Blockers = append(pl.Blockers, "源目录不存在："+pl.From)
	}
}

// ---------- 执行 ----------

// Commit 执行"只改路径"的计划。
func (pl *Plan) Commit(p *variant.Probe, id variant.ID) error {
	if !pl.Ready() {
		return fmt.Errorf("还不能执行：%s", strings.Join(pl.Blockers, "；"))
	}
	if _, err := backupDB(p, id); err != nil {
		return err
	}
	return pl.writePaths(p, id)
}

// writePaths 改 workspaces.path 与 sessions.cwd。
//
// 逐条改写而不是一次 `where cwd in (...)`：每条的原文不同（`C:/…` 与 `C:\…`），
// 必须按原样匹配，否则匹配不上。
func (pl *Plan) writePaths(p *variant.Probe, id variant.ID) error {
	var stmts []migrate.Statement
	for _, v := range pl.CwdVariants {
		stmts = append(stmts, migrate.Statement{
			SQL:    `update sessions set cwd = ? where cwd = ?`,
			Params: []any{pl.To, v},
		})
	}
	for _, v := range pl.WsVariants {
		stmts = append(stmts, migrate.Statement{
			SQL:    `update workspaces set path = ? where path = ?`,
			Params: []any{pl.To, v},
		})
	}
	if len(stmts) == 0 {
		return nil
	}
	if _, err := migrate.Update(p, id, []string{"sessions", "workspaces"}, stmts); err != nil {
		return err
	}
	return nil
}

// CommitMove 执行搬迁：复制文件 → 改库 → 改会话目录名 → 改写正文里的引用。
//
// 顺序是刻意的：**先复制，后改库**。复制失败时源目录与数据库都还是原样，
// 用户最多多了一份没用的副本；反过来先改库再复制，中途失败就得到
// "会话指向一个空目录"。
func (pl *Plan) CommitMove(p *variant.Probe, id variant.ID) error {
	if !pl.Ready() {
		return fmt.Errorf("还不能执行：%s", strings.Join(pl.Blockers, "；"))
	}
	if err := os.MkdirAll(filepath.Dir(pl.To), 0o755); err != nil {
		return fmt.Errorf("创建目标父目录失败: %w", err)
	}
	if err := copyTree(pl.From, pl.To); err != nil {
		return fmt.Errorf("复制文件失败（源目录与数据库都没动）: %w", err)
	}
	if _, err := backupDB(p, id); err != nil {
		return err
	}
	if err := pl.writePaths(p, id); err != nil {
		return err
	}

	projDir := filepath.Join(p.DataDir(id), "projects")
	newSlug := Slug(pl.To)
	for _, oldSlug := range pl.ProjectSlugs {
		src := filepath.Join(projDir, oldSlug)
		dst := filepath.Join(projDir, newSlug)
		if err := mergeDir(src, dst); err != nil {
			return fmt.Errorf("改会话目录失败 %s → %s: %w", src, dst, err)
		}
	}

	if len(pl.RefFiles) > 0 {
		if _, err := rewriteRefs(filepath.Join(projDir, newSlug), pl.From, pl.To); err != nil {
			return fmt.Errorf("改写会话正文里的路径失败: %w", err)
		}
	}
	return nil
}

// EnsureStopped 在客户端运行时返回错误。写操作的入口都该先问一句。
func EnsureStopped(id variant.ID) error {
	if migrate.IsRunning(id) {
		return fmt.Errorf("%s 客户端正在运行，先完全退出它再执行", id)
	}
	return nil
}

// BackupDB 把 workbuddy.db 复制一份带时间戳的备份。
func BackupDB(p *variant.Probe, id variant.ID) (string, error) { return backupDB(p, id) }

func backupDB(p *variant.Probe, id variant.ID) (string, error) {
	src := filepath.Join(p.DataDir(id), "workbuddy.db")
	raw, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("读取数据库失败: %w", err)
	}
	dir := filepath.Join(p.DataDir(id), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "workbuddy-"+time.Now().Format("20060102-150405")+".db")
	if err := os.WriteFile(dst, raw, 0o600); err != nil {
		return "", fmt.Errorf("备份数据库失败: %w", err)
	}
	return dst, nil
}

func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		return os.WriteFile(target, raw, mode)
	})
}

// mergeDir 把 src 的内容并入 dst 后删掉 src。
//
// dst 已存在时按文件逐个并而不是直接失败：同一个项目换盘之后，新 slug 目录
// 可能已经因为别的原因存在了（比如新目录名下先开过一次会话），直接并更实用。
func mergeDir(src, dst string) error {
	if !dirExists(dst) {
		return os.Rename(src, dst)
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// containsPath 判断内容里是否出现该路径。
func containsPath(content, p string) bool {
	for _, cand := range pathLiterals(p) {
		if strings.Contains(content, cand) {
			return true
		}
	}
	return false
}

// pathLiterals 给出一个路径在 JSON 文本里可能的写法。
//
// 会话文件是 JSON，反斜杠会被转义成 `\\`，所以两种都要找；正斜杠形式
// （客户端在别处也会用）同样要覆盖，否则漏改一半。
func pathLiterals(p string) []string {
	u := Unify(p)
	set := []string{u, strings.ReplaceAll(u, `\`, `\\`), strings.ReplaceAll(u, `\`, "/")}
	// 盘符大写形式（旧记录里存在过）
	if len(u) > 1 {
		up := strings.ToUpper(u[:1]) + u[1:]
		set = append(set, up, strings.ReplaceAll(up, `\`, `\\`), strings.ReplaceAll(up, `\`, "/"))
	}
	sort.Slice(set, func(i, j int) bool { return len(set[i]) > len(set[j]) })
	return set
}

// rewriteRefs 把会话正文里的旧路径换成新路径，返回改过的文件数。
func rewriteRefs(dir, from, to string) (int, error) {
	if !dirExists(dir) {
		return 0, nil
	}
	fromU, toU := Unify(from), Unify(to)
	n := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		s := string(raw)
		out := s
		// 长到短依次替换：先换 `\\` 转义形式，再换单反斜杠，最后换正斜杠，
		// 否则短形式会把长形式切碎，留下半截路径。
		for _, pair := range replacements(fromU, toU) {
			out = strings.ReplaceAll(out, pair[0], pair[1])
		}
		if out == s {
			return nil
		}
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

// replacements 给出"旧路径写法 → 新路径写法"的替换对，按旧串长度降序。
func replacements(from, to string) [][2]string {
	pairs := [][2]string{
		{strings.ReplaceAll(from, `\`, `\\`), strings.ReplaceAll(to, `\`, `\\`)},
		{from, to},
		{strings.ReplaceAll(from, `\`, "/"), strings.ReplaceAll(to, `\`, "/")},
	}
	sort.SliceStable(pairs, func(i, j int) bool { return len(pairs[i][0]) > len(pairs[j][0]) })
	return pairs
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
