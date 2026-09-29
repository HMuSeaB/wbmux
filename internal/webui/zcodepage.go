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
	writeJSON(w, map[string]any{
		"path":  path,
		"title": tr.Title,
		"msgs":  len(tr.Msgs),
		"note":  fmt.Sprintf("已导出 %d 条消息到 %s", len(tr.Msgs), path),
	})
}
