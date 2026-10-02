// 把 Codex 会话导出成能直接在浏览器里看的 HTML。
//
// # 为什么是 HTML 而不是 Markdown
//
// 用户的诉求是"要完整对话、图片也能看"。Markdown 有两个缺口：
//   - 图片要么外链（另一个文件夹，一挪就断）、要么塞 base64（绝大多数编辑器
//     的 Markdown 预览不认 data: URI）；
//   - 工具调用/思考过程没有通用写法，塞进去会污染正文。
//
// HTML 一次解决：图片直接内嵌 data URI、工具调用可以折叠、还能搜索。
//
// # 图片怎么处理
//
// 实测 Codex 把图片**原样存在 jsonl 里**（`input_image.image_url` 就是
// `data:image/jpeg;base64,...`），27 张共 5 MB。所以**照搬即可**，不用重编码。
// 代价是页面文件会大（单个会话可能十几 MB）——但这比"图丢了"强得多，
// 而且这是本地文件，不存在网速问题。
//
// # 为什么不解析 event_msg / turn_context
//
// 那些是运行时的状态快照（token 计数、沙箱策略），不是对话内容。
// 用户要审阅的是"说了什么、做了什么"，把噪声塞进去反而难读。
package sessions

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CodexTurn 是还原出来的一轮对话里的一项。
type CodexTurn struct {
	Kind string // user | assistant | tool | reasoning | meta
	Role string
	Text string
	// Image 是 data URI（直接塞进 <img src>）。单张图的便捷字段。
	Image string
	// Images 是一条消息里的多张图。用户发图时常配一句话，图和字要在同一块里。
	Images []string
	// ToolName / ToolArg 用于工具调用。
	ToolName string
	ToolArg  string
	// IsEnv 标记这是环境注入（AGENTS.md / environment_context），默认折叠。
	IsEnv bool
}

// CodexTranscript 是一个还原好的会话。
type CodexTranscript struct {
	ID        string
	CWD       string
	CreatedAt time.Time
	UpdatedAt time.Time
	Source    string
	Turns     []CodexTurn
	// Sub 标记这是子代理的转录。
	Sub bool
}

// 环境注入的段落前缀。这些是客户端塞给模型的上下文，不是用户打的字。
var envPrefixes = []string{
	"# AGENTS.md", "<environment_context>", "<permissions", "<user_instructions>",
	"<skills", "<turn_aborted>", "<subagent_notification>",
	"# Files mentioned by the user", "# Context from my IDE setup",
}

