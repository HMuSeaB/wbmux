// Package webui 提供内嵌的图形界面。
//
// 界面本身是一份 HTML，通过 //go:embed 编进二进制，因此仍然是单文件分发、
// 零运行时依赖。启动时只在 127.0.0.1 上开一个本地 HTTP 服务，
// 再用系统浏览器打开——不对外监听，不写入任何客户端目录。
//
// 为什么不用原生窗口库：那需要 cgo 与第三方依赖，会让交叉编译失效
// （macOS 的 cgo 二进制没法在 Linux 上产出），而本项目的主要卖点之一
// 就是"零依赖 + 一个 ubuntu runner 出全部平台"。
package webui

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/checkin"
	"github.com/HMuSeaB/wbmux/internal/clientproc"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/custommodels"
	"github.com/HMuSeaB/wbmux/internal/doctor"
	"github.com/HMuSeaB/wbmux/internal/growth"
	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/runner"
	"github.com/HMuSeaB/wbmux/internal/telemetry"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

//go:embed assets
var assets embed.FS

// Options 是构造服务端的输入。
type Options struct {
	// Version 展示在界面标题旁。
	Version string
	// Addr 是监听地址，留空则用 127.0.0.1:0（由系统分配空闲端口）。
	//
	// 只允许回环地址：这个服务能启动客户端进程，绝不能暴露到局域网。
	Addr string
	// Token 是访问令牌，留空则随机生成。
	Token string
	// Probe 可注入，便于测试。
	Probe *variant.Probe
	// ParentEnv 透传给子进程的环境变量。
	ParentEnv []string
	// IdleTimeout 是无人访问多久后自动关闭，0 表示不自动关闭（现在也是默认）。
	//
	// # 为什么默认改成了 0（2026-09-27）
	//
	// 判活靠页面每 60 秒的心跳，而浏览器会冻结后台标签页（Edge 的"睡眠标签页"、
	// Chrome 的节流）。心跳一停，服务端就把"用户切去干别的了"误判成"没人用了"
	// 然后自我了断——用户看到的是"我没关它，它自己没了"，而且注入到国内客户端的
	// 模型会跟着一起废（客户端连不上本机代理）。
	//
	// 当初设这个超时是为了解决"双击启动留一个看不见的进程"，而那个问题现在由
	// 托盘图标解决了（进程看得见、点得掉；没有托盘的平台保留控制台窗口）。
	// 想要自动退出的仍然可以显式传 --idle 30m。
	IdleTimeout time.Duration

	// Headless 表示"只当转发服务用"：界面请求关闭时**不退出进程**。
	//
	// # 为什么需要它（2026-09-28 用户提出）
	//
	// 界面（浏览器那一页）和转发服务本来是两件事，却一直共享一条命：关掉界面
	// 就等于把注入到国内客户端的模型一起停掉。于是"想刷新一下界面"这种无害
	// 动作会连带断掉正在跑的会话。传了它之后，界面那一页可以随便关/刷新，
	// 主动停服只剩两条明路：托盘右键退出、Ctrl+C。
	Headless bool
	// Logf 是可选的日志出口（GUI 传自己的日志器）。
	//
	// 存在的理由：代理转发失败时，用户能看到的只有客户端里一句
	// 「502 | Trace Id」，原因全在服务端。没有这个出口，排查只能靠
	// 重新插桩——2026-09-27 那次"注入的模型一用就 502"正是如此。
	Logf func(format string, args ...any)

	// HostExe 是用户钉住的客户端主程序（设置文件里的 hostExe）。
	//
	// 存在的理由：国内版的凭据是加密信封，解开它要启动客户端主程序。
	// 用户钉了位置就必须用他钉的那个——否则会出现"界面读的是这份账号、
	// 解开的却是另一处安装的凭据"这种谁也看不出来的错配。
	HostExe string
}

// Server 是内嵌界面的本地服务端。
type Server struct {
	opts  Options
	token string

	ln   net.Listener
	http *http.Server

	mu         sync.Mutex
	lastActive time.Time

	// cfgMu 串行化本进程内"读设置 → 改字段 → 写设置"这一整段。
	//
	// 为什么需要：设置文件是单份 JSON，几个写入口（提供方卡片、签到开关、
	// 回收站开关）都是 Load→改→Save 的读-改-写。两个请求并发时，后写的会用
	// 它读到的那份旧快照整体覆盖前者刚保存的字段——用户看到的是"刚存的卡片
	// 又把上一个设置改回去了"。config.Save 本身是原子的，但原子只保证"不写坏
	// 文件"，不保证"不丢更新"。
	//
	// 只在 webui 进程内加锁；跨进程（CLI 与界面同时改）不管——那种场景本来就
	// 该由用户避免，而引入文件锁的成本远大于收益。
	cfgMu sync.Mutex

	// proxyLog 是代理事件的环形缓冲（见 proxylog.go），proxyMu 保护它。
	proxyLog []ProxyLogEntry
	proxyMu  sync.Mutex

	// rate 是代理观察到"每个模型此刻能不能用"（见 quotalive.go）。
	//
	// 它存在的理由是：客户端会话文件里的限流记录只增不减，答不了
	// "现在恢复了吗"；只有真正跟上游说话的代理知道。
	rate rateState

	// guardian 负责后台自适应遥测与活跃心跳守护，防服务端风控。
	guardian *telemetry.Guardian

	done     chan struct{}
	doneOnce sync.Once

	// 以下三个是测试接缝：零值走真实实现（官方地址、读认证文件、丢弃日志）。
	// 不导出是因为它们只服务于测试与 CLI 接线，不是调用方该关心的旋钮。
	//
	// logFn 接上后代理事件由 note() 双写：一份进内存给界面看，一份落日志文件。
	logFn          func(format string, args ...any)
	upstreamBaseFn func() string
	intlCredFn     func(*variant.Probe) (usage.ProxyCredentials, error)
	// logPathFn 覆盖"日志文件在哪"。quotalive.go 启动时会从日志尾部恢复
	// 限流状态（进程重启后内存里那份就没了，而模型可能还被限着）。
	logPathFn func() string
	// checkinDepsFn 覆盖签到的依赖（接口地址与凭据）。
	//
	// 没有这个接缝，唯一能验证这条链路的办法就是拿**真账号去签一次**——
	// 那是个写操作，不能出现在测试里。
	checkinDepsFn func() checkin.Deps
}

// upstreamBase 返回国际后端地址。测试要能指到本地假上游，
// 否则唯一能验证转发逻辑的办法就是真打一次官方接口。
func (s *Server) upstreamBase() string {
	if s.upstreamBaseFn != nil {
		return s.upstreamBaseFn()
	}
	return usage.IntlEndpoint()
}

