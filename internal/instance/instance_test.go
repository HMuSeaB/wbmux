package instance

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/config"
)

// withTempConfigDir 把运行标记安置到临时目录，避免动到用户真实的 ~/.wbmux。
func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := config.SetRoot(dir)
	t.Cleanup(restore)
	return dir
}

// serveFake 起一个"像 wbmux 那样"的服务：带对令牌打 /api/ping 返回 200，
// 否则 403。返回地址与带令牌的 URL。
//
// 判活现在要核对身份而不只是连通性，所以测试必须给出一个真的会答话的
// HTTP 服务，不能拿裸 TCP 监听糊弄。
func serveFake(t *testing.T) (addr, url string) {
	t.Helper()
	const token = "test-token"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ping" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Wbmux-Token") != token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), srv.URL + "/?t=" + token
}

// deadAddr 返回一个确定没人监听的地址。
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestLookupWithoutMarker(t *testing.T) {
	withTempConfigDir(t)
	if _, ok := Lookup(); ok {
		t.Error("没有标记文件时不该报告「已有实例」")
	}
}

func TestClaimThenLookup(t *testing.T) {
	withTempConfigDir(t)
	addr, url := serveFake(t)

	want := Info{Addr: addr, URL: url, PID: os.Getpid()}
	if err := Claim(want); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	got, ok := Lookup()
	if !ok {
		t.Fatal("刚登记完就该能查到，说明自己的实例没被识别")
	}
	if got.Addr != want.Addr || got.URL != want.URL || got.PID != want.PID {
		t.Errorf("查到的信息不对\n  得到 %+v\n  期望 %+v", got, want)
	}
}

// TestLookupRejectsWrongToken 是这一块最要紧的回归之一。
//
// 端口上确实有东西在监听、也确实答了 HTTP，但它不是 wbmux——令牌对不上。
// 必须判成「没人跑」，否则用户会被导向一个完全无关的服务，
// 看到的是一堆看不懂的响应。
func TestLookupRejectsWrongToken(t *testing.T) {
	withTempConfigDir(t)
	addr, _ := serveFake(t)

	// 标记里记的是**另一个**令牌，模拟"进程已退出、端口被别的程序接手"
	if err := Claim(Info{
		Addr: addr,
		URL:  "http://" + addr + "/?t=some-other-token",
		PID:  12345,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if _, ok := Lookup(); ok {
		t.Error("令牌对不上却仍被判成「wbmux 在跑」——用户会被导向陌生服务")
	}
}

// TestLookupIgnoresStaleMarker 覆盖"标记残留但服务已死"：
// 此时必须报告「没人跑」，否则会退化成「永远打不开」。
func TestLookupIgnoresStaleMarker(t *testing.T) {
	withTempConfigDir(t)
	dead := deadAddr(t)

	if err := Claim(Info{
		Addr: dead,
		URL:  "http://" + dead + "/?t=x",
		PID:  999999,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if _, ok := Lookup(); ok {
		t.Error("端口已经没人听，仍被当成「已有实例在跑」——这会让界面永远打不开")
	}
}

func TestLookupIgnoresCorruptMarker(t *testing.T) {
	dir := withTempConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{不是 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(); ok {
		t.Error("标记文件损坏时应按「没有实例」处理，而不是报错或误判")
	}
}

func TestLookupIgnoresIncompleteMarker(t *testing.T) {
	dir := withTempConfigDir(t)
	// 只有地址没有 URL：无法核对身份，应当按"没有实例"处理
	if err := os.WriteFile(filepath.Join(dir, fileName),
		[]byte(`{"addr":"127.0.0.1:1","pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Lookup(); ok {
		t.Error("信息不全的标记不该被采信")
	}
}

// TestReleaseOnlyRemovesOwnMarker 覆盖"上一个进程退出晚于新实例启动"的顺序：
// 旧进程的 Release 不能把新实例的标记抹掉。
func TestReleaseOnlyRemovesOwnMarker(t *testing.T) {
	dir := withTempConfigDir(t)
	addr, url := serveFake(t)

	old := Info{Addr: addr, URL: url, PID: 111}
	newer := Info{Addr: addr, URL: url, PID: 222}

	if err := Claim(old); err != nil {
		t.Fatal(err)
	}
	// 新实例接管
	if err := Claim(newer); err != nil {
		t.Fatal(err)
	}
	// 旧进程这时才走到 defer
	Release(old)

	got, ok := Lookup()
	if !ok {
		t.Fatal("旧进程的 Release 把新实例的标记删掉了——下次启动会多开一个窗口")
	}
	if got.PID != newer.PID {
		t.Errorf("标记被改动了: %+v", got)
	}

	// 自己删自己的应当生效
	Release(newer)
	if _, err := os.Stat(filepath.Join(dir, fileName)); !os.IsNotExist(err) {
		t.Error("自己的 Release 应当把标记删掉")
	}
}