func isEnvText(t string) bool {
	for _, p := range envPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// ParseCodex 读一个 rollout-*.jsonl，还原成对话。
func ParseCodex(path string) (*CodexTranscript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tr := &CodexTranscript{Source: path}
	dec := json.NewDecoder(f)
	dec.UseNumber()

	// 工具调用先记下来，等它的 output 到了再合并成一项。
	type pendingCall struct {
		idx  int
		name string
	}
	calls := map[string]pendingCall{}

	for {
		var line map[string]any
		if err := dec.Decode(&line); err != nil {
			break // EOF 或坏行都停：能读多少算多少，别让一行坏了整个导出
		}
		typ, _ := line["type"].(string)
		pl, _ := line["payload"].(map[string]any)
		if pl == nil {
			continue
		}

		if typ == "session_meta" {
			if tr.ID == "" {
				tr.ID, _ = pl["id"].(string)
			}
			if tr.CWD == "" {
				tr.CWD, _ = pl["cwd"].(string)
			}
			if src, ok := pl["source"].(map[string]any); ok {
				if _, ok := src["subagent"]; ok {
					tr.Sub = true
				}
			}
			if ts, ok := pl["timestamp"].(string); ok && tr.CreatedAt.IsZero() {
				tr.CreatedAt = parseRFC3339(ts)
			}
			continue
		}
		if typ != "response_item" {
			continue
		}

		switch pt, _ := pl["type"].(string); pt {
		case "message":
			role, _ := pl["role"].(string)
			// developer 是环境说明，不是对话的一方。
			if role == "developer" {
				continue
			}
			kind := role
			if kind == "" {
				kind = "assistant"
			}
			// **一段消息只出一个气泡。**
			//
			// 实测一条 user 消息的 content 会拆成好几段：
			//   [{"type":"input_text","text":"<image name=[Image #1]>"},
			//    {"type":"input_image","image_url":"data:..."},
			//    {"type":"input_text","text":"</image>"},
			//    {"type":"input_text","text":"帮我构建这个apk"}]
			// 早先按"一段一项"渲染，结果同一句话被切成 4 个气泡，
			// 图片还跟它的说明分家。
			var text strings.Builder
			var imgs []string
			for _, seg := range contentParts(pl["content"]) {
				switch seg.kind {
				case "input_image", "output_image":
					if seg.url != "" {
						imgs = append(imgs, seg.url)
					}
				case "text":
					t := strings.TrimSpace(seg.text)
					if t == "" || isImagePlaceholder(t) {
						continue // 图片占位标记本身没有信息量，图就在旁边
					}
					if text.Len() > 0 {
						text.WriteString("\n")
					}
					text.WriteString(seg.text)
				}
			}
			if text.Len() == 0 && len(imgs) == 0 {
				continue
			}
			if text.Len() > 0 {
				body := text.String()
				tr.Turns = append(tr.Turns, CodexTurn{
					Kind:  kind,
					Role:  role,
					Text:  body,
					IsEnv: isEnvText(strings.TrimSpace(body)),
					// 把这一轮里所有的图挂到同一条上：用户发图时往往配一句话，
					// 分成两个气泡就丢了"哪句话配哪张图"的对应。
					Images: imgs,
				})
			}
		case "reasoning":
			// 思考片段：折叠起来，默认不占版面。
			var sb strings.Builder
			for _, s := range asSlice(pl["summary"]) {
				if m, ok := s.(map[string]any); ok {
					if t, ok := m["text"].(string); ok {
						sb.WriteString(t)
						sb.WriteString("\n")
					}
				}
			}
			if sb.Len() == 0 {
				if ss, ok := pl["content"].(string); ok {
					sb.WriteString(ss)
				}
			}
			if strings.TrimSpace(sb.String()) != "" {
				tr.Turns = append(tr.Turns, CodexTurn{Kind: "reasoning", Text: sb.String()})
			}
		case "function_call", "custom_tool_call":
			name, _ := pl["name"].(string)
			arg, _ := pl["arguments"].(string)
			if arg == "" {
				arg, _ = pl["input"].(string)
			}
			id, _ := pl["call_id"].(string)
			if id == "" {
				id, _ = pl["id"].(string)
			}
			tr.Turns = append(tr.Turns, CodexTurn{
				Kind: "tool", ToolName: name, ToolArg: arg,
			})
			calls[id] = pendingCall{idx: len(tr.Turns) - 1, name: name}
		case "function_call_output", "custom_tool_call_output":
			id, _ := pl["call_id"].(string)
			out := ""
			switch v := pl["output"].(type) {
			case string:
				out = v
			default:
				if b, err := json.Marshal(v); err == nil {
					out = string(b)
				}
			}
			// 合并到对应的那次调用上，而不是单开一项：调用与结果是同一件事。
			if pc, ok := calls[id]; ok && pc.idx < len(tr.Turns) {
				if tr.Turns[pc.idx].ToolArg != "" {
					tr.Turns[pc.idx].ToolArg += "\n\n── 结果 ──\n"
				}
				tr.Turns[pc.idx].ToolArg += out
				delete(calls, id)
			} else {
				tr.Turns = append(tr.Turns, CodexTurn{
					Kind: "tool", ToolName: "(结果)", ToolArg: out,
				})
			}
		}
	}

	if fi, err := os.Stat(path); err == nil {
		tr.UpdatedAt = fi.ModTime()
	}
	return tr, nil
}

// isImagePlaceholder 判断一段文字是不是"图片占位标记"。
//
// Codex 把图片拆成三段：`<image name=[Image #1]>` + base64 图 + `</image>`。
// 首尾两段是纯标记，图就在旁边，留着只会让对话多出两行噪声。
func isImagePlaceholder(t string) bool {
	return t == "</image>" || t == "<image>" ||
		(strings.HasPrefix(t, "<image name=") && strings.HasSuffix(t, ">"))
}

type contentSeg struct {
	kind string
	text string
	url  string
}

// contentParts 拆 content 数组。
//
// **必须判类型**：实测里面有 `input_text` / `input_image` / `output_text`
// 三种 dict，而 `input_image` 那一段带的是 90 KB 级 base64。曾经写过一个
// 版本按 `c["text"]` 直接取，遇到图片段就抛 "str has no attribute get"，
// 结果整个文件被跳过、40 个会话全显示"读不到"。
func contentParts(v any) []contentSeg {
	list := asSlice(v)
	out := make([]contentSeg, 0, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			// 少数会话里 content 直接是字符串数组。
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, contentSeg{kind: "text", text: s})
			}
			continue
		}
		switch t, _ := m["type"].(string); t {
		case "input_image", "output_image":
			u, _ := m["image_url"].(string)
			if u == "" {
				u, _ = m["url"].(string)
			}
			out = append(out, contentSeg{kind: "input_image", url: u})
		default:
			if s, ok := m["text"].(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, contentSeg{kind: "text", text: s})
			}
		}
	}
	return out
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func parseRFC3339(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ---------- HTML 渲染 ----------

// CodexHTMLOptions 控制导出的样子。
type CodexHTMLOptions struct {
	// Title 是页面标题。
	Title string
	// WithTools 是否带上工具调用。默认带但折叠。
	WithTools bool
	// WithReasoning 是否带上思考片段。默认带但折叠。
	WithReasoning bool
	// MaxImageBytes 超过这个大小的图片不内嵌（留个说明）。0 表示不限。
	//
	// 存在的理由：极端情况下单张图可能几 MB，一个页面塞几十张会到几百 MB，
	// 浏览器直接卡死。给个上限，超了就只留标记。
	MaxImageBytes int
}

// RenderCodexHTML 把一个会话渲染成完整的 HTML 页面。
func RenderCodexHTML(tr *CodexTranscript, opts CodexHTMLOptions) string {
	title := opts.Title
	if title == "" {
		title = "Codex 会话"
	}
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\">\n<head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n")
	b.WriteString("<title>" + html.EscapeString(title) + "</title>\n")
	b.WriteString(codexCSS)
	b.WriteString("</head>\n<body>\n")

	// 头部信息
	b.WriteString("<header>\n<h1>" + html.EscapeString(title) + "</h1>\n<div class=\"meta\">")
	if tr.CWD != "" {
		b.WriteString("<span>项目 <code>" + html.EscapeString(tr.CWD) + "</code></span>")
	}
	if !tr.CreatedAt.IsZero() {
		b.WriteString("<span>开始 " + tr.CreatedAt.Format("2006-01-02 15:04") + "</span>")
	}
	b.WriteString("<span>共 " + fmt.Sprint(len(tr.Turns)) + " 项</span>")
	if tr.Sub {
		b.WriteString("<span class=\"tag\">子代理转录</span>")
	}
	b.WriteString("</div>\n")
	b.WriteString("<div class=\"tools\">")
	b.WriteString("<label><input type=\"checkbox\" id=\"hideEnv\" checked> 隐藏环境注入</label>")
	b.WriteString("<label><input type=\"checkbox\" id=\"hideTools\" checked> 折叠工具调用</label>")
	b.WriteString("<label><input type=\"checkbox\" id=\"hideThink\" checked> 折叠思考</label>")
	b.WriteString("<input type=\"search\" id=\"q\" placeholder=\"搜索…\">")
	b.WriteString("</div>\n</header>\n<main>\n")

	// 用户消息里的图片紧跟其后，渲染成一块。
	var imgSeq int
	for _, t := range tr.Turns {
		switch t.Kind {
		case "user", "assistant":
			cls := "msg " + t.Kind
			if t.IsEnv {
				cls += " env"
			}
			b.WriteString("<div class=\"" + cls + "\" data-env=\"" + boolStr(t.IsEnv) + "\">")
			b.WriteString("<div class=\"who\">")
			if t.Kind == "user" {
				b.WriteString("你")
			} else {
				b.WriteString("Codex")
			}
			if t.IsEnv {
				b.WriteString(" <span class=\"tag\">环境注入</span>")
			}
			b.WriteString("</div>\n")
			for _, u := range t.allImages() {
				imgSeq++
				b.WriteString(renderImage(u, imgSeq, opts.MaxImageBytes))
			}
			if strings.TrimSpace(t.Text) != "" {
				b.WriteString("<div class=\"body\">" + renderText(t.Text) + "</div>\n")
			}
			b.WriteString("</div>\n")
		case "reasoning":
			if !opts.WithReasoning {
				continue
			}
			b.WriteString("<details class=\"reasoning\" data-kind=\"think\"><summary>思考</summary>")
			b.WriteString("<div class=\"body\">" + renderText(t.Text) + "</div></details>\n")
		case "tool":
			if !opts.WithTools {
				continue
			}
			b.WriteString("<details class=\"tool\" data-kind=\"tool\"><summary>🔧 " +
				html.EscapeString(t.ToolName) + "</summary>")
			b.WriteString("<pre>" + html.EscapeString(t.ToolArg) + "</pre></details>\n")
		}
	}

	b.WriteString("</main>\n")
	b.WriteString(codexJS)
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// allImages 返回这条消息的所有图（把单张与多张两个字段合起来）。
func (t CodexTurn) allImages() []string {
	if len(t.Images) > 0 {
		return t.Images
	}
	if t.Image != "" {
		return []string{t.Image}
	}
	return nil
}

func boolStr(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// renderImage 把 data URI 变成 <img>。
//
// 超限时**不静默丢掉**，而是留一行说明 + 原始大小：用户至少要知道"这里本来
// 有张图"，而不是以为没有。
func renderImage(dataURI string, seq int, maxBytes int) string {
	if !strings.HasPrefix(dataURI, "data:") {
		// 不是 data URI（极少见），当链接用。
		return "<div class=\"img\"><a href=\"" + html.EscapeString(dataURI) +
			"\" target=\"_blank\">[图片 " + fmt.Sprint(seq) + "]</a></div>\n"
	}
	approx := len(dataURI) * 3 / 4
	if maxBytes > 0 && approx > maxBytes {
		return fmt.Sprintf("<div class=\"img warn\">图片 %d 太大（约 %.1f MB），未内嵌</div>\n",
			seq, float64(approx)/1048576)
	}
	return "<div class=\"img\"><img src=\"" + html.EscapeString(dataURI) +
		"\" alt=\"图片 " + fmt.Sprint(seq) + "\" loading=\"lazy\"></div>\n"
}

// renderText 把纯文本渲染成 HTML：转义 + 保留换行 + 把代码块标出来。
//
// 不做完整 Markdown 解析：会话里混着各种格式，自己写一个半吊子解析器
// 只会把原文弄乱。保住"可读、可搜、可复制"就够了。
func renderText(s string) string {
	esc := html.EscapeString(s)
	// 三个及以上的连续短横线，当成分隔线——Codex 常用它分节。
	esc = strings.ReplaceAll(esc, "\n---\n", "\n<hr>\n")
	return "<pre class=\"txt\">" + esc + "</pre>"
}

// DecodeDataURISize 估算一个 data URI 的原始字节数。
func DecodeDataURISize(s string) int {
	if i := strings.Index(s, ","); i >= 0 {
		s = s[i+1:]
	}
	n, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return len(s) * 3 / 4
	}
	return len(n)
}

// ---------- 批量导出 ----------

// ExportCodexDir 把一批会话导出到一个目录，每个会话一个 HTML。
//
// 同时生成 index.html 作为目录页——40 个文件光看文件名认不出谁是谁。
func ExportCodexDir(files []string, outDir, title string, opts CodexHTMLOptions) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}
	type item struct {
		file string
		tr   *CodexTranscript
		out  string
	}
	var items []item
	used := map[string]int{}

	for _, f := range files {
		tr, err := ParseCodex(f)
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		// 文件名里带时间戳，直接用就够稳定。
		name := sanitizeFileName(base) + ".html"
		if n := used[name]; n > 0 {
			name = fmt.Sprintf("%s-%d.html", sanitizeFileName(base), n)
		}
		used[name]++

		t := opts
		t.Title = firstMeaningful(tr)
		page := RenderCodexHTML(tr, t)
		if err := os.WriteFile(filepath.Join(outDir, name), []byte(page), 0o644); err != nil {
			continue
		}
		items = append(items, item{file: f, tr: tr, out: name})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].tr.UpdatedAt.After(items[j].tr.UpdatedAt)
	})

	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh-CN\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">")
	b.WriteString("<title>" + html.EscapeString(title) + "</title>")
	b.WriteString(codexCSS + "</head><body>\n")
	b.WriteString("<header><h1>" + html.EscapeString(title) + "</h1>")
	b.WriteString("<div class=\"meta\"><span>共 " + fmt.Sprint(len(items)) + " 个会话</span>")
	b.WriteString("<span>导出 " + time.Now().Format("2006-01-02 15:04") + "</span></div>")
	b.WriteString("<div class=\"tools\"><input type=\"search\" id=\"q\" placeholder=\"搜项目 / 内容…\"></div>")
	b.WriteString("</header>\n<main>\n<ul class=\"idx\">")
	for _, it := range items {
		sub := ""
		if it.tr.Sub {
			sub = " <span class=\"tag\">子代理</span>"
		}
		b.WriteString("<li data-s=\"" + html.EscapeString(strings.ToLower(it.tr.CWD+" "+firstMeaningful(it.tr))) + "\">")
		b.WriteString("<a href=\"" + html.EscapeString(it.out) + "\">")
		b.WriteString("<span class=\"t\">" + html.EscapeString(firstMeaningful(it.tr)) + "</span>")
		b.WriteString("<span class=\"d\">" + it.tr.UpdatedAt.Format("2006-01-02 15:04") + "</span>")
		b.WriteString("<span class=\"c\">" + html.EscapeString(it.tr.CWD) + "</span>")
		b.WriteString(sub)
		b.WriteString("</a></li>\n")
	}
	b.WriteString("</ul>\n</main>\n")
	b.WriteString(codexJS + "</body></html>\n")

	idx := filepath.Join(outDir, "index.html")
	if err := os.WriteFile(idx, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return idx, nil
}

