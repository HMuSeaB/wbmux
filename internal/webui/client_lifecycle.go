package webui

import (
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/clientproc"
	"github.com/HMuSeaB/wbmux/internal/runner"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// effectiveParentEnv 返回附带最新注册表环境变量（如 CODEBUDDY_AUTO_COMPACT_WINDOW）的环境列表。
// 避免 wbmux 启动后用户修改了压缩阈值，而子进程依然继承旧环境变量快照的问题。
func (s *Server) effectiveParentEnv() []string {
	env := append([]string{}, s.opts.ParentEnv...)
	if val := strings.TrimSpace(readUserEnv(compactEnvName)); val != "" {
		prefix := compactEnvName + "="
		found := false
		for i, e := range env {
			if strings.HasPrefix(strings.ToUpper(e), strings.ToUpper(prefix)) {
				env[i] = prefix + val
				found = true
				break
			}
		}
		if !found {
			env = append(env, prefix+val)
		}
	}
	return env
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRequest(w, r)
	if !ok {
		return
	}
	res, err := runner.Prepare(runner.Options{
		Target:    req.Target,
		Native:    req.Native,
		ParentEnv: s.effectiveParentEnv(),
		Probe:     s.probe(),
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := res.Launch(); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	view := toPreview(res)
	view.Launched = true
	writeJSON(w, view)
}

// handleRestart 关掉正在运行的客户端再重新启动它。
//
// # 为什么要这个按钮
//
// 有些设置是客户端**启动时**才读的（比如压缩阈值那个环境变量，以及
// wbmux 生成的后端配置）。改完不重启就没效果，而让用户自己去托盘退出、
// 再回来点「启动客户端」，既绕又不明显 —— 他很可能就在这一步以为"改了没用"。
//
// # 顺序不能反
//
// **必须先关干净、再启动**。反过来做会让新进程与旧进程抢同一个用户数据
// 目录的锁，或者新进程被旧实例"接管"（单实例锁），结果是配置没换、
// 界面还停在旧的上面。所以这里等 Terminate 报告进程确实没了才继续。
//
// # 关了但启动失败怎么办
//
// 如实报错，并说明"客户端已经关了"。最坏的情况是用户的客户端被关掉、
// 新的没起来 —— 这种事必须让他立刻知道，而不是给一句"重启失败"让他
// 自己去猜客户端还在不在。
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	req, ok := s.decodeRequest(w, r)
	if !ok {
		return
	}

	target, err := variant.Get(req.Target)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "目标后端无效")
		return
	}

	// 关掉**实际在跑的那个**客户端，而不是按目标后端的名字去找。
	//
	// 2026-10-16 的真实故障：用户点「重启国际版」，但这台机器上跑的是
	// `WorkBuddy.exe`（国内版的名字），而逻辑去找 `WorkBuddyAI.exe` ——
	// 找不到，于是"已关闭 0 个进程"，旧实例继续占着单实例锁，新实例起不来。
	// 用户看到的就是"点了重启但什么都没变"。
	//
	// 两个名字只差一个词尾（`WorkBuddy` / `WorkBuddyAI`），界面上既看不出
	// 自己点的哪个后端，更看不出进程名对不上。所以这里不去猜，**按实际在跑
	// 的那个关**：两侧的名字都试一遍，谁在跑关谁。
	killed, err := clientproc.TerminateAny(variant.ExecutableNames()...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"关闭客户端没成功："+err.Error()+"。为避免两个实例抢同一份数据，"+
				"这一次没有重新启动，请手动在托盘退出后再点「启动客户端」。")
		return
	}
	s.logf("重启客户端：已关闭 %d 个进程，准备重新启动 %s", killed, target.DisplayName)

	// 关掉之后再启动。Prepare 要重新做一遍：它会重新读一遍环境与配置，
	// 这正是我们想要新进程拿到的。
	res, err := runner.Prepare(runner.Options{
		Target:    req.Target,
		Native:    req.Native,
		ParentEnv: s.effectiveParentEnv(),
		Probe:     s.probe(),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError,
			"客户端已经关闭，但重新启动前准备失败："+err.Error())
		return
	}
	if err := res.Launch(); err != nil {
		writeErr(w, http.StatusInternalServerError,
			"客户端已经关闭，但重新启动失败："+err.Error())
		return
	}

	view := toPreview(res)
	view.Launched = true
	view.Restarted = true
	view.Killed = killed
	writeJSON(w, view)
}

// launchBothResult 是一侧的双开结果。
type launchBothResult struct {
	Side string `json:"side"`
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
}

// handleLaunchBoth 双开：两侧各用各的安装**原样启动**。
//
// 双开与"切换"是两个物种：切换是同一份 exe 换后端，一次只能一个；
// 双开是两套安装并行——互斥锁/数据目录/认证文件本就按产品分开
// （2026-09-26 实测，含互斥锁名 workbuddy vs workbuddy-ai）。
// 每侧独立启动：一侧失败（比如没装）不影响另一侧，结果逐侧回传，
// 界面如实汇报，不合并成一个笼统的成功/失败。
func (s *Server) handleLaunchBoth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var results []launchBothResult
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		res := launchBothResult{Side: string(id)}
		pv, err := s.buildPreview(previewRequest{Target: id, Native: true})
		if err == nil {
			res.Name = pv.TargetName
		}
		prep, err := runner.Prepare(runner.Options{
			Target:    id,
			Native:    true,
			ParentEnv: s.effectiveParentEnv(),
			Probe:     s.probe(),
		})
		if err == nil {
			err = prep.Launch()
		}
		if err != nil {
			res.Err = err.Error()
		} else {
			res.OK = true
		}
		results = append(results, res)
	}
	writeJSON(w, map[string]any{"results": results})
}
