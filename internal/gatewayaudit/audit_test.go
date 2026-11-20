package gatewayaudit

import (
	"sync"
	"testing"
	"time"
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
		ModelBusyRetries:  2,
		RetryDelays:       []string{"1.52s", "2.48s"},
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
	if sum.ModelBusyAbsorbedCount != 2 {
		t.Errorf("ModelBusyAbsorbedCount = %d, want 2", sum.ModelBusyAbsorbedCount)
	}
	if sum.ModelCounts["deepseek-v4.1"] != 2 || sum.ModelCounts["gpt-5.6-sol"] != 1 {
		t.Errorf("ModelCounts 不匹配: %v", sum.ModelCounts)
	}
	if len(sum.RecentRecords) != 2 {
		t.Errorf("限制 2 条但返回了 %d 条", len(sum.RecentRecords))
	}
	if sum.RecentRecords[0].ModelBusyRetries != 2 || len(sum.RecentRecords[0].RetryDelays) != 2 {
		t.Errorf("最新记录重试信息未正确保留: %+v", sum.RecentRecords[0])
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

func TestAuditorPersistenceAndDailyStats(t *testing.T) {
	tempDir := t.TempDir()

	// 1. 模拟第一次运行 Session 1
	auditor1 := NewAuditorWithStorage(10, tempDir)

	auditor1.RecordRequest(Record{
		ID:               "req-1",
		ModelRequested:   "step-5-preview",
		StatusCode:       200,
		PromptTokens:     1000,
		CompletionTokens: 200,
		TTFTMs:           350,
		DurationMs:       1200,
	})
	auditor1.RecordRequest(Record{
		ID:               "req-2",
		ModelRequested:   "deepseek-v4.1-flash",
		StatusCode:       200,
		PromptTokens:     2000,
		CompletionTokens: 500,
		TTFTMs:           180,
		DurationMs:       800,
	})

	sum1 := auditor1.GetSummary(10)
	if sum1.TotalRequests != 2 {
		t.Fatalf("Session 1 期望 2 次请求，得到 %d", sum1.TotalRequests)
	}
	if sum1.TotalTokens != 3700 {
		t.Fatalf("Session 1 期望 3700 tokens，得到 %d", sum1.TotalTokens)
	}
	if len(sum1.DailyStats) != 7 {
		t.Fatalf("期望返回近 7 天日度序列，得到 %d", len(sum1.DailyStats))
	}
	todayStat := sum1.DailyStats[len(sum1.DailyStats)-1]
	if todayStat.TotalRequests != 2 || todayStat.TotalTokens != 3700 {
		t.Fatalf("今日日度统计不匹配: %+v", todayStat)
	}
	if todayStat.ModelCounts["step-5-preview"] != 1 || todayStat.ModelCounts["deepseek-v4.1-flash"] != 1 {
		t.Fatalf("今日各模型调用计数不匹配: %+v", todayStat.ModelCounts)
	}

	// 2. 模拟用户退出 wbmux，第二天重新打开（Session 2），指向相同 storageDir
	auditor2 := NewAuditorWithStorage(10, tempDir)
	sum2 := auditor2.GetSummary(10)

	if sum2.TotalRequests != 2 {
		t.Fatalf("Session 2 启动恢复后期望 2 次请求，得到 %d", sum2.TotalRequests)
	}
	if sum2.TotalTokens != 3700 {
		t.Fatalf("Session 2 恢复后 Token 期望 3700，得到 %d", sum2.TotalTokens)
	}
	if len(sum2.RecentRecords) != 2 {
		t.Fatalf("Session 2 恢复后明细流水条数期望 2，得到 %d", len(sum2.RecentRecords))
	}

	// 3. 在 Session 2 中继续产生新请求
	auditor2.RecordRequest(Record{
		ID:               "req-3",
		ModelRequested:   "step-5-preview",
		StatusCode:       200,
		PromptTokens:     500,
		CompletionTokens: 100,
		TTFTMs:           250,
		DurationMs:       600,
	})

	sum3 := auditor2.GetSummary(10)
	if sum3.TotalRequests != 3 || sum3.TotalTokens != 4300 {
		t.Fatalf("Session 2 追加新请求后期望 3 请求/4300 tokens，得到 %d 请求/%d tokens", sum3.TotalRequests, sum3.TotalTokens)
	}

	// 4. 测试 Clear 保留历史日度汇总
	auditor2.Clear()
	sumCleared := auditor2.GetSummary(10)
	if len(sumCleared.RecentRecords) != 0 {
		t.Fatalf("Clear 后活跃流水应为 0，得到 %d", len(sumCleared.RecentRecords))
	}
	if len(sumCleared.DailyStats) != 7 {
		t.Fatalf("Clear 后近 7 天统计仍应保留，得到 %d", len(sumCleared.DailyStats))
	}
	if sumCleared.DailyStats[len(sumCleared.DailyStats)-1].TotalTokens != 4300 {
		t.Fatalf("Clear 仅清空流水，今日日度汇总仍应为 4300，得到 %d", sumCleared.DailyStats[len(sumCleared.DailyStats)-1].TotalTokens)
	}

	// 5. 测试 ClearAll 彻底清空
	auditor2.ClearAll()
	sumAllCleared := auditor2.GetSummary(10)
	if sumAllCleared.TotalRequests != 0 {
		t.Fatalf("ClearAll 后总请求数应为 0，得到 %d", sumAllCleared.TotalRequests)
	}
	if sumAllCleared.DailyStats[len(sumAllCleared.DailyStats)-1].TotalRequests != 0 {
		t.Fatalf("ClearAll 后今日日度统计应为 0，得到 %d", sumAllCleared.DailyStats[len(sumAllCleared.DailyStats)-1].TotalRequests)
	}
}

// TestObserveLocalSessionDedup 钉住直连落账本的去重与口径统一：
// 先一条 Kimi、再一条 DeepSeek，Recent 里两条都在且按时间排，
// Today 等于两条 TotalTokens 之和；重复观察同一条不产生重复写入。
func TestObserveLocalSessionDedup(t *testing.T) {
	a := NewAuditor(100)
	now := time.Now()
	kimi := LocalSessionUsage{
		SessionID: "sess-1", Model: "Kimi-K3.1-Pro", Side: "cn",
		Timestamp:    now.Add(-10 * time.Second),
		PromptTokens: 48000, CompletionTokens: 1000, TotalTokens: 49000,
	}
	ds := LocalSessionUsage{
		SessionID: "sess-1", Model: "DeepSeek-V4.1-Flash", Side: "cn",
		Timestamp:    now,
		PromptTokens: 152000, CompletionTokens: 1000, TotalTokens: 153000,
	}
	if !a.ObserveLocalSession(kimi) {
		t.Fatal("第一次观察 Kimi 应写入")
	}
	if !a.ObserveLocalSession(ds) {
		t.Fatal("第一次观察 DeepSeek 应写入")
	}
	// 重复观察同一条（3 秒巡检 / 5 秒轮询都会反复看到）不产生重复写入
	if a.ObserveLocalSession(kimi) {
		t.Error("重复观察 Kimi 不应再写")
	}
	if a.ObserveLocalSession(ds) {
		t.Error("重复观察 DeepSeek 不应再写")
	}

	sum := a.GetSummary(10)
	if sum.TotalRequests != 2 {
		t.Fatalf("账本应只有 2 条，得到 %d", sum.TotalRequests)
	}
	// Recent 按时间倒序：最新是 DeepSeek，其次 Kimi
	if len(sum.RecentRecords) != 2 ||
		sum.RecentRecords[0].ModelRequested != "DeepSeek-V4.1-Flash" ||
		sum.RecentRecords[1].ModelRequested != "Kimi-K3.1-Pro" {
		t.Fatalf("Recent 应按时间排 [DeepSeek, Kimi]，得到 %+v", sum.RecentRecords)
	}
	rec := sum.RecentRecords[0]
	if rec.Source != "local" || rec.Side != "cn" || rec.SessionID != "sess-1" ||
		rec.StatusCode != 200 || rec.IsStream {
		t.Errorf("直连记录字段不对：%+v", rec)
	}
	// Today 真累加：49000 + 153000
	today := sum.DailyStats[len(sum.DailyStats)-1]
	if today.TotalRequests != 2 || today.TotalTokens != 202000 {
		t.Errorf("今日应为 2 请求/202000 tokens，得到 %+v", today)
	}

	// 同会话、同时间、不同 total → 是另一条请求，照写
	other := kimi
	other.TotalTokens = 49001
	other.CompletionTokens = 1001
	if !a.ObserveLocalSession(other) {
		t.Error("total 不同应是新记录")
	}
	// 不同会话 → 新记录
	other2 := kimi
	other2.SessionID = "sess-2"
	if !a.ObserveLocalSession(other2) {
		t.Error("不同会话应是新记录")
	}
	if got := a.GetSummary(10).TotalRequests; got != 4 {
		t.Errorf("追加两条不同记录后总数应为 4，得到 %d", got)
	}

	// 非法输入不写盘
	if a.ObserveLocalSession(LocalSessionUsage{Model: "x", Timestamp: now}) {
		t.Error("SessionID 为空时不应写入")
	}
	if a.ObserveLocalSession(LocalSessionUsage{SessionID: "s", Model: "x"}) {
		t.Error("Timestamp 为空时不应写入")
	}
	if a.ObserveLocalSession(LocalSessionUsage{SessionID: "s", Model: "x", Timestamp: now}) {
		t.Error("token 全为 0 时不应写入")
	}
}

// TestObserveLocalSessionRestartNoDup 钉住去重不靠内存：
// 全新 Auditor 读同一目录（内存 seen 集是空的），重复观察仍不重复写。
func TestObserveLocalSessionRestartNoDup(t *testing.T) {
	dir := t.TempDir()
	u := LocalSessionUsage{
		SessionID: "sess-1", Model: "Kimi-K3.1-Pro", Side: "cn",
		Timestamp:    time.Now(),
		PromptTokens: 48000, CompletionTokens: 1000, TotalTokens: 49000,
	}

	a1 := NewAuditorWithStorage(100, dir)
	if !a1.ObserveLocalSession(u) {
		t.Fatal("第一次观察应写入")
	}

	// 模拟重启：全新 Auditor 从磁盘恢复，内存里没有任何 seen 集
	a2 := NewAuditorWithStorage(100, dir)
	if got := a2.GetSummary(10).TotalRequests; got != 1 {
		t.Fatalf("重启恢复后期望 1 条，得到 %d", got)
	}
	if a2.ObserveLocalSession(u) {
		t.Error("重启后重复观察不应再写")
	}
	if got := a2.GetSummary(10).TotalRequests; got != 1 {
		t.Errorf("重启后重复观察后总数仍应为 1，得到 %d", got)
	}
	// 磁盘上的记录字段完整
	rec := a2.GetSummary(10).RecentRecords[0]
	if rec.Source != "local" || rec.Side != "cn" || rec.SessionID != "sess-1" {
		t.Errorf("重启恢复的记录字段不对：%+v", rec)
	}
}
