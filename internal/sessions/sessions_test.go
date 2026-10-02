package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// stringsJoinLines 拼出 jsonl 测试样本：每行一条 JSON，尾带换行。
func stringsJoinLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestNormalizeProject(t *testing.T) {
	cases := []struct{ in, want string }{
		{"C:\\Users\\36230\\WorkBuddy AI", "c:/users/36230/workbuddy ai"},
		{"C:/Users/36230/WorkBuddy AI", "c:/users/36230/workbuddy ai"},
		{"D:\\4rchive\\Code\\cc-switch\\", "d:/4rchive/code/cc-switch"},
		{"", "(无项目)"},
		{"   ", "(无项目)"},
		{"C:/", "c:/"},
	}
	for _, c := range cases {
		if got := normalizeProject(c.in); got != c.want {
			t.Errorf("normalizeProject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 候选规则是这次需求的灵魂，单独钉死：超期 + 项目内名次在保底之外。
// "三个月没动但下个月还要用"的项目，最新一条永远不能成为候选。
func TestCandidateRule(t *testing.T) {
	now := time.Now().UnixMilli()
	day := int64(24 * 3600 * 1000)
	mk := func(proj string, updatedDaysAgo int64) Session {
		return Session{ProjectRaw: proj, UpdatedMs: now - updatedDaysAgo*day}
	}
	all := []Session{
		mk("p-old", 90), // 老项目：三条全是 90 天前的
		mk("p-old", 91),
		mk("p-old", 92),
		mk("p-new", 1), // 活跃项目：两天内还有动静
		mk("p-new", 40),
	}
	idx := buildIndex(all, nil, Options{KeepDays: 30, KeepPerProject: 1}, 0)

	var got []string
	for _, s := range idx.Sessions {
		if s.Candidate {
			got = append(got, s.Title)
		}
	}
	// p-old：名次 1（90 天那条）保底安全，名次 2、3 是候选；
	// p-new：名次 1 是 1 天前的（没超期），名次 2 超 30 天 → 候选。
	if len(got) != 3 {
		t.Fatalf("候选数 = %d，want 3（p-old 两条 + p-new 一条）", len(got))
	}
	for _, p := range idx.Projects {
		if p.Path == "p-old" && !p.OnlyStale {
			t.Error("p-old 全部超期，OnlyStale 应为 true")
		}
		if p.Path == "p-new" && p.OnlyStale {
			t.Error("p-new 有新鲜会话，OnlyStale 应为 false")
		}
	}
}

func TestMapStrAndMs(t *testing.T) {
	row := map[string]any{
		"s": "文本", "n": float64(1790570380173), "nil": nil,
	}
	if mapStr(row, "s") != "文本" {
		t.Error("mapStr 字符串列取错")
	}
	if mapStr(row, "missing") != "" || mapStr(row, "nil") != "" {
		t.Error("mapStr 缺列/NULL 应返回空串")
	}
	if mapMs(row, "n") != 1790570380173 {
		t.Error("mapMs 毫秒时间戳精度丢失")
	}
	if mapMs(row, "missing") != 0 {
		t.Error("mapMs 缺列应返回 0")
	}
}

func TestScanCodex(t *testing.T) {
	root := t.TempDir()
	day := filepath.Join(root, "2026", "07", "22")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"timestamp":"2026-07-22T09:49:21.977Z","type":"session_meta","payload":` +
		`{"id":"sid-1","cwd":"D:\\4rchive\\Code\\demo"}}` + "\n"
	if err := os.WriteFile(filepath.Join(day, "rollout-x.jsonl"), []byte(meta+"{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, "session_index.jsonl")
	if err := os.WriteFile(indexPath, []byte(`{"id":"sid-1","thread_name":"演示会话","updated_at":"2026-07-22T10:00:00.5Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ss, warns, err := scanCodex([]string{root}, indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("不该有警告：%v", warns)
	}
	if len(ss) != 1 {
		t.Fatalf("扫出 %d 条，want 1", len(ss))
	}
	s := ss[0]
	if s.ID != "sid-1" || s.Title != "演示会话" {
		t.Errorf("ID/Title = %q/%q", s.ID, s.Title)
	}
	if s.ProjectRaw != `D:\4rchive\Code\demo` {
		t.Errorf("cwd 取错：%q", s.ProjectRaw)
	}
	if s.UpdatedMs == 0 || s.CreatedMs == 0 {
		t.Error("时间戳没取到")
	}
	if s.Source.Kind != "file" {
		t.Error("Codex 源应是 file 型")
	}
}

func TestScanCodexMissingRoot(t *testing.T) {
	ss, _, err := scanCodex([]string{filepath.Join(t.TempDir(), "不存在")}, "")
	if err != nil || ss != nil {
		t.Errorf("没有 Codex 目录应返回空且无错，got %v, %v", ss, err)
	}
}

// 幽灵行与归档去重：首行是合法 JSON 但不是 session_meta 的文件整个跳过
// （绝不能造出空厂商、空项目、空标题的幽灵行）；sessions/ 与
// archived_sessions/ 里同 ID 的拷贝只留一条；归档根里独立的会话照常收录。
func TestScanCodexGhostArchivedDedupe(t *testing.T) {
	base := t.TempDir()
	sessionsRoot := filepath.Join(base, "sessions")
	archRoot := filepath.Join(base, "archived_sessions")
	day := filepath.Join(sessionsRoot, "2026", "07", "22")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(archRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"timestamp":"2026-07-22T09:49:21.977Z","type":"session_meta","payload":` +
		`{"id":"sid-1","cwd":"D:\\4rchive\\Code\\demo"}}` + "\n"
	if err := os.WriteFile(filepath.Join(day, "rollout-a.jsonl"), []byte(meta+"{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-ghost.jsonl"), []byte("{\"type\":\"event_msg\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archMeta := `{"timestamp":"2026-03-16T22:37:48.000Z","type":"session_meta","payload":` +
		`{"id":"sid-2","cwd":"D:\\4rchive\\Code\\arch"}}` + "\n"
	if err := os.WriteFile(filepath.Join(archRoot, "rollout-b.jsonl"), []byte(archMeta+"{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archRoot, "rollout-a-copy.jsonl"), []byte(meta+"{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(base, "session_index.jsonl")
	if err := os.WriteFile(indexPath, []byte(
		`{"id":"sid-1","thread_name":"演示会话","updated_at":"2026-07-22T10:00:00.5Z"}`+"\n"+
			`{"id":"sid-2","thread_name":"归档会话","updated_at":"2026-03-16T23:00:00.5Z"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ss, warns, err := scanCodex([]string{sessionsRoot, archRoot}, indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("不该有警告：%v", warns)
	}
	if len(ss) != 2 {
		t.Fatalf("扫出 %d 条，want 2（幽灵跳过、sid-1 去重）：%+v", len(ss), ss)
	}
	byID := map[string]Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	if byID["sid-1"].Title != "演示会话" || byID["sid-2"].Title != "归档会话" {
		t.Errorf("标题取错：%v", byID)
	}
}

func TestScanClaude(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "D--4rchive-Code-demo")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := stringsJoinLines(
		`{"type":"mode","mode":"normal","sessionId":"cid-1"}`,
		`{"type":"file-history-snapshot","timestamp":"2026-09-24T05:21:45.799Z"}`,
		`{"type":"user","message":{"content":"帮我看看这个项目"},"cwd":"D:\\4rchive\\Code\\demo"}`,
		`{"type":"summary","summary":"项目排查"}`,
	)
	if err := os.WriteFile(filepath.Join(proj, "cid-1.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	// summary 在后的情况：标题应退回首条用户消息
	lines2 := stringsJoinLines(
		`{"type":"user","message":{"content":[{"type":"text","text":"数组形式的消息"}]},"cwd":"D:\\4rchive\\Code\\demo"}`,
	)
	if err := os.WriteFile(filepath.Join(proj, "cid-2.jsonl"), []byte(lines2), 0o644); err != nil {
		t.Fatal(err)
	}

	ss, _, err := scanClaude(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("扫出 %d 条，want 2", len(ss))
	}
	if ss[0].ProjectRaw != `D:\4rchive\Code\demo` {
		t.Errorf("cwd 应从行内容取，got %q", ss[0].ProjectRaw)
	}
	titles := map[string]bool{}
	for _, s := range ss {
		titles[s.Title] = true
	}
	if !titles["项目排查"] || !titles["数组形式的消息"] {
		t.Errorf("标题取错：%v", titles)
	}
}

// ---------- 清理计划 ----------

// mkSession 造一条会话，只填清理计划会用到的字段。
//
// 注意它**不设 Vendor**：BuildCleanPlan 只看 path 与 candidate，
// 而 BuildRowPlan 要看 Vendor 决定删几张表。所以用 mkSessionV 那条。
func mkSession(id, path string, cand bool, size int64) Session {
	return Session{
		ID: id, Candidate: cand, SizeBytes: size,
		Source: Source{Path: path},
	}
}

func mkSessionV(id string, v Vendor, path string, cand bool, size int64) Session {
	s := mkSession(id, path, cand, size)
	s.Vendor = v
	return s
}

// TestCleanPlanRefusesSharedPaths 是这块**最要紧**的一条。
//
// 同一张表里混着两种位置：独占文件（Codex 的 rollout）与共用数据库
// （WB 的 workbuddy.db 里存着 24 条会话）。共用数据库**绝不能按文件删**
// ——删掉那个 .db 就是删掉库里所有会话，不只是候选那条。
//
// 实测数据：workbuddy.db 里 24 条、只有 3 条候选。若按文件删，另外 21 条
// 会一起没。所以这条判据是硬的安全阀，不是优化。
func TestCleanPlanRefusesSharedPaths(t *testing.T) {
	idx := &Index{Sessions: []Session{
		mkSession("a", `/x/.codex/sessions/rollout-1.jsonl`, true, 100),
		mkSession("b", `/x/.workbuddy/workbuddy.db`, true, 0),  // 候选
		mkSession("c", `/x/.workbuddy/workbuddy.db`, false, 0), // 非候选，同库
		mkSession("d", `/x/.workbuddy/workbuddy.db`, false, 0), // 非候选，同库
		mkSession("e", `/x/.codex/sessions/rollout-2.jsonl`, false, 200),
	}}

	plan := BuildCleanPlan(idx)

	if len(plan.Targets) != 1 {
		t.Fatalf("只该有 1 个可删目标，实际 %d：%+v", len(plan.Targets), plan.Targets)
	}
	if plan.Targets[0].Path != `/x/.codex/sessions/rollout-1.jsonl` {
		t.Fatalf("删错目标了：%s", plan.Targets[0].Path)
	}
	if plan.TotalBytes != 100 {
		t.Fatalf("字节合计应为 100，实际 %d", plan.TotalBytes)
	}
	// 被拒的那条必须留下说明，不能悄悄跳过——不然用户会问
	// "候选有 2 条、为什么只清了 1 条"。
	if len(plan.Refused) != 1 {
		t.Fatalf("该有 1 条被拒，实际 %d", len(plan.Refused))
	}
	if plan.Refused[0].SharedBy != 3 {
		t.Fatalf("SharedBy 应为 3（该路径被 3 条会话共用），实际 %d", plan.Refused[0].SharedBy)
	}
	if plan.Refused[0].Reason == "" {
		t.Fatal("被拒的理由不能为空——界面要显示给用户看")
	}
}

// TestCleanPlanIgnoresNonCandidates 确认非候选一律不动。
func TestCleanPlanIgnoresNonCandidates(t *testing.T) {
	idx := &Index{Sessions: []Session{
		mkSession("a", `/x/rollout-1.jsonl`, false, 100),
		mkSession("b", `/x/rollout-2.jsonl`, false, 200),
	}}
	if plan := BuildCleanPlan(idx); len(plan.Targets) != 0 {
		t.Fatalf("非候选不该入选，实际 %+v", plan.Targets)
	}
}

// TestCleanPlanRefusesEmptyPath 挡掉没有位置信息的条目。
func TestCleanPlanRefusesEmptyPath(t *testing.T) {
	idx := &Index{Sessions: []Session{mkSession("a", "", true, 100)}}
	plan := BuildCleanPlan(idx)
	if len(plan.Targets) != 0 {
		t.Fatalf("空路径不该入选：%+v", plan.Targets)
	}
	if len(plan.Refused) != 1 {
		t.Fatalf("该有 1 条被拒，实际 %d", len(plan.Refused))
	}
}

// TestCleanRefusesOutsideSessionFiles 确认"外形检查"挡得住误传进来的东西。
//
// 它防的是调用方传错：比如把某个数据库路径当成会话文件。真删了就是
// "把一个库删掉"这种级别的后果，所以值得单独钉一条。
func TestCleanRefusesOutsideSessionFiles(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "workbuddy.db")
	if err := os.WriteFile(db, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(dir, "agent-abc.jsonl")
	if err := os.WriteFile(agent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var trashed []string
	res, err := CleanTargets(&CleanPlan{Targets: []CleanTarget{
		{Path: db}, {Path: agent},
	}}, CleanOptions{
		Trash: func(p string) error { trashed = append(trashed, p); return nil },
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if res.Moved != 0 || len(trashed) != 0 {
		t.Fatalf(".db 与 agent- 转录都不该被删，实际动了 %v", trashed)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("该有 2 条跳过记录，实际 %v", res.Skipped)
	}
	// 文件必须还在。
	if _, err := os.Stat(db); err != nil {
		t.Fatal("数据库文件被删了")
	}
}

// TestCleanDryRunTouchesNothing 确认预演不碰任何文件。
func TestCleanDryRunTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "rollout-1.jsonl")
	if err := os.WriteFile(f, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	called := false
	res, err := CleanTargets(&CleanPlan{
		Targets: []CleanTarget{{Path: f}}, TotalBytes: 5,
	}, CleanOptions{
		DryRun: true,
		Trash:  func(string) error { called = true; return nil },
	})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if called {
		t.Fatal("预演不该调用回收站")
	}
	if res.Moved != 0 {
		t.Fatalf("预演不该有移动数，实际 %d", res.Moved)
	}
	if _, err := os.Stat(f); err != nil {
		t.Fatal("预演把文件删了")
	}
}

// TestCleanMissingFileIsNotFailure 确认"文件已不在"不算失败。
//
// 计划是上次扫描算的，扫描之后文件可能已被清过或用户自己删了。
// 把它当失败会让整批操作的报告失真。
func TestCleanMissingFileIsNotFailure(t *testing.T) {
	res, err := CleanTargets(&CleanPlan{
		Targets: []CleanTarget{{Path: filepath.Join(t.TempDir(), "nope.jsonl")}},
	}, CleanOptions{Trash: func(string) error { return nil }})
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("不该记为失败：%v", res.Failed)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("该记一条跳过，实际 %v", res.Skipped)
	}
}

// TestCleanRefineVendors 确认"只清某一家"能生效——用户说过"cc 的先别动"，
// 虽然规则本来就不会动 cc，但按厂商收窄的能力要真的存在。
func TestCleanRefineVendors(t *testing.T) {
	plan := &CleanPlan{Targets: []CleanTarget{
		{Path: "/a/rollout-1.jsonl", Vendor: VendorCodex, Size: 10},
		{Path: "/b/rollout-2.jsonl", Vendor: VendorClaude, Size: 20},
	}}
	out := plan.RefineVendors([]Vendor{VendorCodex})
	if len(out.Targets) != 1 || out.Targets[0].Vendor != VendorCodex {
		t.Fatalf("收窄失败：%+v", out.Targets)
	}
	if out.TotalBytes != 10 {
		t.Fatalf("字节合计应跟着收窄，实际 %d", out.TotalBytes)
	}
}

// ---------- 删库行 ----------

// TestBuildRowPlanPicksOnlyDBBacked 确认只挑"在数据库里"的候选。
//
// 与 BuildCleanPlan 互补：那个挑独占文件、这个挑共用数据库，不能重叠。
// 重叠了就会出现"同一会话被两条路径都删一次"。
func TestBuildRowPlanPicksOnlyDBBacked(t *testing.T) {
	idx := &Index{Sessions: []Session{
		mkSession("a", `/x/.codex/rollout-1.jsonl`, true, 100), // 独占文件 → 归 clean
		mkSession("b", `/x/.workbuddy/workbuddy.db`, true, 0),  // 库行 → 归这里
		mkSession("c", `/x/.workbuddy/workbuddy.db`, false, 0), // 同库、非候选
		mkSession("d", `/x/.workbuddy/workbuddy.db`, false, 0),
	}}
	plan := BuildRowPlan(idx)
	if len(plan.Targets) != 1 || plan.Targets[0].ID != "b" {
		t.Fatalf("只该挑出 b，实际 %+v", plan.Targets)
	}

	// 反向确认：clean 那边不该碰这个库。
	cleanPlan := BuildCleanPlan(idx)
	if len(cleanPlan.Targets) != 1 || cleanPlan.Targets[0].Path != `/x/.codex/rollout-1.jsonl` {
		t.Fatalf("clean 该只挑独占文件，实际 %+v", cleanPlan.Targets)
	}
}

// TestRowPlanZCodeCoversAllTables 钉住 ZCode 要删的 6 张表。
//
// 只删 session 行、留下 message/part，那些正文就成了永远读不到的孤儿
// ——占着空间、谁也找不到。所以表的清单必须完整且顺序正确（先子后主）。
func TestRowPlanZCodeCoversAllTables(t *testing.T) {
	idx := &Index{Sessions: []Session{
		mkSessionV("sess_x", VendorZCode, `/x/.zcode/cli/db/db.sqlite`, true, 0),
		mkSessionV("y", VendorZCode, `/x/.zcode/cli/db/db.sqlite`, false, 0),
	}}
	plan := BuildRowPlan(idx)
	if len(plan.Targets) != 1 {
		t.Fatalf("该挑出 1 条，实际 %d", len(plan.Targets))
	}
	got := plan.Targets[0].Tables
	if len(got) != 6 {
		t.Fatalf("ZCode 该删 6 张表，实际 %d：%+v", len(got), got)
	}
	// 最后一张必须是主表：先删子表，中途失败才不会留下孤儿正文。
	if got[len(got)-1].Name != "session" {
		t.Fatalf("主表该最后删，实际最后是 %s", got[len(got)-1].Name)
	}
	names := map[string]bool{}
	for _, tb := range got {
		names[tb.Name] = true
	}
	for _, want := range []string{"part", "message", "todo", "session_entry", "session_input", "session"} {
		if !names[want] {
			t.Errorf("漏了表 %s", want)
		}
	}
}

// TestCleanRowsDoesNotDeleteRowWhenBodyFails 是**最要紧**的一条。
//
// 顺序是"先移正文、再删库行"。如果正文没处理掉就把行删了，那个文件会变成
// 谁也认不出的孤儿——用户看不到它，工具也找不到它。所以正文失败时必须
// **跳过删行**，并如实记下来。
func TestCleanRowsDoesNotDeleteRowWhenBodyFails(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "some-slug")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(proj, "abc.jsonl")
	if err := os.WriteFile(body, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	deleted := false
	res, err := CleanRows(&RowPlan{Targets: []rowTarget{{
		ID: "abc", Vendor: VendorWBCN, DBPath: filepath.Join(dir, "workbuddy.db"),
		Tables: []rowTable{{"sessions", "id"}},
	}}}, RowCleanDeps{
		DataDirOf: func(Vendor) string { return dir },
		Trash:     func(string) error { return os.ErrPermission }, // 故意失败
		DeleteRows: func(string, []string, []SQLStatement) ([]int, error) {
			deleted = true
			return []int{1}, nil
		},
	}, CleanOptions{})

	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if deleted {
		t.Fatal("正文没处理掉就删了库行 —— 会留下谁也认不出的孤儿文件")
	}
	if len(res.Failed) != 1 {
		t.Fatalf("该记一条失败，实际 %v", res.Failed)
	}
	if !strings.Contains(res.Failed[0], "库行未删") {
		t.Fatalf("失败信息该说明库行没删，实际 %q", res.Failed[0])
	}
	// 文件必须还在（失败就不该假装删掉）。
	if _, err := os.Stat(body); err != nil {
		t.Fatal("正文文件不该动")
	}
}

// TestCleanRowsDryRunTouchesNothing 确认预演不删任何东西。
func TestCleanRowsDryRunTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "projects", "s")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := filepath.Join(proj, "abc.jsonl")
	if err := os.WriteFile(body, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	touched := false
	res, err := CleanRows(&RowPlan{Targets: []rowTarget{{
		ID: "abc", Vendor: VendorWBCN, DBPath: filepath.Join(dir, "wb.db"),
		Tables: []rowTable{{"sessions", "id"}},
	}}}, RowCleanDeps{
		DataDirOf:  func(Vendor) string { return dir },
		Trash:      func(string) error { touched = true; return nil },
		DeleteRows: func(string, []string, []SQLStatement) ([]int, error) { touched = true; return nil, nil },
	}, CleanOptions{DryRun: true})

	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if touched {
		t.Fatal("预演动了东西")
	}
	if res.FreedBytes != 5 {
		t.Fatalf("预演该算出会释放 5 字节，实际 %d", res.FreedBytes)
	}
	if _, err := os.Stat(body); err != nil {
		t.Fatal("预演把文件删了")
	}
}

// TestFindBodyFilesIgnoresSlug 确认找正文靠 id 而不是猜目录名。
//
// 目录名是 cwd 推导的 slug，规则耦合在客户端里——拼错了会"以为没文件、
// 于是不删"，留下孤儿。所以必须遍历找 <id>.jsonl。
func TestFindBodyFilesIgnoresSlug(t *testing.T) {
	dir := t.TempDir()
	// 故意用一个"猜不出来"的目录名
	weird := filepath.Join(dir, "projects", "c--Users-x-Ω-测试")
	if err := os.MkdirAll(weird, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(weird, "abc.jsonl")
	if err := os.WriteFile(want, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 干扰项：别的 id
	if err := os.WriteFile(filepath.Join(weird, "other.jsonl"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := FindBodyFiles(dir, "abc")
	if len(got) != 1 || got[0] != want {
		t.Fatalf("该找到 1 个，实际 %v", got)
	}
}

// TestCleanRowsDeletesAllTables 确认多表都真的走到。
func TestCleanRowsDeletesAllTables(t *testing.T) {
	var gotTables []string
	var stmts []SQLStatement
	_, err := CleanRows(&RowPlan{Targets: []rowTarget{{
		ID: "sess_x", Vendor: VendorZCode, DBPath: "/x/db.sqlite",
		Tables: []rowTable{
			{"part", "session_id"}, {"message", "session_id"}, {"session", "id"},
		},
	}}}, RowCleanDeps{
		DataDirOf: func(Vendor) string { return "" }, // ZCode 没有独立正文文件
		DeleteRows: func(_ string, tables []string, s []SQLStatement) ([]int, error) {
			gotTables = tables
			stmts = s
			return []int{1, 2, 3}, nil
		},
	}, CleanOptions{})

	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if len(gotTables) != 3 {
		t.Fatalf("该删 3 张表，实际 %v", gotTables)
	}
	// 每条都必须是参数化的等值删除（sqlite.js 那边也会再拦一次）。
	for _, s := range stmts {
		if !strings.Contains(s.SQL, "= ?") || len(s.Params) != 1 {
			t.Fatalf("语句该是等值 + 占位符：%q %v", s.SQL, s.Params)
		}
	}
}

// TestCodexRootsFollowProbeHome 钉住"Codex 的目录从 probe 取"。
//
// 这不是吹毛求疵：曾经 codexRoots 直接调 os.UserHomeDir()，真实运行时
// 两者恰好相同，看不出问题；但单元测试的 probe.Home 指向临时目录，
// 于是测试造出来的会话**一个都扫不到**，却"通过"了一条什么都不做的路径。
//
// 这类"测试里才暴露的错"最容易漏，所以用一条断言把它钉死：
// 换个 home，roots 必须跟着变。
func TestCodexRootsFollowProbeHome(t *testing.T) {
	p1 := variant.DefaultProbe()
	p2 := variant.DefaultProbe()
	p2.Home = filepath.Join("X:", "some", "other", "home")

	r1 := codexRoots(p1)
	r2 := codexRoots(p2)

	if len(r1) == 0 || len(r2) == 0 {
		t.Fatal("roots 不该为空")
	}
	if r1[0] == r2[0] {
		t.Fatalf("换了 probe.Home 之后 roots 没变（%s）——说明它没走 probe，"+
			"而是直接读了进程环境。测试会因此扫不到自己造的数据。", r1[0])
	}
	// 顺带确认拼的是 .codex 下的两个已知目录。
	for _, want := range []string{"sessions", "archived_sessions"} {
		found := false
		for _, r := range r2 {
			if strings.Contains(r, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("roots 里该有 %s 那个目录，实际 %v", want, r2)
		}
	}
	// session_index.jsonl 同理。
	if codexIndexPath(p1) == codexIndexPath(p2) {
		t.Fatal("codexIndexPath 也没走 probe")
	}
}