// intlCred 读国际侧凭据。同样是测试接缝：假安装环境里没有认证文件。
func (s *Server) intlCred(p *variant.Probe) (usage.ProxyCredentials, error) {
	if s.intlCredFn != nil {
		return s.intlCredFn(p)
	}
	return usage.IntlProxyCredentials(p)
}

// updateConfig 在锁内完成"读设置 → 交给 fn 改 → 写回"。
//
// 所有会改设置的入口都该走这里，避免并发读-改-写互相覆盖（见 cfgMu 注释）。
// fn 返回错误时不写盘，设置保持原样。
func (s *Server) updateConfig(fn func(*config.Config) error) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return config.Save(cfg)
}

// New 构造服务端，但尚未开始监听。
func New(opts Options) (*Server, error) {
	token := opts.Token
	if token == "" {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, fmt.Errorf("生成访问令牌失败: %w", err)
		}
		token = hex.EncodeToString(buf[:])
	}
	p := opts.Probe
	if p == nil {
		p = variant.DefaultProbe()
	}
	srv := &Server{
		opts:       opts,
		token:      token,
		lastActive: time.Now(),
		done:       make(chan struct{}),
		logFn:      opts.Logf,
	}
	srv.guardian = telemetry.NewGuardian(
		p.Home,
		p.GOOS,
		srv.upstreamBase(),
		func() (string, string, error) {
			c, err := srv.intlCred(srv.probe())
			if err != nil {
				return "", "", err
			}
			return c.Token, c.UID, nil
		},
	)
	srv.guardian.SetTokenRefresher(func(ctx context.Context) (bool, error) {
		refreshed, didRefresh, err := credential.RefreshActiveIfNeeded(ctx, probeHTTPClient(), srv.probe(), variant.Intl, 2*time.Hour)
		if didRefresh && refreshed != nil {
			srv.note(ProxyLogEntry{
				Level: "info",
				Text:  fmt.Sprintf("遥测守护已自动为活跃账号静默保鲜续期：%s (%s)", refreshed.Nickname, refreshed.UID),
			})
			usage.InvalidateLiveCache(variant.Intl)
		}
		return didRefresh, err
	})
	return srv, nil
}

// Start 开始监听。
//
// 令牌会写进访问地址的查询串，界面用它作为后续 API 调用的凭据。
// 这样做的原因是防"本地 CSRF"：任何网页都能向 127.0.0.1 发请求，
// 但没有令牌就调不动接口。令牌是每次启动随机生成的，外部猜不到。
func (s *Server) Start() error {
	addr := s.opts.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("无法监听 %s: %w", addr, err)
	}
	s.ln = ln

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/sessions", s.handleSessionsPage)
	mux.HandleFunc("/api/state", s.guard(s.handleState))
	mux.HandleFunc("/api/doctor", s.guard(s.handleDoctor))
	mux.HandleFunc("/api/preview", s.guard(s.handlePreview))
	mux.HandleFunc("/api/launch", s.guard(s.handleLaunch))
	mux.HandleFunc("/api/restart-client", s.guard(s.handleRestart))
	mux.HandleFunc("/api/launch-both", s.guard(s.handleLaunchBoth))
	mux.HandleFunc("/api/probe", s.guard(s.handleProbe))
	mux.HandleFunc("/api/inject-custom-models", s.guard(s.handleInjectCustomModels))
	mux.HandleFunc("/api/custom-models/intl-selection", s.guard(s.handleIntlModelSelection))
	mux.HandleFunc("/api/providers", s.guard(s.handleProviders))
	mux.HandleFunc("/api/proxy-log", s.guard(s.handleProxyLog))
	mux.HandleFunc("/api/zcode/list", s.guard(s.handleZCodeList))
	mux.HandleFunc("/api/zcode/show", s.guard(s.handleZCodeShow))
	mux.HandleFunc("/api/zcode/export", s.guard(s.handleZCodeExport))
	mux.HandleFunc("/api/zimport", s.guard(s.handleZImport))
	s.registerOpenAI(mux)
	mux.HandleFunc("/api/migrate/survey", s.guard(s.handleMigrateSurvey))
	mux.HandleFunc("/api/migrate/apply", s.guard(s.handleMigrateApply))
	mux.HandleFunc("/api/quit", s.guard(s.handleQuit))
	mux.HandleFunc("/api/ping", s.guard(s.handlePing))
	mux.HandleFunc("/api/events", s.guard(s.handleEvents))
	mux.HandleFunc("/api/update/check", s.guard(s.handleUpdateCheck))
	mux.HandleFunc("/api/update/target-info", s.guard(s.handleUpdateTargetInfo))
	mux.HandleFunc("/api/update/apply-stream", s.guard(s.handleUpdateApplyStream))
	mux.HandleFunc("/api/open-folder", s.guard(s.handleOpenFolder))
	mux.HandleFunc("/api/update/apply", s.guard(s.handleUpdateApply))
	mux.HandleFunc("/api/gateway/stats", s.guard(s.handleGatewayStats))
	mux.HandleFunc("/api/gateway/clear", s.guard(s.handleGatewayClear))
	mux.HandleFunc("/api/model-radar/status", s.guard(s.handleModelRadarStatus))
	mux.HandleFunc("/api/model-radar/ping", s.guard(s.handleModelRadarPing))
	mux.HandleFunc("/api/shortcut", s.guard(s.handleShortcut))
	mux.HandleFunc("/api/usage", s.guard(s.handleUsage))
	mux.HandleFunc("/api/sessions", s.guard(s.handleSessionsAPI))
	mux.HandleFunc("/api/sessions/clean", s.guard(s.handleSessionsClean))
	mux.HandleFunc("/api/sessions/clean/rows", s.guard(s.handleSessionsCleanRows))
	mux.HandleFunc("/api/sessions/export", s.guard(s.handleSessionsExport))
	// 整页路由：不走 guard（失败要回 HTML 而不是 JSON），自己校验令牌。
	mux.HandleFunc("/session", s.handleSessionView)
	mux.HandleFunc("/api/recycle/scan", s.guard(s.handleRecycleScan))
	mux.HandleFunc("/api/recycle/clean", s.guard(s.handleRecycleClean))
	mux.HandleFunc("/api/recycle/auto", s.guard(s.handleRecycleAuto))
	mux.HandleFunc("/api/compact", s.guard(s.handleCompactSetting))
	mux.HandleFunc("/api/checkin", s.guard(s.handleCheckin))
	mux.HandleFunc("/api/checkin/claim", s.guard(s.handleCheckinClaim))
	mux.HandleFunc("/api/checkin/auto", s.guard(s.handleCheckinAuto))
	mux.HandleFunc("/api/checkin/auto-toggle", s.guard(s.handleAutoCheckinToggle))
	mux.HandleFunc("/api/client-update", s.guard(s.handleClientUpdatePolicy))
	mux.HandleFunc("/api/workspace", s.guard(s.handleWorkspace))
	mux.HandleFunc("/api/workspace/plan", s.guard(s.handleWorkspacePlan))
	mux.HandleFunc("/api/workspace/apply", s.guard(s.handleWorkspaceApply))
	mux.HandleFunc("/api/telemetry", s.guard(s.handleTelemetry))
	mux.HandleFunc("/api/credentials/import", s.guard(s.handleCredentialImport))
	mux.HandleFunc("/api/credentials/export", s.guard(s.handleCredentialExport))
	mux.HandleFunc("/api/accounts/list", s.guard(s.handleAccountsList))
	mux.HandleFunc("/api/accounts/switch", s.guard(s.handleAccountSwitch))
	mux.HandleFunc("/api/accounts/delete", s.guard(s.handleAccountDelete))
	mux.HandleFunc("/api/accounts/refresh", s.guard(s.handleAccountRefresh))
	mux.HandleFunc("/api/auth/start", s.guard(s.handleAuthStart))
	mux.HandleFunc("/api/auth/poll", s.guard(s.handleAuthPoll))
	mux.HandleFunc("/api/growth/streak", s.guard(s.handleGrowthStreak))
	mux.HandleFunc("/api/growth/redeem", s.guard(s.handleGrowthRedeem))

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = s.http.Serve(ln) }()
	go s.watchIdle()
	go s.startTelemetryGuardian()
	s.resumeAutoClean()
	s.applyAutoUpdatePolicy()
	return nil
}

