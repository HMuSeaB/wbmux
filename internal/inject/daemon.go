package inject

import (
	"context"
	"sync"
	"time"
)

// DefaultCDPPorts 是约定的调试端口：国内版 9222、国际版 9223
// （与 internal/launch.DebugPort 保持一致）。
//
// 守护必须两个都扫：用户可能只开国际版——那时代码里写死 9222 就会永远
// 连不上，表现为"我明明是从 wbmux 启动的，胶囊却没出现"（2026-11-16 实际踩到）。
var DefaultCDPPorts = []int{9222, 9223}

// StatsProvider 由调用方提供胶囊上的指标文字与告警等级。
//
// 分开成回调而不是在本包里读审计：inject 只负责"把东西挂上去"，
// 什么数据、怎么算，属于 wbmux 自己的业务。
type StatsProvider func() (text, level string)

// RunDaemon 常驻保持注入与指标刷新。
//
// 每 3 秒对每个端口做一次：连调试端口 → 读页面里的注入标记，不一致就重注入；
// 一致就把最新指标推进胶囊。覆盖三种情况而不需要任何用户操作：
//   - 客户端重启（页面里的 DOM 全没了）→ 重注入；
//   - wbmux 自己重启换了令牌 → 标记里带着旧 URL，对不上 → 用新地址重注入；
//   - 面板脚本升级 → 版本号变了 → 自动替换。
//
// 端口连不上（客户端没带调试参数、或该档位没开）就静默跳过——这是常态，
// 不该往日志里刷错误。
func RunDaemon(ctx context.Context, embedURL func() string, stats StatsProvider, onDetach func()) {
	attached := false
	run := func() {
		anyConnected := false
		url := embedURL()
		// 每轮现查：--remote-debugging-port=0 的会话重启后端口会变，
		// 缓存端口列表会让守护一直连旧端口。
		for _, p := range DiscoverPorts() {
			injected, connected := Sync(p, url, stats)
			_ = injected
			if connected {
				anyConnected = true
			}
		}
		// 从"挂着"变成"全断"：客户端重启后丢了调试端口，这是最常见的
		// 静默失效（胶囊突然消失、面板打不开）。提示一次，让用户知道
		// 不是坏了，只是需要从 wbmux 重启一次客户端。
		if attached && !anyConnected && onDetach != nil {
			onDetach()
		}
		attached = anyConnected
	}
	run()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// lastPushed 记住每个端口上次成功推进胶囊的 (text, level)：文本没变就
// 不推，省掉每 3 秒一次无意义的 CDP Runtime.evaluate。刚重注入的胶囊
// 除外——新 DOM 里还没有数字，哪怕文本和上次一样也得推一次。
var (
	lastPushedMu sync.Mutex
	lastPushed   = map[int]pushedStats{}
)

type pushedStats struct {
	text  string
	level string
}

// Sync 检查一次：按需注入，并把指标推进胶囊。
//
// 返回 injected 表示这一轮真的做了注入；connected 表示调试端口连通。
// 两者分开是因为调用方要判断"客户端还在、但面板掉了"——那时 connected
// 为 true 而 injected 可能一直 false。
func Sync(port int, embedURL string, stats StatsProvider) (injected, connected bool) {
	if embedURL == "" {
		return false, false
	}
	c, err := Dial(port, 2*time.Second)
	if err != nil {
		return false, false
	}
	defer c.Close()

	// 端口连上就算 connected，后面无论注入成功与否都不影响这个事实。
	injected = false
	tag, err := InstalledTag(c, 2*time.Second)
	if err != nil {
		return false, true
	}
	if tag != TagFor(embedURL) {
		if ok, err := Inject(c, embedURL, 5*time.Second); err == nil && ok {
			injected = true
		}
	}

	// 界面优化：独立 style 标签，按内容 hash 比对。改 ~/.wbmux/theme.css
	// 之后最多 3 秒自动生效，不用重启客户端，也不影响胶囊。
	if _, err := EnsureTheme(c, ThemeCSS()); err != nil {
		// 静默：主题挂了胶囊照常工作。
	}

	// 指标刷新放在注入之后：刚注入的新胶囊先拿到数字，不用等下一轮。
	// 文本与等级都没变时跳过推送——3 秒巡检只负责"注入标记还在不在"，
	// 不负责把同一行数字反复重写一遍。
	if stats != nil {
		if text, level := stats(); text != "" {
			lastPushedMu.Lock()
			prev, seen := lastPushed[port]
			unchanged := seen && !injected && prev.text == text && prev.level == level
			lastPushedMu.Unlock()
			if !unchanged {
				if ok, _ := UpdateStats(c, text, level, 2*time.Second); ok {
					lastPushedMu.Lock()
					lastPushed[port] = pushedStats{text: text, level: level}
					lastPushedMu.Unlock()
				}
			}
		}
	}
	return injected, true
}
