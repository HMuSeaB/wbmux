package launch

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// TestBuildArgsHasDebugPort 验证正常启动会带上调试端口。
//
// 这是"从 wbmux 里启动客户端就能注入面板"的前提：Chromium 的调试端口
// 只能在启动时开启，错过这次就要用户再关一次客户端。
func TestBuildArgsHasDebugPort(t *testing.T) {
	// 用系统分配的空闲端口，避免测试机上 9222 恰好被真实客户端占用。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	args := buildArgsWithPort(`C:\ud`, port)
	want := "--remote-debugging-port=" + strconv.Itoa(port)
	for _, a := range args {
		if a == want {
			return
		}
	}
	t.Fatalf("启动参数缺少 %q: %v", want, args)
}

// TestBuildArgsSkipsBusyPort 验证端口被占时不加调试参数。
// 注入是附加功能，不能反过来影响客户端本身能不能起来。
func TestBuildArgsSkipsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port

	args := buildArgsWithPort(`C:\ud`, port)
	for _, a := range args {
		if strings.HasPrefix(a, "--remote-debugging-port=") {
			t.Fatalf("端口被占用时不应带调试参数: %v", args)
		}
	}
}

// TestDebugPortDiffersByVariant 双开时两个档位必须用不同端口，否则互相抢。
func TestDebugPortDiffersByVariant(t *testing.T) {
	if DebugPort(variant.CN) == DebugPort(variant.Intl) {
		t.Fatalf("国内版与国际版必须使用不同调试端口，当前都是 %d", DebugPort(variant.CN))
	}
}