// applyAutoUpdatePolicy 压住客户端的"启动时静默自动更新"。
//
// 详见 internal/autoupdate 的包注释。一句话：客户端 5.8.3 的
// `early-silent-startup-update`（窗口出现**之前**就跑、会直接静默装）读的是
// `<数据目录>/app/app-config.json` 里的 userSetting —— **不是**我们
// 2026-10-04 写的 settings.json。那个键的优先级最高，写上就等于把这条链路压住。
//
// 为什么值得做：wbmux 靠客户端未公开的配置行为工作，"客户端被悄悄升到新版本"
// 恰恰是机制失效的主要来源。2026-10-15 那次升级就把 models.json 的格式改了，
// 注入的模型集体读不到、上下文回落到 300k。
//
// 可选功能（默认关，仅在用户显式开启时压制）；未开启时不主动触碰客户端配置。
func (s *Server) applyAutoUpdatePolicy() {
	if cfg, err := config.Load(); err == nil && !config.DisableAutoUpdateOn(cfg) {
		return
	}
	if n, err := s.applyClientUpdatePolicy(true); err != nil {
		s.logf("关闭客户端自动更新失败：%s", compactError(err.Error()))
	} else if n > 0 {
		s.logf("已按用户配置关闭客户端的启动静默自动更新（改动了 %d 侧；下次启动客户端生效）", n)
	}
}

// touch 记录一次活动，供空闲判定使用。
func (s *Server) touch() {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
}

// watchIdle 在长时间无人访问后关闭服务。
func (s *Server) watchIdle() {
	d := s.opts.IdleTimeout
	if d <= 0 {
		return
	}
	// 检查间隔取超时的四分之一，保证判定误差不超过 25%。
	// 下限只影响测试里的小取值（真实取值如 30m 对应 7.5m），
	// 设成 100ms 是为了让超时相关的用例不必等上好几秒。
	tick := d / 4
	if tick < 100*time.Millisecond {
		tick = 100 * time.Millisecond
	}
	t := time.NewTicker(tick)
	defer t.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			s.mu.Lock()
			idle := time.Since(s.lastActive)
			s.mu.Unlock()
			if idle >= d {
				s.Shutdown()
				return
			}
		}
	}
}

// URL 返回带令牌的访问地址。
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/?t=%s", s.ln.Addr().String(), s.token)
}

// Addr 返回实际监听的地址。
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Token 返回本次使用的访问令牌。调用方（CLI）需要把它存下来，
// 好让下次启动沿用同一个——令牌变了，注入到国内客户端的模型就失效了
// （见 config.GUIToken 的注释）。
func (s *Server) Token() string { return s.token }

// Done 在界面请求关闭（或空闲超时）后关闭，供调用方阻塞等待。
//
// 返回通道而不是阻塞式方法：调用方通常还要同时等 Ctrl+C，
// 用 select 组合两个来源比开一个 goroutine 转发干净。
func (s *Server) Done() <-chan struct{} { return s.done }

// Shutdown 停止服务。
func (s *Server) Shutdown() {
	// 先停后台循环：服务没了之后它不该还在扫盘清理回收站。
	stopAutoClean()
	s.doneOnce.Do(func() {
		close(s.done)
		if s.http != nil {
			_ = s.http.Close()
		}
	})
}

// Headless 报告当前是否"只当转发服务用"（界面请求关闭不退出进程）。
func (s *Server) Headless() bool { return s.opts.Headless }

// guard 校验令牌。
// checkToken 校验请求带没带对的令牌（头或查询串都认）。
//
// 抽出来是因为**页面**也要用它：会话查看页是整页 HTML，不走 guard
// （guard 失败会回 JSON），但同样必须挡住无令牌的访问
// ——那个页面里内嵌着会话正文。
func (s *Server) checkToken(r *http.Request) bool {
	got := r.Header.Get("X-Wbmux-Token")
	if got == "" {
		got = r.URL.Query().Get("t")
	}
	// 定长比较，避免按字节提前返回泄漏信息。
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		if !s.checkToken(r) {
			writeErr(w, http.StatusForbidden, "令牌无效")
			return
		}
		next(w, r)
	}
}

// handleEvents 是页面生命周期的事件流（SSE）。
//
// 为什么需要它：托盘退出/关闭界面只结束本地服务进程，界面窗口是浏览器
// 开的，进程管不到它——窗口会一直挂着，用户只能手动关（2026-09-28 反馈
// "托盘按退出了，这个还开着"）。有了这条长连，服务端一退连接立刻断开，
// 前端收到就自己 window.close()（--app 模式的窗口允许脚本自关；普通
// 标签页浏览器不让关，前端有自己的提示页兜底）。
//
// 注意 Shutdown 用的是 http.Close（直接掐断活动连接，见 Shutdown 注释），
// 所以下面这层循环不需要优雅收尾——连接被掐，前端 onerror 立刻触发。
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "当前连接不支持事件流")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	// retry 告诉浏览器断线后 2 秒重连；前端的重连判定窗口（3.5 秒）
	// 以它为准。先发一行注释让 onopen 尽早就绪。
	fmt.Fprint(w, "retry: 2000\n\n: hello\n\n")
	fl.Flush()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-t.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-s.done:
			fmt.Fprint(w, "event: bye\ndata: {}\n\n")
			fl.Flush()
			return
		}
	}
}

