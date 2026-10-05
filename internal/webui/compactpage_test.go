package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// useFakeEnv 把环境变量读写换成内存实现，并返回那块内存。
//
// **必须这么做**：真实实现读写的是 HKCU 注册表。测试若直接用它，就会去动
// 开发机上真实的用户环境变量 —— 正是"测试不许碰真实用户状态"那条规矩防的事，
// 而且会在注册表里留下垃圾值。
func useFakeEnv(t *testing.T) *map[string]string {
	t.Helper()
	store := map[string]string{}
	oldR, oldW := envReader, envWriter
	envReader = func(name string) string { return store[name] }
	envWriter = func(name, value string) error {
		if value == "" {
			delete(store, name)
		} else {
			store[name] = value
		}
		return nil
	}
	t.Cleanup(func() { envReader, envWriter = oldR, oldW })
	return &store
}

// callCompact 打一次接口，返回状态码与解码后的状态。
func callCompact(t *testing.T, srv *Server, method, body string) (int, CompactState) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/compact", strings.NewReader(body))
	req.Header.Set("X-Wbmux-Token", srv.token)
	w := httptest.NewRecorder()
	srv.handleCompactSetting(w, req)

	var st CompactState
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatalf("回话不是合法状态：%v\n%s", err, w.Body.String())
		}
	}
	return w.Code, st
}

// TestCompactUnsetUsesDefault 没设过时必须报"走默认"。
//
// "我从没动过"与"我设成了和默认一样的值"对用户意义不同，混起来
// 会让人以为设置丢了。
func TestCompactUnsetUsesDefault(t *testing.T) {
	srv := newRecycleTestServer(t)
	useFakeEnv(t)

	code, st := callCompact(t, srv, http.MethodGet, "")
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	if st.Set {
		t.Error("没设过却报 Set=true")
	}
	if st.Effective != compactDefault {
		t.Errorf("生效值应为默认 %d，实际 %d", compactDefault, st.Effective)
	}
	if st.TriggerAtPreMessage != compactDefault/2 {
		t.Errorf("压缩触发点应为 %d，实际 %d", compactDefault/2, st.TriggerAtPreMessage)
	}
}

// TestCompactReadsEnv 设过就按设的算。
func TestCompactReadsEnv(t *testing.T) {
	srv := newRecycleTestServer(t)
	store := useFakeEnv(t)
	(*store)[compactEnvName] = "300000"

	_, st := callCompact(t, srv, http.MethodGet, "")
	if !st.Set || st.CurrentValue != 300000 {
		t.Errorf("应读到 300000，实际 set=%v value=%d", st.Set, st.CurrentValue)
	}
	if st.TriggerAtPreMessage != 150000 {
		t.Errorf("触发点应为 150000（300k×0.5），实际 %d", st.TriggerAtPreMessage)
	}
}

// TestCompactOutOfRangeReportsClamped 超范围的值客户端会**静默 clamp**。
//
// 这里必须报出"实际按哪个值生效"，否则用户设了 50000、界面显示 50000，
// 他会以为生效了，其实客户端按 100000 跑。
func TestCompactOutOfRangeReportsClamped(t *testing.T) {
	srv := newRecycleTestServer(t)
	store := useFakeEnv(t)

	(*store)[compactEnvName] = "50000"
	_, st := callCompact(t, srv, http.MethodGet, "")
	if st.Effective != compactMin {
		t.Errorf("低于下限应报成 %d，实际 %d", compactMin, st.Effective)
	}
	if st.Note == "" {
		t.Error("被 clamp 了却没给出说明")
	}

	(*store)[compactEnvName] = "99999999"
	_, st = callCompact(t, srv, http.MethodGet, "")
	if st.Effective != compactMax {
		t.Errorf("高于上限应报成 %d，实际 %d", compactMax, st.Effective)
	}
}

// TestCompactGarbageValueReported 值读不懂时要如实说，不能假装是默认。
//
// "1m" 正是那个"写了但静默无效"的典型写法，必须被点出来。
func TestCompactGarbageValueReported(t *testing.T) {
	srv := newRecycleTestServer(t)
	store := useFakeEnv(t)
	(*store)[compactEnvName] = "1m"

	_, st := callCompact(t, srv, http.MethodGet, "")
	if !st.Set {
		t.Error("值存在就该报 Set=true（否则用户不知道有个坏值占着）")
	}
	if st.Note == "" {
		t.Error("值非法却没有任何说明——这正是最难查的那种失败")
	}
	if st.Effective != compactDefault {
		t.Errorf("非法值应回落到默认 %d，实际 %d", compactDefault, st.Effective)
	}
}

