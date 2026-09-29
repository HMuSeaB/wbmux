package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// TestZCodeEndpoints 覆盖三个 ZCode 接口的正常与异常路径。
//
// 这里用**注入的 Probe**（Home 指向临时目录）而不是真实用户目录：
// 一来测试不该依赖本机装没装 ZCode，二来真实目录下跑会去读用户的真数据，
// 那既慢又没必要（读正文的逻辑在 internal/zcode 里另有单测）。
func TestZCodeEndpoints(t *testing.T) {
	// 探针要按 internal/migrate 里那套完整构造：Detect 会用到 ReadFile，
	// 少给一个就空指针（我第一次就是只给了 Home / Getenv）。
	home := t.TempDir()
	probe := &variant.Probe{
		GOOS:   "windows",
		Home:   home,
		Getenv: func(string) string { return "" },
		Exists: func(p string) bool {
			if !strings.HasPrefix(p, home) {
				return false
			}
			_, err := os.Stat(p)
			return err == nil
		},
		ReadFile: os.ReadFile,
	}

	srv, err := New(Options{Token: "tok", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// ① 没装 ZCode：要说清"本机没有"，而不是报错。
	rec := httptest.NewRecorder()
	srv.handleZCodeList(rec, httptest.NewRequest(http.MethodGet, "/api/zcode/list", nil))
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应不是 JSON：%v\n%s", err, rec.Body.String())
	}
	if res["available"] != false {
		t.Errorf("没有库时 available 应为 false，得到 %v", res["available"])
	}
	if note, _ := res["note"].(string); note == "" {
		t.Error("应当说明为什么没有（路径或原因）")
	}

	// ② 方法不对要 405，别静默接受。
	rec = httptest.NewRecorder()
	srv.handleZCodeList(rec, httptest.NewRequest(http.MethodPost, "/api/zcode/list", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 应得 405，得到 %d", rec.Code)
	}

	// ③ 预览缺 id → 400；导出缺 id → 400（错要说清是缺什么）。
	rec = httptest.NewRecorder()
	srv.handleZCodeShow(rec, httptest.NewRequest(http.MethodGet, "/api/zcode/show", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 id 应得 400，得到 %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/zcode/export", strings.NewReader(`{}`))
	srv.handleZCodeExport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("导出缺 id 应得 400，得到 %d", rec.Code)
	}

	// ④ 有库但 id 不存在 → 404，且报错里带上那个 id。
	dir := filepath.Join(home, ".zcode", "cli", "db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.sqlite"), []byte("not a real sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	srv.handleZCodeShow(rec, httptest.NewRequest(http.MethodGet, "/api/zcode/show?id=sess_missing", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("读不到时不该回 200：%s", rec.Body.String())
	}
}

// TestZImportEndpoint 导入接口的两段式流程：
// 第一次不带 confirm 只回说明（写作前提要让用户看清），带 confirm 才真写。
func TestZImportEndpoint(t *testing.T) {
	home := t.TempDir()
	probe := &variant.Probe{
		GOOS:   "windows",
		Home:   home,
		Getenv: func(string) string { return "" },
		Exists: func(p string) bool {
			if !strings.HasPrefix(p, home) {
				return false
			}
			_, err := os.Stat(p)
			return err == nil
		},
		ReadFile: os.ReadFile,
	}
	srv, err := New(Options{Token: "tok", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 方法不对 → 405
	rec := httptest.NewRecorder()
	srv.handleZImport(rec, httptest.NewRequest(http.MethodGet, "/api/zimport", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 应得 405，得到 %d", rec.Code)
	}

	// 缺 id → 400
	rec = httptest.NewRecorder()
	srv.handleZImport(rec, httptest.NewRequest(http.MethodPost, "/api/zimport", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("缺 id 应得 400，得到 %d", rec.Code)
	}

	// 档位写错 → 400（别静默当成 cn）
	rec = httptest.NewRecorder()
	srv.handleZImport(rec, httptest.NewRequest(http.MethodPost, "/api/zimport",
		strings.NewReader(`{"id":"sess_x","to":"mars"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("档位不合法应得 400，得到 %d", rec.Code)
	}

	// 不带 confirm → 200 + needConfirm + 说清"会写什么"
	rec = httptest.NewRecorder()
	srv.handleZImport(rec, httptest.NewRequest(http.MethodPost, "/api/zimport",
		strings.NewReader(`{"id":"sess_x","to":"cn"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("首次询问应得 200，得到 %d：%s", rec.Code, rec.Body.String())
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应不是 JSON：%v", err)
	}
	if res["needConfirm"] != true {
		t.Errorf("首次应要求确认，得到 %v", res)
	}
	note, _ := res["note"].(string)
	for _, want := range []string{"只增不改", "退出"} {
		if !strings.Contains(note, want) {
			t.Errorf("说明里应提到 %q：%s", want, note)
		}
	}
	// 关键：这个阶段不该写出任何东西。
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("仅询问时不该写文件，得到：%v", entries)
	}
}
