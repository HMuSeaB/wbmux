// Package sessions 跨 IDE 的历史会话统一索引。
//
// # 为什么要有它
//
// 用户装了太多各家的 AI IDE：WorkBuddy（国内/国际）、ZCode、Codex、
// Claude Code……每家的历史会话各自关在各自的数据目录里，想知道
// "某个项目我都用哪些工具聊过什么"就得挨个客户端翻。
// 这里把六个源扫一遍，按**项目路径**归组，交给界面一页展示。
//
// # 只读红线
//
// 所有适配器只读文件、只查库（immutable/readonly），绝不删、不改、
// 不移动任何源数据。"清理候选"只是**标记**：超期且同项目还有更新的
// 会话（或项目保底名次之内）才标出来，动不动手永远由用户决定。
//
// # 项目保底（这个规则是需求本身）
//
// 用户明说过："有些项目其实 30 天后也要用，如果一个项目底下一个会话
// 都没有那就不好了。"所以候选规则是两个条件的**与**：
//
//  1. 会话超过 N 天没动（时间维度）；
//  2. 它在项目内按更新时间倒序排的名次 > K（项目维度，保底最新 K 条）。
//
// 只看时间会把"三个月没动但下个月还要用"的老项目清到零会话；
// 加上名次条件，每个项目永远至少留着 K 条。
package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Vendor 是会话来源。一期六源：WorkBuddy 两档、ZCode、Codex、Claude Code。
type Vendor string

const (
	VendorWBCN   Vendor = "wb-cn"   // WorkBuddy 国内档（~/.workbuddy）
	VendorWBIntl Vendor = "wb-intl" // WorkBuddy 国际档（~/.workbuddy-ai）
	VendorZCode  Vendor = "zcode"   // ZCode（~/.zcode）
	VendorCodex  Vendor = "codex"   // OpenAI Codex（~/.codex）
	VendorClaude Vendor = "claude"  // Claude Code（~/.claude）
)

// Label 返回界面上显示的厂商名。
func (v Vendor) Label() string {
	switch v {
	case VendorWBCN:
		return "WB 国内"
	case VendorWBIntl:
		return "WB 国际"
	case VendorZCode:
		return "ZCode"
	case VendorCodex:
		return "Codex"
	case VendorClaude:
		return "Claude"
	}
	return string(v)
}

// Source 记录这条会话数据 physically 在哪。
//
// 界面展示它的价值：用户看到一条 2026-03 的 Codex 会话，想知道
// "删了它到底动的是哪个文件"——这里给的是精确路径，不是黑盒。
type Source struct {
	// Kind 是 "sqlite"（库里的行）或 "file"（独立会话文件）。
	Kind string `json:"kind"`
	// Path 是库文件路径（sqlite）或会话文件路径（file）。
	Path string `json:"path"`
}

