package main

import (
	"fmt"
	"net/url"
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
	"github.com/HMuSeaB/wbmux/internal/update"
	"github.com/HMuSeaB/wbmux/internal/version"
	"github.com/HMuSeaB/wbmux/internal/webui"
)

// defaultIdle 是界面无人访问后自动退出的等待时长。默认 **0：不自动退出**。
//
// # 为什么不再默认 30 分钟（2026-09-27 改）
//
// 原来的理由：双击启动时进程没有控制台窗口，用户关掉标签页后若不自动退出，
// 就只能在任务管理器里结束它。这个理由现在不成立了——有托盘图标，
// 进程看得见也点得掉（没有托盘的平台会保留控制台窗口）。
//
// 而它带来的代价是实打实的：判活靠页面每 60 秒的心跳，**而浏览器会冻结
// 后台标签页**（Edge 的"睡眠标签页"、Chrome 的节流）。心跳一停，服务端就把
// "用户只是切去干别的了"误判成"没人用了"，然后把进程结束掉——用户看到的是
// "我没关它，它自己没了"，注入到客户端里的模型也跟着一起废（客户端连不上
// 本机代理）。这比"残留一个看不见的进程"严重得多。
//
// 仍然可以显式要求自动退出：`wbmux gui --idle 30m`。
const defaultIdle = 0

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
	restart := f.Bool("restart", false)
	mode := f.String("mode", "")
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

	// 上次自助升级留下的 .old 在这里清掉（见 internal/update 的包注释）：
	// Windows 不让删正在运行的 exe，所以升级时只能把它改名让位，清理
	// 只能等下一次启动来做。
	update.CleanupOld()

	guiLog("=== 启动 === 参数=%q 参数个数=%d 独占控制台=%v",
		os.Args[1:], len(os.Args)-1, console.IsExclusiveConsole())

	// 沿用上次的地址与令牌，别每次启动都换一副。
	//
	// 令牌与端口都会写进注入到国内客户端的模型条目（URL + API Key），
	// 而客户端只在启动时读一次——一变，注入的模型就失效，用户必须重启
	// 客户端。沿用之后，"wbmux 重启"对客户端就透明了
	// （见 config.GUIToken 的注释，含安全上的取舍）。
	cfg, err := config.Load()
	if err != nil {
		// 设置读不动不该挡住界面：最坏结果不过是这次换了个地址。
		guiLog("读取设置失败（继续）：%v", err)
		cfg = config.Config{}
	}
	addrFromConfig := false
	if *addr == "" && cfg.GUIAddr != "" {
		*addr = cfg.GUIAddr
		addrFromConfig = true
	}

	// 非回环地址直接拒绝，不做"警告后放行"：这个服务能启动本机进程，
	// 暴露到局域网等于把机器交出去。
	normAddr, err := webui.NormalizeAddr(*addr)
	if err != nil {
		if !addrFromConfig {
			guiLog("失败：监听地址不合法：%v", err)
			return err
		}
		// 设置文件被改坏了不该把界面挡在门外：退回随机端口继续，
		// 大不了这次客户端要重启一次。
		guiLog("设置里记的地址不合法（%q），改用随机端口：%v", *addr, err)
		addrFromConfig = false
		normAddr, _ = webui.NormalizeAddr("")
	}

	idleTimeout, err := parseIdle(*idle)
	if err != nil {
		guiLog("失败：--idle 不合法：%v", err)
		return err
	}

	headless, err := parseMode(*mode)
	if err != nil {
		guiLog("失败：--mode 不合法：%v", err)
		return err
	}

	// 已经在跑就不要再起一个：
	// 两个窗口长得一模一样，用户分不清哪个是活的，旧的那个死掉之后
	// 更是"点了没反应"。直接指向已经在跑的那个即可。
	//
	// 代价是**重新编译后双击换不掉版本**——复用是刻意的（不打断正在跑的东西），
	// 但用户看到的只是"图标/界面怎么还是旧的"（2026-09-28 实际反馈）。
	// 所以这里分三种情况：--restart 换版本、版本不同就明说、其余照旧复用。
	if prev, ok := instance.Lookup(); ok {
		guiLog("发现已有实例 addr=%s pid=%d version=%s，复用它", prev.Addr, prev.PID, prev.Version)
		if *restart {
			guiLog("--restart：先让已有实例退出")
			u.title("wbmux 图形界面")
			u.info("正在让已在运行的界面退出，随后启动新版本…")
			if err := prev.Quit(); err != nil {
				guiLog("--restart 失败：%v", err)
				u.warn("无法让已有界面退出：" + err.Error())
				return err
			}
			if !instance.WaitGone(prev, 5*time.Second) {
				guiLog("--restart：等旧实例释放 %s 超时", prev.Addr)
				u.warn("旧界面还没退干净，稍等一两秒再试一次")
				return fmt.Errorf("等待旧实例退出超时")
			}
			guiLog("旧实例已退出，继续启动新版本")
		} else {
			openURL := prev.URL
			stale := prev.Version != "" && prev.Version != version.Version
			u.title("wbmux 图形界面")
			u.info("已经有一个界面在跑了，直接为你打开它。")
			u.kv("界面地址", prev.URL)
			u.kv("进程号", fmt.Sprintf("%d", prev.PID))
			if stale {
				// 把"你手上这个是新版本"顺路告诉浏览器里的界面，
				// 让它自己弹一条提示——只打在控制台里没人会看到。
				openURL = prev.URL + "&newver=" + url.QueryEscape(version.Version)
				guiLog("版本不同：在跑的是 %s，本次是 %s", prev.Version, version.Version)
				u.blank()
				u.warn("注意：正在运行的界面是 " + prev.Version + "，你刚打开的是 " + version.Version + "。")
				u.info("复用旧实例是刻意的（不打断正在跑的东西），所以不会自动换版本。")
				u.info("要换成新的：在界面里点「关闭界面」再双击一次，或运行 wbmux.exe --restart")
			}
			u.blank()
			if *noOpen {
				u.info("--no-open：请手动打开上面的地址")
			} else if err := browser.Open(openURL); err != nil {
				guiLog("打开浏览器失败：%v", err)
				u.warn("无法自动打开浏览器：" + err.Error())
				u.info("请手动打开上面的地址")
			} else {
				guiLog("已在浏览器中打开已有实例")
				u.ok("已在浏览器中打开")
			}
			return nil
		}
	}

	opts := webui.Options{
		Version:     version.Version,
		Addr:        normAddr,
		Token:       cfg.GUIToken, // 空则随机生成，下面的保存步骤会把它记下来
		ParentEnv:   os.Environ(),
		IdleTimeout: idleTimeout,
		Headless:    headless,
		Logf:        guiLog,
	}
	srv, err := webui.New(opts)
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		if !addrFromConfig {
			guiLog("失败：起服务失败：%v", err)
			return err
		}
		// 上次那个端口被别的程序占了。退回随机端口继续可用，但要明说
		// "这次客户端得重启一次"——不吭声就又是一次"昨天还好好的"。
		guiLog("沿用 %s 失败（%v），改用随机端口", normAddr, err)
		u.warn("上次用的端口被占用了，这次换了一个端口——国内客户端需要重启一次才会认新地址。")
		opts.Addr = ""
		if srv, err = webui.New(opts); err != nil {
			return err
		}
		if err := srv.Start(); err != nil {
			guiLog("失败：起服务失败：%v", err)
			return err
		}
	}
	defer srv.Shutdown()
	guiLog("服务已启动 addr=%s", srv.Addr())

	// 记下本次的地址与令牌，下次启动沿用它；
	// 两者都稳定，注入到客户端的模型才能跨重启继续用。
	if cfg.GUIAddr != srv.Addr() || cfg.GUIToken != srv.Token() {
		cfg.GUIAddr = srv.Addr()
		cfg.GUIToken = srv.Token()
		if err := config.Save(cfg); err != nil {
			guiLog("保存界面地址/令牌失败（下次启动会换地址）：%v", err)
		}
	}

	// 自动同步国际免费模型到国内客户端的自定义清单。
	// 地址与令牌现在跨启动稳定了，但模型清单本身会变（活动、模型上下线），
	// 每次启动对齐一遍最省心。失败只记日志（国内客户端没装/清单损坏
	// 都不该挡住界面）。
	go func() {
		n, err := srv.SyncCustomModels()
		if err != nil {
			guiLog("自动同步国际模型失败: %v", err)
			return
		}
		if n > 0 {
			guiLog("已同步 %d 个国际免费模型到国内客户端自定义清单（地址与令牌沿用上次，客户端不必重启）", n)
		}
	}()

	self := instance.Info{
		Addr:      srv.Addr(),
		URL:       srv.URL(),
		PID:       os.Getpid(),
		StartedAt: time.Now().UnixMilli(),
		Version:   version.Version,
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
				// 悬停提示：说清楚"这是什么"和"怎么用"。图标本身是画出来的
				// （见 internal/tray/icon.go），加上这句才算能认。
				// headless 模式下多一句，免得用户以为"关了界面就停了"。
				Tooltip: trayTooltip(srv.Addr(), headless),
				// 打开失败必须说出来：原先这里是 `_ = browser.Open(...)`，
				// 出错直接丢掉，用户点了菜单什么都没发生，只能理解为
				// "这功能没用"（2026-09-28 的反馈）。气泡里带上地址，
				// 至少能手动访问。
				OnOpen: func() {
					if err := browser.Open(srv.URL()); err != nil {
						guiLog("托盘：打开界面失败：%v", err)
						tray.Notify("打不开浏览器", "请手动访问 "+srv.URL())
						return
					}
					guiLog("托盘：已请求打开界面（%s）", srv.Addr())
				},
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
	} else {
		// 默认不自动退出，明说一句：要么在界面里点「关闭界面」，要么托盘右键退出。
		u.kv("空闲退出", "关（不会自己结束；退出用界面里的「关闭界面」或托盘右键）")
	}
	if headless {
		u.kv("运行模式", "只做转发：关掉/刷新界面都不影响服务，退出用托盘右键或 Ctrl+C")
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

// trayTooltip 拼托盘的悬停提示。
//
// headless 模式下必须点明"关了界面服务还在"——否则用户按常理关掉标签页，
// 以为已经停服了，而注入到国内客户端的模型其实还在跑（或者反过来，
// 用户以为还能用，其实已经退了）。这句话就是两种预期的分界线。
func trayTooltip(addr string, headless bool) string {
	if headless {
		return "wbmux 转发服务（只做转发：关掉界面也继续跑；右键：打开界面 / 退出）· " + addr
	}
	return "wbmux 图形界面（右键：打开界面 / 退出）· " + addr
}

// parseMode 解析 --mode。
//
// 空字符串 = 默认（界面关闭时连服务一起关，与改动前的行为一致）。
// "headless" / "proxy" / "service" = 只做转发：界面那一页关掉/刷新都不影响
// 进程，注入到国内客户端的模型不会因为"关了个标签页"而失效。
func parseMode(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "normal", "all":
		return false, nil
	case "headless", "proxy", "service":
		return true, nil
	}
	return false, fmt.Errorf("--mode 只支持 headless（只做转发），收到 %q", s)
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

// parseIdle 解析 --idle。空字符串取默认值（0＝不自动退出），
// "0"/"off" 也明确表示不自动退出。
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
  --addr <地址>    监听地址，只允许回环地址（默认沿用上次的地址，没有则自动选）
  --no-open        只启动服务，不自动打开浏览器
  --restart        先让已在运行的实例退出，再用本次的二进制启动（换版本用）
  --mode <模式>    默认：关界面=停服。headless：只做转发，关界面照样跑
  --idle <时长>    界面无人访问多久后自动退出（默认不退出；30m、off 均可显式指定）
  --color <模式>   auto（默认）/ always / never
  -h, --help       打印本帮助

示例:
  wbmux gui
  wbmux gui --addr 127.0.0.1:8080
  wbmux gui --no-open --idle off
`)
}
