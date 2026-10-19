package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/autoupdate"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 禁止客户端自动更新（开关与路径自适应） ----------
//
// 与 internal/update（wbmux 自己怎么升级）是两件不同的事：
//   - 这里管的是客户端（WorkBuddy）的启动静默更新；
//   - internal/update 管的是 wbmux 自己的二进制在线升级。
//
// 原理见 internal/autoupdate：客户端 `early-silent-startup-update` 读的是
// `<数据目录>/app/app-config.json` 的 userSetting，那是优先级最高的一层。

// ClientUpdateTarget 描述检测到的一个客户端 app-config.json 目标。
type ClientUpdateTarget struct {
	Side    string `json:"side"`    // "cn" | "intl" | "custom"
	Label   string `json:"label"`   // 显示名，如 "国内版数据目录" / "自定义指定路径"
	DataDir string `json:"dataDir"` // 数据根目录
	Path    string `json:"path"`    // app-config.json 物理绝对路径
	Dir     string `json:"dir"`     // 所在文件夹绝对路径（供打开目录用）
	Exists  bool   `json:"exists"`  // 该文件是否已实际存在
	Status  string `json:"status"`  // "disabled"(已关闭自动更新/受保护) | "official"(官方默认) | "missing"(尚未创建)
}

// ClientUpdateResult 记录策略应用后的汇总状态。
type ClientUpdateResult struct {
	Enabled        bool                 `json:"enabled"`
	CustomPath     string               `json:"customPath"`
	Targets        []ClientUpdateTarget `json:"targets"`
	ModifiedCount  int                  `json:"modifiedCount"`
	ProtectedCount int                  `json:"protectedCount"`
	Note           string               `json:"note"`
}

// handleClientUpdatePolicy 读/写"禁止客户端自动更新"开关与自定义路径。
func (s *Server) handleClientUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Enabled    *bool   `json:"enabled"`              // 可选，允许仅更新路径
			CustomPath *string `json:"customPath,omitempty"` // 可选，更新自定义路径
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体无法解析")
			return
		}

		cfg, err := config.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}

		on := config.DisableAutoUpdateOn(cfg)
		if req.Enabled != nil {
			on = *req.Enabled
		}

		customPath := cfg.CustomClientAppConfigPath
		if req.CustomPath != nil {
			customPath = strings.TrimSpace(*req.CustomPath)
		}

		if err := s.updateConfig(func(c *config.Config) error {
			if req.Enabled != nil {
				c.DisableAutoUpdate = &on
			}
			if req.CustomPath != nil {
				c.CustomClientAppConfigPath = customPath
			}
			return nil
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
			return
		}

		result := s.applyClientUpdatePolicy(on, customPath)
		s.logf("客户端自动更新策略：已%s（目标: %d 处，改动: %d 处）", map[bool]string{true: "开启禁止", false: "恢复官方默认"}[on], len(result.Targets), result.ModifiedCount)
		writeJSON(w, result)
		return
	}

	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET 或 POST")
		return
	}

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	on := config.DisableAutoUpdateOn(cfg)
	targets := s.resolveClientUpdateTargets(cfg.CustomClientAppConfigPath)
	note := ""
	if len(targets) > 0 {
		var paths []string
		for _, t := range targets {
			paths = append(paths, t.Path)
		}
		if on {
			note = fmt.Sprintf("已防护 %d 处客户端配置（启动静默更新已关闭）：%s", len(targets), strings.Join(paths, "，"))
		} else {
			note = fmt.Sprintf("已检测到 %d 处客户端，当前保持官方默认更新策略：%s", len(targets), strings.Join(paths, "，"))
		}
	} else {
		note = "未检测到本地客户端数据目录。若您安装在非默认盘或使用了便携目录，可在下方指定自定义路径。"
	}

	writeJSON(w, ClientUpdateResult{
		Enabled:    on,
		CustomPath: cfg.CustomClientAppConfigPath,
		Targets:    targets,
		Note:       note,
	})
}

