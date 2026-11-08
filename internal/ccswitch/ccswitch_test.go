package ccswitch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestParseSettingsConfig(t *testing.T) {
	// 1. Claude
	claudeJSON := `{"env":{"ANTHROPIC_BASE_URL":"https://api.claude.proxy","ANTHROPIC_AUTH_TOKEN":"sk-ant-12345678","ANTHROPIC_DEFAULT_SONNET_MODEL":"claude-3-7-sonnet"}}`
	baseURL, key, models := ParseSettingsConfig("claude", "test-claude", claudeJSON)
	if baseURL != "https://api.claude.proxy" {
		t.Errorf("expected baseURL https://api.claude.proxy, got %s", baseURL)
	}
	if key != "sk-ant-12345678" {
		t.Errorf("expected key sk-ant-12345678, got %s", key)
	}
	if len(models) != 1 || models[0] != "claude-3-7-sonnet" {
		t.Errorf("expected models [claude-3-7-sonnet], got %v", models)
	}

	// 2. OpenCode
	opencodeJSON := `{"options":{"baseURL":"https://openrouter.ai/api/v1","apiKey":"sk-or-9999"},"models":{"minimax/m2":{"name":"MiniMax"},"deepseek/v3":{"name":"DeepSeek"}}}`
	baseURL, key, models = ParseSettingsConfig("opencode", "test-opencode", opencodeJSON)
	if baseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("expected baseURL https://openrouter.ai/api/v1, got %s", baseURL)
	}
	if key != "sk-or-9999" {
		t.Errorf("expected key sk-or-9999, got %s", key)
	}
	if len(models) != 2 {
		t.Errorf("expected 2 models, got %v", models)
	}

	// 3. Codex
	codexJSON := `{"auth":{"OPENAI_API_KEY":"sk-codex-1111"},"config":"model = \"gpt-5.5\"\nbase_url = \"https://mcgrox.top/v1\"\n"}`
	baseURL, key, models = ParseSettingsConfig("codex", "test-codex", codexJSON)
	if baseURL != "https://mcgrox.top/v1" {
		t.Errorf("expected baseURL https://mcgrox.top/v1, got %s", baseURL)
	}
	if key != "sk-codex-1111" {
		t.Errorf("expected key sk-codex-1111, got %s", key)
	}
	if len(models) != 1 || models[0] != "gpt-5.5" {
		t.Errorf("expected models [gpt-5.5], got %v", models)
	}

	// 4. Hermes
	hermesJSON := `{"base_url":"https://hermes.ai/v1","api_key":"sk-hermes","models":[{"id":"gpt-4o"}]}`
	baseURL, key, models = ParseSettingsConfig("hermes", "test-hermes", hermesJSON)
	if baseURL != "https://hermes.ai/v1" || key != "sk-hermes" || len(models) != 1 || models[0] != "gpt-4o" {
		t.Errorf("unexpected hermes parsed: %s, %s, %v", baseURL, key, models)
	}

	// 5. Fallback name as model
	fallbackJSON := `{"base_url":"https://test.ai"}`
	baseURL, key, models = ParseSettingsConfig("custom", "deepseek-v3", fallbackJSON)
	if baseURL != "https://test.ai" || len(models) != 1 || models[0] != "deepseek-v3" {
		t.Errorf("unexpected fallback parsed: %s, %v", baseURL, models)
	}
}

