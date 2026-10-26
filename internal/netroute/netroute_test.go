package netroute

import (
	"net"
	"net/http"
	"net/url"
	"testing"
)

// TestMatchesAny 覆盖 ProxyOverride / NO_PROXY 的几种写法。
func TestMatchesAny(t *testing.T) {
	cases := []struct {
		host, pat string
		want      bool
	}{
		{"example.com", "*example.com", true},
		{"a.example.com", "*example.com", true},
		{"notexample.com", "*example.com", true}, // Windows 的语义就是后缀匹配
		{"10.1.2.3", "10.*", true},
		{"11.1.2.3", "10.*", false},
		{"foo", "<local>", true},
		{"foo.bar", "<local>", false},
		{"localhost", "localhost", true},
		{"localhost", "127.0.0.1", false},
	}
	for _, c := range cases {
		if got := matchesAny(c.host, []string{c.pat}); got != c.want {
			t.Errorf("matchesAny(%q, %q) = %v，期望 %v", c.host, c.pat, got, c.want)
		}
	}
}

// TestProbeProxyRefusesDeadPort 是这个修复的核心回归：
// 端口上没有监听时必须判定为不可用，让调用方降级直连，
// 而不是把"连接被拒绝"原样吐给用户。
func TestProbeProxyRefusesDeadPort(t *testing.T) {
	// 监听再立刻关掉，拿到一个当前肯定没人用的端口。
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("起不了本地监听：", err)
	}
	dead := l.Addr().String()
	_ = l.Close()

	u, _ := url.Parse("http://" + dead)
	ok, why := probeProxy(u)
	if ok {
		t.Fatalf("端口 %s 没人监听，不该判定为可用", dead)
	}
	if why == "" {
		t.Error("不可用时要给出原因，否则界面上没法解释为什么直连")
	}
}

// TestProbeProxyAcceptsLivePort 反过来：活着的端口要认。
func TestProbeProxyAcceptsLivePort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("起不了本地监听：", err)
	}
	defer l.Close()

	u, _ := url.Parse("http://" + l.Addr().String())
	if ok, why := probeProxy(u); !ok {
		t.Fatalf("端口 %s 在监听，应判定可用，实际 %s", l.Addr(), why)
	}
}

// TestTransportUsesResolvedProxy 保证 Transport 真的把 Resolve 的结果接上，
// 而不是又退回 ProxyFromEnvironment。
func TestTransportUsesResolvedProxy(t *testing.T) {
	Invalidate()
	defer Invalidate()
	tr := Transport()
	if tr.Proxy == nil {
		// 一台既没环境变量也没系统代理的机器上，Proxy 允许为 nil（直连）。
		c := Resolve()
		if c.Source != "direct" {
			t.Fatalf("Proxy 为 nil 但 Resolve 判定走 %s，说明没接上", c.Source)
		}
		return
	}
	req, _ := http.NewRequest("GET", "https://www.workbuddy.ai/v3/config", nil)
	if _, err := tr.Proxy(req); err != nil {
		t.Fatalf("Proxy 回调出错：%v", err)
	}
}