// handlePing 是页面的心跳，只为把服务端的空闲计时顶掉。
//
// 单独一个接口而不是复用 /api/state：心跳每秒都在跑，不该顺带触发
// 一遍安装探测（那要读注册表、扫目录）。
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.touch()
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// 首页也要令牌：否则任何网页都能把它嵌进 iframe 里诱导点击。
	got := r.URL.Query().Get("t")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("令牌无效。请通过 `wbmux gui` 重新打开界面。\n"))
		return
	}

	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "界面资源缺失")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(page)
}

// ---------- 数据形状 ----------
//
// 这些结构是界面与后端之间的契约。刻意与内部类型分开：内部类型
// （variant.Install 等）字段没有 JSON 标签，直接暴露会让界面耦合到实现细节。

// InstallView 是一套安装的探测结果。
type InstallView struct {
	ID          variant.ID `json:"id"`
	DisplayName string     `json:"displayName"`
	Found       bool       `json:"found"`
	Executable  string     `json:"executable"`
	Source      string     `json:"source"`
	Version     string     `json:"version"`
	Build       string     `json:"build"`
	DataDir     string     `json:"dataDir"`
	Problems    []string   `json:"problems"`
}

// BackendView 是一套后端。
type BackendView struct {
	ID             variant.ID `json:"id"`
	DisplayName    string     `json:"displayName"`
	Endpoint       string     `json:"endpoint"`
	DataFolderName string     `json:"dataFolderName"`
	// Installed 表示该档位自己的安装是否在本机存在。
	Installed bool `json:"installed"`
	// IsHost 表示当前会不会拿它当宿主程序。
	IsHost bool `json:"isHost"`
}

// SettingsView 是设置文件的摘要。
type SettingsView struct {
	Path           string   `json:"path"`
	HostVariant    string   `json:"hostVariant"`
	HostExe        string   `json:"hostExe"`
	ExtraEndpoints []string `json:"extraEndpoints"`
}

// State 是界面首屏所需的全部数据。
type State struct {
	Version  string          `json:"version"`
	Installs []InstallView   `json:"installs"`
	Backends []BackendView   `json:"backends"`
	Settings SettingsView    `json:"settings"`
	HostID   variant.ID      `json:"hostId"`
	HostNote string          `json:"hostNote"`
	CanRun   map[string]bool `json:"canRun"`
	// Quota 是当前被限流的模型。界面据此在**顶部**提醒，见下面理由。
	Quota []ProxyQuota `json:"quota,omitempty"`
	// QuotaWarn 是把限流的后果说清楚的一句话。
	//
	// 为什么要在首屏喊一嗓子：客户端拿到 429 之后会**自己换模型**继续跑，
	// 而国内客户端那边的备选往往是**付费**模型。用户看不到这个切换，
	// 等发现时已经在烧钱了。wbmux 拦不住客户端的故障转移（那条路不经过
	// 代理），能做的就是把它说破。
	QuotaWarn string `json:"quotaWarn,omitempty"`
	// SelectedIntlModels 是用户选中的国际端付费/高级模型 ID 列表。
	SelectedIntlModels []string `json:"selectedIntlModels,omitempty"`
}

