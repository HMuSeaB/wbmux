package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/ccswitch"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestHandleCCSwitchPreviewNotFound(t *testing.T) {
	tempRoot := t.TempDir()
	restoreCfg := config.SetRoot(tempRoot)
	defer restoreCfg()

	probe := &variant.Probe{Home: tempRoot}
	srv, err := New(Options{Token: "test", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{
			string(variant.CN): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "minimax-m2.5", Name: "MiniMax-M2.5", Tools: true},
				},
			},
			string(variant.Intl): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "gpt-4o", Name: "GPT-4o", Tools: true},
				},
			},
		}
	}

	// 1. GET不存在的路径
	req := httptest.NewRequest(http.MethodGet, "/api/providers/import/cc-switch/preview?path="+filepath.Join(tempRoot, "nonexistent.db"), nil)
	rec := httptest.NewRecorder()
	srv.handleCCSwitchPreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if res["found"] != false {
		t.Errorf("expected found=false, got %v", res["found"])
	}

	// 2. 拒绝非 GET 请求
	reqPost := httptest.NewRequest(http.MethodPost, "/api/providers/import/cc-switch/preview", nil)
	recPost := httptest.NewRecorder()
	srv.handleCCSwitchPreview(recPost, reqPost)
	if recPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 for POST, got %d", recPost.Code)
	}
}

func TestHandleCCSwitchApply(t *testing.T) {
	tempRoot := t.TempDir()
	restoreCfg := config.SetRoot(tempRoot)
	defer restoreCfg()

	probe := &variant.Probe{Home: tempRoot}
	srv, err := New(Options{Token: "test", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{
			string(variant.CN): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "minimax-m2.5", Name: "MiniMax-M2.5", Tools: true},
				},
			},
			string(variant.Intl): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "gpt-4o", Name: "GPT-4o", Tools: true},
				},
			},
		}
	}

	// 1. 空条目提交应报 400
	emptyBody := bytes.NewReader([]byte(`{"items":[]}`))
	reqBad := httptest.NewRequest(http.MethodPost, "/api/providers/import/cc-switch/apply", emptyBody)
	recBad := httptest.NewRecorder()
	srv.handleCCSwitchApply(recBad, reqBad)
	if recBad.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty items, got %d", recBad.Code)
	}

	// 2. 正常提交两个条目
	payload := map[string]any{
		"items": []ccswitch.ImportItem{
			{
				ID:               "1",
				Name:             "Provider A",
				BaseURL:          "https://a.com/v1",
				APIKey:           "key_a_12345",
				Models:           []string{"model-a1", "model-a2"},
				SupportsToolCall: true,
				SupportsImages:   false,
			},
			{
				ID:               "2",
				Name:             "Provider B",
				BaseURL:          "https://b.com/v1",
				APIKey:           "key_b_67890",
				Models:           []string{"model-b1"},
				SupportsToolCall: true,
				SupportsImages:   true,
			},
		},
		"overwrite": true,
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/providers/import/cc-switch/apply", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleCCSwitchApply(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		OK        bool           `json:"ok"`
		Added     int            `json:"added"`
		Updated   int            `json:"updated"`
		Total     int            `json:"total"`
		Providers []providerView `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.OK || res.Added != 2 || res.Total != 2 {
		t.Errorf("unexpected apply result: %+v", res)
	}

	// 校验配置已落盘
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load config: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Errorf("expected 2 providers in config, got %d", len(cfg.Providers))
	}
	if cfg.Providers[0].Name != "Provider A" || cfg.Providers[0].APIKey != "key_a_12345" {
		t.Errorf("provider 0 mismatch: %+v", cfg.Providers[0])
	}
}

func TestHandleCCSwitchPreviewLive(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	dbPath := filepath.Join(home, ".cc-switch", "cc-switch.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("local cc-switch.db not found, skipping live preview test")
	}

	tempRoot := t.TempDir()
	restoreCfg := config.SetRoot(tempRoot)
	defer restoreCfg()

	probe := variant.DefaultProbe()
	srv, err := New(Options{Token: "test", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{
			string(variant.CN): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "minimax-m2.5", Name: "MiniMax-M2.5", Tools: true},
				},
			},
			string(variant.Intl): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "gpt-4o", Name: "GPT-4o", Tools: true},
				},
			},
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/providers/import/cc-switch/preview", nil)
	rec := httptest.NewRecorder()
	srv.handleCCSwitchPreview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var res struct {
		OK    bool                  `json:"ok"`
		Found bool                  `json:"found"`
		Count int                   `json:"count"`
		Items []ccswitch.ImportItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !res.OK || !res.Found || res.Count == 0 {
		t.Errorf("expected ok=true, found=true, count>0, got %+v", res)
	}
	t.Logf("Live cc-switch preview endpoint returned %d items", res.Count)
}

func TestHandleCCSwitchExport(t *testing.T) {
	tempRoot := t.TempDir()
	restoreCfg := config.SetRoot(tempRoot)
	defer restoreCfg()

	probe := variant.DefaultProbe()
	srv, err := New(Options{Token: "test-token", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1. Preview
	reqPrev := httptest.NewRequest(http.MethodGet, "/api/providers/export/cc-switch/preview", nil)
	recPrev := httptest.NewRecorder()
	srv.handleCCSwitchExportPreview(recPrev, reqPrev)
	if recPrev.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recPrev.Code)
	}
	var prevRes struct {
		OK       bool                   `json:"ok"`
		Found    bool                   `json:"found"`
		Models   []ccswitch.ExportModel `json:"models"`
		AppTypes []string               `json:"appTypes"`
	}
	if err := json.Unmarshal(recPrev.Body.Bytes(), &prevRes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !prevRes.OK || len(prevRes.Models) == 0 {
		t.Errorf("unexpected preview models: %+v", prevRes)
	}

	// 2. Export Apply Live (if db exists)
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".cc-switch", "cc-switch.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("local cc-switch.db not found, skipping apply test")
	}

	applyPayload := map[string]any{
		"dbPath":   dbPath,
		"appTypes": []string{"claude"},
		"models": []map[string]any{
			{"id": "auto-free", "name": "智能免积分"},
		},
		"baseURL": "http://127.0.0.1:23333",
		"token":   "token-xyz",
	}
	body, _ := json.Marshal(applyPayload)
	reqApply := httptest.NewRequest(http.MethodPost, "/api/providers/export/cc-switch/apply", bytes.NewReader(body))
	recApply := httptest.NewRecorder()
	srv.handleCCSwitchExportApply(recApply, reqApply)
	if recApply.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", recApply.Code, recApply.Body.String())
	}
	defer func() {
		delStmts := []migrate.Statement{
			{SQL: "DELETE FROM providers WHERE id = ?", Params: []any{"wbmux-auto-free"}},
		}
		_, _ = migrate.DeleteRows(probe, variant.CN, dbPath, []string{"providers"}, delStmts)
	}()

	var applyRes struct {
		OK       bool   `json:"ok"`
		Inserted int    `json:"inserted"`
		Note     string `json:"note"`
	}
	if err := json.Unmarshal(recApply.Body.Bytes(), &applyRes); err != nil {
		t.Fatalf("unmarshal apply: %v", err)
	}
	if !applyRes.OK || applyRes.Inserted == 0 {
		t.Errorf("unexpected apply result: %+v", applyRes)
	}
}
