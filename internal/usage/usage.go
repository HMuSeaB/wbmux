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
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

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
	// AtMs 是该记录所在文件的最后修改时刻，用来判断"多近"。
	AtMs int64 `json:"atMs"`
	// Model 是撞上限流时用的模型，取不到则为空。
	Model string `json:"model"`
}

// Survey 是两侧限流状态的汇总。
type Survey struct {
	// Limits 按时间倒序，最近的排最前。
	Limits   []LimitEvent `json:"limits"`
	Warnings []string     `json:"warnings"`
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

	// 最近的排最前
	sort.SliceStable(out.Limits, func(i, j int) bool {
		return out.Limits[i].AtMs > out.Limits[j].AtMs
	})
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

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