// TestCompactPostWritesAndReflects 写完要**立刻读回新值**。
//
// 这条钉的是实测踩到的那个 bug：写的是注册表、读的是本进程 os.Getenv，
// 于是点完按钮值没变，界面看着"没生效"。
func TestCompactPostWritesAndReflects(t *testing.T) {
	srv := newRecycleTestServer(t)
	store := useFakeEnv(t)

	code, st := callCompact(t, srv, http.MethodPost, `{"value":1000000}`)
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	if (*store)[compactEnvName] != "1000000" {
		t.Errorf("没写进去，store=%v", *store)
	}
	// 关键：同一次回话里就该反映新值
	if st.Effective != 1000000 {
		t.Errorf("写完生效值应为 1000000，实际 %d（就是那个写了自己看不到的 bug）", st.Effective)
	}
	if !st.Set || st.CurrentValue != 1000000 {
		t.Errorf("写完状态不对：set=%v value=%d", st.Set, st.CurrentValue)
	}
}

// TestCompactPostZeroClears 传 0 表示"清掉、回默认"。
func TestCompactPostZeroClears(t *testing.T) {
	srv := newRecycleTestServer(t)
	store := useFakeEnv(t)
	(*store)[compactEnvName] = "300000"

	_, st := callCompact(t, srv, http.MethodPost, `{"value":0}`)
	if _, ok := (*store)[compactEnvName]; ok {
		t.Error("传 0 应该把变量删掉")
	}
	if st.Set || st.Effective != compactDefault {
		t.Errorf("清掉后应报未设置/走默认，实际 set=%v eff=%d", st.Set, st.Effective)
	}
}

// TestCompactPostRejectsOutOfRange 越界的写请求要拒绝。
//
// 不拒绝的话就会走客户端那条"静默 clamp"的路，用户以为设了别的值。
func TestCompactPostRejectsOutOfRange(t *testing.T) {
	srv := newRecycleTestServer(t)
	useFakeEnv(t)

	for _, v := range []int{-1, 1, compactMin - 1, compactMax + 1} {
		code, _ := callCompact(t, srv, http.MethodPost,
			`{"value":`+strconv.Itoa(v)+`}`)
		if code != http.StatusBadRequest {
			t.Errorf("值 %d 应被拒绝（400），实际 %d", v, code)
		}
	}
}

// TestCompactPostRejectsBadBody 坏请求体不能 panic、要报 400。
func TestCompactPostRejectsBadBody(t *testing.T) {
	srv := newRecycleTestServer(t)
	useFakeEnv(t)
	code, _ := callCompact(t, srv, http.MethodPost, `{这不是 json`)
	if code != http.StatusBadRequest {
		t.Errorf("坏请求体应报 400，实际 %d", code)
	}
}

// TestCompactRejectsOtherMethods 只接受 GET / POST。
func TestCompactRejectsOtherMethods(t *testing.T) {
	srv := newRecycleTestServer(t)
	useFakeEnv(t)
	code, _ := callCompact(t, srv, http.MethodDelete, "")
	if code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE 应报 405，实际 %d", code)
	}
}

// TestCompactLevelsPresent 档位必须齐全，且每个都带取舍说明。
//
// 界面完全靠这份数据渲染：缺档位就是缺功能，缺 desc 用户就没法选。
func TestCompactLevelsPresent(t *testing.T) {
	srv := newRecycleTestServer(t)
	useFakeEnv(t)
	_, st := callCompact(t, srv, http.MethodGet, "")

	keys := map[string]bool{}
	rec := 0
	for _, lv := range st.Levels {
		keys[lv.Key] = true
		if lv.Value != 0 && (lv.Value < compactMin || lv.Value > compactMax) {
			t.Errorf("档位 %s 的值 %d 超出合法范围", lv.Key, lv.Value)
		}
		if lv.Desc == "" {
			t.Errorf("档位 %s 没有取舍说明——用户没法判断该选哪个", lv.Key)
		}
		if lv.Recommended {
			rec++
		}
	}
	for _, want := range []string{"300k", "1M", "default"} {
		if !keys[want] {
			t.Errorf("缺档位 %s", want)
		}
	}
	if rec != 1 {
		t.Errorf("推荐档应恰好 1 个，实际 %d", rec)
	}
}

// TestCompactDefaultsAreConsistent 常量之间不能自相矛盾。
//
// 这几个数字是从客户端源码抄来的，抄错不会报错、只会让界面显示假信息。
func TestCompactDefaultsAreConsistent(t *testing.T) {
	if !(compactMin < compactDefault && compactDefault < compactMax) {
		t.Errorf("默认值 %d 应落在 [%d, %d] 内", compactDefault, compactMin, compactMax)
	}
	if compactPreMessageRatio <= 0 || compactPreMessageRatio >= 1 {
		t.Errorf("preMessage 比例 %v 应是 0~1 之间", compactPreMessageRatio)
	}
}
