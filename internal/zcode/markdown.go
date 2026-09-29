package zcode

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MarkdownOptions 控制导出成什么样。
type MarkdownOptions struct {
	// WithReasoning 是否带上思考片段。默认不带：它是过程，不是对话本身，
	// 一份给人和给别的工具看的记录通常不需要它。
	WithReasoning bool
	// WithTools 是否列出用过的工具名。默认带：它们是"这个会话做了什么"
	// 的关键线索，且只占一行。
	WithTools bool
}

// ToMarkdown 把一个会话渲染成 Markdown。
//
// # 为什么是 Markdown 而不是原样 JSON
//
// 导出有两个真实用途：自己翻/存档，以及**喂给别的工具**（新开一个会话把上
// 一轮的结论带过去）。Markdown 两边都能用，JSON 只对后者友好而对人不可读。
func ToMarkdown(tr *Transcript, opts MarkdownOptions) string {
	var b strings.Builder

	fmt.Fprintf(&b, "# %s\n\n", escapeTitle(tr.Title))
	if tr.Directory != "" {
		fmt.Fprintf(&b, "- 项目：`%s`\n", tr.Directory)
	}
	if tr.CreatedMs > 0 {
		fmt.Fprintf(&b, "- 开始：%s\n", stamp(tr.CreatedMs))
	}
	if tr.UpdatedMs > 0 {
		fmt.Fprintf(&b, "- 最后更新：%s\n", stamp(tr.UpdatedMs))
	}
	fmt.Fprintf(&b, "- 消息数：%d\n", len(tr.Msgs))
	fmt.Fprintf(&b, "- 来源：ZCode（只读导出）\n")
	b.WriteString("\n---\n\n")

	for _, m := range tr.Msgs {
		name := "助手"
		if m.Role == "user" {
			name = "我"
		}
		// 时间只到分钟：一条对话里秒级精度没有意义，反而挤满行首。
		when := ""
		if m.At > 0 {
			when = " · " + time.UnixMilli(m.At).Format("01-02 15:04")
		}
		fmt.Fprintf(&b, "## %s%s\n\n", name, when)

		if opts.WithTools && len(m.Tools) > 0 {
			fmt.Fprintf(&b, "> 工具：%s\n\n", strings.Join(m.Tools, "、"))
		}
		if len(m.Files) > 0 {
			fmt.Fprintf(&b, "> 文件：%s\n\n", strings.Join(codeEach(m.Files), " "))
		}
		if opts.WithReasoning && m.Reasoning != "" {
			b.WriteString("<details><summary>思考过程</summary>\n\n")
			b.WriteString(m.Reasoning)
			b.WriteString("\n\n</details>\n\n")
		}
		if m.Text != "" {
			b.WriteString(m.Text)
			b.WriteString("\n\n")
		}
	}

	if len(tr.Warns) > 0 {
		b.WriteString("---\n\n> 导出时的提示：\n")
		for _, w := range tr.Warns {
			fmt.Fprintf(&b, ">\n> - %s\n", w)
		}
	}
	return b.String()
}

// ExportFile 把会话导出到 path 对应的目录（path 为空则用当前目录）。
//
// 返回实际写出的文件路径。文件名默认取"日期-标题"，把标题里的非法字符换掉。
func ExportFile(tr *Transcript, dir string, opts MarkdownOptions) (string, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建目录失败：%w", err)
	}
	// 文件名：日期 + 标题。日期放前面，同一目录里按时间自然排好。
	day := "unknown-date"
	if tr.UpdatedMs > 0 {
		day = time.UnixMilli(tr.UpdatedMs).Format("2006-01-02")
	}
	base := day + "-" + sanitizeName(tr.Title)
	if base == day+"-" {
		base = day + "-" + shortID(tr.SessionID)
	}
	path := filepath.Join(dir, base+".md")

	// 不让同名文件互相覆盖：加 -2、-3……直到空位。
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("%s-%d.md", base, i))
	}

	if err := os.WriteFile(path, []byte(ToMarkdown(tr, opts)), 0o644); err != nil {
		return "", fmt.Errorf("写入失败：%w", err)
	}
	return path, nil
}

// badNameChars 是 Windows/Linux 文件名里都不能出现的字符，
// 外加控制字符与路径分隔符。标题里出现斜杠尤其常见（"xxx / yyy"）。
var badNameChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]`)

func sanitizeName(s string) string {
	s = badNameChars.ReplaceAllString(s, "-")
	s = strings.Join(strings.Fields(s), " ") // 压掉连续空白
	// 太长的标题截断：Windows 路径总长有限，标题动辄一句话。
	if r := []rune(s); len(r) > 60 {
		s = string(r[:60])
	}
	return strings.Trim(s, " .-")
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[len(id)-12:]
	}
	return id
}

func escapeTitle(s string) string {
	// 标题里的 # 会把 Markdown 标题层级顶乱。
	return strings.ReplaceAll(s, "#", "\\#")
}

func stamp(ms int64) string {
	return time.UnixMilli(ms).Format("2006-01-02 15:04")
}

func codeEach(list []string) []string {
	out := make([]string, 0, len(list))
	for _, s := range list {
		out = append(out, "`"+s+"`")
	}
	return out
}
