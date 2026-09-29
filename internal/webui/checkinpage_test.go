package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/checkin"
	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 签到的接口层 ----------
//
// 这一层要验的是"读与写没有混在一起"，以及档位与错误都如实传出去。
// 真正的签到逻辑在 internal/checkin，那里有自己的表驱动用例。
//
// 后端一律是假的：签到里的"领取"是写操作，测试绝不能碰真账号。

// checkinStatusBody 是一份实测形态的状态回话。
const checkinStatusBody = `{"code":0,"msg":"OK","data":{
  "active":true,"today_checked_in":false,"streak_days":14,
  "daily_credit":100,"today_credit":0,"week_checkin_days":2}}`

// withFakeCheckin 把服务端的签到依赖指到假后端与假凭据。
func withFakeCheckin(t *testing.T, env *testEnv, h http.HandlerFunc) {
	t.Helper()
	up := httptest.NewServer(h)
	t.Cleanup(up.Close)

	env.srv.checkinDepsFn = func() checkin.Deps {
		return checkin.Deps{
			Endpoint: up.URL,
			Cred: func(*variant.Probe, variant.ID) (credential.Credential, error) {
				return credential.Credential{Token: "test-token-0123456789", UID: "uid-abc"}, nil
			},
		}
	}
}

func TestCheckinEndpointReturnsStatus(t *testing.T) {
	env := newTestEnv(t, Options{})
	withFakeCheckin(t, env, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/billing/meter/checkin-activity-status" {
			t.Errorf("路径不对: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, checkinStatusBody)
	})

	res := env.do(t, http.MethodGet, "/api/checkin", "", true)
	if res.code != http.StatusOK {
		t.Fatalf("状态码 %d，正文 %s", res.code, res.body)
	}
	var got struct {
		Side   string         `json:"side"`
		Label  string         `json:"label"`
		Status checkin.Status `json:"status"`
	}
	res.decode(t, &got)
	if got.Side != "cn" {
		t.Fatalf("默认档位应当是 cn，得到 %q", got.Side)
	}
	if got.Label == "" {
		t.Fatal("应当带上档位中文名，界面直接用它")
	}
	if got.Status.StreakDays != 14 || got.Status.TodayCheckedIn {
		t.Fatalf("状态没解析对: %+v", got.Status)
	}
}

func TestCheckinClaimEndpointReturnsResult(t *testing.T) {
	env := newTestEnv(t, Options{})
	withFakeCheckin(t, env, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("上游也应当是 POST，得到 %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "daily-checkin") {
			_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"credit":100}}`)
			return
		}
		_, _ = io.WriteString(w, checkinStatusBody)
	})

	res := env.do(t, http.MethodPost, "/api/checkin/claim", "{}", true)
	if res.code != http.StatusOK {
		t.Fatalf("状态码 %d，正文 %s", res.code, res.body)
	}
	var got struct {
		Side   string         `json:"side"`
		Result checkin.Result `json:"result"`
	}
	res.decode(t, &got)
	if got.Result.Credit != 100 || got.Result.AlreadyCheckedIn {
		t.Fatalf("领取结果不对: %+v", got.Result)
	}
	// 领完要带回新状态，界面才能直接重画。
	if got.Result.Status == nil {
		t.Fatal("应带回领取后的状态")
	}
}

func TestCheckinKeepsReadAndWriteApart(t *testing.T) {
	env := newTestEnv(t, Options{})
	withFakeCheckin(t, env, func(w http.ResponseWriter, r *http.Request) {
		// 任何请求都不该到达：方法不对时必须在进门就挡掉。
		t.Errorf("方法不对的请求不该打到后端: %s %s", r.Method, r.URL.Path)
	})

	// 查询接口只读：POST 上去必须被拒，否则"刷新一下"就可能变成一次写。
	if res := env.do(t, http.MethodPost, "/api/checkin", "{}", true); res.code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 查询接口应当 405，得到 %d", res.code)
	}
	// 领取接口是写：GET 上去也必须被拒。
	if res := env.do(t, http.MethodGet, "/api/checkin/claim", "", true); res.code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 领取接口应当 405，得到 %d", res.code)
	}
}

func TestCheckinTargetSelection(t *testing.T) {
	cases := []struct {
		query string
		want  string
	}{
		{"", "cn"},
		{"?host=cn", "cn"},
		{"?host=intl", "intl"},
		{"?host=INTL", "intl"},
		// 认不出的一律退回国内侧：签到只在国内侧开放，乱填时给国际侧
		// 会得到一句"没有活动"，看起来像功能坏了。
		{"?host=weird", "cn"},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			env := newTestEnv(t, Options{})
			withFakeCheckin(t, env, func(w http.ResponseWriter, r *http.Request) {
				// 活动没开，省掉后续两跳。
				_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":null}`)
			})

			res := env.do(t, http.MethodGet, "/api/checkin"+tc.query, "", true)
			if res.code != http.StatusOK {
				t.Fatalf("状态码 %d，正文 %s", res.code, res.body)
			}
			var got struct {
				Side string `json:"side"`
			}
			res.decode(t, &got)
			if got.Side != tc.want {
				t.Fatalf("档位应当 %q，得到 %q", tc.want, got.Side)
			}
		})
	}
}

func TestCheckinSurfacesUpstreamFailure(t *testing.T) {
	env := newTestEnv(t, Options{})
	withFakeCheckin(t, env, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"msg":"unauthorized"}`)
	})

	res := env.do(t, http.MethodGet, "/api/checkin", "", true)
	if res.code != http.StatusBadGateway {
		t.Fatalf("上游失败应当是 502，得到 %d", res.code)
	}
	// 原因要原样带出去：界面只能显示这个字符串。
	var got map[string]string
	res.decode(t, &got)
	if !strings.Contains(got["error"], "401") {
		t.Fatalf("应保留上游的真实原因: %v", got)
	}
}

func TestCheckinEndpointsNeedToken(t *testing.T) {
	env := newTestEnv(t, Options{})
	withFakeCheckin(t, env, func(w http.ResponseWriter, r *http.Request) {
		t.Error("没有令牌的请求不该到达后端")
	})

	// 领取是写操作，更没有理由放行无令牌的请求。
	// 期望是 403 而不是 401：guard 对令牌不符一律回 403（见 webui.go），
	// 这里跟着既有约定走，不为了本页去改守卫的语义。
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/checkin", ""},
		{http.MethodPost, "/api/checkin/claim", "{}"},
	} {
		if res := env.do(t, tc.method, tc.path, tc.body, false); res.code != http.StatusForbidden {
			t.Fatalf("%s %s 无令牌应当 403，得到 %d", tc.method, tc.path, res.code)
		}
	}
}
