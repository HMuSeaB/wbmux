package gatewayaudit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
)

// Record 记录一次本地 OpenAI 兼容网关调用的结构化审计明细。
type Record struct {
	ID                string    `json:"id"`                          // 请求唯一标识 (UUID / 请求序列)
	Timestamp         time.Time `json:"timestamp"`                   // 请求开始时间
	ModelRequested    string    `json:"modelRequested"`              // 客户端实际请求的模型名
	ModelMapped       string    `json:"modelMapped"`                 // 上游真实映射的模型名
	AccountID         string    `json:"accountId,omitempty"`         // 调用所使用的账号 UID
	AccountMask       string    `json:"accountMask,omitempty"`       // 脱敏账号标示（如 Nickname 或掩码 UID）
	IsStream          bool      `json:"isStream"`                    // 是否为流式输出
	StatusCode        int       `json:"statusCode"`                  // HTTP 状态码
	PromptTokens      int       `json:"promptTokens"`                // 提示词 Token 消耗
	CompletionTokens  int       `json:"completionTokens"`            // 输出补全 Token 消耗
	TotalTokens       int       `json:"totalTokens"`                 // 总计消耗 Tokens
	TTFTMs            int64     `json:"ttftMs"`                      // 首字延迟 (Time to First Token) 毫秒（非流式与总耗时相同）
	DurationMs        int64     `json:"durationMs"`                  // 整个请求总耗时毫秒
	FailoverTriggered bool      `json:"failoverTriggered,omitempty"` // 是否在该请求中触发了账号故障转移
	ErrorMsg          string    `json:"errorMsg,omitempty"`          // 异常错误描述（如有）
	ContextScale      string    `json:"contextScale,omitempty"`      // 会话体积分级: "normal" | "heavy" | "overload"
	BodyBytes         int64     `json:"bodyBytes,omitempty"`         // 请求正文字节数
	ModelBusyRetries  int       `json:"modelBusyRetries,omitempty"`  // 算力繁忙自动防抖重试次数 (0 表示无重试)
	RetryDelays       []string  `json:"retryDelays,omitempty"`       // 每次退避的具体耗时轨迹 (如 ["1.52s", "2.48s"])
}

// DailyStats 记录单日的网关调用聚合指标。
type DailyStats struct {
	Date          string         `json:"date"`          // 形如 "2026-10-21"
	TotalRequests int            `json:"totalRequests"` // 当天总请求数
	SuccessCount  int            `json:"successCount"`  // 成功数
	FailCount     int            `json:"failCount"`     // 失败数
	PromptTokens  int            `json:"promptTokens"`  // 输入 Token
	CompTokens    int            `json:"compTokens"`    // 输出 Token
	TotalTokens   int            `json:"totalTokens"`   // 总 Token
	AvgTTFTMs     int64          `json:"avgTtftMs"`     // 平均首字延迟 ms
	AvgDurationMs int64          `json:"avgDurationMs"` // 平均总耗时 ms
	ModelCounts   map[string]int `json:"modelCounts"`   // 各模型调用次数
	ModelTokens   map[string]int `json:"modelTokens"`   // 各模型消耗的总 Token 数

	sumTTFT     int64 `json:"-"`
	sampleTTFT  int64 `json:"-"`
	sumDuration int64 `json:"-"`
}

// Summary 是网关调用的聚合分析指标概览。
type Summary struct {
	TotalRequests          int            `json:"totalRequests"`          // 累计总请求数
	SuccessCount           int            `json:"successCount"`           // 成功请求数 (200~299)
	FailCount              int            `json:"failCount"`              // 失败请求数
	SuccessRate            float64        `json:"successRate"`            // 成功率 (0.0 ~ 100.0)
	TotalPromptTokens      int            `json:"totalPromptTokens"`      // 累计输入 Tokens
	TotalCompTokens        int            `json:"totalCompTokens"`        // 累计输出 Tokens
	TotalTokens            int            `json:"totalTokens"`            // 累计总 Tokens
	AvgTTFTMs              int64          `json:"avgTtftMs"`              // 平均首字延迟 ms
	AvgDurationMs          int64          `json:"avgDurationMs"`          // 平均响应总耗时 ms
	FailoverCount          int            `json:"failoverCount"`          // 累计触发 Failover 换号次数
	ModelBusyAbsorbedCount int            `json:"modelBusyAbsorbedCount"` // 累计通过算力防抖成功化解的上游 GPU 拥堵次数
	ModelCounts            map[string]int `json:"modelCounts"`            // 各模型调用分布
	RecentRecords          []Record       `json:"recentRecords"`          // 最近的调用明细（按时间倒序）
	DailyStats             []DailyStats   `json:"dailyStats,omitempty"`   // 近 N 天日度用量趋势（按日期升序）
}

// DefaultMaxRecords 默认内存中保留的最大审计流水条数。
const DefaultMaxRecords = 1000

