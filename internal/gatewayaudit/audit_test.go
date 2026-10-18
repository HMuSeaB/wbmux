package gatewayaudit

import (
	"sync"
	"testing"
)

func TestAuditorRingBuffer(t *testing.T) {
	auditor := NewAuditor(3)

	auditor.RecordRequest(Record{ID: "req-1", ModelRequested: "gpt-4o"})
	auditor.RecordRequest(Record{ID: "req-2", ModelRequested: "gpt-4o"})
	auditor.RecordRequest(Record{ID: "req-3", ModelRequested: "claude-3-5"})

	sum := auditor.GetSummary(10)
	if sum.TotalRequests != 3 {
		t.Fatalf("期望总数 3，得到 %d", sum.TotalRequests)
	}
	if sum.RecentRecords[0].ID != "req-3" {
		t.Fatalf("最近一条记录期望 req-3，得到 %s", sum.RecentRecords[0].ID)
	}

	// 触发淘汰
	auditor.RecordRequest(Record{ID: "req-4", ModelRequested: "deepseek-v4.1"})
	sum = auditor.GetSummary(10)
	if sum.TotalRequests != 3 {
		t.Fatalf("超出容量后总数应保持 3，得到 %d", sum.TotalRequests)
	}
	// req-1 应该已被淘汰，最新是 req-4, req-3, req-2
	if sum.RecentRecords[0].ID != "req-4" || sum.RecentRecords[2].ID != "req-2" {
		t.Fatalf("淘汰顺序不正确: %v", sum.RecentRecords)
	}

	// 清空
	auditor.Clear()
	sum = auditor.GetSummary(10)
	if sum.TotalRequests != 0 || len(sum.RecentRecords) != 0 {
		t.Fatalf("清空后总数应为 0，得到 %d", sum.TotalRequests)
	}
}

func TestAuditorSummaryCalculation(t *testing.T) {
	auditor := NewAuditor(100)

	auditor.RecordRequest(Record{
		ID:                "req-1",
		ModelRequested:    "deepseek-v4.1",
		StatusCode:        200,
		PromptTokens:      100,
		CompletionTokens:  50,
		TTFTMs:            200,
		DurationMs:        800,
		FailoverTriggered: false,
	})

	auditor.RecordRequest(Record{
		ID:                "req-2",
		ModelRequested:    "deepseek-v4.1",
		StatusCode:        429,
		PromptTokens:      50,
		CompletionTokens:  0,
		TTFTMs:            0,
		DurationMs:        100,
		FailoverTriggered: true,
	})

	auditor.RecordRequest(Record{
		ID:                "req-3",
		ModelRequested:    "gpt-5.6-sol",
		StatusCode:        200,
		PromptTokens:      200,
		CompletionTokens:  100,
		TTFTMs:            400,
		DurationMs:        1200,
		FailoverTriggered: false,
	})

	sum := auditor.GetSummary(2)
	if sum.TotalRequests != 3 {
		t.Errorf("TotalRequests = %d, want 3", sum.TotalRequests)
	}
	if sum.SuccessCount != 2 || sum.FailCount != 1 {
		t.Errorf("SuccessCount=%d, FailCount=%d, want 2, 1", sum.SuccessCount, sum.FailCount)
	}
	if sum.SuccessRate < 66.6 || sum.SuccessRate > 66.7 {
		t.Errorf("SuccessRate = %f, want ~66.66", sum.SuccessRate)
	}
	if sum.TotalPromptTokens != 350 || sum.TotalCompTokens != 150 || sum.TotalTokens != 500 {
		t.Errorf("Tokens 统计不匹配: P=%d, C=%d, Total=%d", sum.TotalPromptTokens, sum.TotalCompTokens, sum.TotalTokens)
	}
	// TTFT 平均: (200 + 400) / 2 = 300
	if sum.AvgTTFTMs != 300 {
		t.Errorf("AvgTTFTMs = %d, want 300", sum.AvgTTFTMs)
	}
	// 总耗时平均: (800 + 100 + 1200) / 3 = 700
	if sum.AvgDurationMs != 700 {
		t.Errorf("AvgDurationMs = %d, want 700", sum.AvgDurationMs)
	}
	if sum.FailoverCount != 1 {
		t.Errorf("FailoverCount = %d, want 1", sum.FailoverCount)
	}
	if sum.ModelCounts["deepseek-v4.1"] != 2 || sum.ModelCounts["gpt-5.6-sol"] != 1 {
		t.Errorf("ModelCounts 不匹配: %v", sum.ModelCounts)
	}
	if len(sum.RecentRecords) != 2 {
		t.Errorf("限制 2 条但返回了 %d 条", len(sum.RecentRecords))
	}
}

func TestAuditorConcurrent(t *testing.T) {
	auditor := NewAuditor(200)
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			auditor.RecordRequest(Record{
				ID:             "concurrent-req",
				ModelRequested: "test-model",
				StatusCode:     200,
				PromptTokens:   10,
			})
			_ = auditor.GetSummary(10)
		}(i)
	}

	wg.Wait()
	sum := auditor.GetSummary(100)
	if sum.TotalRequests != 50 {
		t.Errorf("并发写入后总记录数期望 50，得到 %d", sum.TotalRequests)
	}
}
