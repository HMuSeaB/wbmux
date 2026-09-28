package webui

import (
	"encoding/json"
	"net/http"

	"github.com/HMuSeaB/wbmux/internal/update"
	"github.com/HMuSeaB/wbmux/internal/version"
)

// ---------- 自助升级 ----------
//
// # 为什么放在界面里
//
// 升级一个单文件绿色程序本该是"下载、覆盖、重启"三步，但下载链路时通时断、
// 覆盖又有"运行中的 exe 不能直接覆盖"的限制——手工做这三步的摩擦足够让
// 用户一直跑旧版。收进界面：检查一次（GitHub 最新 release 对比当前版本），
// 一键完成下载+解包+原位替换，用户只需要按提示重启一次。
//
// 更新源与失败兜底见 internal/update 的包注释。

// handleUpdateCheck 对比当前版本与 GitHub 最新已发布版本。
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	info, err := update.Check(version.Version)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, info)
}

// handleUpdateApply 下载并原位替换 exe。替换完只回话，不重启——
// 正在跑的还是旧版，重启的时机交给用户（托盘退出再启动）。
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		AssetURL string `json:"assetURL"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	msg, err := update.Apply(req.AssetURL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"message": msg})
}
