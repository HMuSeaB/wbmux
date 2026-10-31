package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsLoosePath(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{`C:\Users\36230\WorkBuddy\2026-10-31-14-44-30`, true},
		{`c:\users\36230\workbuddy\2026-09-19-17-39-54`, true},
		{`C:/Users/36230/WorkBuddy AI/2026-09-25-11-15-46`, true},
		{`D:\4rchive\Code\SlateTerm`, false},
		{`d:\Tools\IP-Tester`, false},
		{`d:\Learning\UN\专业课\细胞生物学`, false},
		{"", true},
	}

	for _, tc := range cases {
		got := IsLoosePath(tc.in)
		if got != tc.want {
			t.Errorf("IsLoosePath(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestClientSlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`D:\4rchive\Code\SlateTerm`, `d-4rchive-Code-SlateTerm`},
		{`C:\Users\36230\WorkBuddy\2026-10-31-14-44-30`, `c-Users-36230-WorkBuddy-2026-10-31-14-44-30`},
		{`D:/4rchive/Campus`, `d-4rchive-Campus`},
		{`C:\Users\36230\WorkBuddy AI`, `c-Users-36230-WorkBuddy AI`},
	}

	for _, tc := range cases {
		got := ClientSlug(tc.in)
		if got != tc.want {
			t.Errorf("ClientSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFindSessionFiles(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "projects", "c-Users-test-2026-10-31")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sid := "71fafe08-8ddb-47f9-b81e-3c0ddaed0714"
	jsonl := filepath.Join(projDir, sid+".jsonl")
	ndjson := filepath.Join(projDir, sid+".file-rollback.ndjson")
	subDir := filepath.Join(projDir, sid)

	_ = os.WriteFile(jsonl, []byte("line1\n"), 0o644)
	_ = os.WriteFile(ndjson, []byte("rollback\n"), 0o644)
	_ = os.MkdirAll(subDir, 0o755)
	_ = os.WriteFile(filepath.Join(subDir, "agent.txt"), []byte("agent"), 0o644)

	foundDir, matched, err := findSessionFiles(tmpDir, sid)
	if err != nil {
		t.Fatalf("findSessionFiles error: %v", err)
	}
	if foundDir != projDir {
		t.Errorf("foundDir = %q, want %q", foundDir, projDir)
	}
	if len(matched) != 3 {
		t.Errorf("len(matched) = %d, want 3", len(matched))
	}
}

func TestCanonicalizeTargetCwd(t *testing.T) {
	// 针对无 Probe 时的默认格式化分支进行校验（确保 Windows 盘符强制大写与斜杠归一）
	cases := []struct {
		in   string
		want string
	}{
		{`d:\4rchive\Code\SlateTerm`, `D:\4rchive\Code\SlateTerm`},
		{`d:/4rchive/Code/SlateTerm/`, `D:\4rchive\Code\SlateTerm`},
		{`c:\test`, `C:\test`},
		{`c:`, `C:\`},
	}
	for _, tc := range cases {
		got := CanonicalizeTargetCwd(nil, "", tc.in)
		if got != tc.want {
			t.Errorf("CanonicalizeTargetCwd(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
