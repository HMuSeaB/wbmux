package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/embedstats"
	"github.com/HMuSeaB/wbmux/internal/gatewayaudit"
	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// TestMainSubscriptionPackage 钉住主包的挑法：kind=="subscription" 里
// DeductPriority 最小的那个；bonus 包不算；算不出百分比的不要。
func TestMainSubscriptionPackage(t *testing.T) {
	sub := usage.LivePackage{Kind: "subscription", Total: 100, Remain: 62.5, DeductPriority: 1}
	bonus := usage.LivePackage{Kind: "bonus", Total: 50, Remain: 50, DeductPriority: 2}
	sub2 := usage.LivePackage{Kind: "subscription", Total: 200, Remain: 10, DeductPriority: 2}

	acc := &usage.LiveAccount{OK: true, Packages: []usage.LivePackage{bonus, sub2, sub}}
	got := mainSubscriptionPackage(acc)
	if got == nil || got.Total != 100 {
		t.Fatalf("应挑 DeductPriority 最小的订阅包，得到 %+v", got)
	}

	// 只有 bonus 包 → 没有主包，不显示 0%
	if got := mainSubscriptionPackage(&usage.LiveAccount{OK: true, Packages: []usage.LivePackage{bonus}}); got != nil {
		t.Errorf("没有订阅包时应返回 nil，得到 %+v", got)
	}
	// Total<=0 算不出百分比 → 不要
	if got := mainSubscriptionPackage(&usage.LiveAccount{OK: true, Packages: []usage.LivePackage{
		{Kind: "subscription", Total: 0, Remain: 0, DeductPriority: 1},
	}}); got != nil {
		t.Errorf("Total<=0 时应返回 nil，得到 %+v", got)
	}
	// 账号本身不可用 → 不要
	if got := mainSubscriptionPackage(&usage.LiveAccount{OK: false, Packages: []usage.LivePackage{sub}}); got != nil {
		t.Errorf("账号不可用时应返回 nil，得到 %+v", got)
	}
	if got := mainSubscriptionPackage(nil); got != nil {
		t.Errorf("nil 账号应返回 nil，得到 %+v", got)
	}
}

