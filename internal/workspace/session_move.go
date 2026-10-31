package workspace

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// SessionBrief 是会话简要信息，用于工作区内展现与散落任务归纳。
type SessionBrief struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CustomTitle  string `json:"customTitle,omitempty"`
	DisplayTitle string `json:"displayTitle"`
	Cwd          string `json:"cwd"`
	Status       string `json:"status"`
	CreatedAt    int64  `json:"createdAt"`
	UpdatedAt    int64  `json:"updatedAt"`
	TimeStr      string `json:"timeStr"`
	IsLoose      bool   `json:"isLoose"`
	HasBody      bool   `json:"hasBody"`
}

// TargetSpaceOption 提供给前端下拉点选的正式目标工作区。
type TargetSpaceOption struct {
	Name     string `json:"name"`     // 给人看的空间名，如 "SlateTerm"
	Path     string `json:"path"`     // 绝对路径，如 "D:\4rchive\Code\SlateTerm"
	Sessions int    `json:"sessions"` // 当前已有会话数
	LastOpen string `json:"lastOpen,omitempty"`
}

// MoveSessionResult 记录单次会话转移的执行战报。
type MoveSessionResult struct {
	SessionID     string `json:"sessionId"`
	SessionTitle  string `json:"sessionTitle"`
	OldCwd        string `json:"oldCwd"`
	NewCwd        string `json:"newCwd"`
	OldSlugDir    string `json:"oldSlugDir"`
	NewSlugDir    string `json:"newSlugDir"`
	FilesMoved    int    `json:"filesMoved"`
	BytesMoved    int64  `json:"bytesMoved"`
	OldDirCleaned bool   `json:"oldDirCleaned"`
}

// IsLoosePath 判断一个路径是否为客户端默认创建的临时时间戳/日常办公目录。
func IsLoosePath(cwd string) bool {
	norm := strings.ToLower(Unify(cwd))
	if norm == "" {
		return true
	}
	// 匹配 WorkBuddy\202x-xx-xx-* 格式的临时时间戳目录
	if strings.Contains(norm, `\workbuddy\202`) || strings.Contains(norm, `/workbuddy/202`) {
		return true
	}
	// 匹配 WorkBuddy AI\202x-*
	if strings.Contains(norm, `\workbuddy ai\202`) || strings.Contains(norm, `/workbuddy ai/202`) {
		return true
	}
	return false
}

// ClientSlug 复刻官方 WorkBuddy 真实的 slug 规则（折叠连续路径分隔符与冒号）。
// 例：D:\4rchive\Code\SlateTerm -> d-4rchive-Code-SlateTerm
func ClientSlug(p string) string {
	u := Unify(p)
	var b strings.Builder
	lastDash := false
	for _, r := range u {
		if r == ':' || r == '\\' || r == '/' {
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		} else {
			b.WriteRune(r)
			lastDash = false
		}
	}
	return b.String()
}