// CheckView 是 doctor 的单项结论。
type CheckView struct {
	Name   string `json:"name"`
	Level  string `json:"level"`
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

// DoctorView 是 doctor 的完整结论。
type DoctorView struct {
	Passed  bool        `json:"passed"`
	Checks  []CheckView `json:"checks"`
	Summary string      `json:"summary"`
}

// PreviewView 是一次切换的预览。
type PreviewView struct {
	TargetID    variant.ID `json:"targetId"`
	TargetName  string     `json:"targetName"`
	TargetURL   string     `json:"targetUrl"`
	HostID      variant.ID `json:"hostId"`
	HostName    string     `json:"hostName"`
	Executable  string     `json:"executable"`
	ConfigPath  string     `json:"configPath"`
	DataDir     string     `json:"dataDir"`
	CommandLine string     `json:"commandLine"`
	Changes     []string   `json:"changes"`
	Warnings    []string   `json:"warnings"`
	Native      bool       `json:"native"`
	Launched    bool       `json:"launched"`
	// Restarted 说明这次是"先关掉旧的再启动"，而不是直接启动。
	// 界面据此换一句提示语——用户点的是重启按钮，回话不该说"已启动"。
	Restarted bool `json:"restarted,omitempty"`
	// Killed 是这次关掉了几个旧进程。0 表示本来就没在跑。
	// 报出来是为了让用户知道"它确实处理了旧的"，而不是跳过了一步。
	Killed int `json:"killed,omitempty"`
}

// ---------- 处理函数 ----------

func (s *Server) probe() *variant.Probe {
	if s.opts.Probe != nil {
		return s.opts.Probe
	}
	return variant.DefaultProbe()
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	probe := s.probe()

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfgPath, _ := config.Path()

	st := State{
		Version: s.opts.Version,
		CanRun:  map[string]bool{},
		Quota:   s.quotaSnapshot(),
		Settings: SettingsView{
			Path:           cfgPath,
			HostVariant:    cfg.HostVariant,
			HostExe:        cfg.HostExe,
			ExtraEndpoints: cfg.ExtraEndpoints,
		},
		SelectedIntlModels: cfg.SelectedIntlModels,
	}
	if len(st.Quota) > 0 {
		st.QuotaWarn = quotaWarnText(st.Quota)
	}

	// 逐档位探测安装。
	found := map[variant.ID]bool{}
	for _, b := range variant.All() {
		inst := probe.Detect(b.ID, "")
		found[b.ID] = inst.Found
		st.Installs = append(st.Installs, InstallView{
			ID:          b.ID,
			DisplayName: b.DisplayName,
			Found:       inst.Found,
			Executable:  inst.Executable,
			Source:      inst.Source,
			Version:     inst.Version,
			Build:       shortBuild(inst.Build),
			DataDir:     inst.DataDir,
			Problems:    inst.Problems,
		})
	}

	// 宿主解析失败不算致命：界面仍要能显示"为什么不能启动"。
	hostID, _, hostErr := variant.Resolve(probe, cfg.HostVariant, cfg.HostExe)
	if hostErr != nil {
		st.HostNote = hostErr.Error()
	} else {
		st.HostID = hostID
	}

	for _, b := range variant.All() {
		st.Backends = append(st.Backends, BackendView{
			ID:             b.ID,
			DisplayName:    b.DisplayName,
			Endpoint:       b.Endpoint,
			DataFolderName: b.DataFolderName,
			Installed:      found[b.ID],
			IsHost:         b.ID == hostID,
		})
		// 只要宿主可用，两个后端都能连（覆盖模式不要求目标档位有安装）。
		st.CanRun[string(b.ID)] = hostErr == nil
	}

	writeJSON(w, st)
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	target, err := variant.Parse(r.URL.Query().Get("target"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "目标后端无效")
		return
	}
	cacheDir, err := config.CacheDir()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	rep := doctor.Run(doctor.Options{
		Target:   target,
		CacheDir: cacheDir,
		Probe:    s.probe(),
	})

	view := DoctorView{Passed: !rep.Failed()}
	for _, c := range rep.Checks {
		view.Checks = append(view.Checks, CheckView{
			Name: c.Name, Level: c.Level.String(), Detail: c.Detail, Hint: c.Hint,
		})
	}
	switch {
	case rep.Failed():
		view.Summary = "存在阻断性问题，当前配置下无法可靠切换后端。"
	case rep.Warnings() > 0:
		view.Summary = fmt.Sprintf("有 %d 项提示，功能可用但值得留意。", rep.Warnings())
	default:
		view.Summary = "全部通过。"
	}
	writeJSON(w, view)
}

// previewRequest 是 /api/preview 与 /api/launch 的请求体。
type previewRequest struct {
	Target variant.ID `json:"target"`
	Native bool       `json:"native"`
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRequest(w, r)
	if !ok {
		return
	}
	view, err := s.buildPreview(req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, view)
}

// effectiveParentEnv 返回附带最新注册表环境变量（如 CODEBUDDY_AUTO_COMPACT_WINDOW）的环境列表。
// 避免 wbmux 启动后用户修改了压缩阈值，而子进程依然继承旧环境变量快照的问题。
func (s *Server) effectiveParentEnv() []string {
	env := append([]string{}, s.opts.ParentEnv...)
	if val := strings.TrimSpace(readUserEnv(compactEnvName)); val != "" {
		prefix := compactEnvName + "="
		found := false
		for i, e := range env {
			if strings.HasPrefix(strings.ToUpper(e), strings.ToUpper(prefix)) {
				env[i] = prefix + val
				found = true
				break
			}
		}
		if !found {
			env = append(env, prefix+val)
		}
	}
	return env
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRequest(w, r)
	if !ok {
		return
	}
	res, err := runner.Prepare(runner.Options{
		Target:    req.Target,
		Native:    req.Native,
		ParentEnv: s.effectiveParentEnv(),
		Probe:     s.probe(),
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := res.Launch(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := toPreview(res)
	view.Launched = true
	writeJSON(w, view)
}

// handleRestart 关掉正在运行的客户端再重新启动它。
//
// # 为什么要这个按钮
//
// 有些设置是客户端**启动时**才读的（比如压缩阈值那个环境变量，以及
// wbmux 生成的后端配置）。改完不重启就没效果，而让用户自己去托盘退出、
// 再回来点「启动客户端」，既绕又不明显 —— 他很可能就在这一步以为"改了没用"。
//
// # 顺序不能反
//
// **必须先关干净、再启动**。反过来做会让新进程与旧进程抢同一个用户数据
// 目录的锁，或者新进程被旧实例"接管"（单实例锁），结果是配置没换、
// 界面还停在旧的上面。所以这里等 Terminate 报告进程确实没了才继续。
//
// # 关了但启动失败怎么办
//
// 如实报错，并说明"客户端已经关了"。最坏的情况是用户的客户端被关掉、
// 新的没起来 —— 这种事必须让他立刻知道，而不是给一句"重启失败"让他
// 自己去猜客户端还在不在。
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRequest(w, r)
	if !ok {
		return
	}

	target, err := variant.Get(req.Target)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "目标后端无效")
		return
	}

	// 关掉**实际在跑的那个**客户端，而不是按目标后端的名字去找。
	//
	// 2026-10-16 的真实故障：用户点「重启国际版」，但这台机器上跑的是
	// `WorkBuddy.exe`（国内版的名字），而逻辑去找 `WorkBuddyAI.exe` ——
	// 找不到，于是"已关闭 0 个进程"，旧实例继续占着单实例锁，新实例起不来。
	// 用户看到的就是"点了重启但什么都没变"。
	//
	// 两个名字只差一个词尾（`WorkBuddy` / `WorkBuddyAI`），界面上既看不出
	// 自己点的哪个后端，更看不出进程名对不上。所以这里不去猜，**按实际在跑
	// 的那个关**：两侧的名字都试一遍，谁在跑关谁。
	killed, err := clientproc.TerminateAny(variant.ExecutableNames()...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"关闭客户端没成功："+err.Error()+"。为避免两个实例抢同一份数据，"+
				"这一次没有重新启动，请手动在托盘退出后再点「启动客户端」。")
		return
	}
	s.logf("重启客户端：已关闭 %d 个进程，准备重新启动 %s", killed, target.DisplayName)

	// 关掉之后再启动。Prepare 要重新做一遍：它会重新读一遍环境与配置，
	// 这正是我们想要新进程拿到的。
	res, err := runner.Prepare(runner.Options{
		Target:    req.Target,
		Native:    req.Native,
		ParentEnv: s.effectiveParentEnv(),
		Probe:     s.probe(),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"客户端已经关闭，但重新启动前准备失败："+err.Error())
		return
	}
	if err := res.Launch(); err != nil {
		writeErr(w, http.StatusInternalServerError,
			"客户端已经关闭，但重新启动失败："+err.Error())
		return
	}

	view := toPreview(res)
	view.Launched = true
	view.Restarted = true
	view.Killed = killed
	writeJSON(w, view)
}

// handleInjectCustomModels 把 wbmux 能提供的模型注入国内客户端的自定义
// 模型清单：国际限时免费模型（经代理转发、扣国际额度）+ 自备提供方（BYOK），
// 实现"国内壳子里无缝用别的模型"。
func (s *Server) handleInjectCustomModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	added, note, err := s.syncInto()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"added": added, "note": note})
}

