package main

import (
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"time"

	"github.com/HMuSeaB/wbmux/internal/browser"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/sessions"
	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/version"
	"github.com/HMuSeaB/wbmux/internal/webui"
)

// cmdSessions 是会话中心的命令入口。
//
// 默认输出控制台摘要（项目分组 + 候选清单），--web 起本地界面
// 直开会话页。扫描全程只读：候选只是标记，这里没有任何删除路径。
func cmdSessions(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	web := f.Bool("web", false)
	refresh := f.Bool("refresh", false)
	days := f.String("days", "30")
	keep := f.String("keep", "1")
	noOpen := f.Bool("no-open", false)
	// --export 把会话导出成可浏览的 HTML。
	//
	// 为什么挂在 sessions 下而不是新开一条命令：它就是"会话中心"的另一半
	// ——界面里看是一等公民，导出来带着走是二等需求，同一个入口更省心。
	exportDir := f.String("export", "")
	exportVendor := f.String("vendor", "")
	exportOne := f.String("id", "")
	help := f.Bool("help", false)

	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := newUIWith(c)
	if err != nil {
		return err
	}
	if *help {
		printSessionsHelp(u)
		return nil
	}
	keepDays, err := strconv.Atoi(*days)
	if err != nil || keepDays <= 0 {
		return fmt.Errorf("--days 要正整数，收到 %q", *days)
	}
	keepN, err := strconv.Atoi(*keep)
	if err != nil || keepN <= 0 {
		return fmt.Errorf("--keep 要正整数，收到 %q", *keep)
	}

	probe := variant.DefaultProbe()
	if *web {
		return runSessionsWeb(u, probe, keepDays, keepN, *noOpen)
	}
	if *exportDir != "" {
		return runSessionsExport(u, probe, sessions.ExportOptions{
			Dir:     *exportDir,
			Vendor:  sessions.Vendor(*exportVendor),
			OnlyID:  *exportOne,
			Days:    keepDays,
			Keep:    keepN,
			Refresh: *refresh,
		})
	}

	u.kv("候选规则", fmt.Sprintf("超过 %d 天 且 项目内名次 > %d（保底最新 %d 条）", keepDays, keepN, keepN))
	if *refresh {
		u.kv("扫描", "强制重扫（无视缓存）")
	}
	idx, err := sessions.Scan(probe, sessions.Options{
		Refresh:        *refresh,
		KeepDays:       keepDays,
		KeepPerProject: keepN,
	})
	if err != nil {
		return err
	}

	for _, w := range idx.Warnings {
		u.warn(w)
	}
	if idx.Cached {
		u.info("以下为缓存（--refresh 强制重扫）")
	}

	// 项目摘要：按扫描给出的最近活跃序展示。
	candidates := 0
	u.title(fmt.Sprintf("项目 %d 个 · 会话 %d 条", len(idx.Projects), len(idx.Sessions)))
	for _, p := range idx.Projects {
		var names []string
		total := 0
		for v, n := range p.Vendors {
			names = append(names, fmt.Sprintf("%s×%d", v.Label(), n))
			total += n
		}
		sort.Strings(names)
		stale := ""
		if p.OnlyStale {
			stale = " · ⚠ 全部超期"
		}
		u.kv(displayProject(p.Display), fmt.Sprintf("%d 条 [%s] 最近 %s%s",
			total, joinNames(names), fmtTime(p.LastMs), stale))
		candidates += countCandidates(idx, p.Path)
	}

	// 候选清单：控制台只列前若干条，全量在界面里看。
	var cand []sessions.Session
	for _, s := range idx.Sessions {
		if s.Candidate {
			cand = append(cand, s)
		}
	}
	u.title(fmt.Sprintf("清理候选 %d 条（只标记，不执行）", len(cand)))
	const maxList = 15
	for i, s := range cand {
		if i == maxList {
			u.kv("…", fmt.Sprintf("其余 %d 条见 --web 界面", len(cand)-maxList))
			break
		}
		u.kv(s.Vendor.Label(), fmt.Sprintf("%s · %s · %s · %s",
			shortDate(s.UpdatedMs), truncate(s.Title, 40), humanSize(s.SizeBytes), displayProject(s.ProjectRaw)))
	}
	if len(cand) == 0 {
		u.ok("没有候选——所有超期会话都在各自项目的保底名次之内。")
	}
	u.hint("打开图形界面：wbmux sessions --web；刷新扫描：--refresh；调规则：--days 30 --keep 1")
	return nil
}

