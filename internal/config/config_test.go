package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSaveLoadKeepsGUICredentials 钉住"界面地址与令牌要能存下来"：
// 它们是注入进国内客户端的模型条目里的 URL 与 API Key，丢了就等于
// 客户端那边又要重启一次（见 Config.GUIToken 的注释）。
func TestSaveLoadKeepsGUICredentials(t *testing.T) {
	restore := SetRoot(t.TempDir())
	defer restore()

	want := Config{
		HostVariant:    "intl",
		ExtraEndpoints: []string{"https://example.com"},
		GUIToken:       "f47b229fd206954736b7dcbb2eb0dc18",
		GUIAddr:        "127.0.0.1:8811",
	}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.GUIToken != want.GUIToken || got.GUIAddr != want.GUIAddr {
		t.Errorf("界面地址/令牌没存住：%+v", got)
	}
	// 其它字段也要原样保留，别因为加了新字段就把老的丢了。
	if got.HostVariant != want.HostVariant || len(got.ExtraEndpoints) != 1 {
		t.Errorf("原有字段被改动：%+v", got)
	}
}

// TestSaveLocksDownPermissions 设置文件里有界面令牌，按凭据对待：
// 写出来的文件不能是"谁都能读"的 0644。
func TestSaveLocksDownPermissions(t *testing.T) {
	dir := t.TempDir()
	restore := SetRoot(dir)
	defer restore()

	if err := Save(Config{GUIToken: "t"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	// Windows 上 Go 的权限位只映射只读位，0600 表达不出来，跳过该平台。
	if runtime.GOOS == "windows" {
		return
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("权限位应为 0600，得到 %o", perm)
	}
}

// TestLoadMissingFileIsNotAnError 首次运行没有设置文件，不该报错。
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	restore := SetRoot(t.TempDir())
	defer restore()

	got, err := Load()
	if err != nil {
		t.Fatalf("文件不存在时不该报错：%v", err)
	}
	if got.GUIToken != "" || got.GUIAddr != "" || got.HostVariant != "" || len(got.ExtraEndpoints) != 0 {
		t.Errorf("应返回零值，得到 %+v", got)
	}
}

// TestLoadCorruptFileReportsError 文件坏了要报错（让调用方决定怎么办），
// 而不是静默返回零值把用户的设置悄悄清掉。
func TestLoadCorruptFileReportsError(t *testing.T) {
	dir := t.TempDir()
	restore := SetRoot(dir)
	defer restore()

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Error("坏文件应当报错")
	}
}

// TestSaveOmitsEmptyFields 零值字段不落盘：设置文件是给人看的，
// 一排 "" 只会让人以为哪里坏了。
func TestSaveOmitsEmptyFields(t *testing.T) {
	dir := t.TempDir()
	restore := SetRoot(dir)
	defer restore()

	if err := Save(Config{GUIAddr: "127.0.0.1:8811"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("写出来不是 JSON：%v", err)
	}
	if _, ok := obj["guiToken"]; ok {
		t.Errorf("空字段不该出现：%s", raw)
	}
	if obj["guiAddr"] != "127.0.0.1:8811" {
		t.Errorf("非空字段应写入：%s", raw)
	}
}
