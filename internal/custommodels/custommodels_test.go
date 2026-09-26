package custommodels

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/usage"
)

func lm(id string, free bool) usage.LiveModel {
	return usage.LiveModel{ID: id, Name: id, FreeNow: free}
}

// TestSyncAddsFreeOnlyAndPreservesUserEntries 钉住三条规则：
// 只注入限时免费、用户自己的条目原样保留、旧注入条目整体替换不残留。
func TestSyncAddsFreeOnlyAndPreservesUserEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	user := map[string]any{"id": "gpt-5.6-sol", "vendor": "Custom", "apiKey": "user-key"}
	old := map[string]any{"id": IDPrefix + "stale", "vendor": "Custom"}
	_ = os.WriteFile(path, mustJSON([]map[string]any{user, old}), 0o644)

	added, err := Sync(path, "http://127.0.0.1:8817/v1/chat/completions", "tok",
		[]usage.LiveModel{lm("deepseek-v4.1-flash", true), lm("gpt-6-astra", false)})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if added != 1 {
		t.Fatalf("只应注入 1 个限时免费模型，得到 %d", added)
	}
	raw, _ := os.ReadFile(path)
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("写后应可解析: %v", err)
	}
	var ids []string
	for _, m := range list {
		ids = append(ids, m["id"].(string))
		if m["id"] == "gpt-5.6-sol" && m["apiKey"] != "user-key" {
			t.Errorf("用户条目被改动")
		}
	}
	want := IDPrefix + "deepseek-v4.1-flash"
	found := false
	stale := false
	for _, id := range ids {
		if id == want {
			found = true
		}
		if id == IDPrefix+"stale" {
			stale = true
		}
	}
	if !found {
		t.Errorf("注入条目缺失：%v", ids)
	}
	if stale {
		t.Errorf("旧注入条目应被替换掉：%v", ids)
	}
	if _, err := os.Stat(path + ".wbmux-bak"); err != nil {
		t.Errorf("应留下写前备份")
	}
}

// TestSyncMissingFileCreatesIt 文件不存在时（新客户端）应能凭空创建。
func TestSyncMissingFileCreatesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	added, err := Sync(path, "http://x/v1/chat/completions", "tok", []usage.LiveModel{lm("hy3", true)})
	if err != nil || added != 1 {
		t.Fatalf("added=%d err=%v", added, err)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "hy3") {
		t.Errorf("文件应包含注入条目")
	}
}

// mustJSON 序列化辅助（失败直接 panic——测试前置数据坏了就该炸）。
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
