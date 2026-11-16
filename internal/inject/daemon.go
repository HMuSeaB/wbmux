package inject

import (
	"context"
	"time"
)

// DefaultCDPPort 是 wbmux 与客户端约定的调试端口。
const DefaultCDPPort = 9222

// StatsProvider 由调用方提供胶囊上的指标文字与告警等级。
//
// 分开成回调而不是在本包里读审计：inject 只负责"把东西挂上去"，
// 什么数据、怎么算，属于 wbmux 自己的业务。
type StatsProvider func() (text, level string)

// RunDaemon 常驻保持注入与指标刷新。
//
// 每 3 秒做两件事：连调试端口 → 读页面里的注入标记，不一致就重注入；
// 一致就把最新指标推进胶囊。覆盖三种情况而不需要任何用户操作：
//   - 客户端重启（页面里的 DOM 全没了）→ 重注入；
//   - wbmux 自己重启换了令牌 → 标记里带着旧 URL，对不上 → 用新地址重注入；
//   - 面板脚本升级 → 版本号变了 → 自动替换。
//
// 端口连不上（客户端没带调试参数、或已退出）就静默跳过——这是常态，
// 不该往日志里刷错误。
func RunDaemon(ctx context.Context, port int, embedURL func() string, stats StatsProvider) {
	Sync(port, embedURL(), stats)
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			Sync(port, embedURL(), stats)
		}
	}
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
	if stats != nil {
		if text, level := stats(); text != "" {
			_, _ = UpdateStats(c, text, level, 2*time.Second)
		}
	}
	return injected
}
