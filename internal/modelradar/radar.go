package modelradar

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PingOptions 包含探针调用的拟真客户端参数（账号 UID、通用桌面端 Header 等）。
type PingOptions struct {
	UID          string
	ExtraHeaders map[string]string
}

var requiredProbeIdentityHeaders = []string{
	"X-Conversation-ID",
	"X-Parent-Conversation-ID",
	"X-Session-ID",
	"X-Conversation-Request-ID",
	"X-Conversation-Message-ID",
	"X-Request-ID",
	"X-Root-Request-ID",
	"X-Trace-ID",
}

func newProbeUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Status 定义模型的健康与连通性等级。
type Status string

const (
	StatusFast     Status = "fast"     // TTFT < 800ms，极速畅快
	StatusNormal   Status = "normal"   // 800ms <= TTFT <= 2500ms，响应正常
	StatusSlow     Status = "slow"     // TTFT > 2500ms，延迟偏高
	StatusDown     Status = "down"     // 5xx / 429 / 超时 / 异常
	StatusUntested Status = "untested" // 尚未执行过探测
)

// ModelMeta 是探针需要的基本模型元数据。
type ModelMeta struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"displayName,omitempty"`
	IsFree      bool    `json:"isFree"`
	Rate        float64 `json:"rate"`
}

// ProbeRecord 记录单一模型的雷达测速与健康状态快照。
type ProbeRecord struct {
	Model              string    `json:"model"`
	DisplayName        string    `json:"displayName,omitempty"`
	IsFree             bool      `json:"isFree"`
	Rate               float64   `json:"rate"`
	Status             Status    `json:"status"`
	TTFTMs             int64     `json:"ttftMs"`
	StatusCode         int       `json:"statusCode,omitempty"`
	ErrorMsg           string    `json:"errorMsg,omitempty"`
	IsRateLimited      bool      `json:"isRateLimited,omitempty"`
	RateLimitResetAt   int64     `json:"rateLimitResetAt,omitempty"`
	ResetAtHuman       string    `json:"resetAtHuman,omitempty"`
	ResetRemainingDesc string    `json:"resetRemainingDesc,omitempty"`
	CheckedAt          time.Time `json:"checkedAt"`
}

// Radar 负责管理所有受监控模型的测速快照、并发探针调度与最优选路。
type Radar struct {
	mu      sync.RWMutex
	records map[string]ProbeRecord
	client  *http.Client
}

var (
	defaultRadar *Radar
	radarOnce    sync.Once
)

// Default 返回全局单例 Radar。
func Default() *Radar {
	radarOnce.Do(func() {
		defaultRadar = NewRadar(&http.Client{
			Timeout: 10 * time.Second,
		})
	})
	return defaultRadar
}

// NewRadar 创建一个指定 HTTP Client 的独立 Radar 实例（便于单测与定制）。
func NewRadar(client *http.Client) *Radar {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Radar{
		records: make(map[string]ProbeRecord),
		client:  client,
	}
}

// Ping 对单一目标模型发起轻量流式探针，毫秒级度量首字延迟 (TTFT)。
//
// 探针机制：
// 1. 发送 1-Token 测试流式补全（max_tokens: 1, stream: true）；
// 2. 收到首个有效 SSE chunk（data: ...）后立即截断并关闭连接，不浪费多余算力与 Token；
// 3. 超时或遇到非 200 状态码时，准确记录失败原因。
func (r *Radar) Ping(ctx context.Context, endpoint, token string, meta ModelMeta, opts ...PingOptions) ProbeRecord {
	rec := ProbeRecord{
		Model:       meta.ID,
		DisplayName: meta.DisplayName,
		IsFree:      meta.IsFree,
		Rate:        meta.Rate,
		Status:      StatusDown,
		CheckedAt:   time.Now(),
	}

	var opt PingOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	payload := map[string]any{
		"model": meta.ID,
		"messages": []map[string]string{
			// 官方上游严格风控：首条必须为 system 角色，不能仅有 user 否则直接返回 code 11128 (request illegal)
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "hi"},
		},
		"max_tokens": 1,
		"stream":     true,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		rec.ErrorMsg = "序列化探针载荷失败: " + err.Error()
		r.SetRecord(rec)
		return rec
	}

	reqURL := endpoint
	if !strings.HasSuffix(reqURL, "/chat/completions") {
		reqURL = strings.TrimRight(reqURL, "/") + "/chat/completions"
	}

	// 探针最长等待 7 秒，防挂死
	probeCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		rec.ErrorMsg = "构建探针请求失败: " + err.Error()
		r.SetRecord(rec)
		return rec
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if opt.UID != "" {
		req.Header.Set("X-User-Id", opt.UID)
	}
	// 注入官方 Electron 客户端平台指纹与 UA
	for k, v := range opt.ExtraHeaders {
		req.Header.Set(k, v)
	}
	// 补全会话链路 UUID 追踪头
	probeUUID := newProbeUUID()
	for _, h := range requiredProbeIdentityHeaders {
		if req.Header.Get(h) == "" {
			req.Header.Set(h, probeUUID)
		}
	}

	startTime := time.Now()
	resp, err := r.client.Do(req)
	if err != nil {
		rec.ErrorMsg = "请求超时或网络不可达: " + err.Error()
		r.SetRecord(rec)
		return rec
	}
	defer resp.Body.Close()

	rec.StatusCode = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		rawMsg := strings.TrimSpace(string(snippet))
		if resp.StatusCode == 429 {
			resetAtMs, humanStr, remaining, isRate := ParseRateLimitReset(rawMsg)
			if isRate {
				rec.IsRateLimited = true
				rec.RateLimitResetAt = resetAtMs
				rec.ResetAtHuman = humanStr
				rec.ResetRemainingDesc = remaining
				if humanStr != "" {
					rec.ErrorMsg = fmt.Sprintf("官方限免频控：将于 %s 重置（%s）", humanStr, remaining)
				} else {
					rec.ErrorMsg = "官方限免使用频率已达上限 (429)"
				}
				r.SetRecord(rec)
				return rec
			}
			if strings.Contains(rawMsg, "14003") || strings.Contains(strings.ToLower(rawMsg), "model busy") {
				rec.ErrorMsg = "429 (官方模型算力繁忙 Model Busy)"
				r.SetRecord(rec)
				return rec
			}
		}
		if strings.Contains(rawMsg, "11128") || strings.Contains(rawMsg, "渠道校验") {
			rec.ErrorMsg = "400 (官方渠道拦截: 未通过校验)"
		} else if rawMsg == "" {
			rec.ErrorMsg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else {
			rec.ErrorMsg = cleanErrorSnippet(rawMsg)
		}
		r.SetRecord(rec)
		return rec
	}

	// 监听首个有效 SSE chunk
	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			dataContent := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if dataContent != "" && dataContent != "[DONE]" {
				// 成功捕获首字响应！
				ttft := time.Since(startTime).Milliseconds()
				rec.TTFTMs = ttft
				if ttft < 800 {
					rec.Status = StatusFast
				} else if ttft <= 2500 {
					rec.Status = StatusNormal
				} else {
					rec.Status = StatusSlow
				}
				rec.ErrorMsg = ""
				r.SetRecord(rec)
				return rec
			}
		}
		if readErr != nil {
			break
		}
	}

	// 若未收到有效流式片段直接结束，记录用时作为备用
	rec.TTFTMs = time.Since(startTime).Milliseconds()
	if rec.TTFTMs > 0 {
		rec.Status = StatusNormal
	} else {
		rec.ErrorMsg = "未接收到有效响应内容"
	}
	r.SetRecord(rec)
	return rec
}

// PingBatch 并发控制批量测速。
//
// maxConcurrency 控制最大并发协程数（默认建议 2~3），每个请求之间有微小抖动，保护上游。
func (r *Radar) PingBatch(ctx context.Context, endpoint, token string, models []ModelMeta, maxConcurrency int, opts ...PingOptions) []ProbeRecord {
	if maxConcurrency <= 0 {
		maxConcurrency = 3
	}
	if maxConcurrency > len(models) {
		maxConcurrency = len(models)
	}

	results := make([]ProbeRecord, len(models))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for i, m := range models {
		wg.Add(1)
		go func(idx int, meta ModelMeta) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[idx] = ProbeRecord{
					Model:       meta.ID,
					DisplayName: meta.DisplayName,
					IsFree:      meta.IsFree,
					Rate:        meta.Rate,
					Status:      StatusDown,
					ErrorMsg:    "上下文已取消",
					CheckedAt:   time.Now(),
				}
				return
			}
			defer func() { <-sem }()

			// 微小交错延迟（30ms），杜绝瞬发击穿
			time.Sleep(time.Duration(idx*35) * time.Millisecond)

			results[idx] = r.Ping(ctx, endpoint, token, meta, opts...)
		}(i, m)
	}

	wg.Wait()
	return results
}

// SetRecord 线程安全写入或更新一条测速记录。
func (r *Radar) SetRecord(rec ProbeRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[strings.ToLower(rec.Model)] = rec
}

// GetRecord 线程安全查询一条测速记录。
func (r *Radar) GetRecord(model string) (ProbeRecord, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[strings.ToLower(model)]
	return rec, ok
}

// GetAllRecords 返回当前所有已知的测速记录（排序后）。
func (r *Radar) GetAllRecords() []ProbeRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]ProbeRecord, 0, len(r.records))
	for _, rec := range r.records {
		out = append(out, rec)
	}

	sort.Slice(out, func(i, j int) bool {
		// 1. 限免模型优先
		if out[i].IsFree != out[j].IsFree {
			return out[i].IsFree
		}
		// 2. 健康状态优先 (fast > normal > slow > down > untested)
		rank := map[Status]int{
			StatusFast:     0,
			StatusNormal:   1,
			StatusSlow:     2,
			StatusUntested: 3,
			StatusDown:     4,
		}
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		// 3. 延迟低的优先
		if out[i].TTFTMs > 0 && out[j].TTFTMs > 0 && out[i].TTFTMs != out[j].TTFTMs {
			return out[i].TTFTMs < out[j].TTFTMs
		}
		return out[i].Model < out[j].Model
	})

	return out
}

// FastestFreeModel 智能选优算法：返回当前延迟最低且健康的限免模型 ID。
//
// 选优规则：
// 1. 过滤 IsFree == true 且 Status 为 Fast 或 Normal 的模型；
// 2. 按 TTFTMs 升序选择最小者；
// 3. 若当前没有测速数据，返回 fallback 备选模型（如传入）或空。
func (r *Radar) FastestFreeModel(fallbacks ...string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var candidates []ProbeRecord
	for _, rec := range r.records {
		if rec.IsFree && (rec.Status == StatusFast || rec.Status == StatusNormal) && rec.TTFTMs > 0 {
			candidates = append(candidates, rec)
		}
	}

	if len(candidates) > 0 {
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].TTFTMs < candidates[j].TTFTMs
		})
		return candidates[0].Model, true
	}

	// 如果没有快速/正常模型，检查是否有 slow 但仍然可用的限免模型
	for _, rec := range r.records {
		if rec.IsFree && rec.Status == StatusSlow && rec.TTFTMs > 0 {
			candidates = append(candidates, rec)
		}
	}
	if len(candidates) > 0 {
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].TTFTMs < candidates[j].TTFTMs
		})
		return candidates[0].Model, true
	}

	// 回退兜底
	for _, fb := range fallbacks {
		if strings.TrimSpace(fb) != "" {
			return strings.TrimSpace(fb), false
		}
	}
	return "", false
}

// Clear 清空所有测速记录。
func (r *Radar) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = make(map[string]ProbeRecord)
}

var (
	rateLimitResetAtRe = regexp.MustCompile(`(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2})`)
	rateLimitResetTZRe = regexp.MustCompile(`UTC([+-]\d{1,2})`)
)

// ParseRateLimitReset 从上游 429 报错中解析频控重置时间（支持 JSON 与纯文本）。
func ParseRateLimitReset(raw string) (resetAtMs int64, humanStr, remainingDesc string, isRateLimit bool) {
	lower := strings.ToLower(raw)
	// 判断是否为频控类（code 6004 或包含 frequency limit / 频率限制）
	if !strings.Contains(lower, "6004") &&
		!strings.Contains(lower, "frequency limit") &&
		!strings.Contains(raw, "频率限制") &&
		!strings.Contains(lower, "reset at") &&
		!strings.Contains(raw, "重置") {
		return 0, "", "", false
	}

	m := rateLimitResetAtRe.FindStringSubmatch(raw)
	if m == nil {
		return 0, "", "", true
	}
	dateStr := strings.Replace(m[1], "T", " ", 1)

	off := 8 // 官方默认 UTC+8
	if tz := rateLimitResetTZRe.FindStringSubmatch(raw); tz != nil {
		if n, err := strconv.Atoi(tz[1]); err == nil && n >= -12 && n <= 14 {
			off = n
		}
	}
	loc := time.FixedZone("", off*3600)
	t, err := time.ParseInLocation("2006-01-02 15:04:05", dateStr, loc)
	if err != nil {
		return 0, dateStr, "", true
	}

	resetAtMs = t.UnixMilli()
	humanStr = dateStr
	now := time.Now()
	if t.After(now) {
		diff := t.Sub(now)
		hours := int(diff.Hours())
		mins := int(diff.Minutes()) % 60
		if hours > 0 {
			remainingDesc = fmt.Sprintf("约 %d 小时 %d 分钟后恢复", hours, mins)
		} else if mins > 0 {
			remainingDesc = fmt.Sprintf("约 %d 分钟后恢复", mins)
		} else {
			remainingDesc = "即将恢复"
		}
	} else {
		remainingDesc = "重置时间已到，点击可重新测速"
	}
	return resetAtMs, humanStr, remainingDesc, true
}

// cleanErrorSnippet 从可能为 JSON 的错误串中提取核心消息，避免在 UI 倾倒整段 JSON。
func cleanErrorSnippet(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err == nil {
			if msg, ok := obj["msg"].(string); ok && strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
			if msg, ok := obj["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
		}
	}
	return raw
}
