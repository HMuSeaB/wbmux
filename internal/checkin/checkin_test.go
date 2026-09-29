package checkin

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// statusBody 是一份实测形态的状态回话（2026-09-29，国内侧账号）。
const statusBody = `{"code":0,"msg":"OK","requestId":"x","data":{
  "active":true,"today_checked_in":true,"streak_days":14,
  "daily_credit":100,"today_credit":100,"is_streak_day":false,
  "next_streak_day":0,"streak_bonus_days":0,"streak_bonus_credit":0,
  "checkin_dates":["2026-09-29","2026-09-28"],"week_checkin_days":2}}`

// newDeps 把请求指到假服务器，并给一份假凭据。
//
// 刻意不传 Probe：带了它就会去碰真实文件系统。凭据走 Cred 注入。
func newDeps(t *testing.T, h http.HandlerFunc) (Deps, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return Deps{
		Endpoint: srv.URL,
		Cred: func(*variant.Probe, variant.ID) (credential.Credential, error) {
			return credential.Credential{Token: "test-token-0123456789", UID: "uid-abc"}, nil
		},
	}, srv
}

// reply 造一个固定回话的假服务器。
func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func TestQueryParsesStatus(t *testing.T) {
	d, _ := newDeps(t, reply(http.StatusOK, statusBody))

	st, err := Query(d, variant.CN)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !st.Active || !st.TodayCheckedIn {
		t.Fatalf("active/todayCheckedIn 不对: %+v", st)
	}
	if st.StreakDays != 14 || st.DailyCredit != 100 || st.WeekCheckinDays != 2 {
		t.Fatalf("数值不对: %+v", st)
	}
	if len(st.CheckinDates) != 2 || st.CheckinDates[0] != "2026-09-29" {
		t.Fatalf("日期列表不对: %+v", st.CheckinDates)
	}
}

func TestQueryTreatsNullDataAsInactive(t *testing.T) {
	// 国际侧就是这种形态：接口在，但活动没开。
	d, _ := newDeps(t, reply(http.StatusOK, `{"code":0,"msg":"OK","data":null}`))

	st, err := Query(d, variant.Intl)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if st.Active {
		t.Fatalf("data 为 null 时应当报「没开活动」，得到 %+v", st)
	}
}

func TestQueryReportsLoginExpiry(t *testing.T) {
	d, _ := newDeps(t, reply(http.StatusUnauthorized, `{"msg":"unauthorized"}`))

	_, err := Query(d, variant.CN)
	if err == nil {
		t.Fatal("401 应当报错")
	}
	// 401 的对策是重新登录，必须说清楚，不能只说"失败"。
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "重新登录") {
		t.Fatalf("应提示重新登录: %v", err)
	}
}

func TestQueryReportsForbiddenSeparately(t *testing.T) {
	d, _ := newDeps(t, reply(http.StatusForbidden, `{"msg":"nope"}`))

	_, err := Query(d, variant.CN)
	if err == nil {
		t.Fatal("403 应当报错")
	}
	// 403 **不能**被说成"登录过期"：那是权限/业务拒绝，重登没用。
	if strings.Contains(err.Error(), "登录") {
		t.Fatalf("403 不该说成登录失效: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("应带上状态码: %v", err)
	}
}

func TestRequestsUsePostWithAuthHeaders(t *testing.T) {
	type seen struct {
		method string
		path   string
		auth   string
		uid    string
		ua     string
		length int64
	}
	got := make(chan seen, 2)

	d, srv := newDeps(t, func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.Method, r.URL.Path, r.Header.Get("Authorization"),
			r.Header.Get("X-User-Id"), r.Header.Get("User-Agent"), r.ContentLength}
		_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":null}`)
	})
	_ = srv

	if _, err := Query(d, variant.CN); err != nil {
		t.Fatalf("Query: %v", err)
	}

	s := <-got
	if s.method != http.MethodPost {
		t.Fatalf("应当是 POST（实测两个接口都是 POST），得到 %s", s.method)
	}
	if s.path != "/v2/billing/meter/checkin-activity-status" {
		t.Fatalf("路径不对: %s", s.path)
	}
	if s.auth != "Bearer test-token-0123456789" {
		t.Fatalf("鉴权头不对: %q", s.auth)
	}
	if s.uid != "uid-abc" {
		t.Fatalf("X-User-Id 不对: %q", s.uid)
	}
	if s.ua == "" {
		t.Fatal("必须显式带 User-Agent：网关会把缺省 UA 当爬虫拒掉")
	}
	// 实测两个接口都不需要请求体。
	if s.length > 0 {
		t.Fatalf("不该带请求体，得到 %d 字节", s.length)
	}
}

func TestClaimReadsCreditFromData(t *testing.T) {
	d, _ := newDeps(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/billing/meter/daily-checkin":
			_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"credit":100}}`)
		default:
			_, _ = io.WriteString(w, statusBody)
		}
	})

	res, err := Claim(d, variant.CN)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.AlreadyCheckedIn {
		t.Fatal("这是领取成功，不该标成已签过")
	}
	if res.Credit != 100 {
		t.Fatalf("积分不对: %d", res.Credit)
	}
	// 领完应当顺手带回一份新状态，界面才好直接显示新的连签天数。
	if res.Status == nil {
		t.Fatal("应带回领取后的状态")
	}
	if res.Status.StreakDays != 14 {
		t.Fatalf("状态不对: %+v", res.Status)
	}
}

func TestClaimReadsCreditFromTopLevel(t *testing.T) {
	// 积分也可能直接放在顶层：两种都收。
	d, _ := newDeps(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "daily-checkin") {
			_, _ = io.WriteString(w, `{"code":0,"msg":"OK","credit":100}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":null}`)
	})

	res, err := Claim(d, variant.CN)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.AlreadyCheckedIn || res.Credit != 100 {
		t.Fatalf("顶层 credit 应当被认出来: %+v", res)
	}
}

