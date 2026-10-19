package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/autoupdate"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestClientUpdatePolicyAutoDetectAndCustomPath(t *testing.T) {
	restoreRoot := config.SetRoot(t.TempDir())
	defer restoreRoot()

	fakeHome := t.TempDir()
	// 模拟国内版数据目录
	fakeCNDataDir := filepath.Join(fakeHome, ".workbuddy")
	_ = os.MkdirAll(fakeCNDataDir, 0o755)

	probe := &variant.Probe{
		Home: fakeHome,
		Exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
	}

	srv, err := New(Options{Token: "test-token", Probe: probe})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// 1. GET 初始状态：未开启，但应自动探测到国内版数据目录对应的 app-config.json
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/client-update", nil)
	srv.handleClientUpdatePolicy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/client-update 状态码 = %d", rec.Code)
	}
	var res ClientUpdateResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if res.Enabled {
		t.Errorf("默认状态应为 false")
	}
	if len(res.Targets) == 0 {
		t.Fatalf("应自动探测到 .workbuddy 目标")
	}
	cnTarget := res.Targets[0]
	if !strings.Contains(cnTarget.Path, ".workbuddy") {
		t.Errorf("目标路径应包含 .workbuddy: %s", cnTarget.Path)
	}

	// 2. POST 开启保护，应写入并返回已开启，note 应提示成功保护
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/client-update", strings.NewReader(`{"enabled":true}`))
	srv.handleClientUpdatePolicy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/client-update 状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	res = ClientUpdateResult{}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.Enabled {
		t.Errorf("开启后 Enabled 应为 true")
	}
	if res.ModifiedCount != 1 {
		t.Errorf("首次写入 ModifiedCount 期望 1，得到 %d", res.ModifiedCount)
	}
	if strings.Contains(res.Note, "未检测到本地客户端目录") {
		t.Errorf("探测到了目录，Note 不应报未检测到: %s", res.Note)
	}

	// 验证文件真实落盘
	raw, err := os.ReadFile(cnTarget.Path)
	if err != nil {
		t.Fatalf("无法读取写入的 app-config.json: %v", err)
	}
	if !strings.Contains(string(raw), `"startupForceAutoUpdate": false`) && !strings.Contains(string(raw), `"startupForceAutoUpdate":false`) {
		t.Fatalf("写入的文件未包含 startupForceAutoUpdate: false: %s", string(raw))
	}

	// 3. 再次 POST 开启（幂等），ModifiedCount 应为 0，但 Note 绝不能误报“未检测到本地客户端目录”
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/client-update", strings.NewReader(`{"enabled":true}`))
	srv.handleClientUpdatePolicy(rec, req)
	res = ClientUpdateResult{}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if strings.Contains(res.Note, "未检测到本地客户端目录") {
		t.Errorf("幂等再次开启时，Note 误报未检测到目录: %s", res.Note)
	}
	if !strings.Contains(res.Note, "配置已生效") {
		t.Errorf("Note 应提示配置已生效: %s", res.Note)
	}

	// 4. 自定义路径测试：提供一个非标准目录
	customDir := t.TempDir()
	rec = httptest.NewRecorder()
	reqBody, _ := json.Marshal(map[string]any{
		"enabled":    true,
		"customPath": customDir,
	})
	req = httptest.NewRequest(http.MethodPost, "/api/client-update", strings.NewReader(string(reqBody)))
	srv.handleClientUpdatePolicy(rec, req)
	res = ClientUpdateResult{}
	_ = json.Unmarshal(rec.Body.Bytes(), &res)

	// 应该包含 custom 目标并在 customDir/app/app-config.json 生成了配置
	customAppConfig := filepath.Join(customDir, "app", "app-config.json")
	if _, err := os.Stat(customAppConfig); err != nil {
		t.Fatalf("自定义路径下未生成 app/app-config.json: %v", err)
	}
	rawCustom, _ := os.ReadFile(customAppConfig)
	if !strings.Contains(string(rawCustom), "startupForceAutoUpdate") {
		t.Errorf("自定义配置未正确写入: %s", string(rawCustom))
	}

	// 5. POST 关闭保护，恢复官方原生策略（键被删除）
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/client-update", strings.NewReader(`{"enabled":false}`))
	srv.handleClientUpdatePolicy(rec, req)
	rawAfter, _ := os.ReadFile(cnTarget.Path)
	if strings.Contains(string(rawAfter), autoupdate.Key) {
		t.Errorf("关闭保护后，键应被彻底删除: %s", string(rawAfter))
	}
}
