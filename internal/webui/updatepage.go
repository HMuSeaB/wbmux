package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/shortcut"
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
		AssetURL  string `json:"assetURL"`
		AssetName string `json:"assetName"`
		SumsURL   string `json:"sumsURL"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	msg, err := update.Apply(req.AssetURL, req.AssetName, req.SumsURL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"message": msg})
}

// handleShortcut 在桌面创建/覆盖 wbmux.lnk（internal/shortcut）。
// "装好后想要个双击入口"是装完/升级完的自然下一步，收进界面少一次翻目录。
func (s *Server) handleShortcut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	path, err := shortcut.Create()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{"path": path})
}

// handleUpdateTargetInfo 返回当前主程序路径与临时下载缓存目录，
// 供升级弹窗向用户明确呈现"会下载到哪里、会替换哪个文件"。
func (s *Server) handleUpdateTargetInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	targetExe, tempDir, err := update.TargetInfo()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]string{
		"targetExe": targetExe,
		"tempDir":   tempDir,
	})
}

// handleUpdateApplyStream 通过 SSE 协议流式推送下载解压与替换进度。
// 客户端可通过 EventSource 实时获取字节数、瞬时速率、百分比与当前处理阶段。
func (s *Server) handleUpdateApplyStream(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "当前连接不支持事件流")
		return
	}

	assetURL := r.URL.Query().Get("assetURL")
	assetName := r.URL.Query().Get("assetName")
	sumsURL := r.URL.Query().Get("sumsURL")

	if assetURL == "" || assetName == "" {
		writeErr(w, http.StatusBadRequest, "缺少必要参数 assetURL 或 assetName")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// 建立连接后立刻推送初始事件，让前端感知就绪
	fmt.Fprintf(w, ": stream-ready\n\n")
	fl.Flush()

	onProgress := func(ev update.ProgressEvent) {
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
		fl.Flush()
	}

	msg, err := update.ApplyWithProgress(assetURL, assetName, sumsURL, onProgress)
	if err != nil {
		errEv, _ := json.Marshal(map[string]any{
			"phase": "error",
			"error": err.Error(),
		})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errEv)
		fl.Flush()
		return
	}

	doneEv, _ := json.Marshal(map[string]any{
		"phase":   "done",
		"percent": 100,
		"message": msg,
	})
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneEv)
	fl.Flush()
}

// handleOpenFolder 打开指定的文件或目录所在的位置（调用操作系统资源管理器）。
// 仅允许打开真实存在的路径。
func (s *Server) handleOpenFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	targetPath := strings.TrimSpace(req.Path)
	if targetPath == "" {
		writeErr(w, http.StatusBadRequest, "路径不能为空")
		return
	}
	cleanPath := filepath.Clean(targetPath)
	fi, err := os.Stat(cleanPath)
	if err != nil {
		writeErr(w, http.StatusNotFound, "指定路径不存在: "+err.Error())
		return
	}

	if err := openInExplorer(cleanPath, fi.IsDir()); err != nil {
		writeErr(w, http.StatusInternalServerError, "打开失败: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": cleanPath})
}

// openInExplorer 调用系统文件资源管理器打开目录或高亮选中文件。
func openInExplorer(path string, isDir bool) error {
	switch runtime.GOOS {
	case "windows":
		var cmd *exec.Cmd
		if isDir {
			cmd = exec.Command("explorer.exe", path)
		} else {
			cmd = exec.Command("explorer.exe", "/select,"+path)
		}
		return cmd.Start()
	case "darwin":
		var cmd *exec.Cmd
		if isDir {
			cmd = exec.Command("open", path)
		} else {
			cmd = exec.Command("open", "-R", path)
		}
		return cmd.Start()
	default:
		dir := path
		if !isDir {
			dir = filepath.Dir(path)
		}
		return exec.Command("xdg-open", dir).Start()
	}
}

// browserOpener 负责调用系统外部浏览器打开 URL，支持单测注入桩函数。
var browserOpener = openBrowserURL

// handleOpenURL 在系统默认浏览器中安全打开指定 Web URL（仅限 http:// 或 https://）。
func (s *Server) handleOpenURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	targetURL := strings.TrimSpace(req.URL)
	if targetURL == "" {
		writeErr(w, http.StatusBadRequest, "URL 不能为空")
		return
	}
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		writeErr(w, http.StatusBadRequest, "仅支持打开 http:// 或 https:// 协议的 Web 网页")
		return
	}

	if err := browserOpener(targetURL); err != nil {
		writeErr(w, http.StatusInternalServerError, "打开默认浏览器失败: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "url": targetURL})
}

// openBrowserURL 调用操作系统默认浏览器打开指定的 URL。
func openBrowserURL(rawURL string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start()
	case "darwin":
		return exec.Command("open", rawURL).Start()
	default:
		return exec.Command("xdg-open", rawURL).Start()
	}
}


