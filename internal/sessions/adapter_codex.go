package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Codex 的会话是纯文件：~/.codex/sessions/年/月/日/rollout-*.jsonl，
// 首行就是 session_meta（cwd、session_id、时间），外加一份现成的
// 总索引 ~/.codex/session_index.jsonl（id → thread_name）。
// 2026-09-28 实测首行结构：
//
//	{"timestamp":"…","type":"session_meta","payload":{"id":"…","cwd":"D:\\…",…}}
//
// 项目归属直接取 cwd，标题取索引里的 thread_name——不用碰会话正文，
// 1.4G 的目录也只付元数据的代价。

type codexMeta struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

type codexIndexEntry struct {
	ID        string `json:"id"`
	Name      string `json:"thread_name"`
	UpdatedAt string `json:"updated_at"` // RFC3339 纳秒串，解析失败就退回文件 mtime
}

// defaultCodexRoots 返回 Codex 的两个会话根：在役的 sessions/（按年/月/日
// 分层）与归档的 archived_sessions/（平铺）。归档会话同样是历史——真机
// 实测 archived_sessions/ 里有 7 个 rollout 文件、格式与在役完全相同
// （2026-09-28），只扫 sessions/ 会整块漏掉。
// codexRoots 返回 Codex 的两个会话根。
//
// **必须从 probe 取 home，不能自己调 os.UserHomeDir()。**
// 后者读的是进程环境，而 probe.Home 是"这台机器上该用哪个 home"的权威答案
// ——测试里它指向临时目录。曾经这里直接读 os.UserHomeDir()，结果是：
// 单元测试造出来的会话一个都扫不到（扫的是真实的 ~/.codex），
// 测试于是"通过"了一个什么都不做也能通过的路径。
//
// 这个错只在测试里暴露，是因为真实运行时两者恰好相同。
func codexRoots(p *variant.Probe) []string {
	home := p.Home
	return []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".codex", "archived_sessions"),
	}
}

func codexIndexPath(p *variant.Probe) string {
	return filepath.Join(p.Home, ".codex", "session_index.jsonl")
}

// scanCodex 扫 Codex 会话目录。roots / indexPath 抽出来是为了单测
// 可以喂 testdata 小样本，不用真装一个 Codex。
//
// 两个根可能有同一会话的两份拷贝（归档语义上应是"移动"，但不赌它）：
// 按 session id 去重，保留更新时间较新的那份。
func scanCodex(roots []string, indexPath string) ([]Session, []string, error) {
	var existing []string
	for _, root := range roots {
		if _, err := os.Stat(root); err == nil {
			existing = append(existing, root)
		}
	}
	if len(existing) == 0 {
		return nil, nil, nil // 没装 Codex 不算错
	}
	titles := loadCodexIndex(indexPath)

	byID := map[string]int{} // session id → out 里的下标
	var out []Session
	for _, root := range existing {
		werr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // 单个目录读不动就跳过，别让整源失败
			}
			if d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
				return nil
			}
			s, perr := parseCodexFile(path, titles)
			if perr != nil {
				return nil // 同上：个别文件坏掉不算源失败
			}
			if idx, ok := byID[s.ID]; ok {
				if s.UpdatedMs > out[idx].UpdatedMs {
					out[idx] = s
				}
				return nil
			}
			byID[s.ID] = len(out)
			out = append(out, s)
			return nil
		})
		if werr != nil {
			return nil, nil, werr
		}
	}
	return out, nil, nil
}

