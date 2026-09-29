package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/sessions"
	"github.com/HMuSeaB/wbmux/internal/zimport"
)

// handleZImport 把 ZCode 的一个会话导入 WorkBuddy 的会话库。
//
// 这是**写**操作，所以额外交代清楚：
//   - 目标档位的客户端必须在关闭状态（zimport 会拒绝，并说明原因）；
//   - 只增不改：新造会话 id，绝不覆盖已有的；ZCode 的数据一个字节不动。
func (s *Server) handleZImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		ID            string `json:"id"`
		To            string `json:"to"`
		Project       string `json:"project"`
		Title         string `json:"title"`
		WithReasoning bool   `json:"withReasoning"`
		Confirm       bool   `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析："+err.Error())
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}

	v := sessions.VendorWBCN
	switch strings.ToLower(strings.TrimSpace(req.To)) {
	case "cn", "china", "":
		v = sessions.VendorWBCN
	case "intl", "international", "ai":
		v = sessions.VendorWBIntl
	default:
		writeErr(w, http.StatusBadRequest, "--to 只支持 cn 或 intl，收到 "+req.To)
		return
	}

	// 没有 confirm 就先只回一段"这会做什么"，让界面担起确认框的文案。
	// 前端自己写一遍说明容易和实现走偏；由这里给，改一处就够。
	if !req.Confirm {
		writeJSON(w, map[string]any{
			"needConfirm": true,
			"note": "这会往「" + v.Label() + "」的数据目录里写一份对话文件并插一行索引。" +
				"只增不改，已有会话不会被覆盖；ZCode 的数据一个字节都不动。" +
				"请先完全退出该档位的客户端（它握着列表缓存，边跑边写会看不到新条目）。",
		})
		return
	}

	res, err := zimport.Import(s.probe(), req.ID, zimport.Options{
		Vendor:          v,
		WithReasoning:   req.WithReasoning,
		ProjectOverride: req.Project,
		TitleOverride:   req.Title,
	})
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"sessionId": res.SessionID,
		"jsonlPath": res.JSONLPath,
		"title":     res.Title,
		"project":   res.Project,
		"msgs":      res.Msgs,
		"note": "已导入到「" + v.Label() + "」。启动该档位客户端即可在会话列表里看到：" +
			res.Title,
		"skipped": res.Skipped,
	})
}