func TestApplyImport(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{
				ID:      "p_old",
				Name:    "Old Provider",
				BaseURL: "https://old.com/v1",
				APIKey:  "key1",
				Models:  []string{"m1"},
			},
		},
	}

	items := []ImportItem{
		{
			ID:               "1",
			Name:             "Old Provider",
			BaseURL:          "https://new.com/v1",
			APIKey:           "key_updated",
			Models:           []string{"m1_new"},
			SupportsToolCall: true,
			SupportsImages:   true,
		},
		{
			ID:               "2",
			Name:             "Brand New",
			BaseURL:          "https://brandnew.com/v1",
			APIKey:           "key2",
			Models:           []string{"bn1"},
			SupportsToolCall: true,
		},
	}

	// Test with overwrite = false
	added, updated, err := ApplyImport(cfg, items, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 1 || updated != 0 {
		t.Errorf("expected added=1, updated=0, got added=%d, updated=%d", added, updated)
	}
	if len(cfg.Providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(cfg.Providers))
	}
	// Old provider should remain untouched
	if cfg.Providers[0].BaseURL != "https://old.com/v1" {
		t.Errorf("expected old baseURL untouched, got %s", cfg.Providers[0].BaseURL)
	}

	// Test with overwrite = true on fresh cfg
	cfg2 := &config.Config{
		Providers: []config.Provider{
			{
				ID:      "p_old",
				Name:    "Old Provider",
				BaseURL: "https://old.com/v1",
				APIKey:  "key1",
				Models:  []string{"m1"},
			},
		},
	}
	added, updated, err = ApplyImport(cfg2, items, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added != 1 || updated != 1 {
		t.Errorf("expected added=1, updated=1, got added=%d, updated=%d", added, updated)
	}
	if cfg2.Providers[0].BaseURL != "https://new.com/v1" {
		t.Errorf("expected old provider updated to https://new.com/v1, got %s", cfg2.Providers[0].BaseURL)
	}
}

func TestPreviewFromDBLive(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no user home dir")
	}
	dbPath := filepath.Join(home, ".cc-switch", "cc-switch.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("local cc-switch.db not found, skipping live probe test")
	}

	probe := variant.DefaultProbe()
	items, err := PreviewFromDB(probe, dbPath)
	if err != nil {
		t.Fatalf("PreviewFromDB failed: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected items > 0, got 0")
	}
	t.Logf("Successfully previewed %d items from real cc-switch.db", len(items))
}

func TestExportToCCSwitchLive(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no user home dir")
	}
	dbPath := filepath.Join(home, ".cc-switch", "cc-switch.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Skip("local cc-switch.db not found, skipping live export test")
	}

	probe := variant.DefaultProbe()
	req := ExportRequest{
		DBPath:   dbPath,
		AppTypes: []string{"claude", "opencode", "codex"},
		Models: []ExportModel{
			{ID: "auto-free", Name: "智能免积分优选", Category: "auto", Selected: true},
			{ID: "wbmux-cn-minimax-m2.5", Name: "MiniMax-M2.5", Category: "cn", Selected: true},
		},
		BaseURL: "http://127.0.0.1:23333",
		Token:   "test-token",
	}

	inserted, err := ExportToCCSwitch(probe, req)
	if err != nil {
		t.Fatalf("ExportToCCSwitch failed: %v", err)
	}
	defer func() {
		delStmts := []migrate.Statement{
			{SQL: "DELETE FROM providers WHERE id LIKE ?", Params: []any{"wbmux-%"}},
		}
		_, _ = migrate.DeleteRows(probe, variant.CN, dbPath, []string{"providers"}, delStmts)
	}()
	if inserted == 0 {
		t.Errorf("expected inserted > 0, got 0")
	}
	t.Logf("ExportToCCSwitch inserted %d providers into cc-switch.db", inserted)
}