// TestEmbedStatsQuotaSegment 钉住 /api/embed/stats 的 quota 段：
// last.model 的限流态 + 两档主包余量（分开，不合并；没数据的档位省略）。
func TestEmbedStatsQuotaSegment(t *testing.T) {
	s := newQuotaTestServer(t)
	s.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{
			"cn": {
				OK: true,
				Packages: []usage.LivePackage{
					{Kind: "subscription", Total: 100, Remain: 62.5, DeductPriority: 1, ExpiryRuleZh: "每月 1 日重置（月底清零更新）"},
					{Kind: "bonus", Total: 50, Remain: 50, DeductPriority: 2, ExpiryRuleZh: "发放起 30 天有效（到期清零）"},
				},
			},
			// intl 只有 bonus 包 → 该档不渲染
			"intl": {
				OK:       true,
				Packages: []usage.LivePackage{{Kind: "bonus", Total: 50, Remain: 50, DeductPriority: 2}},
			},
		}
	}
	// 代理亲眼见到的限流：恢复时刻写到 2099 年，保证测试跑的时候还没过
	s.observe("deepseek-v4.1-flash", http.StatusTooManyRequests,
		"usage exceeds frequency limit, but don't worry, your usage will reset at 2099-01-01 22:18:43 UTC+8, alternatively, you can switch to the other models")
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "deepseek-v4.1-flash",
		PromptTokens:     260000,
		CompletionTokens: 50,
		TotalTokens:      260050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/embed/stats", nil)
	rec := httptest.NewRecorder()
	s.handleEmbedStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleEmbedStats 应返回 200，得到 %d", rec.Code)
	}
	var resp struct {
		Last struct {
			Model string `json:"model"`
		} `json:"last"`
		Quota *struct {
			Model *struct {
				Model     string `json:"model"`
				State     string `json:"state"`
				UntilText string `json:"untilText"`
			} `json:"model"`
			Packages []struct {
				Side      string  `json:"side"`
				Percent   float64 `json:"percent"`
				CycleText string  `json:"cycleText"`
			} `json:"packages"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应无法解析: %v", err)
	}
	if resp.Quota == nil {
		t.Fatal("quota 段缺失")
	}
	if resp.Quota.Model == nil || resp.Quota.Model.State != "rate_limited" ||
		resp.Quota.Model.Model != "deepseek-v4.1-flash" {
		t.Errorf("quota.model 应为 deepseek-v4.1-flash 的限流记录，得到 %+v", resp.Quota.Model)
	}
	if len(resp.Quota.Packages) != 1 {
		t.Fatalf("应只有国内包一行（国际档无订阅包），得到 %d 行 %+v", len(resp.Quota.Packages), resp.Quota.Packages)
	}
	pkg := resp.Quota.Packages[0]
	if pkg.Side != "cn" || pkg.Percent != 62.5 || pkg.CycleText != "每月 1 日重置（月底清零更新）" {
		t.Errorf("国内包应为 cn/62.5%%/每月 1 日重置，得到 %+v", pkg)
	}
}

// TestServeEmbedLast 钉住面板侧兜底：新记录替换旧记录、nil 时给兜底且
// 年龄按兜底时间戳重算、满 120 分钟变 idle；holder 为 nil 时不兜底。
func TestServeEmbedLast(t *testing.T) {
	now := time.Now()
	s := &Server{lastHold: embedstats.NewLastHold[embedLastStats]()}

	got := s.serveEmbedLast(&embedLastStats{Model: "Kimi-K3.1-Pro", PromptTokens: 61000}, now)
	if got == nil || got.Model != "Kimi-K3.1-Pro" || got.Freshness != "fresh" {
		t.Fatalf("新记录应原样给出且为 fresh，得到 %+v", got)
	}
	// nil candidate → 兜底，AgeSec 重算为约 0 秒
	got = s.serveEmbedLast(nil, time.Time{})
	if got == nil || got.Model != "Kimi-K3.1-Pro" {
		t.Fatalf("nil candidate 应给兜底，得到 %+v", got)
	}
	if got.AgeSec < 0 || got.AgeSec > 5 {
		t.Errorf("兜底 AgeSec 应为约 0 秒，得到 %d", got.AgeSec)
	}
	// 更旧的 candidate 不替换兜底
	got = s.serveEmbedLast(&embedLastStats{Model: "old", PromptTokens: 1000}, now.Add(-time.Hour))
	if got == nil || got.Model != "Kimi-K3.1-Pro" {
		t.Errorf("旧 candidate 不应替换兜底，得到 %+v", got)
	}
	// 更新的 candidate 替换兜底
	got = s.serveEmbedLast(&embedLastStats{Model: "DeepSeek-V4.1-Flash", PromptTokens: 62000}, now.Add(time.Minute))
	if got == nil || got.Model != "DeepSeek-V4.1-Flash" {
		t.Errorf("新 candidate 应替换兜底，得到 %+v", got)
	}

	// 121 分钟前的兜底 → idle；40 分钟前 → aged
	sOld := &Server{lastHold: embedstats.NewLastHold[embedLastStats]()}
	sOld.serveEmbedLast(&embedLastStats{Model: "Kimi-K3.1-Pro", PromptTokens: 61000}, now.Add(-121*time.Minute))
	if got := sOld.serveEmbedLast(nil, time.Time{}); got == nil || got.Freshness != "idle" {
		t.Errorf("121 分钟前的兜底应判 idle，得到 %+v", got)
	}
	sAged := &Server{lastHold: embedstats.NewLastHold[embedLastStats]()}
	sAged.serveEmbedLast(&embedLastStats{Model: "Kimi-K3.1-Pro", PromptTokens: 61000}, now.Add(-40*time.Minute))
	if got := sAged.serveEmbedLast(nil, time.Time{}); got == nil || got.Freshness != "aged" {
		t.Errorf("40 分钟前的兜底应判 aged，得到 %+v", got)
	}

	// holder 为 nil（测试直接拼的 Server）时不兜底
	bare := &Server{}
	if got := bare.serveEmbedLast(&embedLastStats{Model: "x"}, now); got == nil || got.Model != "x" {
		t.Errorf("nil holder 应原样返回 candidate，得到 %+v", got)
	}
	if got := bare.serveEmbedLast(nil, time.Time{}); got != nil {
		t.Errorf("nil holder + nil candidate 应返回 nil，得到 %+v", got)
	}
}

// TestEmbedStatsHoldEndToEnd 走一遍 handleEmbedStats 的兜底全流程：
// 遥测有新鲜记录 → last 正常；把会话文件改写成只有填充行（模拟新一轮
// 生成中旧记录被挤出窗口、新用量未落盘）→ last 仍是上一条，不跌回空闲；
// 再写入更新的记录 → last 被替换。
func TestEmbedStatsHoldEndToEnd(t *testing.T) {
	// 测试二进制里 Default() 不落盘，Clear 只清内存，不动用户真实日志。
	gatewayaudit.Default().Clear()

	tmpDir := t.TempDir()
	sessDir := filepath.Join(tmpDir, ".workbuddy", "projects", "proj1")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessFile := filepath.Join(sessDir, "session.jsonl")
	writeUsage := func(model string, prompt int, ts time.Time) {
		t.Helper()
		line := fmt.Sprintf(`{"id":"u1","timestamp":%d,"providerData":{"requestModelName":%q,"rawUsage":{"prompt_tokens":%d,"completion_tokens":120,"total_tokens":%d}}}`+"\n",
			ts.UnixMilli(), model, prompt, prompt+120)
		if err := os.WriteFile(sessFile, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	callStats := func(s *Server) (model, freshness string, prompt int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/embed/stats", nil)
		rec := httptest.NewRecorder()
		s.handleEmbedStats(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("handleEmbedStats 应返回 200，得到 %d", rec.Code)
		}
		var resp struct {
			Last *struct {
				Model        string `json:"model"`
				PromptTokens int    `json:"promptTokens"`
				Freshness    string `json:"freshness"`
			} `json:"last"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("响应无法解析: %v", err)
		}
		if resp.Last == nil {
			return "", "", 0
		}
		return resp.Last.Model, resp.Last.Freshness, resp.Last.PromptTokens
	}

	s := newQuotaTestServer(t)
	s.lastHold = embedstats.NewLastHold[embedLastStats]()
	s.opts.Probe = &variant.Probe{Home: tmpDir}
	s.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{}
	}

	now := time.Now()
	writeUsage("Kimi-K3.1-Pro", 61000, now)
	if model, freshness, prompt := callStats(s); model != "Kimi-K3.1-Pro" || freshness != "fresh" || prompt != 61000 {
		t.Fatalf("第一次应拿到新鲜记录，得到 %q/%q/%d", model, freshness, prompt)
	}

	// 模拟生成中：日志里只剩填充行（不含 usage/tokens 关键字），遥测返回 nil
	filler := ""
	for i := 0; i < 200; i++ {
		filler += `{"type":"thought","text":"thinking very hard, filling the log line by line..."}` + "\n"
	}
	if err := os.WriteFile(sessFile, []byte(filler), 0o644); err != nil {
		t.Fatal(err)
	}
	if model, freshness, prompt := callStats(s); model != "Kimi-K3.1-Pro" || prompt != 61000 {
		t.Fatalf("遥测 miss 时应兜底上一条，得到 %q/%q/%d", model, freshness, prompt)
	}

	// 更新的记录一到就替换兜底
	writeUsage("DeepSeek-V4.1-Flash", 62000, time.Now())
	if model, _, prompt := callStats(s); model != "DeepSeek-V4.1-Flash" || prompt != 62000 {
		t.Fatalf("新记录应替换兜底，得到 %q/%d", model, prompt)
	}
}

