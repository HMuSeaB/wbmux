package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/HMuSeaB/wbmux/internal/inject"
	"github.com/HMuSeaB/wbmux/internal/instance"
)

// embedURLFrom 由界面访问地址推出嵌入面板地址。
//
// 两者共用同一个令牌：令牌每次启动随机生成，所以这里必须从运行标记里
// 现读，不能缓存、更不能写死。
func embedURLFrom(full string) string {
	u, err := url.Parse(full)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/embed?t=" + url.QueryEscape(u.Query().Get("t"))
}

// cmdInject 一次性把面板注入当前运行的 WorkBuddy。
//
// 前提：客户端必须带 --remote-debugging-port 启动（见 tools/windows
// 下的重启脚本）。图形界面常驻时不需要手动跑这个命令——守护进程每
// 3 秒会自己补注入，客户每次重启都能自动恢复。
func cmdInject(args []string) error {
	f := newFlags()
	portStr := f.String("cdp", fmt.Sprint(inject.DefaultCDPPort))
	f.Alias("h", "help")
	help := f.Bool("help", false)
	if err := f.Parse(args); err != nil {
		return err
	}
	if *help {
		fmt.Println("用法: wbmux inject [--cdp 9222]\n\n把嵌入面板注入到正在运行的 WorkBuddy 客户端。")
		return nil
	}

	var port int
	if _, err := fmt.Sscanf(*portStr, "%d", &port); err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("--cdp 需要 1-65535 的端口号，收到 %q", *portStr)
	}

	info, ok := instance.Lookup()
	if !ok {
		return errors.New("图形界面没在运行：先执行 `wbmux gui`，再回来注入")
	}
	embed := embedURLFrom(info.URL)
	if embed == "" {
		return errors.New("运行标记里的界面地址无法解析，试试重启图形界面")
	}

	u := newUI(os.Stdout)
	c, err := inject.Dial(port, 3*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()

	injected, err := inject.Inject(c, embed, 6*time.Second)
	if err != nil {
		return err
	}
	if injected {
		u.ok("面板已注入 WorkBuddy 顶栏右侧")
		u.kv("面板地址", embed)
		u.info("刷新客户端窗口即消失；不修改客户端任何文件")
	} else {
		u.warn("注入脚本跑了，但没找到挂载结果——客户端界面可能还在加载，稍后再试")
	}
	if tag, err := inject.InstalledTag(c, 2*time.Second); err == nil && tag != "" {
		u.kv("注入标记", tag)
	}
	return nil
}