// parseCodexFile 读首行 session_meta + 扫文件头部找首条真用户消息。
//
// 用 Scanner（1MB 行上限）而不是 ReadBytes：行可能极大（整段粘贴、
// base64 图），无上限的累积读取会吃内存；超长行直接放弃标题兜底——
// 标题是尽力而为，不为它冒内存风险。
func parseCodexFile(path string, titles map[string]codexIndexEntry) (Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer f.Close()

	sc := bufio.NewScanner(bufio.NewReaderSize(f, 1<<20))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	if !sc.Scan() {
		if serr := sc.Err(); serr != nil {
			return Session{}, serr
		}
		return Session{}, fmt.Errorf("空文件")
	}
	var meta codexMeta
	if uerr := json.Unmarshal(sc.Bytes(), &meta); uerr != nil {
		return Session{}, uerr
	}
	if meta.Type != "session_meta" {
		// 首行是合法 JSON 但不是 session_meta（截断、非会话文件）——
		// 绝不能返回零值会话：那会在列表里造出一行空厂商、空项目、
		// 空标题的"幽灵"。返回错误让调用方按坏文件整个跳过。
		return Session{}, fmt.Errorf("首行不是 session_meta（%s）", meta.Type)
	}

	st, err := os.Stat(path)
	updatedMs := int64(0)
	if err == nil {
		updatedMs = st.ModTime().UnixMilli()
	}
	if e, ok := titles[meta.Payload.ID]; ok {
		if ms, perr := parseRFC3339Nanos(e.UpdatedAt); perr == nil && ms > 0 {
			updatedMs = ms
		}
	}
	// Created 用会话自己的时间戳（session_meta 顶层就有）：首行时间才是
	// "这场对话什么时候开始的"，文件 mtime 只是兜底。
	createdMs := updatedMs
	if ms, terr := parseRFC3339Nanos(meta.Timestamp); terr == nil && ms > 0 {
		createdMs = ms
	}
	title := ""
	if e, ok := titles[meta.Payload.ID]; ok && strings.TrimSpace(e.Name) != "" {
		title = strings.TrimSpace(e.Name)
	} else {
		// 标题兜底：索引里没有 thread_name 时，从正文头部找第一条"真用户
		// 消息"当标题——这是 WorkBuddy 手动清理清单里最有价值的信息
		// （"它在聊什么"），收进适配器后面板就不用代劳。
		if fp := firstUserPrompt(sc); fp != "" {
			title = fp
		}
	}
	if title == "" {
		title = "(无标题)"
	}

	return Session{
		Vendor:     VendorCodex,
		ProjectRaw: meta.Payload.Cwd,
		ID:         meta.Payload.ID,
		Title:      title,
		CreatedMs:  createdMs,
		UpdatedMs:  updatedMs,
		SizeBytes:  fileSize(path),
		Kind:       "thread",
		Source:     Source{Kind: "file", Path: path},
	}, nil
}

// firstUserPrompt 沿同一个 Scanner 继续扫，找第一条真用户消息（截 80 字）。
// 只再扫 300 行：首条用户消息总在文件头部。单行超 1MB 会终止扫描——
// 标题是尽力而为。坏行、环境注入（复用渲染器的 envPrefixes/isEnvText，
// 两处识别口径保持一致）都跳过。
func firstUserPrompt(sc *bufio.Scanner) string {
	for i := 0; i < 300 && sc.Scan(); i++ {
		var p struct {
			Payload struct {
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"payload"`
		}
		// 注意层级：顶层 type 是 "response_item"，"message"/"user" 在 payload 里。
		if json.Unmarshal(sc.Bytes(), &p) != nil || p.Payload.Type != "message" || p.Payload.Role != "user" {
			continue
		}
		text := strings.TrimSpace(claudeUserText(p.Payload.Content))
		if text == "" || isEnvText(text) {
			continue
		}
		out := strings.ReplaceAll(text, "\r", "")
		if j := strings.IndexByte(out, '\n'); j >= 0 {
			out = out[:j]
		}
		r := []rune(out)
		if len(r) > 80 {
			out = string(r[:80]) + "…"
		}
		return out
	}
	return ""
}

// loadCodexIndex 把 session_index.jsonl 读成 id → 条目。
// 文件可能正在被 Codex 追加写，逐行解析时容忍尾部残行。
func loadCodexIndex(path string) map[string]codexIndexEntry {
	out := map[string]codexIndexEntry{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e codexIndexEntry
		if json.Unmarshal([]byte(line), &e) == nil && e.ID != "" {
			out[e.ID] = e
		}
	}
	return out
}

// parseRFC3339Nanos 解析 Codex 索引里的时间串。
// Go 的 RFC3339 布局能吃纳秒小数（time.RFC3339Nano 只是打印格式，
// 解析用哪个都行），但那位小数可能被截断成 7 位——time.Parse 照样认。
func parseRFC3339Nanos(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

func fileSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}