// handleIntlModelSelection 获取或更新自选的国际付费模型注入列表
func (s *Server) handleIntlModelSelection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"selected": cfg.SelectedIntlModels,
		})
	case http.MethodPost:
		var req struct {
			Selected []string `json:"selected"`
			Model    string   `json:"model"`
			Action   string   `json:"action"` // "toggle", "add", "remove"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求解析失败："+err.Error())
			return
		}

		cfg, err := config.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		var updated []string
		if req.Model != "" {
			modelID := strings.TrimSpace(req.Model)
			current := make(map[string]bool)
			for _, m := range cfg.SelectedIntlModels {
				current[strings.ToLower(m)] = true
			}
			targetLower := strings.ToLower(modelID)
			switch req.Action {
			case "remove":
				delete(current, targetLower)
			case "add":
				current[targetLower] = true
			case "toggle", "":
				if current[targetLower] {
					delete(current, targetLower)
				} else {
					current[targetLower] = true
				}
			}
			// 维持已有顺序并去重
			for _, m := range cfg.SelectedIntlModels {
				if current[strings.ToLower(m)] {
					updated = append(updated, m)
					delete(current, strings.ToLower(m))
				}
			}
			if current[targetLower] {
				updated = append(updated, modelID)
			}
		} else {
			// 批量覆盖模式，清洗与去重
			seen := make(map[string]bool)
			for _, m := range req.Selected {
				clean := strings.TrimSpace(m)
				if clean != "" && !seen[strings.ToLower(clean)] {
					seen[strings.ToLower(clean)] = true
					updated = append(updated, clean)
				}
			}
		}

		cfg.SelectedIntlModels = updated
		if err := config.Save(cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
			return
		}

		// 每次选定或取消后，立即触发 syncInto()，实时同步到 models.json
		added, note, syncErr := s.syncInto()
		errMsg := ""
		if syncErr != nil {
			errMsg = syncErr.Error()
		}

		writeJSON(w, map[string]any{
			"ok":       true,
			"selected": cfg.SelectedIntlModels,
			"added":    added,
			"note":     note,
			"syncErr":  errMsg,
		})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
	}
}

// syncInto 把当前能提供的模型写进国内客户端的清单，返回注入条数与一句
// 给用户看的话。
//
// 来源：国际侧免费/自选模型 + 自备提供方（BYOK）。互相独立，国际侧凭据不可用时
// 仍然要注入自备提供方的模型。
func (s *Server) syncInto() (int, string, error) {
	cfg, err := config.Load()
	if err != nil {
		// 设置读不动只影响 BYOK 那部分，不该挡住国际侧的注入。
		s.note(ProxyLogEntry{Level: "bad", Text: "读取设置失败（自备提供方将不注入）：" + compactError(err.Error())})
		cfg = config.Config{}
	}

	var intlErr string
	var intl []usage.LiveModel
	if live := usage.LiveAccounts(s.probe())[string(variant.Intl)]; live != nil && live.OK {
		intl = live.Models
	} else if live != nil && live.Err != "" {
		intlErr = live.Err
	} else {
		intlErr = "国际侧凭据不可用"
	}

	base := "http://" + s.Addr() + "/v1/chat/completions"
	path := filepath.Join(s.probe().DataDir(variant.CN), "models.json")

	// 遵循"限流也不能不注入"原则：全量注入所有可用模型。
	// 即使某个模型刚才偶发频控，也不能剥夺客户端的模型入口；
	// 网关底层已配备 Auto-Failover 多账号故障转移与重试兜底。
	var rateLimitedWarn []string
	for _, m := range intl {
		if q := s.quotaFor(m.ID); q != nil && (q.State == "rate_limited" || q.State == "exhausted") {
			rateLimitedWarn = append(rateLimitedWarn, m.ID)
		}
	}

	added, err := custommodels.Sync(path, base, s.token, intl, cfg.Providers, cfg.SelectedIntlModels)
	if err != nil {
		return 0, "", err
	}

	selectedSet := make(map[string]bool, len(cfg.SelectedIntlModels))
	for _, id := range cfg.SelectedIntlModels {
		if clean := strings.ToLower(strings.TrimSpace(id)); clean != "" {
			selectedSet[clean] = true
		}
	}

	free := 0
	paid := 0
	for _, m := range intl {
		if m.FreeNow {
			free++
		} else if selectedSet[strings.ToLower(m.ID)] {
			paid++
		}
	}
	own := 0
	for _, p := range cfg.Providers {
		own += len(p.Models)
	}

	var detailParts []string
	if free > 0 {
		detailParts = append(detailParts, fmt.Sprintf("国际免费 %d", free))
	}
	if paid > 0 {
		detailParts = append(detailParts, fmt.Sprintf("国际付费 %d", paid))
	}
	if own > 0 {
		detailParts = append(detailParts, fmt.Sprintf("自备提供方 %d", own))
	}
	detailStr := strings.Join(detailParts, " + ")
	if detailStr == "" {
		detailStr = "0"
	}

	parts := fmt.Sprintf("已注入 %d 个模型（%s）", added, detailStr)
	if intlErr != "" {
		parts += "；国际侧这一轮没注入：" + intlErr
	}
	if len(rateLimitedWarn) > 0 {
		parts += "；注：" + strings.Join(rateLimitedWarn, "、") + " 刚才曾有上游频控，网关已配备自动故障转移守护"
	}
	if added == free {
		// 一个自有模型都没注入：多半是还没建卡片，顺带提一句入口在哪。
		parts += "。想接自己的 key：侧边栏「提供方」页"
	}
	parts += "。重启国内客户端后在模型选择器里可见"
	return added, parts, nil
}

// SyncCustomModels 是 GUI 启动时的自动同步入口（模型清单会变，每次对齐
// 一遍）。失败只记日志，不该挡住界面。
func (s *Server) SyncCustomModels() (int, error) {
	added, note, err := s.syncInto()
	if err != nil {
		return 0, err
	}
	if note != "" {
		s.note(ProxyLogEntry{Level: "info", Text: note})
	}
	return added, nil
}

// launchBothResult 是一侧的双开结果。
type launchBothResult struct {
	Side string `json:"side"`
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
}

// handleLaunchBoth 双开：两侧各用各的安装**原样启动**。
//
// 双开与"切换"是两个物种：切换是同一份 exe 换后端，一次只能一个；
// 双开是两套安装并行——互斥锁/数据目录/认证文件本就按产品分开
// （2026-09-26 实测，含互斥锁名 workbuddy vs workbuddy-ai）。
// 每侧独立启动：一侧失败（比如没装）不影响另一侧，结果逐侧回传，
// 界面如实汇报，不合并成一个笼统的成功/失败。
func (s *Server) handleLaunchBoth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var results []launchBothResult
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		res := launchBothResult{Side: string(id)}
		pv, err := s.buildPreview(previewRequest{Target: id, Native: true})
		if err == nil {
			res.Name = pv.TargetName
		}
		prep, err := runner.Prepare(runner.Options{
			Target:    id,
			Native:    true,
			ParentEnv: s.effectiveParentEnv(),
			Probe:     s.probe(),
		})
		if err == nil {
			err = prep.Launch()
		}
		if err != nil {
			res.Err = err.Error()
		} else {
			res.OK = true
		}
		results = append(results, res)
	}
	writeJSON(w, map[string]any{"results": results})
}

