package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
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

// cmdInject 一次性把面板注入当前运行的客户端。
//
// 默认依次尝试国内版（9222）与国际版（9223）两个端口——用户可能只开了其中一个。
// 图形界面常驻时不需要手动跑这个命令：守护每 3 秒会自己补注入，
// 客户端每次重启都能自动恢复。
func cmdInject(args []string) error {
	f := newFlags()
	portStr := f.String("cdp", "")
	f.Alias("h", "help")
	help := f.Bool("help", false)
	if err := f.Parse(args); err != nil {
		return err
	}
	if *help {
		fmt.Println("用法: wbmux inject [--cdp 9222|9223]\n\n把嵌入面板注入到正在运行的客户端；--cdp 不填时两个端口都试。")
		return nil
	}

	ports := inject.DefaultCDPPorts
	if *portStr != "" {
		p, err := strconv.Atoi(*portStr)
		if err != nil || p <= 0 || p > 65535 {
			return fmt.Errorf("--cdp 需要 1-65535 的端口号，收到 %q", *portStr)
		}
		ports = []int{p}
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
	var lastErr error
	for _, port := range ports {
		c, err := inject.Dial(port, 3*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		injected, err := inject.Inject(c, embed, 6*time.Second)
		if err != nil {
			lastErr = err
			c.Close()
			continue
		}
		if !injected {
			// 端口通、但脚本没回挂载结果：多半界面还在加载。
			u.warn(fmt.Sprintf("端口 %d 已连上，但没找到挂载结果——客户端可能还在加载，稍后再试", port))
			c.Close()
			return nil
		}
		u.ok(fmt.Sprintf("面板已注入（调试端口 %d）", port))
		u.kv("面板地址", embed)
		u.info("刷新客户端窗口即消失；不修改客户端任何文件")
		if tag, tagErr := inject.InstalledTag(c, 2*time.Second); tagErr == nil && tag != "" {
			u.kv("注入标记", tag)
		}
		c.Close()
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("没有找到可注入的客户端页面")
	}
	return lastErr
}
