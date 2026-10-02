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
)

// newRecycleTestServer 起一个只用来打接口的服务，并把设置目录指到临时目录。
//
// **必须隔离**：这几个用例会真的往 config.json 里写开关，不隔离就会改到
// 用户真实的 ~/.wbmux/（GuardRealWrite 会当场 panic，但更好的做法是不让它
// 有机会碰）。
func newRecycleTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(config.SetRoot(root))
	srv, err := New(Options{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// TestAutoCleanPersists 钉住 2026-10-02 修的那个 bug：自动清理开关必须落盘。
//
// 原本它只存在内存的包级变量里。用户勾上、关掉界面、再打开，发现自己被
// 悄悄关掉了——而他勾它的理由正是"别再往回收站里塞"。这种"设置自己会
// 消失"比没有这个设置更糟。
func TestAutoCleanPersists(t *testing.T) {
	srv := newRecycleTestServer(t)

	post := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/recycle/auto", strings.NewReader(body))
		req.Header.Set("X-Wbmux-Token", srv.token)
		w := httptest.NewRecorder()
		srv.handleRecycleAuto(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 %d：%s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	defer stopAutoClean()

	post(`{"enabled":true}`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoCleanRecycle == nil {
		t.Fatal("开启没写进配置——重启后这个开关会自己消失")
	}
	if !*cfg.AutoCleanRecycle {
		t.Error("配置里记的是关闭，但接口刚才是开着的")
	}

	// 关掉同样要落盘，否则下次启动又变成开的
	post(`{"enabled":false}`)
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoCleanRecycle == nil {
		t.Fatal("关闭没落盘")
	}
	if *cfg.AutoCleanRecycle {
		t.Error("配置里记的还是开启")
	}
}

// TestAutoCleanDefaultsOff 确认默认是关的：它会删东西，不该默认开着。
func TestAutoCleanDefaultsOff(t *testing.T) {
	newRecycleTestServer(t)
	if autoCleanOn() {
		t.Error("没设过时必须是关闭的")
	}
}

// TestAutoCleanOffOnBadConfig 配置读不出来时按关闭处理。
//
// 方向是刻意的：环境有问题时不该还在定时删用户的回收站。
func TestAutoCleanOffOnBadConfig(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(config.SetRoot(root))

	// 往真实位置写一份坏 JSON，逼 Load 失败
	bad := filepath.Join(root, "config.json")
	if err := os.WriteFile(bad, []byte("{ 这不是 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if autoCleanOn() {
		t.Error("配置损坏时应按关闭处理，而不是照着默认值开启")
	}
}

// TestStopAutoCleanIdempotent 重复停必须安全。
//
// stopAutoClean 会 close 停止通道，对已关闭的通道再 close 一次是 panic。
// 服务关闭与开关切换都可能走到这里。
func TestStopAutoCleanIdempotent(t *testing.T) {
	newRecycleTestServer(t)

	stopAutoClean() // 没在跑的时候停一次

	startAutoClean()
	stopAutoClean()
	stopAutoClean() // 再来一次：必须不 panic

	// stopAutoClean 把 autoStop 置 nil 了，这里必须能重新拉起来
	startAutoClean()
	stopAutoClean()
}

// TestStartAutoCleanOnlyOnce 重复 start 不会起出第二个循环。
//
// 两个循环同时扫盘 = 清理并发跑，而 Clean 是成对删 $I/$R 的，
// 并发下去可能删到一半。
func TestStartAutoCleanOnlyOnce(t *testing.T) {
	newRecycleTestServer(t)
	defer stopAutoClean()

	startAutoClean()
	autoMu.Lock()
	first := autoStop
	autoMu.Unlock()

	startAutoClean()
	autoMu.Lock()
	second := autoStop
	autoMu.Unlock()

	if first == nil || second == nil {
		t.Fatal("循环没起来")
	}
	if first != second {
		t.Error("重复 start 起了第二个循环——同一时刻会有两个在扫盘")
	}
}

// TestResumeAutoCleanFollowsConfig 启动时按配置恢复开关。
func TestResumeAutoCleanFollowsConfig(t *testing.T) {
	srv := newRecycleTestServer(t)
	defer stopAutoClean()

	// 先存成"开"，再让一个新的服务实例去恢复
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	on := true
	cfg.AutoCleanRecycle = &on
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	srv.resumeAutoClean()
	if !autoSnapshot().Enabled {
		t.Error("配置里是开的，但启动后没恢复")
	}
	autoMu.Lock()
	started := autoStop != nil
	autoMu.Unlock()
	if !started {
		t.Error("恢复成开启后，后台循环却没起来")
	}
}
