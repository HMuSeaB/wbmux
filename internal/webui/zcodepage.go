package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/zcode"
)

// ---------- ZCode 会话：只读预览 + 导出 Markdown ----------
//
// 两条红线：
//   - ZCode 的数据只读，任何接口都不写它；
//   - 导出写到用户指定的目录，默认给一个明确的落点（不是当前工作目录——
//     服务是双击启动的，那个"当前目录"用户根本不知道在哪）。

// handleZCodeList 列出 ZCode 的会话。
func (s *Server) handleZCodeList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	if !zcode.Available(s.probe()) {
		writeJSON(w, map[string]any{
			"available": false,
			"note":      "本机没有找到 ZCode 的会话库（~/.zcode/cli/db/db.sqlite）",
		})
		return
	}
	limit := 60
	rows, err := zcode.List(s.probe(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"available": true, "sessions": rows})
}

// handleZCodeShow 读一个会话的对话（只读预览）。
func (s *Server) handleZCodeShow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	tr, err := zcode.Load(s.probe(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, map[string]any{"session": tr})
}

// handleZCodeExport 导出成 Markdown。
func (s *Server) handleZCodeExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		ID            string `json:"id"`
		Dir           string `json:"dir"`
		WithReasoning bool   `json:"withReasoning"`
		NoTools       bool   `json:"noTools"`
		DeleteSource  bool   `json:"deleteSource"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析："+err.Error())
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}

	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		// 默认落点：设置目录下的 exports\zcode。放在这儿而不是"当前目录"，
		// 是因为服务多半是双击启动的，那个当前目录对用户没有意义。
		cfgDir, err := config.Dir()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		dir = filepath.Join(cfgDir, "exports", "zcode")
	}

	tr, err := zcode.Load(s.probe(), req.ID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	path, err := zcode.ExportFile(tr, dir, zcode.MarkdownOptions{
		WithReasoning: req.WithReasoning,
		WithTools:     !req.NoTools,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	deleted := false
	delNote := ""
	if req.DeleteSource {
		if rows, err := zcode.DeleteSession(s.probe(), req.ID); err != nil {
			delNote = "；但从 ZCode 清理原会话失败：" + err.Error()
		} else {
			deleted = true
			delNote = fmt.Sprintf("；已从 ZCode 删除原会话（清理了 %d 行，已自动备份）", rows)
		}
	}

	writeJSON(w, map[string]any{
		"path":    path,
		"title":   tr.Title,
		"msgs":    len(tr.Msgs),
		"deleted": deleted,
		"note":    fmt.Sprintf("已导出 %d 条消息到 %s%s", len(tr.Msgs), path, delNote),
	})
}

// handleZCodeDelete 从 ZCode 数据库中彻底删除会话。
func (s *Server) handleZCodeDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		ID      string `json:"id"`
		Confirm bool   `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析："+err.Error())
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		writeErr(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if !req.Confirm {
		writeJSON(w, map[string]any{
			"needConfirm": true,
			"note":        "这将从 ZCode 数据库中彻底删除该会话及其全部对话片段（删前会自动创建数据库备份）。是否继续？",
		})
		return
	}

	rows, err := zcode.DeleteSession(s.probe(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{
		"deleted": true,
		"rows":    rows,
		"note":    fmt.Sprintf("已成功从 ZCode 删除该会话（清理了 %d 行记录，已自动备份数据库）", rows),
	})
}

