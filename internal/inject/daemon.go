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
func RunDaemon(ctx context.Context, ports []int, embedURL func() string, stats StatsProvider) {
	run := func() {
		for _, p := range ports {
			Sync(p, embedURL(), stats)
		}
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

// Sync 检查一次：按需注入，并把指标推进胶囊。返回是否真的注入了。
func Sync(port int, embedURL string, stats StatsProvider) bool {
	if embedURL == "" {
		return false
	}
	c, err := Dial(port, 2*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()

	injected := false
	tag, err := InstalledTag(c, 2*time.Second)
	if err != nil {
		return false
	}
	if tag != TagFor(embedURL) {
		if ok, err := Inject(c, embedURL, 5*time.Second); err == nil && ok {
			injected = true
		}
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
	return injected
}