func TestClaimTreatsAlreadyCheckedInAsSuccess(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		// 实测的三种"已签"形态都要按正常结果处理。
		{"HTTP 400 + 业务码", http.StatusBadRequest,
			`{"code":10001,"msg":"今天已签到，请明天再来"}`},
		{"HTTP 200 + 业务码", http.StatusOK,
			`{"code":10001,"msg":"今天已签到，请明天再来"}`},
		{"HTTP 200 + data 为 null", http.StatusOK,
			`{"code":0,"msg":"OK","data":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newDeps(t, reply(tc.status, tc.body))

			res, err := Claim(d, variant.CN)
			if err != nil {
				// 幂等是这套用法的前提：已签过必须是正常结果，不是错误。
				t.Fatalf("已签过不该报错: %v", err)
			}
			if !res.AlreadyCheckedIn {
				t.Fatalf("应标成已签过: %+v", res)
			}
			if res.Credit != 0 {
				t.Fatalf("已签过不该报积分: %d", res.Credit)
			}
			if tc.status == http.StatusBadRequest && !strings.Contains(res.Message, "明天") {
				t.Fatalf("应把后端原话带出来: %q", res.Message)
			}
		})
	}
}

func TestClaimRejectsUnknownShapeInsteadOfClaimingSuccess(t *testing.T) {
	// HTTP 200、业务码正常，但没有 credit 也不是空 data。
	// 这既不是成功也不是"已签"，必须报错——猜成"已签"的话用户会以为签了、
	// 其实没领到，而且永远不会再查。
	d, _ := newDeps(t, reply(http.StatusOK, `{"code":0,"msg":"OK","data":{"weird":1}}`))

	_, err := Claim(d, variant.CN)
	if err == nil {
		t.Fatal("形状不认识时应当报错，不能猜")
	}
	if !strings.Contains(err.Error(), "credit") {
		t.Fatalf("应指出缺了什么: %v", err)
	}
}

func TestClaimSurfacesBusinessErrorCode(t *testing.T) {
	d, _ := newDeps(t, reply(http.StatusOK, `{"code":50000,"msg":"activity ended"}`))

	_, err := Claim(d, variant.CN)
	if err == nil {
		t.Fatal("业务错误码应当报错")
	}
	// 靠 BizError 的码分支，而不是匹配文案——文案会变，码不会。
	var biz *BizError
	if !errors.As(err, &biz) {
		t.Fatalf("应当是 BizError，得到 %T: %v", err, err)
	}
	if biz.Code != 50000 || !strings.Contains(err.Error(), "activity ended") {
		t.Fatalf("业务码与文案都要带出: %+v", biz)
	}
}

func TestCredentialFailureSkipsTheRequest(t *testing.T) {
	hit := false
	d, _ := newDeps(t, func(w http.ResponseWriter, r *http.Request) {
		hit = true
	})
	d.Cred = func(*variant.Probe, variant.ID) (credential.Credential, error) {
		return credential.Credential{}, errors.New("凭据没解开")
	}

	if _, err := Query(d, variant.CN); err == nil {
		t.Fatal("凭据失败应当报错")
	}
	if hit {
		t.Fatal("凭据拿不到时不该发请求")
	}
}

func TestEmptyTokenIsRejectedBeforeRequest(t *testing.T) {
	hit := false
	d, _ := newDeps(t, func(w http.ResponseWriter, r *http.Request) { hit = true })
	d.Cred = func(*variant.Probe, variant.ID) (credential.Credential, error) {
		return credential.Credential{UID: "u"}, nil // 有 uid 没 token
	}

	if _, err := Query(d, variant.CN); err == nil {
		t.Fatal("空令牌应当报错")
	}
	if hit {
		t.Fatal("没有令牌就不该发请求")
	}
}

func TestEndpointComesFromProductConfigWhenNotOverridden(t *testing.T) {
	// 不覆盖 Endpoint 时应取档位的产品配置端点，而不是本文件里再抄一份域名。
	d := Deps{}
	got, err := d.endpoint(variant.CN)
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	b, err := variant.Get(variant.CN)
	if err != nil {
		t.Fatalf("variant.Get: %v", err)
	}
	if got != b.Endpoint {
		t.Fatalf("应取产品配置端点 %s，得到 %s", b.Endpoint, got)
	}
	if strings.HasSuffix(got, "/") {
		t.Fatalf("末尾不该带斜杠: %s", got)
	}
}

func TestSummarizeTruncatesAndFlattens(t *testing.T) {
	long := strings.Repeat("a", 400)
	got := summarize([]byte(long))
	if len(got) > 170 || !strings.HasSuffix(got, "…") {
		t.Fatalf("应当截断: %d 字符", len(got))
	}
	if strings.Contains(got, "\n") {
		t.Fatal("应当把换行压平")
	}
	if summarize(nil) != "（空正文）" {
		t.Fatalf("空正文应有明确表示: %q", summarize(nil))
	}
}
