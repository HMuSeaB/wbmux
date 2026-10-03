package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

// 复刻 dep-view 那条"无标题"会话的行结构：developer 注入、
// 双块 user 消息（环境+真实）、$@ 这种奇怪提问，验证标题兜底。
func TestParseCodexFileTitleFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollout-2026-04-01T13-16-03-x.jsonl")
	lines := []string{
		`{"timestamp":"2026-04-01T13:16:03.000Z","type":"session_meta","payload":{"id":"sid-dep","cwd":"D:\\4rchive\\Code\\dep-view","timestamp":"2026-04-01T13:16:03.000Z"}}`,
		`{"timestamp":"2026-04-01T13:16:04.000Z","type":"event_msg","payload":{"type":"task_started"}}`,
		`{"timestamp":"2026-04-01T13:16:05.000Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions> sandbox stuff"}]}}`,
		`{"timestamp":"2026-04-01T13:16:06.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions for D:\\4rchive\\Code\\dep-view"},{"type":"input_text","text":"<user_instructions> more env"}]}}`,
		`{"timestamp":"2026-04-01T13:16:07.000Z","type":"event_msg","payload":{"type":"turn_context"}}`,
		`{"timestamp":"2026-04-01T13:16:08.000Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"$@"}]}}`,
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := parseCodexFile(p, map[string]codexIndexEntry{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Title == "(无标题)" || s.Title == "" {
		t.Fatalf("标题兜底失败：%q", s.Title)
	}
	if s.Title != "$@" {
		t.Errorf("标题 = %q, want %q", s.Title, "$@")
	}
	if s.ProjectRaw != `D:\4rchive\Code\dep-view` {
		t.Errorf("项目 = %q", s.ProjectRaw)
	}
}
