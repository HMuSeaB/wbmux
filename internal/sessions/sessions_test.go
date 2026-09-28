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

	ss, warns, err := scanCodex(root, indexPath)
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
	ss, _, err := scanCodex(filepath.Join(t.TempDir(), "不存在"), "")
	if err != nil || ss != nil {
		t.Errorf("没有 Codex 目录应返回空且无错，got %v, %v", ss, err)
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
