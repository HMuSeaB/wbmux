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
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/doctor"
	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/runner"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

//go:embed assets/index.html
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
	// IdleTimeout 是无人访问多久后自动关闭，0 表示不自动关闭。
	//
	// 存在的理由：界面跑在浏览器里，用户直接关掉标签页时服务端无从知晓。
	// 尤其是双击启动（无控制台窗口）的场景，进程一旦残留就只能靠任务管理器
	// 收拾。页面每 60 秒发一次心跳，因此"标签页还开着"不会被误判为空闲。
	IdleTimeout time.Duration
}

// Server 是内嵌界面的本地服务端。
type Server struct {
	opts  Options
	token string

	ln   net.Listener
	http *http.Server

	mu         sync.Mutex
	lastActive time.Time

	done     chan struct{}
	doneOnce sync.Once
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
	return &Server{
		opts:       opts,
		token:      token,
		lastActive: time.Now(),
		done:       make(chan struct{}),
	}, nil
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
	mux.HandleFunc("/api/state", s.guard(s.handleState))
	mux.HandleFunc("/api/doctor", s.guard(s.handleDoctor))
	mux.HandleFunc("/api/preview", s.guard(s.handlePreview))
	mux.HandleFunc("/api/launch", s.guard(s.handleLaunch))
	mux.HandleFunc("/api/migrate/survey", s.guard(s.handleMigrateSurvey))
	mux.HandleFunc("/api/migrate/apply", s.guard(s.handleMigrateApply))
	mux.HandleFunc("/api/quit", s.guard(s.handleQuit))
	mux.HandleFunc("/api/ping", s.guard(s.handlePing))
	mux.HandleFunc("/api/usage", s.guard(s.handleUsage))

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = s.http.Serve(ln) }()
	go s.watchIdle()
	return nil
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
	// 下限只影响测试里的小取值（真实取值 30m 对应 7.5m），
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

// Addr 返回实际监听地址。
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Done 在界面请求关闭（或空闲超时）后关闭，供调用方阻塞等待。
//
// 返回通道而不是阻塞式方法：调用方通常还要同时等 Ctrl+C，
// 用 select 组合两个来源比开一个 goroutine 转发干净。
func (s *Server) Done() <-chan struct{} { return s.done }

// Shutdown 停止服务。
func (s *Server) Shutdown() {
	s.doneOnce.Do(func() {
		close(s.done)
		if s.http != nil {
			_ = s.http.Close()
		}
	})
}

// guard 校验令牌。
func (s *Server) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		got := r.Header.Get("X-Wbmux-Token")
		if got == "" {
			got = r.URL.Query().Get("t")
		}
		// 定长比较，避免按字节提前返回泄漏信息。
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeErr(w, http.StatusForbidden, "令牌无效")
			return
		}
		next(w, r)
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
		Settings: SettingsView{
			Path:           cfgPath,
			HostVariant:    cfg.HostVariant,
			HostExe:        cfg.HostExe,
			ExtraEndpoints: cfg.ExtraEndpoints,
		},
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

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRequest(w, r)
	if !ok {
		return
	}
	res, err := runner.Prepare(runner.Options{
		Target:    req.Target,
		Native:    req.Native,
		ParentEnv: s.opts.ParentEnv,
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

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"ok": true})
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
		ParentEnv: s.opts.ParentEnv,
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
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, usage.Build(s.probe()))
}

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
