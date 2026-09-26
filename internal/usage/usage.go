// Package usage 从两侧数据目录里读出「限流状态」与「额度消耗」。
//
// # 为什么需要它
//
// wbmux 能把同一个客户端指向不同后端，于是用户会遇到一个新的困惑：
// 屏幕上弹出「使用量已超出频率限制」，但**看不出这是国内账号还是国际账号
// 被限的**。两个后端各自限流、各自重置，界面上却长得一模一样。
//
// 好在这些状态都落在本地文件里，而且**天然带有归属**：会话文件放在哪个
// 数据目录下，就属于哪个后端。这是唯一可靠的判据，不该靠猜。
//
// # 只读，且不动活动状态
//
// 这里只做读取。数据库一律以 immutable 方式打开（不碰 -wal/-shm），
// 客户端照常运行不受影响。
package usage

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// LimitEvent 是一次被限流的记录。
type LimitEvent struct {
	// Side 明确指出这是哪个后端被限的。这正是用户最缺的信息。
	Side string `json:"side"`
	// Kind 区分两种 429：rate=频率超限（等重置即可），
	// exhausted=额度用尽（得充值）。两者对策完全不同，不能混为一谈。
	Kind string `json:"kind"`
	// ResetAt 是重置时间原文，按后端给的原样保留，不做解析——
	// 解析错了比不解析更糟（用户会按错的时间干等）。
	ResetAt string `json:"resetAt"`
	// SessionID 便于用户定位是哪段对话撞上的。
	SessionID string `json:"sessionId"`
	// AtMs 是这条记录的排序依据，语义见 sortMs 的说明。
	AtMs int64 `json:"atMs"`
	// Model 是撞上限流时用的模型，取不到则为空。
	Model string `json:"model"`
}

// sortMs 决定一条记录排多前。
//
// 优先用**事件本身的时间**（频率超限的重置时刻），解析不出来才退回文件
// 修改时间。这个次序很关键：早先一律用文件 mtime，结果"当前状态"可能显示
// 一个 9-18 的重置时间，而下方历史里却躺着更晚的 9-20——用户一眼就能看出
// 自相矛盾。文件 mtime 只反映"最后写过"，不反映"那条限流是什么时候的事"。
//
// 重置时间可能落在未来（还没到点），那正好说明这条限制**现在仍然生效**，
// 排序时它会自然排到最前——这正是我们想要的。
func (e LimitEvent) sortMs() int64 {
	if ms := parseResetMs(e.ResetAt); ms > 0 {
		return ms
	}
	return e.AtMs
}

// resetRe 拆出重置时间里的日期、时刻与 UTC 偏移。
//
// 客户端给的原形有两种：
//
//	2026-09-26 13:40:52 UTC+8
//	2026-09-26 13:40:52 UTC
//
// 时区写作 "UTC+8" 而不是 Go 能直接认的 "+0800"，所以自己拆。
// 注意"UTC"三个字母要在偏移之外：偏移是可选的，但 UTC 这两个写法都有，
// 早先把它们捆在同一组里，导致**不带偏移的那种直接匹配不上**。
var resetRe = regexp.MustCompile(
	`^\s*(\d{4})-(\d{2})-(\d{2})\s+(\d{2}):(\d{2}):(\d{2})\s*(?:UTC\s*(?:([+-])(\d{1,2})(?::?(\d{2}))?)?)?\s*$`)

