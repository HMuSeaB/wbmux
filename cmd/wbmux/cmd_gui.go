package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/browser"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/console"
	"github.com/HMuSeaB/wbmux/internal/instance"
	"github.com/HMuSeaB/wbmux/internal/tray"
	"github.com/HMuSeaB/wbmux/internal/version"
	"github.com/HMuSeaB/wbmux/internal/webui"
)

// defaultIdle 是界面无人访问后自动退出的等待时长。
//
// 页面每 60 秒发一次心跳，所以"标签页还开着"不会被算作空闲。
// 这个兜底主要针对一种残留：双击启动时进程没有控制台窗口，
// 用户关掉标签页后如果不自动退出，就只能去任务管理器里结束它。
const defaultIdle = 30 * time.Minute

// guiLog 把启动过程追加到设置目录下的 gui.log。
//
// # 为什么必须有它
//
// 双击启动时控制台会被收起来（见下面的 HideConsoleWindow）。那之后任何
// 报错都进了看不见的地方，用户能看到的只有"双击了，什么都没发生"——
// 这是最难排查的一类反馈，因为连一条线索都没有。
//
// 把关键步骤落成文件，出问题时至少有个地方能看。日志只追加、不轮转：
// 一次启动也就十几行，跑一年也到不了几 MB。
func guiLog(format string, args ...any) {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "gui.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	line := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), line)
}

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

	guiLog("=== 启动 === 参数=%q 参数个数=%d 独占控制台=%v",
		os.Args[1:], len(os.Args)-1, console.IsExclusiveConsole())

	// 非回环地址直接拒绝，不做"警告后放行"：这个服务能启动本机进程，
	// 暴露到局域网等于把机器交出去。
	normAddr, err := webui.NormalizeAddr(*addr)
	if err != nil {
		guiLog("失败：监听地址不合法：%v", err)
		return err
	}

	idleTimeout, err := parseIdle(*idle)
	if err != nil {
		guiLog("失败：--idle 不合法：%v", err)
		return err
	}

	// 已经在跑就不要再起一个：
	// 两个窗口长得一模一样，用户分不清哪个是活的，旧的那个死掉之后
	// 更是"点了没反应"。直接指向已经在跑的那个即可。
	if prev, ok := instance.Lookup(); ok {
		guiLog("发现已有实例 addr=%s pid=%d，复用它", prev.Addr, prev.PID)
		u.title("wbmux 图形界面")
		u.info("已经有一个界面在跑了，直接为你打开它。")
		u.kv("界面地址", prev.URL)
		u.kv("进程号", fmt.Sprintf("%d", prev.PID))
		u.blank()
		if *noOpen {
			u.info("--no-open：请手动打开上面的地址")
		} else if err := browser.Open(prev.URL); err != nil {
			guiLog("打开浏览器失败：%v", err)
			u.warn("无法自动打开浏览器：" + err.Error())
			u.info("请手动打开上面的地址")
		} else {
			guiLog("已在浏览器中打开已有实例")
			u.ok("已在浏览器中打开")
		}
		return nil
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
		guiLog("失败：起服务失败：%v", err)
		return err
	}
	defer srv.Shutdown()
	guiLog("服务已启动 addr=%s", srv.Addr())

	self := instance.Info{
		Addr:      srv.Addr(),
		URL:       srv.URL(),
		PID:       os.Getpid(),
		StartedAt: time.Now().UnixMilli(),
	}
	if err := instance.Claim(self); err != nil {
		// 登记失败不影响本次使用，只是下次启动可能多开一个窗口。
		// 为这点小事拒绝启动反而更糟。
		u.warn("无法登记运行状态：" + err.Error())
	}
	defer instance.Release(self)

	// 托盘图标：让"进程到底在不在跑"变得可见。
	//
	// 界面是开在系统浏览器里的，浏览器窗口一关就看不出进程还在不在，
	// 用户只能靠任务管理器判断。托盘补上这个信息，顺带提供"打开界面 / 退出"。
	ready := make(chan struct{}, 1)

	if tray.Available() {
		go func() {
			// Win32 的消息循环必须跑在创建窗口的那个线程上，
			// 所以这个 goroutine 要锁住自己的 OS 线程。
			runtime.LockOSThread()
			err := tray.Run(tray.Options{
				Tooltip: "wbmux 图形界面 · " + srv.Addr(),
				OnOpen:  func() { _ = browser.Open(srv.URL()) },
				OnQuit: func() {
					guiLog("托盘：选择退出")
					srv.Shutdown()
				},
				Ready: func() {
					guiLog("托盘：图标已就绪")
					u.ok("托盘图标已就绪（右键可打开界面或退出）")
					select {
					case ready <- struct{}{}:
					default:
					}
				},
			})
			if err != nil {
				guiLog("托盘启动失败：%v", err)
				u.warn("托盘图标未能显示：" + err.Error())
			}
			select {
			case ready <- struct{}{}:
			default:
			}
		}()
	} else {
		guiLog("当前平台不支持托盘")
		u.info("当前平台没有托盘支持，用 Ctrl+C 或界面里的「关闭界面」退出")
		ready <- struct{}{}
	}

	u.title("wbmux 图形界面")
	u.kv("界面地址", srv.URL())
	u.kv("监听", srv.Addr())
	if idleTimeout > 0 {
		u.kv("空闲退出", idleTimeout.String()+"（页面关闭后自动结束）")
	}
	u.blank()

	if *noOpen {
		guiLog("--no-open，不自动开浏览器")
		u.info("--no-open：请手动打开上面的地址")
	} else if err := browser.Open(srv.URL()); err != nil {
		// 打不开浏览器不算致命：地址已经打印出来了，用户能自己复制。
		guiLog("打开浏览器失败：%v（界面地址 %s）", err, srv.URL())
		u.warn("无法自动打开浏览器：" + err.Error())
		u.info("请手动打开上面的地址")
	} else {
		guiLog("已请求系统浏览器打开界面")
		u.ok("已在浏览器中打开")
	}

	// 收起控制台前先等托盘图标就位。
	//
	// 托盘是"进程还在跑"唯一的外在标志。早先是无条件收起来，一旦托盘
	// 没起来，用户桌面上就什么都不剩——没有窗口、没有图标，正是
	// "双击了跟没点一样"的由来。等不到就干脆留着控制台，让报错看得见。
	if console.IsExclusiveConsole() {
		select {
		case <-ready:
			guiLog("托盘已就绪，收起控制台")
			console.HideConsoleWindow()
		case <-time.After(5 * time.Second):
			guiLog("等托盘超时，保留控制台窗口以便看到报错")
			u.warn("托盘图标没有出现，控制台保留着以便查看信息")
		}
	}

	u.info("按 Ctrl+C 结束（关闭界面窗口也会自动结束）")
	waitForQuit(srv)
	guiLog("退出")
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
