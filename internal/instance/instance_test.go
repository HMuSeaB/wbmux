package instance

import (
	"net"
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

// listen 起一个真实监听并返回地址，用完自动关闭。
//
// 判活是靠"能不能连上端口"，所以测试必须给出一个真的在听的地址，
// 不能拿假地址糊弄——那样验的是相反的行为。
func listen(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func TestLookupWithoutMarker(t *testing.T) {
	withTempConfigDir(t)
	if _, ok := Lookup(); ok {
		t.Error("没有标记文件时不该报告「已有实例」")
	}
}

func TestClaimThenLookup(t *testing.T) {
	withTempConfigDir(t)
	addr := listen(t)

	want := Info{Addr: addr, URL: "http://" + addr + "/?t=abc", PID: os.Getpid()}
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

// TestLookupIgnoresStaleMarker 是这一块最要紧的回归：
// 标记残留但服务已死时必须报告"没人跑"，否则用户会永远打不开界面
// ——程序以为有人在，实际那个端口早就没了。
func TestLookupIgnoresStaleMarker(t *testing.T) {
	withTempConfigDir(t)

	// 先占一个端口再放掉，拿到一个确定没人监听的地址
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("起监听失败: %v", err)
	}
	deadAddr := ln.Addr().String()
	_ = ln.Close()

	if err := Claim(Info{Addr: deadAddr, URL: "http://" + deadAddr + "/", PID: 999999}); err != nil {
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

// TestReleaseOnlyRemovesOwnMarker 覆盖"上一个进程退出晚于新实例启动"的顺序：
// 旧进程的 Release 不能把新实例的标记抹掉。
func TestReleaseOnlyRemovesOwnMarker(t *testing.T) {
	dir := withTempConfigDir(t)
	addr := listen(t)

	old := Info{Addr: addr, URL: "http://old/", PID: 111}
	newer := Info{Addr: addr, URL: "http://new/", PID: 222}

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