// parseResetMs 把重置时间原文解析成 Unix 毫秒；解析不了返回 0。
//
// 返回 0 而不是报错：读不出来就退回文件 mtime，总比整条记录消失好。
func parseResetMs(s string) int64 {
	m := resetRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}

	// 只吃数字，不带符号——符号单独取。
	// 早先这里把 "+8" 整串丢给一个逐字符累加的函数，'+' - '0' 得到负数，
	// 于是 "+8" 算成了 -42 小时，排序全错。
	atoi := func(v string) int {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				continue
			}
			n = n*10 + int(c-'0')
		}
		return n
	}

	year, month, day := atoi(m[1]), atoi(m[2]), atoi(m[3])
	hour, min, sec := atoi(m[4]), atoi(m[5]), atoi(m[6])
	if year < 1970 || month < 1 || month > 12 || day < 1 || day > 31 ||
		hour > 23 || min > 59 || sec > 59 {
		return 0
	}

	offMin := 0
	if m[7] != "" { // 有符号才说明带了偏移
		offMin = atoi(m[8]) * 60
		if m[9] != "" {
			offMin += atoi(m[9])
		}
		if m[7] == "-" {
			offMin = -offMin
		}
	}

	// 墙钟时间减去偏移得到 UTC：UTC+8 的 13:40 就是 UTC 05:40。
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0, time.UTC)
	return t.Add(-time.Duration(offMin) * time.Minute).UnixMilli()
}

// Survey 是两侧限流状态的汇总。
type Survey struct {
	// Latest 是每侧最新的一条，键为档位 id（"cn" / "intl"）。
	//
	// 有它是因为用户真正要问的是"**现在**是不是被限着、哪一侧、什么时候恢复"，
	// 而不是一份历史清单。只有 Limits 的话，他得自己在列表里翻找，
	// 而列表的顺序会随客户端持续写文件而变化——最关键的那条未必排在第一个。
	Latest map[string]LimitEvent `json:"latest"`
	// Limits 按时间倒序，最近的排最前。
	Limits []LimitEvent `json:"limits"`
	// Costs 是每条会话的额度消耗，按吃掉的量倒序。
	Costs []SessionCost `json:"costs"`
	// Warnings 是读取过程中的问题。读不到额度表时仍然会返回限流部分，
	// 所以这里只是补充说明，不是致命错误。
	Warnings []string `json:"warnings"`
}

// Build 汇总限流与消耗两块。
//
// 不叫 Survey：包里已经有一个 Survey 类型，同一包内类型与函数不能同名。
func Build(probe *variant.Probe) Survey {
	limits := SurveyLimits(probe)
	costs, warns := SurveyCosts(probe)
	return Survey{
		Latest:   limits.Latest,
		Limits:   limits.Limits,
		Costs:    costs,
		Warnings: append(limits.Warnings, warns...),
	}
}

// recentWindow 是只看多久以内动过的会话文件。
//
// 限流是即时状态，看很久以前的没有意义；而会话文件可能有几百 MB，
// 全扫一遍既慢又没必要。
const recentWindow = 7 * 24 * time.Hour

// maxSessionsPerSide 是每侧最多扫多少个会话文件。
//
// 从最近改动的开始扫，够用了；加这个上限是为了避免某个目录下
// 堆了几百个历史会话时把界面卡住。
const maxSessionsPerSide = 12

// 两种限流文案。分开匹配是因为对策不同，混在一起会误导用户。
var (
	rateRe      = regexp.MustCompile(`使用量已超出频率限制[^\n]*?将在\s*([0-9]{4}-[0-9]{2}-[0-9]{2}\s+[0-9:]{8}\s*UTC[^\s，,。]*)\s*重置`)
	exhaustedRe = regexp.MustCompile(`429\s+Credits\s+exhausted`)
)

