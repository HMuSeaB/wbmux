package sessiontelemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// SessionMetric 是从客户端本地活跃会话文件中提取的最新交互指标。
type SessionMetric struct {
	SessionID        string    `json:"sessionId"`
	FilePath         string    `json:"filePath"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
	TotalTokens      int       `json:"totalTokens"`
	Credit           float64   `json:"credit,omitempty"`
	Timestamp        time.Time `json:"timestamp"`
	Side             string    `json:"side"` // "cn" 或 "intl"
}

// FindLatestActiveSession 扫描国内端与国际端数据目录，找到最近修改的活跃 .jsonl 会话文件。
// 仅搜索 projects/ 目录下的 .jsonl 文件，返回最新修改的文件路径与所属档位。
func FindLatestActiveSession(probe *variant.Probe) (latestPath string, latestSide string, latestMtime time.Time) {
	if probe == nil {
		probe = variant.DefaultProbe()
	}
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		dataDir := probe.DataDir(id)
		if dataDir == "" {
			continue
		}
		projDir := filepath.Join(dataDir, "projects")
		if fi, err := os.Stat(projDir); err != nil || !fi.IsDir() {
			continue
		}

		_ = filepath.Walk(projDir, func(p string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() {
				return nil
			}
			if strings.HasSuffix(info.Name(), ".jsonl") {
				mtime := info.ModTime()
				if mtime.After(latestMtime) {
					latestMtime = mtime
					latestPath = p
					latestSide = string(id)
				}
			}
			return nil
		})
	}
	return latestPath, latestSide, latestMtime
}

// TailReadUsage 使用 O(1) 尾部 Seek 仅读取末尾数据块（默认 64KB），
// 逆序解析最新一轮对话中记录的 Token 与用量指标。
func TailReadUsage(filePath, side string) (*SessionMetric, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size == 0 {
		return nil, nil
	}

	const maxTailBytes = int64(64 * 1024)
	readBytes := maxTailBytes
	var offset int64 = 0
	if size > maxTailBytes {
		offset = size - maxTailBytes
		readBytes = maxTailBytes
	} else {
		readBytes = size
	}

	buf := make([]byte, readBytes)
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	buf = buf[:n]

	lines := bytes.Split(buf, []byte("\n"))
	// 如果不是从文件头读起的，切片的第一行大概率是截断的行，丢弃
	startIdx := 0
	if offset > 0 && len(lines) > 0 {
		startIdx = 1
	}

	// 从后往前逆序找最新的一条包含 Token 消耗的数据行
	for i := len(lines) - 1; i >= startIdx; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		// 快速前置判断：没有 usage 关键字直接跳过，避免无谓的 json 反序列化开销
		if !bytes.Contains(line, []byte("usage")) && !bytes.Contains(line, []byte("tokens")) {
			continue
		}

		metric := parseLineMetric(line)
		if metric != nil {
			if metric.SessionID == "" {
				metric.SessionID = strings.TrimSuffix(filepath.Base(filePath), ".jsonl")
			}
			metric.FilePath = filePath
			metric.Side = side
			if metric.Timestamp.IsZero() {
				metric.Timestamp = fi.ModTime()
			}
			return metric, nil
		}
	}

	return nil, nil
}

// parseLineMetric 从单行 JSON 中尽可能挖掘完整的 Token、模型与消耗字段。
func parseLineMetric(data []byte) *SessionMetric {
	var row struct {
		ID           string `json:"id"`
		SessionID    string `json:"sessionId"`
		Timestamp    int64  `json:"timestamp"`
		ProviderData struct {
			Model            string `json:"model"`
			RequestModelId   string `json:"requestModelId"`
			RequestModelName string `json:"requestModelName"`
			RawUsage         struct {
				PromptTokens     int     `json:"prompt_tokens"`
				CompletionTokens int     `json:"completion_tokens"`
				TotalTokens      int     `json:"total_tokens"`
				Credit           float64 `json:"credit"`
			} `json:"rawUsage"`
			Usage struct {
				InputTokens  int `json:"inputTokens"`
				OutputTokens int `json:"outputTokens"`
				TotalTokens  int `json:"totalTokens"`
			} `json:"usage"`
		} `json:"providerData"`
		Message struct {
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}

	if err := json.Unmarshal(data, &row); err != nil {
		return nil
	}

	prompt := row.ProviderData.RawUsage.PromptTokens
	if prompt == 0 {
		prompt = row.Message.Usage.InputTokens
	}
	if prompt == 0 {
		prompt = row.ProviderData.Usage.InputTokens
	}

	completion := row.ProviderData.RawUsage.CompletionTokens
	if completion == 0 {
		completion = row.Message.Usage.OutputTokens
	}
	if completion == 0 {
		completion = row.ProviderData.Usage.OutputTokens
	}

	total := row.ProviderData.RawUsage.TotalTokens
	if total == 0 {
		total = row.Message.Usage.TotalTokens
	}
	if total == 0 {
		total = row.ProviderData.Usage.TotalTokens
	}
	if total == 0 && (prompt > 0 || completion > 0) {
		total = prompt + completion
	}

	// 如果没有提取出任何有效 token，不算度量行
	if prompt == 0 && completion == 0 && total == 0 {
		return nil
	}

	model := row.ProviderData.RequestModelName
	if model == "" {
		model = row.ProviderData.RequestModelId
	}
	if model == "" {
		model = row.ProviderData.Model
	}

	var t time.Time
	if row.Timestamp > 0 {
		t = time.UnixMilli(row.Timestamp)
	}

	return &SessionMetric{
		SessionID:        row.SessionID,
		Model:            model,
		PromptTokens:     prompt,
		CompletionTokens: completion,
		TotalTokens:      total,
		Credit:           row.ProviderData.RawUsage.Credit,
		Timestamp:        t,
	}
}

// GetLatestSessionUsage 聚合定位与读取：一键获取最新活跃会话的 Token 指标。
func GetLatestSessionUsage(probe *variant.Probe) *SessionMetric {
	path, side, _ := FindLatestActiveSession(probe)
	if path == "" {
		return nil
	}
	m, err := TailReadUsage(path, side)
	if err != nil || m == nil {
		return nil
	}
	return m
}
