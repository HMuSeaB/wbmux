package custommodels

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/usage"
)

func lm(id string, free bool) usage.LiveModel {
	return usage.LiveModel{ID: id, Name: id, FreeNow: free}
}

// TestSyncCarriesOfficialCapabilities 钉住注入条目里的能力与上限来自
// 官方配置：supportsToolCall 写错成 false，国内客户端就会把请求体里的
// tools 删掉，Agent 直接废掉（2026-09-27 一并修掉的问题）。
func TestSyncCarriesOfficialCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	m := usage.LiveModel{
		ID: "deepseek-v4.1-flash", Name: "Deepseek-V4.1-Flash", FreeNow: true,
		Tools: true, Images: true, Ctx: 1000000, MaxOutput: 128000,
	}
	if _, err := Sync(path, "http://127.0.0.1:1/v1/chat/completions", "tok", []usage.LiveModel{m}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	raw, _ := os.ReadFile(path)
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("写后应可解析: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 条，得到 %d", len(list))
	}
	got := list[0]
	if got["supportsToolCall"] != true || got["supportsImages"] != true {
		t.Errorf("能力开关应照抄官方配置：%v", got)
	}
	if got["maxInputTokens"] != float64(1000000) || got["maxOutputTokens"] != float64(128000) {
		t.Errorf("上限应照抄官方配置：%v", got)
	}
	if got["url"] != "http://127.0.0.1:1/v1/chat/completions" || got["useCustomProtocol"] != true {
		t.Errorf("代理地址与协议标志不对：%v", got)
	}
}

// TestSyncOmitsUnknownLimits 官方没给上限时不能写 0——
// 客户端会把 0 当成"上限为零"，而不是"未知"。
func TestSyncOmitsUnknownLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if _, err := Sync(path, "http://x/v1/chat/completions", "tok", []usage.LiveModel{lm("hy3", true)}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "maxInputTokens") || strings.Contains(string(raw), "maxOutputTokens") {
		t.Errorf("未知上限不应写进条目：%s", raw)
	}
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
		[]usage.LiveModel{lm("deepseek-v4.1-flash", true), lm("gpt-6-astra", false)}, nil)
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
	added, err := Sync(path, "http://x/v1/chat/completions", "tok", []usage.LiveModel{lm("hy3", true)}, nil)
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

// TestSyncInjectsBYOKProviders 自备提供方也要按卡片清单注入，并且：
//   - 条目 id 带提供方前缀（代理靠它路由到对的那个端点）
//   - 条目里的 url/apiKey 指向**本机代理**，上游 key 绝不能落到客户端目录里
//   - 能力开关缺省为"支持工具、不支持图片"（写 false 会让客户端删掉 tools）
func TestSyncInjectsBYOKProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	const upstreamKey = "sk-upstream-should-not-leak"
	providers := []config.Provider{{
		ID:      "p1",
		Name:    "自备",
		BaseURL: "https://api.example.com/v1/chat/completions",
		APIKey:  upstreamKey,
		Models:  []string{"deepseek-chat", "qwen-max"},
	}}
	added, err := Sync(path, "http://127.0.0.1:9/v1/chat/completions", "gui-token", nil, providers)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if added != 2 {
		t.Fatalf("两张模型应各注入一条，得到 %d", added)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), upstreamKey) {
		t.Fatalf("上游 key 不该出现在客户端清单里：%s", raw)
	}
	var list []map[string]any
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("写后应可解析: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, m := range list {
		byID[m["id"].(string)] = m
	}
	entry, ok := byID["wbmux-byok-p1-deepseek-chat"]
	if !ok {
		t.Fatalf("缺少带提供方前缀的条目，实际有 %v", keysOf(byID))
	}
	if entry["url"] != "http://127.0.0.1:9/v1/chat/completions" || entry["apiKey"] != "gui-token" {
		t.Errorf("条目应指向本机代理：%v", entry)
	}
	if entry["supportsToolCall"] != true || entry["supportsImages"] != false {
		t.Errorf("能力缺省应为「支持工具、不支持图片」：%v", entry)
	}
	if name, _ := entry["name"].(string); !strings.Contains(name, "自备") || !strings.Contains(name, "deepseek-chat") {
		t.Errorf("名字应能让人认出是哪张卡片：%q", name)
	}
}

// TestSyncReplacesStaleBYOKEntries 卡片删掉后，旧条目也要跟着消失——
// 否则用户会把已经失效的模型留在选择器里点。
func TestSyncReplacesStaleBYOKEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	one := []config.Provider{{ID: "p1", Name: "A", BaseURL: "https://a/v1/chat/completions", APIKey: "k", Models: []string{"m1", "m2"}}}
	if _, err := Sync(path, "http://127.0.0.1:9/v1/chat/completions", "tok", nil, one); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	// 缩到只剩 m1（等价于用户改卡片时删了一个模型）
	shrunk := []config.Provider{{ID: "p1", Name: "A", BaseURL: "https://a/v1/chat/completions", APIKey: "k", Models: []string{"m1"}}}
	if _, err := Sync(path, "http://127.0.0.1:9/v1/chat/completions", "tok", nil, shrunk); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "m2") {
		t.Errorf("旧的 BYOK 条目应被清掉：%s", raw)
	}
}

func keysOf(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
