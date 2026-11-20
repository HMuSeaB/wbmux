package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
