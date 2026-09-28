package sessions

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	Type    string `json:"type"`
	Payload struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

type codexIndexEntry struct {
	ID        string `json:"id"`
	Name      string `json:"thread_name"`
	UpdatedAt string `json:"updated_at"` // RFC3339 纳秒串，解析失败就退回文件 mtime
}

func defaultCodexRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

func defaultCodexIndex() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "session_index.jsonl")
}

// scanCodex 扫 Codex 会话目录。root / indexPath 抽出来是为了单测
// 可以喂 testdata 小样本，不用真装一个 Codex。
func scanCodex(root, indexPath string) ([]Session, []string, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, nil, nil // 没装 Codex 不算错
	}
	titles := loadCodexIndex(indexPath)

	var out []Session
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		out = append(out, s)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out, nil, nil
}

// parseCodexFile 只读首行。首行是 session_meta（约 10K 上下文说明），
// 第二行开始才是逐条事件——用 ReadBytes 而不是 Scanner，
// 免得某行超长把整次扫描带崩。
func parseCodexFile(path string, titles map[string]codexIndexEntry) (Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 1<<20)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Session{}, err
	}
	var meta codexMeta
	if err := json.Unmarshal(line, &meta); err != nil || meta.Type != "session_meta" {
		return Session{}, err
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
	title := "(无标题)"
	if e, ok := titles[meta.Payload.ID]; ok && strings.TrimSpace(e.Name) != "" {
		title = strings.TrimSpace(e.Name)
	}

	return Session{
		Vendor:     VendorCodex,
		ProjectRaw: meta.Payload.Cwd,
		ID:         meta.Payload.ID,
		Title:      title,
		CreatedMs:  updatedMs, // 会话创建时间取不到精确值时，更新时间兜底
		UpdatedMs:  updatedMs,
		SizeBytes:  fileSize(path),
		Kind:       "thread",
		Source:     Source{Kind: "file", Path: path},
	}, nil
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
