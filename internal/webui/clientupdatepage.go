package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/HMuSeaB/wbmux/internal/autoupdate"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 禁止客户端自动更新（开关） ----------
//
// 与上面那个"wbmux 自己怎么升级"是两件不同的事，别混：
//   - 这里管的是**客户端**（WorkBuddy）的启动静默更新；
//   - internal/update 管的是 wbmux 自己的版本。
//
// 底层落在 internal/autoupdate，原理见那个包的注释：客户端
// `early-silent-startup-update` 读的是 `<数据目录>/app/app-config.json` 的
// userSetting，那是优先级最高的一层。

// handleClientUpdatePolicy 读/写"禁止客户端自动更新"这个开关。
//
// 写法与自动签到那个开关一致（handleAutoCheckinToggle）：写的是 wbmux
// 自己的配置，不碰账号，所以不需要二次确认。
//
// 但有一步额外动作：**写完配置立刻落到客户端文件上**。不能只存配置等下次
// 启动——用户点这个开关的动机往往是"客户端又要偷偷升级了"，那正是此刻要
// 拦住的事；等下次启动才生效就晚了。
func (s *Server) handleClientUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Enabled bool `json:"enabled"` // true = 禁止客户端自动更新
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "请求体无法解析")
			return
		}
		on := req.Enabled
		if err := s.updateConfig(func(cfg *config.Config) error {
			cfg.DisableAutoUpdate = &on
			return nil
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
			return
		}
		applied, err := s.applyClientUpdatePolicy(on)
		note := ""
		if err != nil {
			note = "配置已保存，但写客户端设置失败：" + err.Error()
		} else if applied == 0 {
			if on {
				note = "配置已保存。未检测到本地客户端目录，后续启动客户端时若开启此项将按需写入。"
			} else {
				note = "配置已保存。当前客户端保持官方默认更新策略。"
			}
		}
		s.logf("客户端自动更新策略：已%s（改动了 %d 侧）", map[bool]string{true: "开启禁止", false: "恢复官方默认"}[on], applied)
		writeJSON(w, map[string]any{"enabled": on, "applied": applied, "note": note})
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
	writeJSON(w, map[string]any{"enabled": config.DisableAutoUpdateOn(cfg)})
}

// applyClientUpdatePolicy 按 on 把策略写到各侧客户端的 app-config.json。
//
// 返回真正改动过的侧数（不含"本来就是对的"）。
//
// 开启禁止 (on=true) 时写入 "startupForceAutoUpdate": false。
// 关闭禁止 (on=false) 时是**把键删掉**（回到"客户端官方默认决定"），不是写 true ——
// 写 true 等于替用户做了"允许自动更新"这个决定，而用户想表达的只是
// "按官方原生策略处理"。这个区别在客户端升级策略变化时会体现出来。
func (s *Server) applyClientUpdatePolicy(on bool) (int, error) {
	applied := 0
	var firstErr error
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		dataDir := s.probe().DataDir(id)
		// 只有该侧客户端的数据根目录存在（说明已安装并运行过该版本）才处理
		if _, err := os.Stat(dataDir); err != nil {
			continue
		}
		p := filepath.Join(dataDir, "app", "app-config.json")
		if on {
			// 用户主动开启禁止，如果 app/ 目录尚未创建则预埋
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
		} else {
			// 关闭禁止：若 app/ 目录都不存在，无需动
			if _, err := os.Stat(filepath.Dir(p)); err != nil {
				continue
			}
		}
		var changed bool
		var err error
		if on {
			changed, err = autoupdate.Ensure(p)
		} else {
			changed, err = autoupdate.Release(p)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if changed {
			applied++
		}
	}
	return applied, firstErr
}
