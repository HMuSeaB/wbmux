package webui

import (
	"encoding/json"
	"net/http"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// handleMigrateSurvey 只读地列出可以搬运的东西。
//
// 单独一个只读接口而不是复用 /api/state：扫描要读两侧的 projects 目录、
// 还要起一次 Electron 去读会话索引，比首屏那次探测重得多，不该在打开
// 界面时就付出这个代价。
func (s *Server) handleMigrateSurvey(w http.ResponseWriter, r *http.Request) {
	from, err := variant.Parse(r.URL.Query().Get("from"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "来源档位无效")
		return
	}
	to, err := variant.Parse(r.URL.Query().Get("to"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "目标档位无效")
		return
	}
	if from == to {
		writeErr(w, http.StatusBadRequest, "来源与目标不能是同一套后端")
		return
	}

	res, err := migrate.Survey(migrate.Options{Source: from, Target: to, Probe: s.probe()})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, res)
}

// migrateApplyRequest 是 /api/migrate/apply 的请求体。
//
// 只收档位与选择：具体要复制哪些文件、写哪些行，全部由服务端根据选择
// 重新推导。界面传来的路径一概不采信——那等于把写文件的位置交给浏览器。
type migrateApplyRequest struct {
	From       variant.ID     `json:"from"`
	To         variant.ID     `json:"to"`
	Kinds      []migrate.Kind `json:"kinds"`
	SessionIDs []string       `json:"sessionIds"`
}

func (s *Server) handleMigrateApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req migrateApplyRequest
	// 上限放宽到 1 MB：勾选的会话 id 可能很多，但也不该无限大。
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析")
		return
	}
	if _, err := variant.Get(req.From); err != nil {
		writeErr(w, http.StatusBadRequest, "来源档位无效")
		return
	}
	if _, err := variant.Get(req.To); err != nil {
		writeErr(w, http.StatusBadRequest, "目标档位无效")
		return
	}
	if req.From == req.To {
		writeErr(w, http.StatusBadRequest, "来源与目标不能是同一套后端")
		return
	}

	rep, err := migrate.Apply(
		migrate.Options{Source: req.From, Target: req.To, Probe: s.probe()},
		migrate.Selection{Kinds: req.Kinds, SessionIDs: req.SessionIDs},
	)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, rep)
}
