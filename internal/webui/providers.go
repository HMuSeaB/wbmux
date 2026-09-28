package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
)

// ---------- 自备提供方（BYOK）----------
//
// 一张卡片 = 一个 OpenAI 兼容端点 + 它的 key + 要注入的模型清单。
// 保存后立刻重新注入，国内客户端的模型选择器里就会出现「<卡片名> · <模型>」。
//
// 安全上两条硬规矩：
//   - key **永不回给界面**，只回最后 4 位（界面留空就表示"不改动"）；
//   - 注入到客户端目录里的条目持有的是**本机代理的令牌**，不是上游 key，
//     所以上游 key 不会落到客户端的数据目录里。

// providerView 是回给界面的卡片形态。
type providerView struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	BaseURL          string   `json:"baseURL"`
	KeyTail          string   `json:"keyTail"` // 只回最后 4 位
	HasKey           bool     `json:"hasKey"`
	Models           []string `json:"models"`
	MaxInputTokens   int      `json:"maxInputTokens"`
	MaxOutputTokens  int      `json:"maxOutputTokens"`
	SupportsToolCall bool     `json:"supportsToolCall"`
	SupportsImages   bool     `json:"supportsImages"`
}

// toView 转成界面形态，顺带把 key 截成尾巴。
func toView(p config.Provider) providerView {
	return providerView{
		ID:               p.ID,
		Name:             p.Name,
		BaseURL:          p.BaseURL,
		KeyTail:          keyTail(p.APIKey),
		HasKey:           p.APIKey != "",
		Models:           p.Models,
		MaxInputTokens:   p.MaxInputTokens,
		MaxOutputTokens:  p.MaxOutputTokens,
		SupportsToolCall: p.ToolsSupported(),
		SupportsImages:   p.ImagesSupported(),
	}
}

// keyTail 只保留最后 4 位。短 key 一律不给尾巴——那样等于把 key 给出去了。
func keyTail(k string) string {
	if len(k) <= 4 {
		return ""
	}
	return k[len(k)-4:]
}

// providerRequest 是界面提交的卡片内容。
type providerRequest struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	BaseURL          string   `json:"baseURL"`
	APIKey           string   `json:"apiKey"` // 留空 = 不改动已有 key
	Models           []string `json:"models"`
	MaxInputTokens   int      `json:"maxInputTokens"`
	MaxOutputTokens  int      `json:"maxOutputTokens"`
	SupportsToolCall *bool    `json:"supportsToolCall"`
	SupportsImages   *bool    `json:"supportsImages"`
	Remove           bool     `json:"remove"`
}

// handleProviders 读取 / 保存自备提供方。
//
//	GET  /api/providers  → 卡片列表（key 只有尾巴）
//	POST /api/providers  → 新增或修改一张卡（remove=true 则删除），保存后重新注入
func (s *Server) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := config.Load()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		views := make([]providerView, 0, len(cfg.Providers))
		for _, p := range cfg.Providers {
			views = append(views, toView(p))
		}
		writeJSON(w, map[string]any{"providers": views})
	case http.MethodPost:
		s.saveProvider(w, r)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET / POST")
	}
}

func (s *Server) saveProvider(w http.ResponseWriter, r *http.Request) {
	var req providerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体无法解析："+err.Error())
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if req.Remove {
		if strings.TrimSpace(req.ID) == "" {
			writeErr(w, http.StatusBadRequest, "缺少 id，不知道要删哪一张")
			return
		}
		list, found := config.RemoveProvider(cfg.Providers, req.ID)
		if !found {
			writeErr(w, http.StatusNotFound, "没有这张卡片（可能已经被删掉了）")
			return
		}
		cfg.Providers = list
	} else {
		p := config.Provider{
			ID:               strings.TrimSpace(req.ID),
			Name:             strings.TrimSpace(req.Name),
			BaseURL:          strings.TrimSpace(req.BaseURL),
			APIKey:           strings.TrimSpace(req.APIKey),
			Models:           cleanModels(req.Models),
			MaxInputTokens:   req.MaxInputTokens,
			MaxOutputTokens:  req.MaxOutputTokens,
			SupportsToolCall: req.SupportsToolCall,
			SupportsImages:   req.SupportsImages,
		}
		if p.ID == "" {
			p.ID = config.NextProviderID(cfg.Providers)
		}
		// 留空表示"沿用原来的 key"：界面只显示尾巴，逼用户重新粘贴整串
		// 既没必要，也容易让人误以为 key 丢了。
		if p.APIKey == "" {
			if old := config.FindProvider(cfg.Providers, p.ID); old != nil {
				p.APIKey = old.APIKey
			}
		}
		if err := p.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg.Providers = config.UpsertProvider(cfg.Providers, p)
	}

	if err := config.Save(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 保存后立刻重新注入：卡片改完就该能在客户端的模型选择器里看到
	// （客户端仍需重启一次才会重读 models.json，这一点在 note 里说清）。
	added, note, syncErr := s.syncInto()
	if syncErr != nil {
		s.note(ProxyLogEntry{Level: "bad", Text: "保存提供方后重新注入失败：" + compactError(syncErr.Error())})
		writeErr(w, http.StatusInternalServerError, "已保存，但重新注入失败："+syncErr.Error())
		return
	}
	views := make([]providerView, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		views = append(views, toView(p))
	}
	writeJSON(w, map[string]any{
		"providers": views,
		"added":     added,
		"note":      note,
	})
}

// cleanModels 去掉空白项与重复项，保持用户填的顺序。
func cleanModels(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}
