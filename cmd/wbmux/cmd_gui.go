package main

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/browser"
	"github.com/HMuSeaB/wbmux/internal/console"
	"github.com/HMuSeaB/wbmux/internal/version"
	"github.com/HMuSeaB/wbmux/internal/webui"
)

// defaultIdle 是界面无人访问后自动退出的等待时长。
//
// 页面每 60 秒发一次心跳，所以"标签页还开着"不会被算作空闲。
// 这个兜底主要针对一种残留：双击启动时进程没有控制台窗口，
// 用户关掉标签页后如果不自动退出，就只能去任务管理器里结束它。
const defaultIdle = 30 * time.Minute

func cmdGUI(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	addr := f.String("addr", "")
	noOpen := f.Bool("no-open", false)
	idle := f.String("idle", "")
	help := f.Bool("help", false)

	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := newUIWith(c)
	if err != nil {
		return err
	}
	if *help {
		printGUIHelp(u)
		return nil
	}
	if len(f.Args) > 0 {
		return fmt.Errorf("gui 不接受位置参数，收到 %q", strings.Join(f.Args, " "))
	}

	// 非回环地址直接拒绝，不做"警告后放行"：这个服务能启动本机进程，
	// 暴露到局域网等于把机器交出去。
	normAddr, err := webui.NormalizeAddr(*addr)
	if err != nil {
		return err
	}

	idleTimeout, err := parseIdle(*idle)
	if err != nil {
		return err
	}

	srv, err := webui.New(webui.Options{
		Version:     version.Version,
		Addr:        normAddr,
		ParentEnv:   os.Environ(),
		IdleTimeout: idleTimeout,
	})
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		return err
	}
	defer srv.Shutdown()

	u.title("wbmux 图形界面")
	u.kv("界面地址", srv.URL())
	u.kv("监听", srv.Addr())
	if idleTimeout > 0 {
		u.kv("空闲退出", idleTimeout.String()+"（页面关闭后自动结束）")
	}
	u.blank()

	if *noOpen {
		u.info("--no-open：请手动打开上面的地址")
	} else if err := browser.Open(srv.URL()); err != nil {
		// 打不开浏览器不算致命：地址已经打印出来了，用户能自己复制。
		u.warn("无法自动打开浏览器：" + err.Error())
		u.info("请手动打开上面的地址")
	} else {
		u.ok("已在浏览器中打开")
	}

	// 到这一步界面已经起来了，双击启动的场景可以把控制台收起来。
	// 放在成功之后而不是更早，是为了让启动过程中的报错仍然看得见。
	if console.IsExclusiveConsole() {
		console.HideConsoleWindow()
	}

	u.info("按 Ctrl+C 结束（关闭界面窗口也会自动结束）")
	waitForQuit(srv)
	u.blank()
	u.info("已退出")
	return nil
}

// waitForQuit 阻塞到界面请求关闭或收到中断信号。
func waitForQuit(srv *webui.Server) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)

	select {
	case <-srv.Done():
	case <-sig:
	}
}

// parseIdle 解析 --idle。空字符串取默认值，"0"/"off" 表示不自动退出。
func parseIdle(s string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return defaultIdle, nil
	case "0", "off", "never":
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("--idle 需要时长（如 30m、1h）或 off，收到 %q", s)
	}
	if d < 0 {
		return 0, fmt.Errorf("--idle 不能为负数")
	}
	return d, nil
}

func printGUIHelp(u *ui) {
	fmt.Fprint(u.w, `用法: wbmux gui [选项]

打开图形界面。界面是一份内嵌的网页，由本进程在 127.0.0.1 上提供服务，
再用系统浏览器打开——不联网、不写任何客户端目录。

双击 wbmux.exe 等同于执行本命令。

选项:
  --addr <地址>    监听地址，只允许回环地址（默认 127.0.0.1:0 自动选端口）
  --no-open        只启动服务，不自动打开浏览器
  --idle <时长>    界面无人访问多久后自动退出（默认 30m，off 表示不退出）
  --color <模式>   auto（默认）/ always / never
  -h, --help       打印本帮助

示例:
  wbmux gui
  wbmux gui --addr 127.0.0.1:8080
  wbmux gui --no-open --idle off
`)
}