func TestListExportableModels(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{
				ID:     "p1",
				Name:   "StepFun",
				Models: []string{"step-5-preview"},
			},
		},
		SelectedIntlModels: []string{"deepseek-v4.1-flash-sg", "glm-5.3-flash"},
	}

	models := ListExportableModels(cfg, nil)
	if len(models) == 0 {
		t.Fatalf("expected non-empty models")
	}

	foundMap := make(map[string]ExportModel)
	for _, m := range models {
		foundMap[m.ID] = m
	}

	// 1. 验证智能免积分
	if m, ok := foundMap["auto-free"]; !ok || m.Category != "auto" || !m.Selected {
		t.Errorf("auto-free model missing or invalid: %+v", m)
	}

	// 2. 验证用户高频限免模型 hy4free 和 hy3
	if m, ok := foundMap["hy4free"]; !ok || m.Category != "free" || !m.Selected {
		t.Errorf("hy4free model missing or invalid: %+v", m)
	}
	if m, ok := foundMap["hy3"]; !ok || m.Category != "free" || !m.Selected {
		t.Errorf("hy3 model missing or invalid: %+v", m)
	}

	// 3. 验证用户高频花积分特惠模型 ds4.1flash 和 glm5.3 flash
	if m, ok := foundMap["deepseek-v4.1-flash-sg"]; !ok || m.Category != "intl" || !m.Selected {
		t.Errorf("deepseek-v4.1-flash-sg model missing or invalid: %+v", m)
	}
	if m, ok := foundMap["glm-5.3-flash"]; !ok || m.Category != "intl" || !m.Selected {
		t.Errorf("glm-5.3-flash model missing or invalid: %+v", m)
	}

	// 4. 验证杜绝旧虚假模型占位
	if _, ok := foundMap["deepseek-chat"]; ok {
		t.Errorf("obsolete deepseek-chat should NOT be present")
	}
	if _, ok := foundMap["gpt-4o"]; ok {
		t.Errorf("obsolete gpt-4o should NOT be present")
	}

	// 5. 验证国内精选模型（升级为 GLM-5.3）
	if m, ok := foundMap["wbmux-cn-minimax-m2.5"]; !ok || m.Category != "cn" || !m.Selected {
		t.Errorf("wbmux-cn-minimax-m2.5 model missing or invalid: %+v", m)
	}

	// 6. 验证 BYOK
	if m, ok := foundMap["wbmux-byok-p1-step-5-preview"]; !ok || m.Category != "byok" || m.Selected {
		t.Errorf("BYOK model missing or invalid: %+v", m)
	}

	// 7. 验证分端模式 (split mode)
	splitModels := ListExportableModels(cfg, nil, "split")
	splitMap := make(map[string]ExportModel)
	for _, m := range splitModels {
		splitMap[m.ID] = m
	}
	if m, ok := splitMap["wbmux-cn-glm-5.3"]; !ok || m.Channel != "cn" || !m.Selected {
		t.Errorf("split mode: wbmux-cn-glm-5.3 missing: %+v", m)
	}
	if m, ok := splitMap["wbmux-cn-hy3"]; !ok || m.Channel != "cn" || !m.Selected {
		t.Errorf("split mode: wbmux-cn-hy3 missing: %+v", m)
	}
	if m, ok := splitMap["hy3"]; !ok || m.Channel != "intl" || !m.Selected {
		t.Errorf("split mode: intl hy3 missing: %+v", m)
	}
}

func TestFormatProviderName(t *testing.T) {
	cases := []struct {
		m    ExportModel
		want string
	}{
		{ExportModel{ID: "wbmux-cn-glm-5.3", Name: "GLM-5.3", Channel: "cn"}, "WorkBuddy (国内) / GLM-5.3"},
		{ExportModel{ID: "hy4free", Name: "Hy4", Channel: "intl"}, "WorkBuddy (国际) / Hy4"},
		{ExportModel{ID: "auto-free", Name: "智能免积分", Channel: "auto"}, "WorkBuddy / 智能免积分"},
		{ExportModel{ID: "wbmux-byok-p1-step", Name: "step", Channel: "byok"}, "WorkBuddy (BYOK) / step"},
	}
	for _, c := range cases {
		got := formatProviderName(c.m)
		if got != c.want {
			t.Errorf("formatProviderName(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestExportProviderID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"auto-free", "wbmux-auto-free"},
		{"hy4free", "wbmux-hy4free"},
		{"wbmux-cn-glm-5.1", "wbmux-cn-glm-5-1"},
		{"wbmux-byok-p1-step-5-preview", "wbmux-byok-p1-step-5-preview"},
		{"deepseek-v4.1-flash-sg", "wbmux-deepseek-v4-1-flash-sg"},
	}
	for _, c := range cases {
		got := exportProviderID(c.in)
		if got != c.want {
			t.Errorf("exportProviderID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
