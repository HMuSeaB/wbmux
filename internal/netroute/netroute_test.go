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

// TestTransportProxyIsReevaluated 钉住 2026-10-28 的回归：
// Transport 的 Proxy 必须每次请求重新求值，不能在建 Transport 时固化。
//
// 原实现写的是 `Proxy: Resolve().Proxy`——一次性求值。而 chatHTTPClient
// 是包级变量，进程启动时就把当时的代理决定焊死了。之后代理进程退出，
// 它仍然拿着那个坏代理去连，日志里表现为连续一批
// "proxyconnect tcp: dial tcp 127.0.0.1:7890: actively refused it"，
// 而其实直连是通的。
func TestTransportProxyIsReevaluated(t *testing.T) {
	// 拿两个都活着的端口：一个给"旧代理"，一个给"新代理"。
	oldPx, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("起不了本地监听：", err)
	}
	defer oldPx.Close()
	newPx, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("起不了本地监听：", err)
	}
	defer newPx.Close()

	t.Setenv("HTTPS_PROXY", "http://"+oldPx.Addr().String())
	t.Setenv("HTTP_PROXY", "http://"+oldPx.Addr().String())
	Invalidate()
	defer Invalidate()

	tr := Transport()
	req, _ := http.NewRequest("GET", "https://www.workbuddy.ai/v3/config", nil)
	u, err := tr.Proxy(req)
	if err != nil || u == nil {
		t.Fatalf("活端口应被选为代理，得到 %v / %v", u, err)
	}
	if u.Host != oldPx.Addr().String() {
		t.Fatalf("应走环境变量代理 %s，得到 %s", oldPx.Addr(), u.Host)
	}

	// 关键断言：换成新端口后，**同一个 Transport** 必须跟着变。
	// 若 Proxy 被固化成一次性求值，这里仍会返回旧端口，测试失败。
	t.Setenv("HTTPS_PROXY", "http://"+newPx.Addr().String())
	t.Setenv("HTTP_PROXY", "http://"+newPx.Addr().String())
	Invalidate()

	u2, err := tr.Proxy(req)
	if err != nil {
		t.Fatalf("Proxy 求值出错：%v", err)
	}
	if u2 == nil {
		t.Fatal("代理变了，同一个 Transport 却返回直连")
	}
	if u2.Host != newPx.Addr().String() {
		t.Errorf("应切到新代理 %s，仍停在 %s——Proxy 被固化了", newPx.Addr(), u2.Host)
	}
}
