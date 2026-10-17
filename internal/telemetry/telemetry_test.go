package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestDeviceIDPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	ResetCacheForTest()

	id1 := DeviceID(tmpDir)
	if len(id1) != 32 {
		t.Fatalf("DeviceID 长度应为 32，得到 %d: %s", len(id1), id1)
	}

	// 确认落盘文件存在
	targetFile := filepath.Join(tmpDir, ".wbmux", "device.id")
	raw, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("读取落盘的 device.id 失败: %v", err)
	}
	if string(raw) != id1 {
		t.Fatalf("文件内容不匹配: %s != %s", string(raw), id1)
	}

	// 清空内存缓存，再读一次确认幂等
	ResetCacheForTest()
	id2 := DeviceID(tmpDir)
	if id1 != id2 {
		t.Fatalf("重新加载时 DeviceID 不一致: %s != %s", id1, id2)
	}
}

func TestUserAgentSanitization(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		ua := UserAgent(goos)
		if strings.Contains(ua, "wbmux") {
			t.Errorf("UA 不应包含任何 wbmux 特征: %s", ua)
		}
		if !strings.Contains(ua, "Mozilla/5.0") || !strings.Contains(ua, "WorkBuddy") {
			t.Errorf("UA 格式异常: %s", ua)
		}
	}
}

func TestCommonHeaders(t *testing.T) {
	tmpDir := t.TempDir()
	ResetCacheForTest()

	hdrs := CommonHeaders(variant.Intl, tmpDir, "windows")
	if hdrs["X-Product"] != "workbuddy-ai" {
		t.Errorf("X-Product 错误: %s", hdrs["X-Product"])
	}
	if hdrs["X-Client-Version"] != "5.5.2" {
		t.Errorf("X-Client-Version 错误: %s", hdrs["X-Client-Version"])
	}
	if hdrs["X-Platform"] != "win32" {
		t.Errorf("X-Platform 错误: %s", hdrs["X-Platform"])
	}
	if len(hdrs["X-Device-Id"]) != 32 {
		t.Errorf("X-Device-Id 长度异常: %s", hdrs["X-Device-Id"])
	}
	if strings.Contains(hdrs["User-Agent"], "wbmux") {
		t.Errorf("UA 泄露了 wbmux 标识")
	}
}

func TestGuardianPingOnce(t *testing.T) {
	tmpDir := t.TempDir()
	ResetCacheForTest()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/config" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("Authorization 错误")
		}
		if r.Header.Get("X-User-Id") != "uid-12345" {
			t.Errorf("X-User-Id 错误")
		}
		if r.Header.Get("X-Product") != "workbuddy-ai" {
			t.Errorf("X-Product 错误")
		}
		if r.Header.Get("X-Device-Id") == "" {
			t.Errorf("X-Device-Id 为空")
		}
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer server.Close()

	g := NewGuardian(tmpDir, "windows", server.URL, func() (token, uid string, err error) {
		return "test-token", "uid-12345", nil
	})

	err := g.PingOnce(context.Background())
	if err != nil {
		t.Fatalf("PingOnce 失败: %v", err)
	}
	if !called {
		t.Fatal("服务端未收到请求")
	}

	st := g.GetStatus()
	if !st.LastPingOK || st.TotalPings != 1 {
		t.Fatalf("状态记录异常: %+v", st)
	}
}
