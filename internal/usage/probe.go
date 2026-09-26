package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ---------- 一键测试：发最小消息验证通路与计费归属 ----------
//
// 用户授权的实弹测试（2026-09-26："让他消耗一点 token 去测试一下"）。
// 目的不是聊天，是回答三个问题：后端通不通、实际服务的是哪个模型、
// 扣的是哪个账号多少积分。因此：
//   - 消息最小化（system + 一句话，max_tokens 收敛）
//   - 模型选择绝不盲发：优先限时免费，其次 0 倍率，再最低倍率
//   - 前后各查一次积分包，用账单差值说话，不信界面感觉

// ChatProbeResult 是一侧的测试结果。
type ChatProbeResult struct {
	Side string `json:"side"`
	OK   bool   `json:"ok"`
	Err  string `json:"err,omitempty"`
	// Model 是响应里回显的实际服务模型（不是请求里填的那个）。
	Model string `json:"model,omitempty"`
	// Reply 是模型回复的前几十个字符，证明真的产生了对话。
	Reply string `json:"reply,omitempty"`
	// PromptTok / CompletionTok 来自响应的 usage 字段。
	PromptTok     int `json:"promptTokens,omitempty"`
	CompletionTok int `json:"completionTokens,omitempty"`
	// Credit 是响应 usage 里报的本次积分消耗（0 表示免费）。
	Credit float64 `json:"credit"`
	// BillingChanged 表示前后账单是否变化；Delta 是人话描述。
	BillingChanged bool   `json:"billingChanged"`
	Delta          string `json:"delta,omitempty"`
}

// pickProbeModel 挑测试模型：限时免费 > 0 倍率 > 最低倍率。
// 返回空串表示清单里没有任何可安全测试的模型。
func pickProbeModel(models []LiveModel) string {
	var zero, cheapest string
	cheapestRate := -1.0
	for _, m := range models {
		if m.FreeNow {
			return m.ID // 限时免费：零成本且明确，直接用
		}
		if m.Rate == 0 && zero == "" {
			zero = m.ID
		}
		if cheapest == "" || (m.Rate > 0 && m.Rate < cheapestRate) {
			if m.Rate > 0 {
				cheapest, cheapestRate = m.ID, m.Rate
			}
		}
	}
	if zero != "" {
		return zero
	}
	return cheapest
}

// sendProbeMessage 发一条最小聊天消息并解析流式响应。
// 返回：服务端回显的模型、回复文本、token 用量、报的积分消耗。
func sendProbeMessage(endpoint string, cred liveCredentials, id variant.ID, model string) (served, reply string, promptTok, completionTok int, credit float64, err error) {
	body := map[string]any{
		"model":  model,
		"stream": true, // 实测非流式被拒（code 11101）
		"messages": []map[string]string{
			// 实测第一条必须是 system（code 11128），不能只有 user
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "只回复两个字：收到"},
		},
	}
	req, err := http.NewRequest("POST", endpoint+"/v2/chat/completions",
		strings.NewReader(stringify(body)))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+cred.Token)
	req.Header.Set("X-User-Id", cred.UID)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range liveCommonHeaders(id) {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 160))
		return
	}
	// SSE 解析：data: {...} 行，[DONE] 结束。
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int     `json:"prompt_tokens"`
				CompletionTokens int     `json:"completion_tokens"`
				Credit           float64 `json:"credit"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil {
			continue
		}
		if chunk.Model != "" {
			served = chunk.Model
		}
		for _, ch := range chunk.Choices {
			reply += ch.Delta.Content
		}
		if chunk.Usage != nil {
			promptTok = chunk.Usage.PromptTokens
			completionTok = chunk.Usage.CompletionTokens
			credit = chunk.Usage.Credit
		}
	}
	err = sc.Err()
	return
}

func stringify(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// truncate 截断长文本用于错误信息。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// packagesDelta 比较前后积分包，返回人话差值；无变化返回空串。
func packagesDelta(before, after []LivePackage) string {
	if len(before) == 0 || len(after) == 0 {
		return ""
	}
	bm := map[string]LivePackage{}
	for _, p := range before {
		bm[p.Code] = p
	}
	var parts []string
	for _, p := range after {
		if b, ok := bm[p.Code]; ok && b.Used != p.Used {
			parts = append(parts, fmt.Sprintf("…%s 用量 %g→%g %s",
				p.Code[maxInt(0, len(p.Code)-6):], b.Used, p.Used, p.Unit))
		}
	}
	return strings.Join(parts, "；")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SendChatProbe 对一侧执行完整的测试流程：基线账单 → 发最小消息 →
// 复查账单。任何一步失败都如实写进结果，绝不让一侧的失败影响另一侧。
func SendChatProbe(probe *variant.Probe, id variant.ID) *ChatProbeResult {
	res := &ChatProbeResult{Side: string(id)}
	cred, err := readLiveCredentials(liveAuthFile(probe, id))
	if err != nil {
		res.Err = err.Error()
		return res
	}
	if cred.Envelope {
		res.Err = "凭据是加密信封（WorkBuddy 5.6+），无法用本地凭据发起测试；请在客户端里发一条消息后看面板归属"
		return res
	}
	endpoint := liveEndpoint[id]

	before, err := fetchLivePackages(endpoint, cred)
	if err != nil {
		before = nil // 基线拿不到也继续测，只是没有账单差值
	}

	models, _, err := fetchLiveModels(endpoint, cred, id)
	if err != nil {
		res.Err = "拉模型清单失败：" + err.Error()
		return res
	}
	model := pickProbeModel(models)
	if model == "" {
		res.Err = "清单里没有可安全测试的模型（避免盲选计费模型），未发送"
		return res
	}

	served, reply, ptok, ctok, credit, err := sendProbeMessage(endpoint, cred, id, model)
	if err != nil {
		res.Err = err.Error()
		res.Model = model
		return res
	}
	res.OK = true
	res.Model = served
	res.Reply = truncate(strings.TrimSpace(reply), 60)
	res.PromptTok = ptok
	res.CompletionTok = ctok
	res.Credit = credit

	if after, err := fetchLivePackages(endpoint, cred); err == nil {
		res.Delta = packagesDelta(before, after)
		res.BillingChanged = res.Delta != ""
	}
	return res
}
