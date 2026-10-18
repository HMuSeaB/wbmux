package modelradar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRadarPingSuccess(t *testing.T) {
	// 模拟正常 SSE 流式上游
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		time.Sleep(30 * time.Millisecond) // 模拟轻微延迟
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
		flusher.Flush()
	}))
	defer ts.Close()

	r := NewRadar(ts.Client())
	meta := ModelMeta{
		ID:          "deepseek-chat",
		DisplayName: "DeepSeek V3",
		IsFree:      true,
		Rate:        0.0,
	}

	rec := r.Ping(context.Background(), ts.URL, "test-token", meta)

	if rec.Status != StatusFast {
		t.Errorf("期望 Status 为 fast，实际得到 %s", rec.Status)
	}
	if rec.TTFTMs <= 0 {
		t.Errorf("期望 TTFTMs > 0，实际得到 %d", rec.TTFTMs)
	}
	if rec.StatusCode != 200 {
		t.Errorf("期望 StatusCode == 200，实际得到 %d", rec.StatusCode)
	}
	if rec.ErrorMsg != "" {
		t.Errorf("期望无错误信息，实际得到 %s", rec.ErrorMsg)
	}

	// 验证已存入状态池
	stored, ok := r.GetRecord("deepseek-chat")
	if !ok || stored.Model != "deepseek-chat" {
		t.Errorf("期望在状态池中查到 deepseek-chat")
	}
}

func TestRadarPingError429(t *testing.T) {
	// 模拟 429 限流
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit reached"}}`))
	}))
	defer ts.Close()

	r := NewRadar(ts.Client())
	meta := ModelMeta{
		ID:     "gpt-5",
		IsFree: false,
	}

	rec := r.Ping(context.Background(), ts.URL, "token", meta)
	if rec.Status != StatusDown {
		t.Errorf("期望 Status 为 down，实际为 %s", rec.Status)
	}
	if rec.StatusCode != 429 {
		t.Errorf("期望 StatusCode 429，实际得到 %d", rec.StatusCode)
	}
	if !strings.Contains(rec.ErrorMsg, "Rate limit reached") {
		t.Errorf("期望 ErrorMsg 包含 Rate limit reached，实际得到 %s", rec.ErrorMsg)
	}
}

func TestRadarFastestFreeModel(t *testing.T) {
	r := NewRadar(nil)

	// 1. 无数据时返回 fallback
	best, ok := r.FastestFreeModel("default-fallback")
	if ok {
		t.Errorf("无数据时不应 ok")
	}
	if best != "default-fallback" {
		t.Errorf("期望返回 default-fallback，得到 %s", best)
	}

	// 2. 插入多个模型：
	// - free-slow (IsFree=true, TTFT=1200ms, Normal)
	// - free-fast (IsFree=true, TTFT=280ms, Fast)
	// - paid-fast (IsFree=false, TTFT=100ms, Fast - 付费模型不应该被选为免费最速)
	// - free-down (IsFree=true, TTFT=0, Down)
	r.SetRecord(ProbeRecord{
		Model:  "free-slow",
		IsFree: true,
		Status: StatusNormal,
		TTFTMs: 1200,
	})
	r.SetRecord(ProbeRecord{
		Model:  "free-fast",
		IsFree: true,
		Status: StatusFast,
		TTFTMs: 280,
	})
	r.SetRecord(ProbeRecord{
		Model:  "paid-fast",
		IsFree: false,
		Status: StatusFast,
		TTFTMs: 100,
	})
	r.SetRecord(ProbeRecord{
		Model:  "free-down",
		IsFree: true,
		Status: StatusDown,
		TTFTMs: 0,
	})

	fastest, found := r.FastestFreeModel()
	if !found {
		t.Fatalf("期望找到最优免费模型")
	}
	if fastest != "free-fast" {
		t.Errorf("期望选出 free-fast，实际得到 %s", fastest)
	}
}

func TestRadarPingBatchConcurrency(t *testing.T) {
	var activeConn int32
	var maxActive int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&activeConn, 1)
		defer atomic.AddInt32(&activeConn, -1)

		// 记录最大并发连接数
		for {
			old := atomic.LoadInt32(&maxActive)
			if cur <= old || atomic.CompareAndSwapInt32(&maxActive, old, cur) {
				break
			}
		}

		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
	}))
	defer ts.Close()

	r := NewRadar(ts.Client())
	models := []ModelMeta{
		{ID: "m1", IsFree: true},
		{ID: "m2", IsFree: true},
		{ID: "m3", IsFree: true},
		{ID: "m4", IsFree: true},
		{ID: "m5", IsFree: true},
	}

	// 限制最大并发为 2
	results := r.PingBatch(context.Background(), ts.URL, "tok", models, 2)
	if len(results) != 5 {
		t.Fatalf("期望得到 5 条测速结果，得到 %d", len(results))
	}

	observedMax := atomic.LoadInt32(&maxActive)
	if observedMax > 2 {
		t.Errorf("最大并发数不应超过 2，实际观测到 %d", observedMax)
	}
}