// runSessionsExport 把会话导出成可浏览的 HTML。
//
// 它是**只读**的：只读会话文件、只往导出目录写。所以不做"客户端在跑就拒绝"
// 那道闸——那道闸是给改数据的操作准备的。
func runSessionsExport(u *ui, probe *variant.Probe, opts sessions.ExportOptions) error {
	u.kv("导出到", opts.Dir)
	if opts.Vendor != "" {
		u.kv("只导出", string(opts.Vendor))
	}
	if opts.OnlyID != "" {
		u.kv("只导出会话", opts.OnlyID)
	}
	u.info("正在扫描会话…")

	rep, err := sessions.ExportSessions(probe, opts)
	if err != nil {
		return err
	}

	for _, s := range rep.Skipped {
		u.warn(s)
	}
	for _, f := range rep.Failed {
		u.warn(f)
	}

	if rep.Exported == 0 {
		u.warn("没有可导出的会话。")
		u.hint("用 wbmux sessions 看有哪些源与会话；目前只有 Codex 支持导出。")
		return nil
	}

	u.ok(fmt.Sprintf("导出 %d 个会话，用时 %s", rep.Exported, rep.Elapsed.Round(time.Millisecond)))
	u.kv("打开", rep.Index)
	u.hint("直接双击上面的 index.html 即可在浏览器里翻；每个会话是一个单独的页面。")
	return nil
}

// runSessionsWeb 起一个只服务界面的本地服务并直开会话页。
//
// 复用 webui.Server 而不是另写一套：路由、令牌、空闲看护都是现成的。
// 不带托盘——这是个查一眼就走的服务，控制台窗口本身就是存在感，
// Ctrl+C 或页面闲置都能退。
func runSessionsWeb(u *ui, probe *variant.Probe, days, keep int, noOpen bool) error {
	// 令牌沿用 ~/.wbmux/config.json 的 GUIToken（与主界面同一把钥匙、
	// 同一个信任域）：随机令牌会让用户开着的标签页和书签在每次
	// --web 重启后全部 403。首次生成后记回去，下次沿用。
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Config{} // 设置读不动不该挡住界面：最坏结果是换一把令牌
	}
	srv, err := webui.New(webui.Options{
		Version: version.String(),
		Probe:   probe,
		Token:   cfg.GUIToken,
		Logf:    func(format string, args ...any) { u.kv("web", fmt.Sprintf(format, args...)) },
	})
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		return err
	}
	if cfg.GUIToken != srv.Token() {
		cfg.GUIToken = srv.Token()
		if serr := config.Save(cfg); serr != nil {
			u.warn("无法保存访问令牌：" + serr.Error())
		}
	}
	// URL() 自带 "?t=令牌" 且路径固定是 "/"，会话页要自己拼路径，
	// 所以从 Addr() 出发重新组装，别在 URL() 后面续字符串。
	openURL := fmt.Sprintf("http://%s/sessions?t=%s", srv.Addr(), srv.Token())
	u.ok("会话中心已启动：" + openURL)
	if !noOpen {
		if err := browser.Open(openURL); err != nil {
			u.warn("打不开浏览器，请手动访问上面的地址：" + err.Error())
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	select {
	case <-srv.Done():
	case <-sig:
		srv.Shutdown()
	}
	return nil
}

// ---- 输出小工具 ----

func countCandidates(idx *sessions.Index, project string) int {
	n := 0
	for _, s := range idx.Sessions {
		if s.Project == project && s.Candidate {
			n++
		}
	}
	return n
}

func displayProject(p string) string {
	if len(p) > 58 {
		return "…" + p[len(p)-57:]
	}
	return p
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += " "
		}
		out += n
	}
	return out
}

func fmtTime(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04")
}

func shortDate(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	return time.UnixMilli(ms).Format("01-02")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func printSessionsHelp(u *ui) {
	fmt.Fprint(u.w, `用法：
  wbmux sessions [选项]

跨 IDE 的历史会话中心：把 WorkBuddy 国内/国际、ZCode、Codex、Claude Code
的会话按项目归组展示，并标出清理候选（超期 且 项目保底名次之外）。
全程只读——候选只是标记，本命令不删除任何东西。

选项：
  --web          起本地界面并打开会话页（默认是控制台摘要）
  --export DIR   把会话导出成可浏览的 HTML 到 DIR（只读，直接双击 index.html 看）
  --vendor NAME  --export 时只导某一家：codex（目前只支持它）
  --id ID        --export 时只导某一个会话
  --refresh      无视缓存强制重扫
  --days N       候选规则的保留期，默认 30 天
  --keep K       每个项目保底保留的最新会话数，默认 1
  --no-open      --web 时不自动开浏览器
  --color auto|always|never
  -h, --help     本帮助

示例：
  wbmux sessions                                  看概览
  wbmux sessions --web                            开界面翻
  wbmux sessions --export "D:/会话归档"            全量导出成 HTML
  wbmux sessions --export ./out --vendor codex    只导 Codex
`)
}
