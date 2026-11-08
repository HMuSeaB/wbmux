package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// TestHandleInjectCNIntoIntl 端到端验证 /api/inject-cn-into-intl 接口：
// 1. 仅允许 POST 方法
// 2. 正确从 CN 获取模型并反向注入国际客户端数据目录的 models.json
// 3. 过滤多媒体模型，正确计算并返回注入数量与说明
func TestHandleInjectCNIntoIntl(t *testing.T) {
	tempRoot := t.TempDir()
	restoreCfg := config.SetRoot(tempRoot)
	defer restoreCfg()

	// 准备模拟的国际客户端数据目录（~/.workbuddy-ai）
	mockIntlDataDir := filepath.Join(tempRoot, ".workbuddy-ai")
	if err := os.MkdirAll(mockIntlDataDir, 0o755); err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}

	probe := &variant.Probe{Home: tempRoot}

	srv, err := New(Options{Token: "test-token", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 模拟实时模型自省
	srv.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{
			string(variant.CN): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "minimax-m2.5", Name: "MiniMax-M2.5", Tools: true, Ctx: 200000},
					{ID: "glm-5.1", Name: "GLM-5.1", Tools: true, Ctx: 256000},
					{ID: "kling-video-v1", Name: "可灵生视频"}, // 多媒体模型，应被过滤
				},
			},
		}
	}

	// 1. GET 请求应被拒绝 (405)
	reqGet := httptest.NewRequest(http.MethodGet, "/api/inject-cn-into-intl", nil)
	recGet := httptest.NewRecorder()
	srv.handleInjectCNIntoIntl(recGet, reqGet)
	if recGet.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 请求期望 405，得到 %d", recGet.Code)
	}

	// 2. POST 请求应执行反向注入
	reqPost := httptest.NewRequest(http.MethodPost, "/api/inject-cn-into-intl", strings.NewReader("{}"))
	recPost := httptest.NewRecorder()
	srv.handleInjectCNIntoIntl(recPost, reqPost)

	if recPost.Code != http.StatusOK {
		t.Fatalf("POST 请求期望 200，得到 %d: %s", recPost.Code, recPost.Body.String())
	}

	var res struct {
		Added int    `json:"added"`
		Note  string `json:"note"`
	}
	if err := json.Unmarshal(recPost.Body.Bytes(), &res); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}

	// 期望注入 2 个（过滤掉 kling-video-v1）
	if res.Added != 2 {
		t.Errorf("期望注入 2 个模型，实际注入 %d 个", res.Added)
	}
	if !strings.Contains(res.Note, "国内精选 2") {
		t.Errorf("说明文本应包含国内精选数量信息，得到: %s", res.Note)
	}

	// 3. 验证写入磁盘的 models.json 内容
	modelsFile := filepath.Join(mockIntlDataDir, "models.json")
	raw, err := os.ReadFile(modelsFile)
	if err != nil {
		t.Fatalf("未能读取生成的 models.json: %v", err)
	}

	var doc struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 models.json 失败: %v", err)
	}

	if len(doc.Models) != 2 {
		t.Fatalf("models.json 期望包含 2 个模型，实际得到 %d", len(doc.Models))
	}

	for _, m := range doc.Models {
		id, _ := m["id"].(string)
		if !strings.HasPrefix(id, custommodels.IDPrefixCN) {
			t.Errorf("注入模型 ID 应以 %s 为前缀，得到 %s", custommodels.IDPrefixCN, id)
		}
		if cw, ok := m["contextWindow"].(map[string]any); !ok {
			t.Errorf("模型 %s 缺少 contextWindow 结构", id)
		} else {
			lengths, ok := cw["supportedLengths"].([]any)
			if !ok || len(lengths) < 2 || len(lengths) > 3 {
				t.Errorf("模型 %s 的 supportedLengths 阶梯必须为 2~3 档，得到 %v", id, lengths)
			}
		}
	}
}

// TestHandleCNModelSelection 验证国内专属模型自选与精选管理接口：
// 1. GET 获取初始精选白名单与自选列表
// 2. POST 切换单模型勾选并持久化
// 3. POST 重置为精选模式
func TestHandleCNModelSelection(t *testing.T) {
	tempRoot := t.TempDir()
	restoreCfg := config.SetRoot(tempRoot)
	defer restoreCfg()

	mockIntlDataDir := filepath.Join(tempRoot, ".workbuddy-ai")
	_ = os.MkdirAll(mockIntlDataDir, 0o755)

	probe := &variant.Probe{Home: tempRoot}
	srv, err := New(Options{Token: "test-token", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	srv.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{
			string(variant.CN): {
				OK: true,
				Models: []usage.LiveModel{
					{ID: "minimax-m2.5", Ctx: 200000},
					{ID: "glm-5.1", Ctx: 200000},
					{ID: "kimi-k2.6", Ctx: 256000},
				},
			},
		}
	}

	// 1. GET 查询
	reqGet := httptest.NewRequest(http.MethodGet, "/api/custom-models/cn-selection", nil)
	recGet := httptest.NewRecorder()
	srv.handleCNModelSelection(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("GET 状态码期望 200，得到 %d", recGet.Code)
	}
	var getResp struct {
		Selected []string `json:"selected"`
		Curated  []string `json:"curated"`
	}
	_ = json.Unmarshal(recGet.Body.Bytes(), &getResp)
	if len(getResp.Curated) == 0 {
		t.Errorf("期望返回默认精选列表")
	}

	// 2. POST 切换勾选 glm-5.1（从默认精选中移出）
	bodyToggle := `{"model":"glm-5.1","action":"toggle"}`
	reqToggle := httptest.NewRequest(http.MethodPost, "/api/custom-models/cn-selection", strings.NewReader(bodyToggle))
	recToggle := httptest.NewRecorder()
	srv.handleCNModelSelection(recToggle, reqToggle)
	if recToggle.Code != http.StatusOK {
		t.Fatalf("POST 状态码期望 200，得到 %d: %s", recToggle.Code, recToggle.Body.String())
	}
	var toggleResp struct {
		Ok       bool     `json:"ok"`
		Selected []string `json:"selected"`
		Added    int      `json:"added"`
	}
	_ = json.Unmarshal(recToggle.Body.Bytes(), &toggleResp)
	if !toggleResp.Ok {
		t.Errorf("响应 ok 期望为 true")
	}
	// 验证 glm-5.1 是否被移出
	for _, m := range toggleResp.Selected {
		if m == "glm-5.1" {
			t.Errorf("glm-5.1 应被 toggle 移出，但仍在列表中")
		}
	}

	// 3. POST 重置为精选模式
	bodyReset := `{"action":"reset"}`
	reqReset := httptest.NewRequest(http.MethodPost, "/api/custom-models/cn-selection", strings.NewReader(bodyReset))
	recReset := httptest.NewRecorder()
	srv.handleCNModelSelection(recReset, reqReset)
	if recReset.Code != http.StatusOK {
		t.Fatalf("Reset 状态码期望 200，得到 %d", recReset.Code)
	}
	var resetResp struct {
		Selected []string `json:"selected"`
	}
	_ = json.Unmarshal(recReset.Body.Bytes(), &resetResp)
	if len(resetResp.Selected) != 0 {
		t.Errorf("重置后 Selected 期望为空切回精选模式，实际得到 %v", resetResp.Selected)
	}
}
