package sessions

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Cursor 的会话数据在 globalStorage/state.vscdb 的 cursorDiskKV 表里
// （2026-10-02 实测：composerData 92 条、bubbleId 30942 条、总库 706MB）。
//
// # 为什么只读 composerData、绝不碰 bubbleId
//
// 正文全在 bubbleId 行里（几百 KB 一条很常见），而 composerData 一行就有
// 标题（name）、创建/更新时间、状态——元数据扫描需要的全齐了。全量拉
// composerData 约几 MB，拉正文则是几百 MB，"数据会比较多"的担忧就断在这。
//
// # 项目归属是启发式
//
// composerData 里**没有** workspace 字段（Cursor 的会话是全局的，不绑定
// 工作区）。最靠谱的归属线索是会话正文里出现过的文件路径——"path":"/d:/…"
// 与 file:///d%3A/… 两种形态（实测 92 条里 32 条有）。取每条会话里出现
// 最多的文件夹当项目：一多半会话能归组；归不进的落"Cursor（全局）"。
const cursorQuery = `select key, value from cursorDiskKV where key like 'composerData:%'`

type cursorComposer struct {
	ComposerID    string  `json:"composerId"`
	Name          string  `json:"name"`
	Text          string  `json:"text"`
	CreatedAt     float64 `json:"createdAt"`
	LastUpdatedAt float64 `json:"lastUpdatedAt"`
	Status        string  `json:"status"`
}

func defaultCursorDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "Roaming", "Cursor", "User", "globalStorage", "state.vscdb")
}

// scanCursor 扫 Cursor 的全局会话库。用 WorkBuddy 自带的 better-sqlite3
// 桥（同一个执行器读第三家的库，见 adapter_zcode 的说明）。
func scanCursor(probe *variant.Probe) ([]Session, []string, error) {
	dbPath := defaultCursorDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil, nil // 没装 Cursor 不算错
	}
	rows, warns := queryLive(probe, variant.CN, dbPath, cursorQuery, "Cursor")
	if rows == nil {
		return nil, warns, fmt.Errorf("数据库读取失败")
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		raw := mapStr(r, "value")
		if raw == "" {
			continue
		}
		var c cursorComposer
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			continue // 单条坏数据跳过，不炸整源
		}
		s := Session{
			Vendor:     VendorCursor,
			ProjectRaw: cursorProject(raw),
			ID:         c.ComposerID,
			CreatedMs:  int64(c.CreatedAt),
			UpdatedMs:  int64(c.LastUpdatedAt),
			Kind:       "composer",
			Source:     Source{Kind: "sqlite", Path: dbPath},
		}
		if s.Title = strings.TrimSpace(c.Name); s.Title == "" {
			s.Title = firstLine(c.Text)
		}
		if s.Title == "" {
			s.Title = "(无标题)"
		}
		if len([]rune(s.Title)) > 80 {
			s.Title = string([]rune(s.Title)[:80]) + "…"
		}
		if s.UpdatedMs == 0 {
			s.UpdatedMs = fileMs(dbPath)
		}
		if s.CreatedMs == 0 {
			s.CreatedMs = s.UpdatedMs
		}
		out = append(out, s)
	}
	return out, warns, nil
}

// firstLine 取多行文本的第一行当标题，去掉 Markdown 噪音符号。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimLeft(s, "#>-* ")
	s = strings.TrimSpace(s)
	return s
}

// cursorProject 从会话原始 JSON 里数文件路径，取出现最多的文件夹当项目。
//
// 两种实测形态都收：
//   - "path":"/d:/4rchive/AI/Cursor"   （workspace URI 的 path 字段）
//   - file:///d%3A/4rchive/…           （file URI，%3A 是冒号）
//
// 反斜杠形态（D:\\4rchive\\…）也认，虽然实测里很少。
// 一条会话可能碰过多个文件夹——出现最多的那个最有资格当"它的项目"。
func cursorProject(raw string) string {
	count := map[string]int{}
	grab := func(p string) {
		p = normalizeProject(p)
		if p != "(无项目)" {
			count[p]++
		}
	}
	for _, m := range regexp.MustCompile(`"path" *: *"/([A-Za-z]):/([^"]{1,200})"`).FindAllStringSubmatch(raw, -1) {
		grab(m[1] + ":/" + m[2])
	}
	for _, m := range regexp.MustCompile(`file:///([A-Za-z]):(?:%3A|/)([^"\s\\]{1,200})`).FindAllStringSubmatch(raw, -1) {
		p, err := url.PathUnescape(m[2])
		if err != nil {
			continue
		}
		grab(m[1] + ":/" + p)
	}
	best, bestN := "", 0
	for p, n := range count {
		if n > bestN || (n == bestN && p < best) {
			best, bestN = p, n
		}
	}
	if best == "" {
		return "Cursor（全局）"
	}
	// sort 上面 map 遍历无序，best 的选择在同频时靠字典序保持稳定。
	return best
}

// fileMs 取文件修改时间的毫秒值，读不到就 0。
func fileMs(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.ModTime().UnixMilli()
}