// SurveyLimits 扫描两侧，返回最近被限流的记录。
func SurveyLimits(probe *variant.Probe) Survey {
	var out Survey
	cutoff := time.Now().Add(-recentWindow)

	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		dir := probe.DataDir(id)
		if dir == "" {
			continue
		}
		root := filepath.Join(dir, "projects")
		if !dirExists(root) {
			continue
		}

		files, err := recentSessionFiles(root, cutoff, maxSessionsPerSide)
		if err != nil {
			out.Warnings = append(out.Warnings, warn(id, "扫描会话目录失败："+err.Error()))
			continue
		}
		for _, f := range files {
			ev, ok := scanFileForLimit(f, id)
			if ok {
				out.Limits = append(out.Limits, ev)
			}
		}
	}

	// 按事件本身的时间倒序，而不是文件 mtime。见 sortMs 的说明。
	sort.SliceStable(out.Limits, func(i, j int) bool {
		return out.Limits[i].sortMs() > out.Limits[j].sortMs()
	})

	// 每侧挑一条最新的：Limits 已按时间倒序，所以遇到的第一个就是。
	out.Latest = map[string]LimitEvent{}
	for _, e := range out.Limits {
		if _, ok := out.Latest[e.Side]; !ok {
			out.Latest[e.Side] = e
		}
	}
	return out
}

func warn(id variant.ID, msg string) string {
	return string(id) + "：" + msg
}

