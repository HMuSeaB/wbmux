package migrate

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 读会话头部时只看前 64 KB / 30 行，与客户端自己重建索引时的策略一致。
//
// 理由：单个会话文件可以到十几 MB，为了拿一个标题而整份读进来，
// 在扫几十条会话时是几百 MB 的无谓 IO。而 cwd 与 ai-title 一定在最前面
// ——ai-title 事件由客户端在首轮回复后写入，位置固定在开头几行。
const (
	headBytes = 64 << 10
	headLines = 30
)

// headInfo 是从会话文件头部能提取到的信息。
type headInfo struct {
	Cwd   string
	Title string
}

// sessionFiles 是一条会话在磁盘上的全部文件。
type sessionFiles struct {
	JSONL    string
	Meta     string
	Rollback string
	Size     int64
	Head     headInfo
}

// scanSessions 遍历数据目录下 projects/ 的所有工作区，把会话按 id 归组。
//
// 目录不存在不算错误：用户可能刚装好客户端还没建过会话。
func scanSessions(dataDir string) (map[string]sessionFiles, error) {
	out := map[string]sessionFiles{}
	if dataDir == "" {
		return out, nil
	}
	root := filepath.Join(dataDir, "projects")

	dirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		// 单个工作区目录读不动（权限、被占用）不该让整次扫描失败，
		// 跳过即可——其余工作区的会话照样能搬。
		entries, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}

		// 先把划词临时会话的标记收齐。这类会话是「划词」快捷提问产生的，
		// 客户端用 sidecar 文件把它挡在会话列表之外；我们没有 sidecar 判据
		// 就没法区分，不如直接跳过，免得把一堆临时会话灌进对方列表。
		quick := map[string]bool{}
		for _, e := range entries {
			if n, ok := strings.CutSuffix(e.Name(), quickAskSuffix); ok {
				quick[n] = true
			}
		}

		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".jsonl")
			if id == "" || quick[id] {
				continue
			}

			jsonlPath := filepath.Join(root, d.Name(), e.Name())
			info, err := os.Stat(jsonlPath)
			if err != nil || info.Size() == 0 {
				// 空文件是客户端写了一半或已废弃的残留，搬过去只会是坏记录。
				continue
			}

			sf := sessionFiles{JSONL: jsonlPath, Size: info.Size()}
			if p := filepath.Join(root, d.Name(), id+".meta.json"); fileExists(p) {
				sf.Meta = p
			}
			if p := filepath.Join(root, d.Name(), id+".file-rollback.ndjson"); fileExists(p) {
				sf.Rollback = p
			}

			// 头部读不动就仍然收下这条会话，只是拿不到 cwd 与标题。
			// 丢一条会话比显示一条标题空白的会话糟得多。
			sf.Head, _ = readHead(jsonlPath)
			out[id] = sf
		}
	}
	return out, nil
}

// quickAskSuffix 是划词临时会话的 sidecar 标记文件后缀。
const quickAskSuffix = ".quickask"

// readHead 从会话文件头部提取 cwd 与标题。
func readHead(path string) (headInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return headInfo{}, err
	}
	defer f.Close()

	buf := make([]byte, headBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return headInfo{}, err
	}
	buf = buf[:n]

	var out headInfo
	for i, line := range strings.Split(string(buf), "\n") {
		if i >= headLines {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 64 KB 的窗口可能把最后一行切断（也可能切断在多字节字符中间）。
		// 解析失败就跳过——切断的只可能是尾部，前面几行是完整的。
		var ev struct {
			Type        string `json:"type"`
			Cwd         string `json:"cwd"`
			AiTitle     string `json:"aiTitle"`
			CustomTitle string `json:"customTitle"`
			Topic       string `json:"topic"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}

		if out.Cwd == "" && ev.Cwd != "" {
			out.Cwd = ev.Cwd
		}
		// 标题取第一个命中的。custom-title 是用户手改的，优先于自动生成的
		// ai-title，但两者出现顺序不定，所以宁可先到先得也不做优先级比较。
		if out.Title == "" {
			switch ev.Type {
			case "ai-title":
				out.Title = ev.AiTitle
			case "custom-title":
				out.Title = ev.CustomTitle
			case "topic":
				out.Title = ev.Topic
			}
		}
	}
	return out, nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