// TestEmbedStatsQuotaSegmentEmpty 没数据时 quota 段整个省略，
// 前端不渲染，绝不显示 0%。
func TestEmbedStatsQuotaSegmentEmpty(t *testing.T) {
	s := newQuotaTestServer(t)
	s.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{}
	}
	gatewayaudit.Default().RecordRequest(gatewayaudit.Record{
		ModelRequested:   "deepseek-v4.1-flash",
		PromptTokens:     100000,
		CompletionTokens: 50,
		TotalTokens:      100050,
		StatusCode:       200,
		DurationMs:       1000,
		TTFTMs:           200,
		IsStream:         true,
		Timestamp:        time.Now(),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/embed/stats", nil)
	rec := httptest.NewRecorder()
	s.handleEmbedStats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleEmbedStats 应返回 200，得到 %d", rec.Code)
	}
	var resp struct {
		Quota *struct {
			Model    any `json:"model"`
			Packages any `json:"packages"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应无法解析: %v", err)
	}
	if resp.Quota != nil {
		t.Errorf("没数据时 quota 段应省略，得到 %+v", resp.Quota)
	}
}

// TestCapsulePackages 钉住胶囊常驻段的数据面：各档主包余量百分比，
// 国内在前、国际在后；百分比四舍五入到整数（面板保留一位小数）；
// 没数据的档位不出现，绝不返回 0%。
func TestCapsulePackages(t *testing.T) {
	accounts := func(cn, intl *usage.LiveAccount) map[string]*usage.LiveAccount {
		return map[string]*usage.LiveAccount{"cn": cn, "intl": intl}
	}
	sub := func(total, remain float64) usage.LivePackage {
		return usage.LivePackage{Kind: "subscription", Total: total, Remain: remain, DeductPriority: 1}
	}

	s := newQuotaTestServer(t)
	s.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return accounts(
			&usage.LiveAccount{OK: true, Packages: []usage.LivePackage{sub(100, 62)}},
			&usage.LiveAccount{OK: true, Packages: []usage.LivePackage{sub(200, 82)}},
		)
	}
	got := s.CapsulePackages()
	if len(got) != 2 || got[0] != (CapsulePackage{Side: "cn", Percent: 62}) ||
		got[1] != (CapsulePackage{Side: "intl", Percent: 41}) {
		t.Fatalf("两边有数据时应为 [{cn 62} {intl 41}]，得到 %+v", got)
	}

	// 整数四舍五入：面板 62.5%，胶囊 63%
	s.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return accounts(
			&usage.LiveAccount{OK: true, Packages: []usage.LivePackage{sub(100, 62.5)}},
			nil,
		)
	}
	got = s.CapsulePackages()
	if len(got) != 1 || got[0] != (CapsulePackage{Side: "cn", Percent: 63}) {
		t.Errorf("62.5%% 应四舍五入为 63，且只有国内档，得到 %+v", got)
	}

	// 没数据的档位不出现：账号不可用、Total<=0、只有 bonus 包
	s.liveAccountsFn = func(*variant.Probe) map[string]*usage.LiveAccount {
		return accounts(
			&usage.LiveAccount{OK: false, Packages: []usage.LivePackage{sub(100, 62)}},
			&usage.LiveAccount{OK: true, Packages: []usage.LivePackage{
				{Kind: "subscription", Total: 0, Remain: 0, DeductPriority: 1},
				{Kind: "bonus", Total: 50, Remain: 50, DeductPriority: 2},
			}},
		)
	}
	if got := s.CapsulePackages(); len(got) != 0 {
		t.Errorf("两边都没主包时应返回空，得到 %+v", got)
	}
}

// TestResetsInDays 钉住"还剩几天清零"的口径：毫秒时间戳优先、
// CycleEndTime 兜底、已过期与无信息都返回 0（不猜）。
func TestResetsInDays(t *testing.T) {
	now := time.Now()
	if d := resetsInDays(&usage.LivePackage{ExpiresAtMs: now.Add(72 * time.Hour).UnixMilli()}); d != 3 {
		t.Errorf("3 天后到期应为 3，得到 %d", d)
	}
	if d := resetsInDays(&usage.LivePackage{ExpiresAtMs: now.Add(-time.Hour).UnixMilli()}); d != 0 {
		t.Errorf("已过期应为 0，得到 %d", d)
	}
	if d := resetsInDays(&usage.LivePackage{}); d != 0 {
		t.Errorf("无到期信息应为 0，得到 %d", d)
	}
	// CycleEndTime 兜底：50 小时向上取整为 3 天
	end := now.Add(50 * time.Hour).Format("2006-01-02 15:04:05")
	if d := resetsInDays(&usage.LivePackage{CycleEndTime: end}); d != 3 {
		t.Errorf("CycleEndTime 50 小时后应为 3，得到 %d", d)
	}
}
