// Package netroute 统一 wbmux 出站请求的代理选择。
//
// 为什么需要统一：代理配置有三处来源，谁先谁后、失效了怎么办，
// 各写一遍必然分叉。历史上就分叉过一次——更新检查读了注册表代理，
// 而模型清单与登录授权只走环境变量，于是在"系统代理关着、环境变量还
// 指着旧端口"的机器上，同一个进程里有的请求能出去、有的出不去。
//
// 策略（见 Resolve）：
//
//	环境变量代理 → 注册表系统代理 → 直连
//
// 前两级都要先探活；探不通就降级，并把原因记进 Reason 里给人看。
// 这一条很关键：FlClash 这类工具退出时不会清理 HKCU 里的
// ProxyEnable=1 / ProxyServer=127.0.0.1:7890，照着配置硬连就会得到
// "No connection could be made because the target machine actively
// refused it"，而这时直连往往是通的。
package netroute

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	probeTimeout = 300 * time.Millisecond
	// probeCacheTTL 是"探测结论"的保鲜期。
	//
	// 别设长：代理进程的生死是按秒变的（Clash 切换节点、重启内核都会短暂
	// 断开）。缓存的唯一目的是避免同一轮里多次拨号，2 秒足够覆盖一批并发
	// 请求，又不会让"代理刚死"这件事被瞒太久。
	probeCacheTTL  = 2 * time.Second
	defaultDialTTL = 30 * time.Second
)

// pickCache 让同一轮请求不必反复拨号探活：一次启动要发模型清单、账单、
// 更新检查好几路请求，每个都拨一次代理端口既慢又难看。
var pickCache struct {
	sync.Mutex
	at  time.Time
	got Choice
}

// Choice 是 Resolve 的结果。
type Choice struct {
	Proxy  func(*http.Request) (*url.URL, error) // nil 表示直连
	Source string                                // "env" / "registry" / "direct"
	Reason string                                // 降级原因，正常情况下为空
}

// Resolve 按"环境变量 → 注册表 → 直连"的顺序选出一个能用的出口。
func Resolve() Choice {
	pickCache.Lock()
	defer pickCache.Unlock()
	if !pickCache.at.IsZero() && time.Since(pickCache.at) < probeCacheTTL {
		return pickCache.got
	}
	got := resolveUncached()
	pickCache.at = time.Now()
	pickCache.got = got
	return got
}

// Invalidate 让下一次 Resolve 重新探测（界面上手动重试时调用）。
func Invalidate() {
	pickCache.Lock()
	defer pickCache.Unlock()
	pickCache.at = time.Time{}
}

func resolveUncached() Choice {
	var reasons []string

	// ① 环境变量。注意 NO_PROXY 已经由 ProxyFromEnvironment 处理，
	// 这里只判"配了没有、端口活不活"。
	if raw := envProxy(); raw != "" {
		if u, err := url.Parse(raw); err == nil {
			if ok, why := probeProxy(u); ok {
				return Choice{Proxy: http.ProxyURL(u), Source: "env"}
			} else {
				reasons = append(reasons, fmt.Sprintf("环境变量代理 %s %s", raw, why))
			}
		}
	}

	// ② 注册表系统代理（Windows）。ProxyOverride 一并尊重：NO_PROXY 里
	// 写的 127.0.0.1/内网段不该被绕出去。
	if raw, override := systemProxy(); raw != "" {
		if u, err := url.Parse(raw); err == nil {
			if ok, why := probeProxy(u); ok {
				return Choice{Proxy: bypassFunc(u, override), Source: "registry"}
			} else {
				reasons = append(reasons, fmt.Sprintf("系统代理 %s %s", raw, why))
			}
		}
	}

	return Choice{Source: "direct", Reason: strings.Join(reasons, "；")}
}

// envProxy 返回第一个非空的环境变量代理地址。
func envProxy() string {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
				v = "http://" + v
			}
			return v
		}
	}
	return ""
}