// Auditor 是网关审计追踪器，内部维护定长环形队列与线程安全的读写锁，并支持本地文件持久化。
type Auditor struct {
	mu         sync.RWMutex
	maxRecords int
	records    []Record
	storageDir string
	dailyMap   map[string]*DailyStats
}

var (
	defaultAuditor *Auditor
	once           sync.Once
)

// Default 获取全局默认网关审计器单例，并初始化本地持久化引擎。
func Default() *Auditor {
	once.Do(func() {
		dir, _ := config.Dir()
		defaultAuditor = NewAuditorWithStorage(DefaultMaxRecords, dir)
	})
	return defaultAuditor
}

// NewAuditor 创建一个指定容量的纯内存审计追踪器（主要供单元测试使用）。
func NewAuditor(maxRecords int) *Auditor {
	return NewAuditorWithStorage(maxRecords, "")
}

// NewAuditorWithStorage 创建一个支持持久化存储的网关审计追踪器。
func NewAuditorWithStorage(maxRecords int, storageDir string) *Auditor {
	if maxRecords <= 0 {
		maxRecords = DefaultMaxRecords
	}
	a := &Auditor{
		maxRecords: maxRecords,
		records:    make([]Record, 0, maxRecords),
		storageDir: storageDir,
		dailyMap:   make(map[string]*DailyStats),
	}

	if storageDir != "" {
		a.loadFromDisk()
	}
	return a
}

// RecordRequest 记录一次网关调用。同时更新实时环形队列与当日聚合统计，并落盘持久化。
func (a *Auditor) RecordRequest(r Record) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	if r.TotalTokens == 0 && (r.PromptTokens > 0 || r.CompletionTokens > 0) {
		r.TotalTokens = r.PromptTokens + r.CompletionTokens
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// 1. 更新内存环形切片
	if len(a.records) >= a.maxRecords {
		copy(a.records, a.records[1:])
		a.records[len(a.records)-1] = r
	} else {
		a.records = append(a.records, r)
	}

	// 2. 更新当日聚合指标
	dateStr := r.Timestamp.Format("2006-01-02")
	st, ok := a.dailyMap[dateStr]
	if !ok {
		st = &DailyStats{
			Date:        dateStr,
			ModelCounts: make(map[string]int),
			ModelTokens: make(map[string]int),
		}
		a.dailyMap[dateStr] = st
	}

	st.TotalRequests++
	if r.StatusCode >= 200 && r.StatusCode < 300 {
		st.SuccessCount++
	} else {
		st.FailCount++
	}
	st.PromptTokens += r.PromptTokens
	st.CompTokens += r.CompletionTokens
	st.TotalTokens += r.TotalTokens

	if r.TTFTMs > 0 {
		st.sumTTFT += r.TTFTMs
		st.sampleTTFT++
		st.AvgTTFTMs = st.sumTTFT / st.sampleTTFT
	}
	if r.DurationMs > 0 {
		st.sumDuration += r.DurationMs
		st.AvgDurationMs = st.sumDuration / int64(st.TotalRequests)
	}

	m := r.ModelRequested
	if m == "" {
		m = r.ModelMapped
	}
	if m == "" {
		m = "unknown"
	}
	st.ModelCounts[m]++
	st.ModelTokens[m] += r.TotalTokens

	// 3. 落盘持久化（若配置了存储目录）
	if a.storageDir != "" {
		a.appendRecordToDisk(r)
		a.saveDailyToDisk()
	}
}

// GetSummary 计算并返回聚合分析摘要，以及最近 limit 条明细记录与近 7 天日度用量走势。
func (a *Auditor) GetSummary(limit int) Summary {
	a.mu.RLock()
	defer a.mu.RUnlock()

	s := Summary{
		ModelCounts:   make(map[string]int),
		RecentRecords: make([]Record, 0),
		DailyStats:    a.getRecentDailyStatsLocked(7),
	}

	s.TotalRequests = len(a.records)
	if s.TotalRequests == 0 {
		return s
	}

	var sumTTFT int64
	var sumDuration int64
	var ttftSampleCount int64

	for _, r := range a.records {
		if r.StatusCode >= 200 && r.StatusCode < 300 {
			s.SuccessCount++
		} else {
			s.FailCount++
		}

		s.TotalPromptTokens += r.PromptTokens
		s.TotalCompTokens += r.CompletionTokens
		s.TotalTokens += r.TotalTokens

		if r.TTFTMs > 0 {
			sumTTFT += r.TTFTMs
			ttftSampleCount++
		}
		if r.DurationMs > 0 {
			sumDuration += r.DurationMs
		}

		if r.FailoverTriggered {
			s.FailoverCount++
		}
		if r.ModelBusyRetries > 0 {
			s.ModelBusyAbsorbedCount += r.ModelBusyRetries
		}

		m := r.ModelRequested
		if m == "" {
			m = r.ModelMapped
		}
		if m == "" {
			m = "unknown"
		}
		s.ModelCounts[m]++
	}

	if s.TotalRequests > 0 {
		s.SuccessRate = float64(s.SuccessCount) / float64(s.TotalRequests) * 100.0
		s.AvgDurationMs = sumDuration / int64(s.TotalRequests)
	}
	if ttftSampleCount > 0 {
		s.AvgTTFTMs = sumTTFT / ttftSampleCount
	} else if s.TotalRequests > 0 {
		s.AvgTTFTMs = s.AvgDurationMs
	}

	// 提取最近 limit 条记录，时间倒序
	if limit <= 0 || limit > len(a.records) {
		limit = len(a.records)
	}
	s.RecentRecords = make([]Record, 0, limit)
	for i := len(a.records) - 1; i >= len(a.records)-limit; i-- {
		s.RecentRecords = append(s.RecentRecords, a.records[i])
	}

	return s
}