// ListLooseSessions 找出所有未指定正式项目空间、散落在临时时间戳目录里的会话任务。
func ListLooseSessions(p *variant.Probe, id variant.ID) ([]SessionBrief, error) {
	rows, err := migrate.Query(p, id, `
		SELECT id, cwd, title, custom_title, status, created_at, updated_at, deleted_at, is_playground
		FROM sessions
		WHERE deleted_at IS NULL OR deleted_at = 0
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("读取会话列表失败: %w", err)
	}

	wsList, _ := readWorkspaces(p, id)
	knownWs := map[string]bool{}
	for _, ws := range wsList {
		knownWs[strings.ToLower(Unify(ws.Path))] = true
	}

	dataDir := p.DataDir(id)
	bodies := sessionBodyIndex(p, id)

	var out []SessionBrief
	for _, r := range rows {
		sid := str(r["id"])
		cwd := str(r["cwd"])
		normCwd := strings.ToLower(Unify(cwd))

		isLoose := toInt(r["is_playground"]) == 1 || IsLoosePath(cwd) || !knownWs[normCwd]
		if !isLoose {
			continue
		}

		title := str(r["title"])
		custom := str(r["custom_title"])
		display := custom
		if display == "" {
			display = title
		}
		if display == "" {
			display = "未命名任务 (" + sid[:8] + ")"
		}

		upMs := int64(toInt(r["updated_at"]))
		crMs := int64(toInt(r["created_at"]))
		timeStr := ""
		if upMs > 0 {
			timeStr = time.UnixMilli(upMs).Format("2006-01-02 15:04")
		} else if crMs > 0 {
			timeStr = time.UnixMilli(crMs).Format("2006-01-02 15:04")
		}

		_, hasBody := bodies[sid]
		if !hasBody && dataDir != "" {
			if _, files, _ := findSessionFiles(dataDir, sid); len(files) > 0 {
				hasBody = true
			}
		}

		out = append(out, SessionBrief{
			ID:           sid,
			Title:        title,
			CustomTitle:  custom,
			DisplayTitle: display,
			Cwd:          cwd,
			Status:       str(r["status"]),
			CreatedAt:    crMs,
			UpdatedAt:    upMs,
			TimeStr:      timeStr,
			IsLoose:      true,
			HasBody:      hasBody,
		})
	}
	return out, nil
}

// GetTargetSpaceOptions 获取当前所有正式工作区列表，格式化为人性化下拉选项。
func GetTargetSpaceOptions(p *variant.Probe, id variant.ID) ([]TargetSpaceOption, error) {
	ov, err := Inspect(p, id)
	if err != nil {
		return nil, err
	}
	var out []TargetSpaceOption
	for _, sp := range ov.Spaces {
		if !sp.InList && sp.Sessions == 0 {
			continue
		}
		if IsLoosePath(sp.Path) {
			continue
		}

		name := filepath.Base(sp.Path)
		if name == "" || name == "." || name == `\` {
			name = sp.Path
		}
		out = append(out, TargetSpaceOption{
			Name:     name,
			Path:     sp.Path,
			Sessions: sp.Sessions,
			LastOpen: sp.LastOpen,
		})
	}
	return out, nil
}

// FindOrCreateProjectSlugDir 定位目标工作区在 projects/ 下对应的物理目录（优先复用已有）。
func FindOrCreateProjectSlugDir(dataDir, targetCwd string) (string, error) {
	projectsDir := filepath.Join(dataDir, "projects")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		return "", err
	}

	targetSlug := ClientSlug(targetCwd)
	targetLegacySlug := Slug(targetCwd)

	entries, err := os.ReadDir(projectsDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.EqualFold(name, targetSlug) || strings.EqualFold(name, targetLegacySlug) {
				return filepath.Join(projectsDir, name), nil
			}
		}
	}

	dst := filepath.Join(projectsDir, targetSlug)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", err
	}
	return dst, nil
}

// findSessionFiles 定位该会话在 projects/ 下所有的物理文件与目录。
func findSessionFiles(dataDir, sessionID string) (srcDir string, files []string, err error) {
	projectsDir := filepath.Join(dataDir, "projects")
	if !dirExists(projectsDir) {
		return "", nil, nil
	}

	var foundDir string
	_ = filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() && filepath.Base(path) == sessionID+".jsonl" {
			foundDir = filepath.Dir(path)
			return filepath.SkipAll
		}
		return nil
	})

	if foundDir == "" {
		_ = filepath.Walk(projectsDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if info.IsDir() && filepath.Base(path) == sessionID {
				foundDir = filepath.Dir(path)
				return filepath.SkipAll
			}
			return nil
		})
	}

	if foundDir == "" {
		return "", nil, nil
	}

	entries, err := os.ReadDir(foundDir)
	if err != nil {
		return foundDir, nil, err
	}

	var matched []string
	for _, e := range entries {
		name := e.Name()
		if name == sessionID || strings.HasPrefix(name, sessionID+".") || strings.HasPrefix(name, sessionID+"-") {
			matched = append(matched, filepath.Join(foundDir, name))
		}
	}
	return foundDir, matched, nil
}

// moveOrCopyFileOrDir 安全地跨卷搬移单个文件或整个目录。
func moveOrCopyFileOrDir(src, dst string) (int64, error) {
	fi, err := os.Stat(src)
	if err != nil {
		return 0, err
	}

	// 优先尝试原子重命名（同卷瞬间完成）
	if err := os.Rename(src, dst); err == nil {
		if fi.IsDir() {
			return 0, nil
		}
		return fi.Size(), nil
	}

	// 跨卷或被占用回退为 Copy + Remove
	if fi.IsDir() {
		if err := copyTree(src, dst); err != nil {
			return 0, err
		}
		_ = os.RemoveAll(src)
		return 0, nil
	}

	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode())
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.Copy(out, in)
	if err != nil {
		return 0, err
	}
	_ = in.Close()
	_ = out.Close()
	_ = os.Remove(src)
	return n, nil
}

// CanonicalizeTargetCwd 对齐目标工作区的确切大小写与规范形式。
// 优先对齐已有的 workspaces 表记录大小写，保留 Windows 盘符大写（如 D:\...），
// 避免因 SQLite 的 BINARY 字节比对规则导致大小写不一致而在客户端中缺失会话。
func CanonicalizeTargetCwd(p *variant.Probe, id variant.ID, targetCwd string) string {
	s := strings.TrimSpace(targetCwd)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "/", `\`)

	if p != nil && id != "" {
		// 1. 优先采用 workspaces 表中已有的精确大小写
		wsList, _ := readWorkspaces(p, id)
		for _, ws := range wsList {
			if SamePath(ws.Path, s) {
				return ws.Path
			}
		}

		// 2. 其次采用 sessions 表中已有会话的精确大小写
		rows, _ := migrate.Query(p, id, `SELECT cwd FROM sessions WHERE deleted_at IS NULL GROUP BY cwd`)
		for _, r := range rows {
			c := str(r["cwd"])
			if SamePath(c, s) {
				return c
			}
		}
	}

	// 3. 否则盘符首字母统一大写（如 D:\...）
	if len(s) >= 2 && s[1] == ':' {
		s = strings.ToUpper(s[:1]) + s[1:]
	}
	s = strings.TrimRight(s, `\`)
	if len(s) == 2 && s[1] == ':' {
		s += `\`
	}
	return s
}

// MoveSession 将指定的会话安全地从当前工作区转移归入目标工作区。
// 必须在客户端退出状态下执行！
func MoveSession(p *variant.Probe, id variant.ID, sessionID, targetCwd string) (*MoveSessionResult, error) {
	if err := EnsureStopped(id); err != nil {
		return nil, err
	}
	sessionID = strings.TrimSpace(sessionID)
	targetCwd = CanonicalizeTargetCwd(p, id, targetCwd)
	if sessionID == "" {
		return nil, fmt.Errorf("sessionID 不能为空")
	}
	if targetCwd == "" {
		return nil, fmt.Errorf("目标工作区路径不能为空")
	}

	// 1. 读取原会话记录
	rows, err := migrate.Query(p, id, `
		SELECT id, cwd, title, custom_title
		FROM sessions
		WHERE id = ?
	`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("查询会话记录失败: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("未找到 ID 为 %s 的会话", sessionID)
	}

	oldCwd := str(rows[0]["cwd"])
	title := str(rows[0]["custom_title"])
	if title == "" {
		title = str(rows[0]["title"])
	}
	if title == "" {
		title = sessionID
	}

	if oldCwd == targetCwd && toInt(rows[0]["is_playground"]) == 0 {
		return nil, fmt.Errorf("该会话已归属于目标工作区（%s），无需移动", targetCwd)
	}

	// 2. 自动备份数据库
	if _, err := backupDB(p, id); err != nil {
		return nil, fmt.Errorf("自动备份数据库失败: %w", err)
	}

	dataDir := p.DataDir(id)
	res := &MoveSessionResult{
		SessionID:    sessionID,
		SessionTitle: title,
		OldCwd:       oldCwd,
		NewCwd:       targetCwd,
	}

	// 3. 搬移 projects/ 下的会话正文文件
	srcDir, matchedFiles, err := findSessionFiles(dataDir, sessionID)
	if err != nil {
		return nil, fmt.Errorf("扫描会话正文文件失败: %w", err)
	}

	if srcDir != "" && len(matchedFiles) > 0 {
		dstDir, err := FindOrCreateProjectSlugDir(dataDir, targetCwd)
		if err != nil {
			return nil, fmt.Errorf("定位目标工作区目录失败: %w", err)
		}
		res.OldSlugDir = srcDir
		res.NewSlugDir = dstDir

		for _, srcPath := range matchedFiles {
			baseName := filepath.Base(srcPath)
			dstPath := filepath.Join(dstDir, baseName)
			n, err := moveOrCopyFileOrDir(srcPath, dstPath)
			if err != nil {
				return nil, fmt.Errorf("搬移会话文件失败 %s -> %s: %w", srcPath, dstPath, err)
			}
			res.FilesMoved++
			res.BytesMoved += n
		}
	}

	// 4. 更新 SQLite 数据库中的会话 cwd 与 is_playground
	// 关键：WorkBuddy 客户端区分顶部的「任务」与底部的「空间」，依据就是 is_playground！
	// 演练场/散落任务 is_playground = 1；归入具体工作区后必须置为 0，才会脱离「任务」进入「空间」。
	var stmts []migrate.Statement
	stmts = append(stmts, migrate.Statement{
		SQL:    `UPDATE sessions SET cwd = ?, is_playground = 0 WHERE id = ?`,
		Params: []any{targetCwd, sessionID},
	})

	if _, err := migrate.Update(p, id, []string{"sessions"}, stmts); err != nil {
		return nil, fmt.Errorf("更新数据库会话归属失败: %w", err)
	}

	// 5. 检查原临时目录是否已空，若已空且为临时目录则清理文件系统中的空目录
	if IsLoosePath(oldCwd) {
		if srcDir != "" {
			ents, _ := os.ReadDir(srcDir)
			if len(ents) == 0 {
				_ = os.Remove(srcDir)
				res.OldDirCleaned = true
			}
		}
		if dirExists(oldCwd) {
			ents, _ := os.ReadDir(oldCwd)
			if len(ents) == 0 {
				_ = os.Remove(oldCwd)
			}
		}
	}

	return res, nil
}
