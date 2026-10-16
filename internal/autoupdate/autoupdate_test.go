package autoupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readDoc 把 app-config.json 读成 map[string]any，便于断言。
func readDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 %s: %v（内容 %s）", path, err, raw)
	}
	return doc
}

// TestEnsureWritesKey 首次写入：文件只有别的键，我们加自己的。
func TestEnsureWritesKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app-config.json")
	// 现状就是如此（2026-10-16 实测：只有 locale）
	_ = os.WriteFile(p, []byte("{\n  \"locale\": \"zh-CN\"\n}"), 0o644)

	changed, err := Ensure(p)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !changed {
		t.Fatal("首次写入应报告改动")
	}

	doc := readDoc(t, p)
	if doc["locale"] != "zh-CN" {
		t.Errorf("原有键必须原样保留，得到 %v", doc["locale"])
	}
	got, ok := doc[Key]
	if !ok {
		t.Fatalf("没写进 %s：%v", Key, doc)
	}
	if got != false {
		t.Errorf("%s 应为 false，得到 %v", Key, got)
	}
}

// TestEnsureIdempotent 已经是对的值就**一个字节都不写**。
//
// 这条不只是省事：每写一次别人的设置文件就多一次写坏它的机会，
// 所以"没东西可写"必须短路。
func TestEnsureIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app-config.json")
	body := "{\n  \"locale\": \"zh-CN\",\n  \"startupForceAutoUpdate\": false\n}\n"
	_ = os.WriteFile(p, []byte(body), 0o644)

	before, _ := os.Stat(p)
	changed, err := Ensure(p)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if changed {
		t.Error("已经是 false，不该报告改动")
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != body {
		t.Errorf("不该动文件，实际变成：%s", raw)
	}
	after, _ := os.Stat(p)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("不该重写文件（mtime 变了）")
	}
}

// TestEnsureOverridesTrue 键存在但是 true 时，要改过来。
func TestEnsureOverridesTrue(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app-config.json")
	_ = os.WriteFile(p, []byte(`{"startupForceAutoUpdate":true,"locale":"zh-CN"}`), 0o644)

	changed, err := Ensure(p)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !changed {
		t.Fatal("true 应被改掉")
	}
	if v := readDoc(t, p)[Key]; v != false {
		t.Errorf("应为 false，得到 %v", v)
	}
}

// TestEnsureCreatesMissing 文件不存在时从空文档建，不报错。
func TestEnsureCreatesMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app", "app-config.json") // 连目录都不存在

	changed, err := Ensure(p)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !changed {
		t.Fatal("应创建并写入")
	}
	if v := readDoc(t, p)[Key]; v != false {
		t.Errorf("应为 false，得到 %v", v)
	}
	if _, err := os.Stat(p + ".wbmux-bak"); !os.IsNotExist(err) {
		t.Error("原文件不存在时不该造备份")
	}
}

// TestEnsureBacksUpExisting 改动前要留一份原文可回溯。
func TestEnsureBacksUpExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app-config.json")
	orig := "{\n  \"locale\": \"zh-CN\"\n}"
	_ = os.WriteFile(p, []byte(orig), 0o644)

	if _, err := Ensure(p); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p + ".wbmux-bak")
	if err != nil {
		t.Fatalf("应留备份：%v", err)
	}
	if string(bak) != orig {
		t.Errorf("备份应是改动前的原文，得到 %s", bak)
	}
}

// TestEnsureRecoversFromCorrupt 内容坏了：留档并重建，不把坏内容当数据。
func TestEnsureRecoversFromCorrupt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app-config.json")
	_ = os.WriteFile(p, []byte("{oops"), 0o644)

	changed, err := Ensure(p)
	if err != nil {
		t.Fatalf("坏内容不该让 Ensure 失败：%v", err)
	}
	if !changed {
		t.Fatal("应重建")
	}
	if _, err := os.Stat(p + ".wbmux-corrupt"); err != nil {
		t.Error("坏内容应留档")
	}
	if v := readDoc(t, p)[Key]; v != false {
		t.Errorf("应为 false，得到 %v", v)
	}
}

// TestEnsureNullAndEmptyTreatAsNoContent null 与空文件都当"没有内容"。
//
// null 是个陷阱：它 Unmarshal 进 map 会成功并留下空 map，
// 不显式判断就可能被当成"用户写了一堆空配置"。
func TestEnsureNullAndEmptyTreatAsNoContent(t *testing.T) {
	for _, body := range []string{"null", "", "   \n"} {
		dir := t.TempDir()
		p := filepath.Join(dir, "app-config.json")
		_ = os.WriteFile(p, []byte(body), 0o644)

		changed, err := Ensure(p)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if !changed {
			t.Errorf("%q: 应写入", body)
		}
		if v := readDoc(t, p)[Key]; v != false {
			t.Errorf("%q: 应为 false，得到 %v", body, v)
		}
		// 空内容不是坏内容，不该留 corrupt 档。
		if _, err := os.Stat(p + ".wbmux-corrupt"); !os.IsNotExist(err) {
			t.Errorf("%q: 不该留 corrupt 档", body)
		}
	}
}

// TestEnsureLeavesNoTemp rename 之后不该留临时文件。
func TestEnsureLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app-config.json")
	if _, err := Ensure(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".wbmux-tmp"); !os.IsNotExist(err) {
		t.Error("不该留临时文件")
	}
}

// TestEnabled 读值：没设过时报 ok=false（客户端会落到安装目录那层）。
func TestEnabled(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app-config.json")

	if _, ok := Enabled(p); ok {
		t.Error("文件不存在时应报 ok=false")
	}
	_ = os.WriteFile(p, []byte(`{"locale":"zh-CN"}`), 0o644)
	if _, ok := Enabled(p); ok {
		t.Error("没设过这个键时应报 ok=false")
	}
	_ = os.WriteFile(p, []byte(`{"startupForceAutoUpdate":false}`), 0o644)
	if v, ok := Enabled(p); !ok || v {
		t.Errorf("应报 false/true，得到 %v/%v", v, ok)
	}
}