// recentSessionFiles 收集最近改动的会话文件，按新→旧排序。
func recentSessionFiles(root string, cutoff time.Time, limit int) ([]string, error) {
	type entry struct {
		path  string
		mtime time.Time
	}
	var all []entry

	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 单个子目录读不动就跳过，不拖累整体
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".jsonl") {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		all = append(all, entry{path: p, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool { return all[i].mtime.After(all[j].mtime) })

	out := make([]string, 0, limit)
	for i, e := range all {
		if i >= limit {
			break
		}
		out = append(out, e.path)
	}
	return out, nil
}

// scanFileForLimit 在一个会话文件里找限流记录。
//
// 只取最后一条：用户关心的是"现在是不是被限着 / 什么时候能恢复"，
// 历史上的每一次都列出来只会淹掉重点。
func scanFileForLimit(path string, id variant.ID) (LimitEvent, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return LimitEvent{}, false
	}

	f, err := os.Open(path)
	if err != nil {
		return LimitEvent{}, false
	}
	defer func() { _ = f.Close() }()

	// 先用缓冲逐行找，避免把几 MB 的文件整个读进内存。
	// 记录只出现在某几行里，顺序扫描足够。
	var (
		ev      LimitEvent
		found   bool
		lastIdx int
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for idx := 0; sc.Scan(); idx++ {
		line := sc.Text()
		if m := rateRe.FindStringSubmatch(line); m != nil {
			ev = LimitEvent{
				Side:    string(id),
				Kind:    "rate",
				ResetAt: strings.TrimSpace(m[1]),
				AtMs:    info.ModTime().UnixMilli(),
				Model:   modelOf(line),
			}
			found, lastIdx = true, idx
			continue
		}
		if exhaustedRe.MatchString(line) {
			ev = LimitEvent{
				Side:    string(id),
				Kind:    "exhausted",
				ResetAt: "",
				AtMs:    info.ModTime().UnixMilli(),
				Model:   modelOf(line),
			}
			found, lastIdx = true, idx
		}
	}
	_ = lastIdx

	if !found {
		return LimitEvent{}, false
	}
	ev.SessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return ev, true
}

// modelOf 尝试从这一行里取模型名。
//
// 取不到就返回空——宁可空着，也不要拿一个猜错的值误导用户。
func modelOf(line string) string {
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return ""
	}
	for _, key := range []string{"model", "modelId", "model_name"} {
		if v, ok := rec[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ---------- 各对话的额度消耗 ----------

// SessionCost 是一条会话的额度消耗。
type SessionCost struct {
	Side      string `json:"side"`
	SessionID string `json:"sessionId"`
	// Title 取不到就为空：宁可空着，也不要拿 session_id 冒充标题。
	Title string `json:"title"`
	// Credits 是这条会话吃掉的 credit 总量，四舍五入到两位小数。
	//
	// 单位是客户端自己记的 credit，不是钱：本机没有任何价格数据
	// （数据目录下的 models.json 只有几条自定义供应商配置，**不含价格字段**，
	// 里面还存着 apiKey，也不该拿来展示）。所以只能横向比较"谁吃得多"，
	// 换不成金额——宁可只给能确定的部分，也不拿猜出来的单价去算钱。
	Credits float64 `json:"credits"`
	// Parts 是 credit_json 里的分项个数。
	//
	// 实测（2026-09-26）credit_json 长这样：
	//   {"01a0d7a9f1f87b239d5ff27668a42917": 5.89, ...}
	// 键是 32 位十六进制的**不透明哈希**，不是模型名——本机没有任何
	// 哈希到模型名的映射，硬把它标成"每个模型的消耗"就是编造。
	// 所以只下发分项个数，明细数组不下发：界面上用不上，还白占带宽。
	// 哪天搞清了键的含义，再在这里恢复明细。
	Parts int `json:"parts"`
	// Used / Size 是客户端记录的这条会话的用量与上限。单位未证实
	// （字节还是 token），只原样展示，不解释。
	Used int64 `json:"used"`
	Size int64 `json:"size"`
	// UpdatedAt 是毫秒时间戳（实测 13 位），取不到就为 0。
	UpdatedAt int64 `json:"updatedAt"`
}

// costQuery 读出额度表，并顺带取会话标题。
//
// LEFT JOIN 而不是 INNER：额度表里可能记着索引里已经没有的会话，
// 那种也要显示出来——否则就少了一块真实的消耗，用户会对不上账。
const costQuery = `select u.session_id as session_id, u.used as used, ` +
	`u.size as size, u.updated_at as updated_at, u.credit_json as credit_json, ` +
	`s.title as title from session_usage u ` +
	`left join sessions s on s.id = u.session_id ` +
	`order by u.updated_at desc limit 300`

// SurveyCosts 读出两侧每条会话的额度消耗。
//
// 数据库一律以 immutable 方式打开（不碰 -wal/-shm），客户端在跑也照样能读。
// 读不到就记一条警告，而不是让整栏消失——限流那部分仍然有用。
func SurveyCosts(probe *variant.Probe) ([]SessionCost, []string) {
	var out []SessionCost
	var warns []string

	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		rows, err := migrate.Query(probe, id, costQuery)
		if err != nil {
			warns = append(warns, warn(id, "读额度表失败："+err.Error()))
			continue
		}
		for _, r := range rows {
			out = append(out, rowToCost(string(id), r))
		}
	}

	// 吃得多的排最前
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Credits != out[j].Credits {
			return out[i].Credits > out[j].Credits
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, warns
}

// rowToCost 把一行查询结果转成 SessionCost。
//
// 全部字段都做"取不到就留零值"的处理：这张表是客户端内部实现，
// 哪天改了列名就取不到，那时宁可显示一堆零，也不要 panic 或者整栏消失。
func rowToCost(side string, row map[string]any) SessionCost {
	c := SessionCost{Side: side}

	if v, ok := row["session_id"].(string); ok {
		c.SessionID = v
	}
	if v, ok := row["title"].(string); ok {
		c.Title = v
	}
	c.UpdatedAt = asInt64(row["updated_at"])
	c.Used = asInt64(row["used"])
	c.Size = asInt64(row["size"])

	// credit_json 在库里可以为 NULL（实测 intl 侧就有），类型断言失败即按空处理。
	raw, _ := row["credit_json"].(string)
	if raw != "" && raw != "None" {
		var m map[string]float64
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			c.Parts = len(m)
			for _, v := range m {
				c.Credits += v
			}
			// 客户端写入的值带着 5.8900000000000015 这样的浮点尾巴，
			// 累加后会拖出 229.089999…。界面只展示两位小数，
			// 这里就先归整，别让 JSON 里也躺着一串脏数。
			c.Credits = math.Round(c.Credits*100) / 100
		}
	}
	return c
}

// asInt64 接住 SQLite 查询结果里可能出现的两种数：JSON 桥把整数
// 解成 float64，也可能原样是 int64。都试一遍，取不到就 0。
func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
