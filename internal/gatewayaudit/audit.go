package gatewayaudit

import (
	"sync"
	"time"
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
}

// DefaultMaxRecords 默认内存中保留的最大审计流水条数。
const DefaultMaxRecords = 1000

// Auditor 是网关审计追踪器，内部维护定长环形队列与线程安全的读写锁。
type Auditor struct {
	mu         sync.RWMutex
	maxRecords int
	records    []Record
}

var (
	defaultAuditor *Auditor
	once           sync.Once
)

// Default 获取全局默认网关审计器单例。
func Default() *Auditor {
	once.Do(func() {
		defaultAuditor = NewAuditor(DefaultMaxRecords)
	})
	return defaultAuditor
}

// NewAuditor 创建一个指定容量的审计追踪器。
func NewAuditor(maxRecords int) *Auditor {
	if maxRecords <= 0 {
		maxRecords = DefaultMaxRecords
	}
	return &Auditor{
		maxRecords: maxRecords,
		records:    make([]Record, 0, maxRecords),
	}
}

// RecordRequest 记录一次网关调用。若超出容量上限，自动淘汰最旧的一条记录。
func (a *Auditor) RecordRequest(r Record) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	if r.TotalTokens == 0 && (r.PromptTokens > 0 || r.CompletionTokens > 0) {
		r.TotalTokens = r.PromptTokens + r.CompletionTokens
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.records) >= a.maxRecords {
		// 原地平移复用底层切片，避免头指针后移导致 cap 耗尽触发频繁内存分配
		copy(a.records, a.records[1:])
		a.records[len(a.records)-1] = r
	} else {
		a.records = append(a.records, r)
	}
}

// GetSummary 计算并返回聚合分析摘要，以及最近 limit 条明细记录（按时间倒序）。
func (a *Auditor) GetSummary(limit int) Summary {
	a.mu.RLock()
	defer a.mu.RUnlock()

	s := Summary{
		ModelCounts:   make(map[string]int),
		RecentRecords: make([]Record, 0),
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

// Clear 清空全部审计流水记录。
func (a *Auditor) Clear() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = make([]Record, 0, a.maxRecords)
}
