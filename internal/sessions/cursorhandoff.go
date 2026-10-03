package sessions

// Cursor 会话的 Markdown handoff 导出。
//
// # 定位：跨工具续聊的"搬运件"
//
// 各家 IDE 的工具调用机制互不兼容（Cursor 的 diff 轨迹/代码库索引在
// ZCode 里没有对应物，反向同理）——原样导入必丢代理状态。能无损搬运的
// 是**对话内容 + 涉及文件 + 未完成事项**，落成一份 Markdown handoff：
// 在目标工具开新会话时把整份文档发进去，即可接上上下文。
// 这与用户 bot 项目里已经在用的"大类 handoff"是同一模式，实践证明够用。
//
// # 提炼规则
//
//   - 问答按 fullConversationHeadersOnly 的顺序成对/成段排出；
//   - 空的工具泡/检查点泡跳过（与 ParseCursorTail 同一判据）；
//   - 文件清单从正文里数出现次数，按频次排序——出现最多的大概率就是
//     这场对话的主战场；
//   - 未完成事项来自 Cursor 自己的 todos（status 非 completed 的标出来）。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// cursorHandoffMaxTurn 是单条 turn 的正文字节上限。实测最长的 AI 气泡
// 4408 字符，正常内容远够；超限的（整段粘贴的大文件）截断并注明，
// 避免一条会话导出几十 MB 的 markdown。
const cursorHandoffMaxTurn = 64 << 10

// RenderCursorHandoff 把一个 Cursor composer 提炼成 Markdown handoff。
// title 为空时用 composer 自己的 name。
func RenderCursorHandoff(probe *variant.Probe, dbPath, composerID, title string) ([]byte, error) {
	tr, err := ParseCursorTailAll(probe, dbPath, composerID)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = composerID
	}

	// composer 原文再读一次：todos / 文件路径 / 代码行数不进转录结构，
	// 但 handoff 需要。多一次桥调用只发生在显式导出时，可以接受。
	compRows, err := migrateQueryCursor(probe, dbPath,
		`select value from cursorDiskKV where key = 'composerData:' || ?`, composerID)
	if err != nil {
		return nil, err
	}
	todos := []cursorTodo{}
	linesAdded, linesRemoved := int64(0), int64(0)
	if len(compRows) > 0 {
		var comp struct {
			Todos        []cursorTodo `json:"todos"`
			LinesAdded   int64        `json:"totalLinesAdded"`
			LinesRemoved int64        `json:"totalLinesRemoved"`
		}
		if json.Unmarshal([]byte(mapStr(compRows[0], "value")), &comp) == nil {
			todos = comp.Todos
			linesAdded, linesRemoved = comp.LinesAdded, comp.LinesRemoved
		}
	}
	paths := cursorPathCounts(mapStr(compRows[0], "value"))

	var b strings.Builder
	b.WriteString("# handoff：" + mdEscape(title) + "\n\n")
	b.WriteString("- 来源：Cursor（composer `" + shortID(composerID) + "`）\n")
	if tr.CreatedAt.UnixMilli() > 0 {
		b.WriteString("- 创建：" + tr.CreatedAt.Format("2006-01-02 15:04") + "\n")
	}
	if tr.UpdatedAt.UnixMilli() > 0 {
		b.WriteString("- 最后更新：" + tr.UpdatedAt.Format("2006-01-02 15:04") + "\n")
	}
	if linesAdded > 0 || linesRemoved > 0 {
		fmt.Fprintf(&b, "- 代码变动：+%d / -%d 行\n", linesAdded, linesRemoved)
	}
	b.WriteString("\n> 本文档由 wbmux 从 Cursor 会话自动提炼，用于跨工具续聊：把整份文档发给新会话即可接上上下文。\n\n")

	b.WriteString("## 对话记录\n\n")
	shown := 0
	for _, t := range tr.Turns {
		if strings.TrimSpace(t.Text) == "" {
			continue
		}
		role := "助手"
		if t.Kind == "user" {
			role = "用户"
		}
		head := firstMdLine(t.Text)
		b.WriteString("### 【" + role + "】" + mdEscape(head) + "\n\n")
		b.WriteString(mdClamp(t.Text))
		b.WriteString("\n\n")
		shown++
	}
	if shown == 0 {
		b.WriteString("（这场会话没有可提炼的正文。）\n\n")
	}

	if len(paths) > 0 {
		b.WriteString("## 涉及的文件与目录\n\n")
		type pc struct {
			p string
			n int
		}
		var xs []pc
		for p, n := range paths {
			xs = append(xs, pc{p, n})
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i].n > xs[j].n })
		for _, x := range xs {
			b.WriteString("- `" + x.p + "`（出现 " + fmt.Sprint(x.n) + " 次）\n")
		}
		b.WriteString("\n")
	}

	if len(todos) > 0 {
		b.WriteString("## Cursor 待办（未完成事项看未勾选的）\n\n")
		for _, td := range todos {
			mark := " "
			if strings.EqualFold(td.Status, "completed") {
				mark = "x"
			}
			b.WriteString("- [" + mark + "] " + mdEscape(strings.TrimSpace(td.Content)) + "\n")
		}
		b.WriteString("\n")
	}

	return []byte(b.String()), nil
}

type cursorTodo struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// mdEscape 把 markdown 特殊字符转成实体级安全文本——只需要防"以 #/- 开头
// 被当成结构"，行内字符交给阅读器。
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	for strings.HasPrefix(s, "#") || strings.HasPrefix(s, "-") {
		s = s[1:]
	}
	return strings.TrimSpace(s)
}

// mdClamp 截断超长正文，保留整体结构。
func mdClamp(s string) string {
	if len(s) <= cursorHandoffMaxTurn {
		return s
	}
	return s[:cursorHandoffMaxTurn] + "\n\n> …（这一段过长，已截断；完整内容在 Cursor 里）"
}

// firstMdLine 取首行做小节标题。
func firstMdLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return s
}

// shortID 取 id 前 8 位。
func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// exportSlug 把标题压成文件名安全的短 slug：路径敌对字符换成连字符，
// 空白折叠，封顶 40 字。全空时用 untitled。
func exportSlug(title string) string {
	t := strings.TrimSpace(title)
	if t == "" || t == "(无标题)" {
		return "untitled"
	}
	var b strings.Builder
	for _, r := range t {
		if strings.ContainsRune(`\/:*?"<>|`, r) || r == '\t' || r == '\n' || r == '\r' {
			b.WriteRune('-')
			continue
		}
		b.WriteRune(r)
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(strings.Join(strings.Fields(b.String()), "-"), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

// ParseCursorTailAll 与 ParseCursorTail 相同，但不设窗口——导出要全量。
func ParseCursorTailAll(probe *variant.Probe, dbPath, composerID string) (*CodexTranscript, error) {
	return parseCursorTailAll(probe, dbPath, composerID)
}