// Session 是一条历史会话的索引行。
type Session struct {
	Vendor Vendor `json:"vendor"`
	// Project 是归一化后的项目路径，跨源归组的键。
	// 归一化规则见 normalizeProject：同一路径在 WB 里可能是
	// "C:/Users/…"，在 Codex 里是 "C:\\Users\\…"，直接比字符串会散。
	Project string `json:"project"`
	// ProjectRaw 是来源里记的原样路径，界面展示用——
	// 归一化把盘符统一成小写、斜杠统一成正斜杠，原样路径更亲切。
	ProjectRaw string `json:"projectRaw"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	CreatedMs  int64  `json:"createdMs"`
	UpdatedMs  int64  `json:"updatedMs"`
	SizeBytes  int64  `json:"sizeBytes"`
	// Kind 是来源自己的会话类别：WB 的 status、ZCode 的 task_type
	// （interactive / subagent_child）等。原样保留，不过度解读。
	Kind string `json:"kind"`
	// Archived 目前只有 ZCode 会给（time_archived 非空）。
	Archived bool `json:"archived"`
	// Rank 是项目内按更新时间倒序的名次，1 起始。候选规则用它做保底。
	Rank int `json:"rank"`
	// Candidate 是清理候选标记。见包注释"项目保底"：只标记，不动手。
	Candidate bool   `json:"candidate"`
	Source    Source `json:"source"`
}

// Project 是归组后的项目摘要。
type Project struct {
	// Path 是归一化路径（归组键），Display 是第一次见到时的原样路径。
	Path    string         `json:"path"`
	Display string         `json:"display"`
	Count   int            `json:"count"`
	Vendors map[Vendor]int `json:"vendors"`
	LastMs  int64          `json:"lastMs"`
	// TotalBytes 是所有会话载体的大小合计。sqlite 源给 0——
	// 库是多家共用的，把整个库算进某个项目是撒谎；文件源才给真实大小。
	TotalBytes int64 `json:"totalBytes"`
	// OnlyStale 表示这个项目的全部会话都超过保留期。
	// 这正是用户担心的场景："清完一个会话都不剩"。有这个标记的项目
	// 即使会话再老，保底的那 K 条也永远不是候选——这里只是给个视觉警示。
	OnlyStale bool `json:"onlyStale"`
}

// Index 是一次扫描的完整结果。
type Index struct {
	GeneratedAt int64     `json:"generatedAt"`
	KeepDays    int       `json:"keepDays"`
	KeepPerProj int       `json:"keepPerProject"`
	Sessions    []Session `json:"sessions"`
	Projects    []Project `json:"projects"`
	Warnings    []string  `json:"warnings"`
	// Cached 表示这份索引来自缓存而不是刚扫完。界面把它显示成头部的
	// "（缓存）"角标；Warnings 里只放真警告（某源读失败之类），
	// 缓存说明不往那儿混——否则每次秒开都挂着一条黄色警告。
	Cached bool `json:"cached,omitempty"`
}

// Options 控制一次扫描。
type Options struct {
	// Refresh 为 true 时无视缓存重扫全部源。
	Refresh bool
	// KeepDays / KeepPerProject 是候选规则的两个参数，见包注释。
	KeepDays       int
	KeepPerProject int
}

// Scan 扫全部源并按项目归组。
//
// 三个 sqlite 源（WB×2、ZCode）并行扫：每源要各自起一次 Electron
// 子进程（借 better-sqlite3），串行的话最坏能攒到一分多钟；
// 并行后墙钟时间约等于最慢的一个。两个文件源（Codex、Claude）只碰
// 文件元数据和首行，快，跟着一起并发不亏。
//
// 任何单源失败都记进 Warnings 继续——四个源挂了一个，剩下五个照样
// 有用，不该让整页空白。
func Scan(probe *variant.Probe, opts Options) (*Index, error) {
	if opts.KeepDays <= 0 {
		opts.KeepDays = 30
	}
	if opts.KeepPerProject <= 0 {
		opts.KeepPerProject = 1
	}

	if !opts.Refresh {
		if cached := loadCacheSessions(); cached != nil {
			// 缓存命中的是"会话清单"；候选与归组依赖当前参数和"现在"，
			// 必须当场重算，不能信缓存里的旧值。缓存说明走 Cached 字段，
			// 不混进 Warnings——那里只放真警告（某源读失败之类）。
			idx := buildIndex(cached, nil, opts, cachedGeneratedAt)
			idx.Cached = true
			return idx, nil
		}
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		sessions []Session
		warnings []string
	)
	add := func(ss []Session, warns []string) {
		mu.Lock()
		defer mu.Unlock()
		sessions = append(sessions, ss...)
		warnings = append(warnings, warns...)
	}

	run := func(name string, fn func() ([]Session, []string, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss, warns, err := fn()
			if err != nil {
				add(nil, []string{name + "：" + err.Error()})
				return
			}
			add(ss, warns)
		}()
	}

	run("WB 国内", func() ([]Session, []string, error) { return scanWB(probe, variant.CN, VendorWBCN) })
	run("WB 国际", func() ([]Session, []string, error) { return scanWB(probe, variant.Intl, VendorWBIntl) })
	run("ZCode", func() ([]Session, []string, error) { return scanZCode(probe) })
	run("Codex", func() ([]Session, []string, error) { return scanCodex(defaultCodexRoots(), defaultCodexIndex()) })
	run("Claude", func() ([]Session, []string, error) { return scanClaude(defaultClaudeRoot()) })
	wg.Wait()

	idx := buildIndex(sessions, warnings, opts, time.Now().UnixMilli())
	saveCache(idx)
	return idx, nil
}

// buildIndex 归一化、归组、排名、标候选。对同一份会话反复调用是安全的——
// 所有派生字段（Project/Rank/Candidate/Projects）都在这里从头算。
func buildIndex(all []Session, warnings []string, opts Options, generatedAt int64) *Index {
	// 归一化 + 排序：项目内按更新时间倒序，名次从这里来。
	for i := range all {
		all[i].Project = normalizeProject(all[i].ProjectRaw)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Project != all[j].Project {
			return all[i].Project < all[j].Project
		}
		return all[i].UpdatedMs > all[j].UpdatedMs
	})

	cutoff := time.Now().Add(-time.Duration(opts.KeepDays) * 24 * time.Hour).UnixMilli()
	byProject := map[string]*Project{}
	var order []string
	lastProject := ""
	rank := 0
	for i := range all {
		s := &all[i]
		if s.Project != lastProject {
			lastProject = s.Project
			rank = 0
			order = append(order, s.Project)
			byProject[s.Project] = &Project{Path: s.Project, Display: s.ProjectRaw, Vendors: map[Vendor]int{}}
		}
		rank++
		s.Rank = rank
		// 候选 = 超期 且 名次在保底之外。两个条件都硬性，见包注释。
		s.Candidate = s.UpdatedMs < cutoff && rank > opts.KeepPerProject
		p := byProject[s.Project]
		p.Count++
		p.Vendors[s.Vendor]++
		if s.UpdatedMs > p.LastMs {
			p.LastMs = s.UpdatedMs
		}
		p.TotalBytes += s.SizeBytes
		if s.ProjectRaw != "" && len(s.ProjectRaw) > len(p.Display) {
			// 展示取见过的最长的原样路径：短的往往是别的源记的省略形式。
			p.Display = s.ProjectRaw
		}
	}

	var projects []Project
	for _, key := range order {
		p := *byProject[key]
		projects = append(projects, p)
	}
	// 项目按最后活跃倒序：用户找"最近在做的项目"最多。
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].LastMs > projects[j].LastMs })

	// OnlyStale 要在名次算完之后才能判：全部会话的更新时间都早于截止线。
	for i := range projects {
		stale := true
		for _, s := range all {
			if s.Project == projects[i].Path && s.UpdatedMs >= cutoff {
				stale = false
				break
			}
		}
		projects[i].OnlyStale = stale
	}
	// OnlyStale 的项目里，保底之外的候选照样标——保底那 K 条不标，
	// 这正是"不至于清到零"的机制本身，不需要额外修正。

	return &Index{
		GeneratedAt: generatedAt,
		KeepDays:    opts.KeepDays,
		KeepPerProj: opts.KeepPerProject,
		Sessions:    all,
		Projects:    projects,
		Warnings:    warnings,
	}
}

// normalizeProject 把各家记的项目路径归一成同一个键。
//
// 实测同一路径在不同源里长这样：
//
//	C:/Users/36230/WorkBuddy AI     （WB sessions.cwd，正斜杠）
//	C:\Users\36230\WorkBuddy AI     （ZCode session.directory，反斜杠）
//	D:\4rchive\Code\…               （Codex session_meta.cwd，反斜杠）
//
// 差异只有三处：斜杠方向、盘符大小写、尾部斜杠。Windows 路径不区分
// 大小写，整串 casefold 最稳——代价是理论上把两个只差大小写的真实目录
// 并成一组，在这台机器上没这个场景，比"不归组"便宜得多。
func normalizeProject(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "(无项目)"
	}
	p = strings.ReplaceAll(p, "\\", "/")
	// 去掉尾部斜杠（根目录 "C:/" 除外——它归一后还是 "c:/"）。
	for len(p) > 3 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return strings.ToLower(p)
}

// ---------- 缓存 ----------

// 缓存文件放 wbmux 自己的数据目录（~/.wbmux），沿 config.Dir 的既有习惯。
//
// 实测（2026-10-01）：强制重扫约 0.22 秒、缓存命中 7 毫秒。
// 缓存仍值得留——扫一次要起几个 Electron 子进程读各家的库，属于别人的
// 活动数据，每次开页都去碰没有必要。
//
// 刷新永远手动（--refresh / 界面按钮），不做后台自动扫。
func cachePath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions-index.json"), nil
}

func loadCacheSessions() []Session {
	path, err := cachePath()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var idx Index
	if json.Unmarshal(raw, &idx) != nil || len(idx.Sessions) == 0 {
		return nil
	}
	cachedGeneratedAt = idx.GeneratedAt
	return idx.Sessions
}

// cachedGeneratedAt 是 loadCacheSessions 读到的原始扫描时间，
// 让缓存路径的界面仍然显示"数据是那次扫的"，而不是装成刚扫完。
var cachedGeneratedAt int64

// InvalidateCache 丢掉磁盘上的会话索引缓存。
//
// 什么时候需要：**磁盘上的会话文件变了**（比如刚清理过一批）。
// 不丢的话下次打开会话中心会拿旧缓存，看到刚清掉的那些还在列表里——
// 用户会以为清理没生效。
//
// 删不掉不算错误：缓存本来就是"有就用、没有就重扫"的东西，
// 最坏情况是多扫一次。
func InvalidateCache() {
	path, err := cachePath()
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

func saveCache(idx *Index) {
	path, err := cachePath()
	if err != nil {
		return
	}
	raw, err := json.Marshal(idx)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
