package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code 的会话按项目分目录：~/.claude/projects/<编码路径>/<uuid>.jsonl。
//
// 目录名是路径编码（非字母数字字符全部变成 "-"，如
// D:\4rchive\Code\cc-switch → D--4rchive-Code-cc-switch）。这个编码
// 不可逆——路径里本来就带 "-" 的目录（比如 N_m3u8DL-CLI）解不回去，
// 所以项目路径不从目录名反推，而是从会话文件行内的 cwd 字段拿；
// 目录名只用来缩小遍历范围。
//
// 行结构（2026-09-28 实测）：
//
//	{"type":"mode","mode":"normal","sessionId":"…"}
//	{"type":"file-history-snapshot",…,"timestamp":"2026-09-24T05:21:45.799Z"}
//	{"type":"user","message":{"role":"user","content":"…"},"cwd":"…",…}
//	{"type":"summary","summary":"用户起的会话名",…}

type claudeLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionId"`
	Message   struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Summary string `json:"summary"`
}

func defaultClaudeRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// scanClaude 扫 Claude Code 会话目录。
func scanClaude(root string) ([]Session, []string, error) {
	projDirs, err := os.ReadDir(root)
	if err != nil {
		return nil, nil, nil // 没装 Claude Code 不算错
	}
	var out []Session
	for _, pd := range projDirs {
		if !pd.IsDir() {
			continue
		}
		pdir := filepath.Join(root, pd.Name())
		files, err := os.ReadDir(pdir)
		if err != nil {
			continue
		}
		for _, f := range files {
			// 会话文件是 <uuid>.jsonl；同名目录（无扩展名）是它的附属快照，
			// 不在遍历范围里。
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(pdir, f.Name())
			s, err := parseClaudeFile(path, pd.Name())
			if err != nil {
				continue
			}
			out = append(out, s)
		}
	}
	return out, nil, nil
}

// parseClaudeFile 只扫文件头部。
//
// cwd 和标题都藏在正文里，但正文动辄几十兆，全读就背离"元数据扫描"了。
// 实测两者都出现在文件靠前的位置（cwd 随每条消息带、summary 行在最前），
// 所以最多读前 maxProbeLines 行，找不到就认了——宁可标"(无标题)"、
// 项目落到目录名，也不为一小部分老会话读全量正文。
func parseClaudeFile(path, dirName string) (Session, error) {
	const maxProbeLines = 200

	f, err := os.Open(path)
	if err != nil {
		return Session{}, err
	}
	defer f.Close()

	var (
		cwd       string
		sessionID string
		title     string
		firstTs   string
		firstUser string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024) // 单行可能是一整次粘贴的内容
	for i := 0; i < maxProbeLines && sc.Scan(); i++ {
		var ln claudeLine
		if json.Unmarshal(sc.Bytes(), &ln) != nil {
			continue
		}
		if firstTs == "" && ln.Timestamp != "" {
			firstTs = ln.Timestamp
		}
		if cwd == "" && ln.Cwd != "" {
			cwd = ln.Cwd
		}
		if sessionID == "" && ln.SessionID != "" {
			sessionID = ln.SessionID
		}
		if title == "" && ln.Type == "summary" && strings.TrimSpace(ln.Summary) != "" {
			title = strings.TrimSpace(ln.Summary)
		}
		if firstUser == "" && ln.Type == "user" {
			firstUser = claudeUserText(ln.Message.Content)
		}
		if cwd != "" && title != "" {
			break
		}
	}

	if title == "" {
		title = firstUser
	}
	if title == "" {
		title = "(无标题)"
	}
	if len(title) > 120 {
		title = title[:120] + "…"
	}

	// 项目路径拿不到时退回目录名：展示是"加密样子"但归组仍然正确——
	// 同一目录名必然对应同一个真实项目。
	if cwd == "" {
		cwd = dirName
	}

	st, err := os.Stat(path)
	updatedMs := int64(0)
	if err == nil {
		updatedMs = st.ModTime().UnixMilli()
	}
	createdMs := updatedMs
	if ms, err := parseRFC3339Nanos(firstTs); err == nil && ms > 0 {
		createdMs = ms
	}

	return Session{
		Vendor:     VendorClaude,
		ProjectRaw: cwd,
		ID:         sessionID,
		Title:      title,
		CreatedMs:  createdMs,
		UpdatedMs:  updatedMs,
		SizeBytes:  fileSize(path),
		Kind:       "session",
		Source:     Source{Kind: "file", Path: path},
	}, nil
}

// claudeUserText 从 message.content 里抠出可读文本。
// content 可能是纯字符串，也可能是分块数组——块的 type 有两种实测形态：
// Claude 是 "text"，Codex 是 "input_text"，两样都认；
// 数组里取第一块文本就够当标题了。
func claudeUserText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if (b.Type == "text" || b.Type == "input_text") && strings.TrimSpace(b.Text) != "" {
				return strings.TrimSpace(b.Text)
			}
		}
	}
	return ""
}