// resolveClientUpdateTargets 搜集并解析所有需要管理的 app-config.json 物理目标。
func (s *Server) resolveClientUpdateTargets(customPath string) []ClientUpdateTarget {
	var targets []ClientUpdateTarget
	seen := make(map[string]bool)

	addTarget := func(side, label, dataDir, targetFile string) {
		clean := filepath.Clean(targetFile)
		lower := strings.ToLower(clean)
		if seen[lower] {
			return
		}
		seen[lower] = true

		exists := false
		status := "missing"
		if raw, err := os.ReadFile(clean); err == nil {
			exists = true
			var doc map[string]json.RawMessage
			if json.Unmarshal(raw, &doc) == nil {
				if v, ok := doc[autoupdate.Key]; ok {
					var cur bool
					if json.Unmarshal(v, &cur) == nil && !cur {
						status = "disabled"
					} else {
						status = "official"
					}
				} else {
					status = "official"
				}
			} else {
				status = "official"
			}
		}

		targets = append(targets, ClientUpdateTarget{
			Side:    side,
			Label:   label,
			DataDir: dataDir,
			Path:    clean,
			Dir:     filepath.Dir(clean),
			Exists:  exists,
			Status:  status,
		})
	}

	// 1. 自定义路径优先（如果传入或配置了）
	customPath = strings.TrimSpace(customPath)
	if customPath != "" {
		absPath, err := filepath.Abs(customPath)
		if err == nil {
			fi, statErr := os.Stat(absPath)
			if statErr == nil && !fi.IsDir() {
				// 用户直接选定了具体 json 文件
				addTarget("custom", "自定义指定配置", filepath.Dir(absPath), absPath)
			} else {
				// 用户给的是目录
				pApp := filepath.Join(absPath, "app", "app-config.json")
				pRoot := filepath.Join(absPath, "app-config.json")
				if _, err := os.Stat(pApp); err == nil {
					addTarget("custom", "自定义数据目录", absPath, pApp)
				} else if _, err := os.Stat(pRoot); err == nil {
					addTarget("custom", "自定义配置目录", absPath, pRoot)
				} else {
					addTarget("custom", "自定义数据目录", absPath, pApp)
				}
			}
		}
	}

	// 2. 自动探测国内版与国际版
	sides := []struct {
		id    variant.ID
		side  string
		label string
	}{
		{variant.CN, "cn", "国内版数据目录"},
		{variant.Intl, "intl", "国际版数据目录"},
	}

	for _, item := range sides {
		dataDir := s.probe().DataDir(item.id)
		if dataDir == "" {
			continue
		}
		appConfigPath := filepath.Join(dataDir, "app", "app-config.json")
		if _, err := os.Stat(dataDir); err == nil {
			addTarget(item.side, item.label, dataDir, appConfigPath)
		} else if _, err := os.Stat(appConfigPath); err == nil {
			addTarget(item.side, item.label, dataDir, appConfigPath)
		}
	}

	// 3. 兜底策略：若宿主安装定位到了数据目录
	if len(targets) == 0 {
		for _, id := range []variant.ID{variant.CN, variant.Intl} {
			inst := s.probe().Detect(id, "")
			if inst.Found && inst.DataDir != "" {
				appConfigPath := filepath.Join(inst.DataDir, "app", "app-config.json")
				addTarget(string(id), string(id)+"宿主探测目录", inst.DataDir, appConfigPath)
			}
		}
	}

	return targets
}

// applyClientUpdatePolicy 应用策略到所有目标客户端配置。
func (s *Server) applyClientUpdatePolicy(on bool, customPath string) ClientUpdateResult {
	targets := s.resolveClientUpdateTargets(customPath)
	res := ClientUpdateResult{
		Enabled:    on,
		CustomPath: customPath,
		Targets:    targets,
	}

	if len(targets) == 0 {
		if on {
			res.Note = "未检测到本地客户端数据目录。若您安装在非默认盘或使用了便携目录，可在下方指定自定义路径。"
		} else {
			res.Note = "未检测到本地客户端数据目录，当前无需修改配置。"
		}
		return res
	}

	var firstErr error
	for i, t := range targets {
		if on {
			_ = os.MkdirAll(filepath.Dir(t.Path), 0o755)
			changed, err := autoupdate.Ensure(t.Path)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if changed {
				res.ModifiedCount++
			}
			res.ProtectedCount++
			targets[i].Exists = true
			targets[i].Status = "disabled"
		} else {
			if _, err := os.Stat(t.Path); err != nil {
				// 文件原本就不存在，不需要动
				continue
			}
			changed, err := autoupdate.Release(t.Path)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if changed {
				res.ModifiedCount++
			}
			targets[i].Status = "official"
		}
	}
	res.Targets = targets

	if firstErr != nil {
		res.Note = "部分客户端配置写入失败: " + firstErr.Error()
		return res
	}

	var paths []string
	for _, t := range targets {
		paths = append(paths, t.Path)
	}
	pathSummary := strings.Join(paths, "，")

	if on {
		if res.ModifiedCount > 0 {
			res.Note = fmt.Sprintf("已成功开启保护并写入 %d 处客户端配置（已压制启动静默更新）：%s", res.ModifiedCount, pathSummary)
		} else {
			res.Note = fmt.Sprintf("配置已生效，已防护 %d 处客户端（启动静默更新已处于关闭状态）：%s", len(targets), pathSummary)
		}
	} else {
		if res.ModifiedCount > 0 {
			res.Note = fmt.Sprintf("已成功恢复 %d 处客户端官方原生更新策略：%s", res.ModifiedCount, pathSummary)
		} else {
			res.Note = fmt.Sprintf("已恢复客户端官方原生更新策略：%s", pathSummary)
		}
	}

	return res
}