// getRecentDailyStatsLocked 获取最近 N 天的日度统计序列（包含今天，按日期升序）。无锁保护，由调用方加锁。
func (a *Auditor) getRecentDailyStatsLocked(days int) []DailyStats {
	if days <= 0 {
		days = 7
	}
	out := make([]DailyStats, 0, days)
	now := time.Now()
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		if st, ok := a.dailyMap[d]; ok {
			cloned := *st
			cloned.ModelCounts = make(map[string]int, len(st.ModelCounts))
			for k, v := range st.ModelCounts {
				cloned.ModelCounts[k] = v
			}
			cloned.ModelTokens = make(map[string]int, len(st.ModelTokens))
			for k, v := range st.ModelTokens {
				cloned.ModelTokens[k] = v
			}
			out = append(out, cloned)
		} else {
			out = append(out, DailyStats{
				Date:        d,
				ModelCounts: make(map[string]int),
				ModelTokens: make(map[string]int),
			})
		}
	}
	return out
}

// Clear 清空全部审计流水记录与本地流水日志文件，但保留历史日度统计。
func (a *Auditor) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = make([]Record, 0, a.maxRecords)
	if a.storageDir != "" {
		logPath := filepath.Join(a.storageDir, "gateway_audit.jsonl")
		_ = os.WriteFile(logPath, nil, 0600)
	}
}

// ClearAll 清空全部审计流水及所有历史日度汇总文件。
func (a *Auditor) ClearAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = make([]Record, 0, a.maxRecords)
	a.dailyMap = make(map[string]*DailyStats)
	if a.storageDir != "" {
		_ = os.Remove(filepath.Join(a.storageDir, "gateway_audit.jsonl"))
		_ = os.Remove(filepath.Join(a.storageDir, "gateway_daily.json"))
	}
}

// loadFromDisk 从本地存储目录恢复日度统计与近期审计流水。
func (a *Auditor) loadFromDisk() {
	if a.storageDir == "" {
		return
	}
	_ = os.MkdirAll(a.storageDir, 0700)

	// 1. 加载 gateway_daily.json
	dailyPath := filepath.Join(a.storageDir, "gateway_daily.json")
	if data, err := os.ReadFile(dailyPath); err == nil {
		var list []*DailyStats
		if err := json.Unmarshal(data, &list); err == nil {
			for _, st := range list {
				if st != nil && st.Date != "" {
					if st.ModelCounts == nil {
						st.ModelCounts = make(map[string]int)
					}
					if st.ModelTokens == nil {
						st.ModelTokens = make(map[string]int)
					}
					a.dailyMap[st.Date] = st
				}
			}
		}
	}

	// 2. 加载 gateway_audit.jsonl
	logPath := filepath.Join(a.storageDir, "gateway_audit.jsonl")
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var loadedRecords []Record
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err == nil {
			loadedRecords = append(loadedRecords, r)
		}
	}

	if len(loadedRecords) > a.maxRecords {
		a.records = loadedRecords[len(loadedRecords)-a.maxRecords:]
	} else {
		a.records = loadedRecords
	}
}

// appendRecordToDisk 将单条记录以追加方式写入本地 JSONL 文件。
func (a *Auditor) appendRecordToDisk(r Record) {
	if a.storageDir == "" {
		return
	}
	logPath := filepath.Join(a.storageDir, "gateway_audit.jsonl")
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	data = append(data, '\n')

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, _ = f.Write(data)
	_ = f.Close()
}

// saveDailyToDisk 将日度汇总数据按日期排序后写回 gateway_daily.json，最多保留最近 60 天。
func (a *Auditor) saveDailyToDisk() {
	if a.storageDir == "" {
		return
	}
	dailyPath := filepath.Join(a.storageDir, "gateway_daily.json")
	list := make([]*DailyStats, 0, len(a.dailyMap))
	for _, st := range a.dailyMap {
		list = append(list, st)
	}

	sort.Slice(list, func(i, j int) bool {
		return list[i].Date < list[j].Date
	})

	if len(list) > 60 {
		list = list[len(list)-60:]
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err == nil {
		_ = os.WriteFile(dailyPath, data, 0600)
	}
}