// firstMeaningful 取第一段有意义的用户话当标题。
//
// 优先对话正文；环境注入不算（每条都以 AGENTS.md 开头，全一样，认不出谁是谁）。
func firstMeaningful(tr *CodexTranscript) string {
	for _, t := range tr.Turns {
		if t.IsEnv || t.Kind != "user" {
			continue
		}
		s := strings.Join(strings.Fields(t.Text), " ")
		if s == "" || strings.HasPrefix(s, "<image") {
			continue
		}
		if r := []rune(s); len(r) > 60 {
			s = string(r[:60]) + "…"
		}
		return s
	}
	// 只有图片的会话，退回项目名。
	if tr.CWD != "" {
		return filepath.Base(tr.CWD)
	}
	return "(无标题)"
}

func sanitizeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' ||
			r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

const codexCSS = `<style>
:root{--bg:#0e0f12;--pan:#16181d;--pan2:#1c1f25;--ink:#e6e8ec;--ink2:#a4aab5;
--ink3:#6f757f;--line:#2a2e36;--user:#3d7ea6;--ai:#4a9d6a;--mono:ui-monospace,
SFMono-Regular,"Cascadia Mono",Consolas,monospace;--sans:system-ui,-apple-system,
"Segoe UI","Microsoft YaHei",sans-serif}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.7 var(--sans)}
header{position:sticky;top:0;z-index:9;background:rgba(14,15,18,.94);
backdrop-filter:blur(8px);border-bottom:1px solid var(--line);padding:14px 22px 10px}
h1{margin:0 0 6px;font-size:17px;font-weight:600}
.meta{display:flex;flex-wrap:wrap;gap:14px;color:var(--ink2);font-size:12.5px}
.meta code{font-family:var(--mono);color:var(--ink2)}
.tag{background:var(--pan2);border:1px solid var(--line);border-radius:4px;
padding:0 6px;font-size:11px;color:var(--ink3)}
.tools{display:flex;gap:14px;align-items:center;flex-wrap:wrap;margin-top:9px;font-size:12.5px;color:var(--ink2)}
.tools label{display:flex;gap:5px;align-items:center;cursor:pointer}
.tools input[type=search]{background:var(--pan);border:1px solid var(--line);
color:var(--ink);border-radius:6px;padding:4px 10px;min-width:190px;font-family:var(--sans)}
main{max-width:940px;margin:0 auto;padding:20px 22px 60px}
.msg{border-left:3px solid var(--line);background:var(--pan);border-radius:0 8px 8px 0;
padding:11px 15px;margin:13px 0}
.msg.user{border-left-color:var(--user)}
.msg.assistant{border-left-color:var(--ai)}
.msg.env{border-left-color:var(--line);background:var(--pan2);opacity:.72}
.who{font-size:12px;color:var(--ink3);margin-bottom:5px;font-weight:600;letter-spacing:.02em}
.body .txt{white-space:pre-wrap;word-break:break-word;margin:0;font:13.5px/1.72 var(--mono)}
.msg.user .body .txt{font-family:var(--sans);font-size:14px}
.body hr{border:0;border-top:1px solid var(--line);margin:10px 0}
details{margin:10px 0;border:1px solid var(--line);border-radius:7px;background:var(--pan)}
details summary{cursor:pointer;padding:6px 11px;font-size:12.5px;color:var(--ink2);user-select:none}
details[open] summary{border-bottom:1px solid var(--line)}
details.reasoning{border-left:3px solid #6b5f8a}
details.tool{border-left:3px solid #8a7350}
details pre{margin:0;padding:10px 12px;overflow:auto;max-height:420px;
white-space:pre-wrap;word-break:break-word;font:12.5px/1.6 var(--mono);color:var(--ink2)}
.img{margin:9px 0}
.img img{max-width:100%;border-radius:8px;border:1px solid var(--line);display:block}
.img.warn{color:#b8863b;font-size:12.5px;padding:7px 10px;background:var(--pan2);border-radius:6px}
.idx{list-style:none;padding:0;margin:0}
.idx li{margin-bottom:7px}
.idx a{display:grid;grid-template-columns:1fr auto;gap:3px 14px;align-items:baseline;
padding:10px 13px;background:var(--pan);border:1px solid transparent;border-radius:8px;
text-decoration:none;color:var(--ink)}
.idx a:hover{border-color:var(--line);background:var(--pan2)}
.idx .t{font-weight:500}
.idx .d{color:var(--ink3);font-size:12px;white-space:nowrap}
.idx .c{grid-column:1/-1;color:var(--ink3);font-size:11.5px;font-family:var(--mono)}
mark{background:#5a4a1e;color:var(--ink)}
</style>`