// handleProbe 一键测试：两侧各发一条最小消息并对比账单。
// 逐侧汇报（模型/积分/账单差值），一侧失败不影响另一侧；
// 消息本身已获用户授权（"让他消耗一点 token 去测试"）。
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var results []*usage.ChatProbeResult
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		res := usage.SendChatProbe(s.probe(), id)
		results = append(results, res)
		if res.OK && res.Model != "" {
			// 一键测试成功证明该模型可用，立即清除陈旧限流标记
			s.observe(res.Model, http.StatusOK, "")
		}
	}
	writeJSON(w, map[string]any{"results": results})
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	// 只当转发服务用的模式下，界面那一页关掉**不等于**停服：
	// 注入到国内客户端的模型正指着这个端口，停了它们就一起废。
	// 想真停服请用托盘右键退出或 Ctrl+C（界面上也会如实提示）。
	if s.opts.Headless {
		s.note(ProxyLogEntry{Level: "info", Text: "收到界面关闭请求：当前是「只做转发」模式，服务继续运行"})
		writeJSON(w, map[string]any{
			"ok":      true,
			"stopped": false,
			"note":    "当前是「只做转发」模式，界面已关闭但服务继续运行；要停服请用托盘右键退出",
		})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "stopped": true})
	// 先回响应再关服务，否则界面拿不到确认。
	go func() {
		time.Sleep(120 * time.Millisecond)
		s.Shutdown()
	}()
}

func (s *Server) decodeRequest(w http.ResponseWriter, r *http.Request) (previewRequest, bool) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return previewRequest{}, false
	}
	var req previewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return previewRequest{}, false
	}
	if _, err := variant.Get(req.Target); err != nil {
		writeErr(w, http.StatusBadRequest, "目标后端无效")
		return previewRequest{}, false
	}
	return req, true
}

func (s *Server) buildPreview(req previewRequest) (PreviewView, error) {
	res, err := runner.Prepare(runner.Options{
		Target:    req.Target,
		Native:    req.Native,
		ParentEnv: s.effectiveParentEnv(),
		Probe:     s.probe(),
	})
	if err != nil {
		return PreviewView{}, err
	}
	return toPreview(res), nil
}

func toPreview(res runner.Result) PreviewView {
	view := PreviewView{
		TargetID:    res.Target.ID,
		TargetName:  res.Target.DisplayName,
		TargetURL:   res.Target.Endpoint,
		HostID:      res.Host.ID,
		HostName:    res.Host.DisplayName,
		Executable:  res.Executable,
		ConfigPath:  res.ConfigPath,
		DataDir:     res.DataDir,
		CommandLine: res.CommandLine,
		Native:      res.Native,
		Warnings:    res.Warnings,
	}
	for _, c := range res.Changes {
		view.Changes = append(view.Changes, c.String())
	}
	return view
}

// ---------- 历史搬运 ----------

// handleMigrateSurvey 只读地列出可以搬运的东西。
//
// 单独一个只读接口而不是复用 /api/state：扫描要读两侧的 projects 目录、
// 还要起一次 Electron 去读会话索引，比首屏那次探测重得多，不该在打开
// 界面时就付出这个代价。
func (s *Server) handleMigrateSurvey(w http.ResponseWriter, r *http.Request) {
	from, err := variant.Parse(r.URL.Query().Get("from"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "来源档位无效")
		return
	}
	to, err := variant.Parse(r.URL.Query().Get("to"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "目标档位无效")
		return
	}
	if from == to {
		writeErr(w, http.StatusBadRequest, "来源与目标不能是同一套后端")
		return
	}

	res, err := migrate.Survey(migrate.Options{Source: from, Target: to, Probe: s.probe()})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, res)
}

// handleUsage 返回两侧的限流状态。
//
// 这是界面上「额度与计费」那一栏的第一部分。之所以先做限流而不是先做
// 消耗统计：限流是**纯文件扫描**，不碰数据库，因此客户端正在跑也照样能看——
// 而消耗要走数据库桥接，被运行时会被挡住。先给用户一个什么时候都可靠的部分。
// handleUsage 见 quotalive.go——它在 Survey 之外还挂了"代理亲眼见到的
// 当前限流状态"，那份才是判断"现在能不能用"的依据。

// migrateApplyRequest 是 /api/migrate/apply 的请求体。
//
// 只收档位与选择：具体要复制哪些文件、写哪些行，全部由服务端根据选择
// 重新推导。界面传来的路径一概不采信——那等于把写文件的位置交给浏览器。
type migrateApplyRequest struct {
	From       variant.ID     `json:"from"`
	To         variant.ID     `json:"to"`
	Kinds      []migrate.Kind `json:"kinds"`
	SessionIDs []string       `json:"sessionIds"`
}

func (s *Server) handleMigrateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req migrateApplyRequest
	// 上限放宽到 1 MB：勾选的会话 id 可能很多，但也不该无限大。
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	if _, err := variant.Get(req.From); err != nil {
		writeErr(w, http.StatusBadRequest, "来源档位无效")
		return
	}
	if _, err := variant.Get(req.To); err != nil {
		writeErr(w, http.StatusBadRequest, "目标档位无效")
		return
	}
	if req.From == req.To {
		writeErr(w, http.StatusBadRequest, "来源与目标不能是同一套后端")
		return
	}

	rep, err := migrate.Apply(
		migrate.Options{Source: req.From, Target: req.To, Probe: s.probe()},
		migrate.Selection{Kinds: req.Kinds, SessionIDs: req.SessionIDs},
	)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, rep)
}

// ---------- 输出辅助 ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	// 路径里会有中文，不要转成 \uXXXX。
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(map[string]string{"error": msg})
}

func shortBuild(b string) string {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// NormalizeAddr 把用户给的 --addr 收拢到回环地址。
//
// 这个服务能启动本机进程，暴露到非回环地址等于把机器交出去。
// 因此不做"警告后放行"，直接拒绝。
func NormalizeAddr(addr string) (string, error) {
	if addr == "" {
		return "", nil
	}
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return addr, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("只允许监听回环地址，收到 %q", addr)
	}
	return addr, nil
}

// startTelemetryGuardian 在后台启动遥测自适应守护循环。
func (s *Server) startTelemetryGuardian() {
	if s.guardian == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-s.done
		cancel()
	}()
	s.guardian.Start(ctx)
}

// handleTelemetry 返回当前遥测守护状态。
func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	if s.guardian == nil {
		writeJSON(w, map[string]any{"running": false})
		return
	}
	writeJSON(w, s.guardian.GetStatus())
}

