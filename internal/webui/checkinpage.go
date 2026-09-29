package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/checkin"
	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 签到 ----------
//
// # 为什么查询与领取是两个接口、两个按钮
//
// 查询（GET）随便刷——打开页面就打一次，没副作用。
// 领取（POST）是**写操作**，会真的发积分。
//
// 把两者合成一个"刷新一下顺手领了"的动作，等于让账号状态在用户没看见的
// 时候被改掉。所以：进页面只查询，领取必须点按钮，点了之后把结果如实回报
// （领到多少 / 今天已经签过了）。这与 internal/checkin 的分包理由是同一条。
//
// # 为什么默认只给国内侧
//
// 签到只在国内侧开放（国际侧接口在、active=false，见 internal/checkin 的
// 包注释）。界面仍允许指定档位，但默认是 cn；不给国际侧单开一页，是因为
// 那会是一个永远显示"没有活动"的页面。

// handleCheckin 查一侧的签到状态。只读。
func (s *Server) handleCheckin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	id := checkinTarget(r.URL.Query().Get("host"))
	st, err := checkin.Query(s.checkinDeps(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"side":   string(id),
		"label":  sideLabelFor(id),
		"status": st,
	})
}

// handleCheckinClaim 领取今天的积分。写操作。
func (s *Server) handleCheckinClaim(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Host string `json:"host"`
	}
	// 允许空请求体：界面按默认档位领时不带参数。
	if r.ContentLength > 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体无法解析")
			return
		}
	}
	id := checkinTarget(req.Host)

	res, err := checkin.Claim(s.checkinDeps(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// 领取改了账号状态，落一行日志。事后要能回答"那次是谁点的"——
	// 界面上的动作不该只活在浏览器里。
	if res.AlreadyCheckedIn {
		s.logf("签到：%s 已经签过了，未重复领取", id)
	} else {
		s.logf("签到：%s 领取成功，本次 %d 积分", id, res.Credit)
	}
	writeJSON(w, map[string]any{
		"side":   string(id),
		"label":  sideLabelFor(id),
		"result": res,
	})
}

// checkinTarget 把界面传来的档位名转成 id。
//
// 认不出就退回国内侧，而不是"自动探测到哪套就用哪套"：探测到只装了国际版的
// 机器上会得到一句"没有活动"，看起来像功能坏了。要查国际侧得显式传。
func checkinTarget(host string) variant.ID {
	if strings.EqualFold(strings.TrimSpace(host), string(variant.Intl)) {
		return variant.Intl
	}
	return variant.CN
}

// checkinDeps 组装签到依赖。
//
// 把"钉住的客户端位置"一路传下去：解信封要用**同一处**安装。否则用户钉了
// A 处的客户端，凭据却可能被 B 处的运行时解开，而两者连的是不同后端——
// 这种错配在界面上完全看不出来。
func (s *Server) checkinDeps() checkin.Deps {
	if s.checkinDepsFn != nil {
		return s.checkinDepsFn()
	}
	d := checkin.Deps{Probe: s.probe()}
	if exe := strings.TrimSpace(s.opts.HostExe); exe != "" {
		d.Cred = func(p *variant.Probe, id variant.ID) (credential.Credential, error) {
			return credential.ResolveWith(p, id, exe)
		}
	}
	return d
}

// sideLabelFor 返回档位的中文名，取不到就用 id。
func sideLabelFor(id variant.ID) string {
	if b, err := variant.Get(id); err == nil {
		return b.DisplayName
	}
	return string(id)
}
