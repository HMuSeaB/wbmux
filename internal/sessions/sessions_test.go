package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// mkSession 造一条会话，只填 BuildCleanPlan 会用到的字段。
func mkSession(id, path string, cand bool, size int64) Session {
	return Session{
		ID: id, Candidate: cand, SizeBytes: size,
		Source: Source{Path: path},
	}
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