// probeProxy 用 300ms 拨一下代理端口。只判可达性，不发请求：
// SOCKS 与 HTTP 代理都能被 TCP 握手测出来，而真发 HTTP 请求会引入
// 一次到远端的往返，不该出现在热路径上。
func probeProxy(u *url.URL) (bool, string) {
	host := u.Host
	if host == "" {
		return false, "地址无效"
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		if u.Scheme == "socks5" || u.Scheme == "socks5h" {
			host += ":1080"
		} else {
			host += ":80"
		}
	}
	conn, err := net.DialTimeout("tcp", host, probeTimeout)
	if err != nil {
		return false, "端口不通（" + shortErr(err) + "），已跳过"
	}
	_ = conn.Close()
	return true, ""
}

// shortErr 把 Go 的网络错误压成一句人话。
func shortErr(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "actively refused"):
		return "连接被拒绝，代理没在跑"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "Timed out"):
		return "连接超时"
	}
	return msg
}

// bypassFunc 在记住的代理之上加 NO_PROXY / ProxyOverride 判断。
func bypassFunc(proxy *url.URL, override string) func(*http.Request) (*url.URL, error) {
	noProxy := os.Getenv("NO_PROXY")
	if noProxy == "" {
		noProxy = os.Getenv("no_proxy")
	}
	patterns := append(splitList(noProxy), splitList(override)...)
	if len(patterns) == 0 {
		return http.ProxyURL(proxy)
	}
	return func(r *http.Request) (*url.URL, error) {
		if matchesAny(r.URL.Hostname(), patterns) {
			return nil, nil
		}
		return proxy, nil
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matchesAny 支持 Windows 那套通配写法：*example.com、10.*、<local>。
func matchesAny(host string, patterns []string) bool {
	host = strings.ToLower(host)
	for _, p := range patterns {
		if p == "<local>" {
			if !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if strings.HasPrefix(p, "*") {
			if strings.HasSuffix(host, strings.TrimPrefix(p, "*")) {
				return true
			}
			continue
		}
		if strings.HasSuffix(p, "*") {
			if strings.HasPrefix(host, strings.TrimSuffix(p, "*")) {
				return true
			}
			continue
		}
		if host == p {
			return true
		}
	}
	return false
}

// Transport 是带"探活降级"代理的 http.Transport，各处出站请求都应走它。
//
// 不直接用 http.DefaultTransport 的原因：DefaultTransport 的 Proxy 是
// ProxyFromEnvironment，它只看环境变量，不看端口死活——这正是 7890
// 那种"配置在、进程不在"的场景翻车的地方。
func Transport() *http.Transport {
	t := &http.Transport{
		// Proxy 必须是个**每次都重新求值**的函数，不能写成 Resolve().Proxy。
		//
		// 写成 Resolve().Proxy 的后果（2026-10-28 实测）：那是一次性求值，
		// 而 chatHTTPClient 是包级变量，进程启动时就固化了。此后代理进程
		// 退出、或启动瞬间代理还没起来，这个 Transport 会一直拿着坏代理
		// 直到重启——日志里那批 "proxyconnect ... 7890 refused" 就是这么来的。
		Proxy:                 proxyOf,
		DialContext:           (&net.Dialer{Timeout: defaultDialTTL, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return t
}

// Resolve().Proxy 为 nil 时（直连）要能安全解引用，所以包一层：
// 直接调用 Choice.Proxy 会 panic。
func proxyOf(r *http.Request) (*url.URL, error) {
	c := Resolve()
	if c.Proxy == nil {
		return nil, nil
	}
	return c.Proxy(r)
}

// Describe 给界面/日志用的一句话：走哪条路，有没有降级。
func Describe() string {
	c := Resolve()
	switch c.Source {
	case "env":
		return "经环境变量代理"
	case "registry":
		return "经系统代理"
	}
	if c.Reason != "" {
		return "直连（已跳过不可用的代理：" + c.Reason + "）"
	}
	return "直连"
}

// DetectedProxy 返回系统或环境变量中检测到的代理原始地址，未配置时返回空。
func DetectedProxy() string {
	if raw := envProxy(); raw != "" {
		return raw
	}
	if raw, _ := systemProxy(); raw != "" {
		return raw
	}
	return ""
}