// handleCredentialImport 接收用户手动录入的 AccessToken，验证后落盘凭据文件。
func (s *Server) handleCredentialImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Side     string `json:"side"`
		Token    string `json:"token"`
		UID      string `json:"uid"`
		Nickname string `json:"nickname"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	targetID := variant.ID(strings.TrimSpace(req.Side))
	if targetID == "" {
		targetID = variant.Intl
	}
	if targetID != variant.CN && targetID != variant.Intl {
		writeErr(w, http.StatusBadRequest, "不支持的目标档位")
		return
	}

	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeErr(w, http.StatusBadRequest, "Token 不能为空")
		return
	}

	// 探针校验：调用官方接口验证 Token 合法性，并自动补齐账号信息
	cred, err := usage.ValidateAndProbeToken(targetID, token, strings.TrimSpace(req.UID))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "凭据验证失败: "+err.Error())
		return
	}

	if req.Nickname != "" {
		cred.Nickname = strings.TrimSpace(req.Nickname)
	}

	// 存入多账号保险库，并激活该账号
	acc := credential.Account{
		UID:      cred.UID,
		Nickname: cred.Nickname,
		Token:    cred.Token,
		Domain:   cred.Domain,
	}
	savedAcc, err := credential.SaveAccount(s.probe(), targetID, acc, true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "保存凭据失败: "+err.Error())
		return
	}

	// 使缓存失效，下一次查用量时直接拉新账号
	usage.InvalidateLiveCache(targetID)

	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("用户手动导入凭据成功（%s），UID: %s", targetID, cred.UID),
	})

	writeJSON(w, map[string]any{
		"ok":       true,
		"side":     string(targetID),
		"id":       savedAcc.ID,
		"uid":      cred.UID,
		"nickname": cred.Nickname,
		"note":     "凭据导入成功并已纳管至多账号库！",
	})
}

// handleAccountsList 返回指定档位已保存的全部脱敏账号列表。
func (s *Server) handleAccountsList(w http.ResponseWriter, r *http.Request) {
	side := variant.ID(strings.TrimSpace(r.URL.Query().Get("side")))
	if side == "" {
		side = variant.Intl
	}
	accounts, err := credential.ListAccounts(s.probe(), side)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "获取账号列表失败: "+err.Error())
		return
	}
	redacted := make([]credential.RedactedAccount, 0, len(accounts))
	for _, a := range accounts {
		redacted = append(redacted, a.Redact())
	}
	writeJSON(w, map[string]any{
		"side":     string(side),
		"accounts": redacted,
	})
}

// handleAccountSwitch 切换当前活跃账号。
func (s *Server) handleAccountSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Side string `json:"side"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	side := variant.ID(strings.TrimSpace(req.Side))
	if side == "" {
		side = variant.Intl
	}
	acc, err := credential.SwitchAccount(s.probe(), side, strings.TrimSpace(req.ID))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	usage.InvalidateLiveCache(side)
	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("已切换至账号：%s (%s)", acc.Nickname, acc.UID),
	})
	writeJSON(w, map[string]any{
		"ok":      true,
		"account": acc.Redact(),
	})
}

// handleAccountDelete 移除指定账号。
func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Side string `json:"side"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	side := variant.ID(strings.TrimSpace(req.Side))
	if side == "" {
		side = variant.Intl
	}
	if err := credential.DeleteAccount(s.probe(), side, strings.TrimSpace(req.ID)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	usage.InvalidateLiveCache(side)
	writeJSON(w, map[string]any{"ok": true})
}

// handleAccountRefresh 使用 RefreshToken 刷新指定账号（或当前激活账号）的 AccessToken。
func (s *Server) handleAccountRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Side string `json:"side"`
		ID   string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	side := variant.ID(strings.TrimSpace(req.Side))
	if side == "" {
		side = variant.Intl
	}
	acc, err := credential.RefreshAccount(r.Context(), probeHTTPClient(), s.probe(), side, strings.TrimSpace(req.ID))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "刷新失败: "+err.Error())
		return
	}
	usage.InvalidateLiveCache(side)
	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("已成功刷新并保鲜账号：%s (%s)", acc.Nickname, acc.UID),
	})
	writeJSON(w, map[string]any{
		"ok":      true,
		"account": acc.Redact(),
	})
}

// handleAuthStart 发起官方原生拟真登录授权流程。
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	mgr := credential.GetDefaultFlowManager(s.probe())
	flow, err := mgr.StartLoginFlow(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"ok":      true,
		"state":   flow.State,
		"authUrl": flow.AuthURL,
	})
}

// handleAuthPoll 轮询官方登录授权状态。
func (s *Server) handleAuthPoll(w http.ResponseWriter, r *http.Request) {
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		writeErr(w, http.StatusBadRequest, "state 不能为空")
		return
	}
	mgr := credential.GetDefaultFlowManager(s.probe())
	res, err := mgr.PollLoginStatus(r.Context(), state)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.Status == "success" {
		usage.InvalidateLiveCache(variant.Intl)
		s.note(ProxyLogEntry{
			Level: "info",
			Text:  fmt.Sprintf("官方拟真授权成功，已纳管并激活账号：%s (%s)", res.Nickname, res.UID),
		})
	}
	writeJSON(w, res)
}

// handleGrowthStreak 读取国际端当前账号的成长中心连续活跃打卡与阶梯积分状态。
func (s *Server) handleGrowthStreak(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	p := s.probe()
	c, err := s.intlCred(p)
	if err != nil || c.Token == "" {
		writeErr(w, http.StatusNotFound, "未配置国际版账号凭证")
		return
	}
	client := growth.NewClient(s.upstreamBase())
	streak, err := client.FetchStreak(credential.Credential{Token: c.Token, UID: c.UID})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "读取成长中心状态失败: "+err.Error())
		return
	}
	writeJSON(w, streak)
}

// handleGrowthRedeem 兑换国际端连续活跃里程碑阶梯积分奖励（如 14d / 28d）。
func (s *Server) handleGrowthRedeem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Tier == "" {
		writeErr(w, http.StatusBadRequest, "请求缺少 tier 参数")
		return
	}

	p := s.probe()
	c, err := s.intlCred(p)
	if err != nil || c.Token == "" {
		writeErr(w, http.StatusNotFound, "未配置国际版账号凭证")
		return
	}

	client := growth.NewClient(s.upstreamBase())
	granted, err := client.RedeemTier(credential.Credential{Token: c.Token, UID: c.UID}, req.Tier)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	usage.InvalidateLiveCache(variant.Intl)
	s.note(ProxyLogEntry{
		Level: "info",
		Text:  fmt.Sprintf("已成功兑换国际端成长中心「%s」里程碑奖励，获得 %d 积分！", req.Tier, granted),
	})

	writeJSON(w, map[string]any{
		"ok":      true,
		"tier":    req.Tier,
		"granted": granted,
	})
}
