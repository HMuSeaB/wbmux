package inject

import (
	"context"
	"time"
)

// DefaultCDPPort 是 wbmux 与客户端约定的调试端口。
const DefaultCDPPort = 9222

// RunDaemon 常驻保持注入。
//
// 每 3 秒做一件事：连调试端口 → 读页面里的注入标记 → 与期望不一致就重注入。
// 覆盖三种情况而不需要任何用户操作：
//   - 客户端重启（页面里的 DOM 全没了）→ 重注入；
//   - wbmux 自己重启换了令牌 → 标记里带着旧 URL，对不上 → 用新地址重注入；
//   - 面板脚本升级 → 版本号变了 → 自动替换。
//
// 端口连不上（客户端没带调试参数、或已退出）就静默跳过——这是常态，
// 不该往日志里刷错误。
func RunDaemon(ctx context.Context, port int, embedURL func() string) {
	Sync(port, embedURL())
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			Sync(port, embedURL())
		}
	}
}

// Sync 检查一次并按需注入，返回是否真的注入了。
func Sync(port int, embedURL string) bool {
	if embedURL == "" {
		return false
	}
	c, err := Dial(port, 2*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()

	tag, err := InstalledTag(c, 2*time.Second)
	if err != nil {
		return false
	}
	if tag == TagFor(embedURL) {
		return false
	}
	ok, err := Inject(c, embedURL, 5*time.Second)
	return err == nil && ok
}