const codexJS = `<script>
(function(){
  var hideEnv=document.getElementById('hideEnv'),
      hideTools=document.getElementById('hideTools'),
      hideThink=document.getElementById('hideThink'),
      q=document.getElementById('q');

  function apply(){
    if(hideEnv){document.querySelectorAll('[data-env="1"]').forEach(function(e){
      e.style.display=hideEnv.checked?'none':'';});}
    if(hideTools){document.querySelectorAll('[data-kind="tool"]').forEach(function(e){
      e.open=!hideTools.checked;e.style.display='';});}
    if(hideThink){document.querySelectorAll('[data-kind="think"]').forEach(function(e){
      e.open=!hideThink.checked;e.style.display='';});}
  }
  ['hideEnv','hideTools','hideThink'].forEach(function(id){
    var el=document.getElementById(id); if(el) el.addEventListener('change',apply);
  });
  apply();

  // 搜索：会话页里过滤消息块；目录页里过滤条目。
  if(q){
    q.addEventListener('input',function(){
      var s=q.value.trim().toLowerCase();
      var nodes=document.querySelectorAll('.msg, .idx li');
      nodes.forEach(function(n){
        var hit=!s||(n.textContent||'').toLowerCase().indexOf(s)>=0;
        var d=n.getAttribute&&n.getAttribute('data-s');
        if(d) hit=!s||d.indexOf(s)>=0;
        n.style.display=hit?'':'none';
      });
      // 搜索时把折叠块展开，否则命中的内容可能在折叠里看不见。
      if(s){document.querySelectorAll('details').forEach(function(d){d.open=true;});}
      else{apply();}
    });
  }
})();
</script>`
