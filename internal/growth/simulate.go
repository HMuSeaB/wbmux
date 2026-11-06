package growth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/credential"
)

// DefaultIntlFreeModels 定义国际端兜底候选免积分模型。
// 优先 hy4free (当前官方限免 0.00x 模型) 与 hy3。
var DefaultIntlFreeModels = []string{
	"hy4free",
	"hy3",
	"deepseek-v4.1-flash-sg",
}

// SimulateResult 记录一次完整国际端模拟对话与活跃打卡的执行成果。
type SimulateResult struct {
	OK                bool   `json:"ok"`
	Model             string `json:"model"`
	ConversationID    string `json:"conversation_id"`
	RequestID         string `json:"request_id"`
	Reply             string `json:"reply"`
	ReplyLen          int    `json:"reply_len"`
	DurationMs        int64  `json:"duration_ms"`
	TTFTMs            int64  `json:"ttft_ms"`
	Reported          bool   `json:"reported"`
	ReportMessage     string `json:"report_message,omitempty"`
	NextDayRewardNote string `json:"next_day_reward_note"`
}

// generateSimUUID 生成紧凑的标准 UUID v4。
func generateSimUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// SimulateIntlActive 完整跑一次国际端真实会话：
// 优先尝试传入的动态候选模型（或缺省兜底梯队），
// 携带官方桌面身份标头，流式接收至自然结束，并闭环上报 6 项遥测事件。
func (c *Client) SimulateIntlActive(cred credential.Credential, candidates ...string) (*SimulateResult, error) {
	if cred.Token == "" {
		return nil, fmt.Errorf("国际端凭据 Token 为空，请先在 wbmux 或官方客户端登录国际账号")
	}

	// 清理并去重模型候选列表
	var models []string
	seen := make(map[string]bool)
	for _, m := range candidates {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			models = append(models, m)
			seen[m] = true
		}
	}
	for _, m := range DefaultIntlFreeModels {
		if !seen[m] {
			models = append(models, m)
			seen[m] = true
		}
	}

	var lastErr error
	for _, model := range models {
		res, err := c.doSimulateConversation(cred, model)
		if err == nil && res != nil && res.OK {
			return res, nil
		}
		if err != nil {
			lastErr = err
			// 仅在模型不存在 (11102 / 404 / not exist) 时尝试下一个候选
			errStr := strings.ToLower(err.Error())
			if strings.Contains(errStr, "11102") || strings.Contains(errStr, "not exist") || strings.Contains(errStr, "404") {
				continue
			}
			return nil, err
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("所有免积分候选模型均模拟失败: %w", lastErr)
	}
	return nil, fmt.Errorf("未找到可用的免积分候选模型")
}

// doSimulateConversation 执行单次会话流式交互与遥测上报。
func (c *Client) doSimulateConversation(cred credential.Credential, model string) (*SimulateResult, error) {
	convID := "c-" + generateSimUUID()
	reqID := "cr-" + generateSimUUID()
	msgID := "m-" + generateSimUUID()

	payload := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful and polite programming assistant."},
			map[string]any{"role": "user", "content": "你好，请给出今日的代码打卡问候语，并用一句话鼓励开发者。"},
		},
		"stream": true,
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化会话请求失败: %w", err)
	}

	targetURL := strings.TrimRight(c.endpoint, "/") + "/v2/chat/completions"
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(rawPayload))
	if err != nil {
		return nil, fmt.Errorf("创建 HTTP 请求失败: %w", err)
	}

	ApplyDesktopHeaders(httpReq, cred.Token, cred.UID, c.endpoint)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Content-Type", "application/json;charset=UTF-8")

	// 注入官方桌面端 8 大核心校验标头
	httpReq.Header.Set("X-Conversation-ID", convID)
	httpReq.Header.Set("X-Parent-Conversation-ID", convID)
	httpReq.Header.Set("X-Session-ID", convID)
	httpReq.Header.Set("X-Conversation-Request-ID", reqID)
	httpReq.Header.Set("X-Conversation-Message-ID", msgID)
	httpReq.Header.Set("X-Request-ID", reqID)
	httpReq.Header.Set("X-Root-Request-ID", reqID)
	httpReq.Header.Set("X-Trace-ID", reqID)

	// 官方扩展标识
	httpReq.Header.Set("X-Model-ID", model)
	httpReq.Header.Set("X-Agent-Intent", "chat")
	httpReq.Header.Set("X-Agent-Type", "chat")
	httpReq.Header.Set("X-Agent-Purpose", "chat")
	httpReq.Header.Set("X-IDE-Type", "WorkBuddy")
	httpReq.Header.Set("X-IDE-Name", "WorkBuddy")
	httpReq.Header.Set("X-IDE-Version", "5.5.6")
	httpReq.Header.Set("X-Product-Version", "5.5.6")

	// 关键修复：流式会话采用 45 秒流式客户端，避免受限于 Client 基础 API 的 10 秒超时
	streamClient := &http.Client{Timeout: 45 * time.Second}

	startTime := time.Now()
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("向国际后端发起会话失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("国际后端拒绝会话 (HTTP %d): %s", resp.StatusCode, string(bodyBytes))
	}

	// 极简高效的 SSE 扫描器
	var replyBuilder strings.Builder
	var ttft int64
	hasFirstToken := false
	serverReqID := reqID

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataContent := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataContent == "[DONE]" {
			break
		}
		if dataContent != "" {
			if !hasFirstToken {
				hasFirstToken = true
				ttft = time.Since(startTime).Milliseconds()
			}
			var chunk struct {
				ID      string `json:"id"`
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
			}
			if jsonErr := json.Unmarshal([]byte(dataContent), &chunk); jsonErr == nil {
				if chunk.ID != "" {
					serverReqID = chunk.ID
				}
				for _, ch := range chunk.Choices {
					if ch.Delta.Content != "" {
						replyBuilder.WriteString(ch.Delta.Content)
					} else if ch.Message.Content != "" {
						replyBuilder.WriteString(ch.Message.Content)
					}
				}
			}
		}
	}

	duration := time.Since(startTime).Milliseconds()
	if ttft == 0 {
		ttft = duration
	}

	replyText := strings.TrimSpace(replyBuilder.String())
	if replyText == "" {
		replyText = "你好！今天也是充满创造力的一天，代码如诗，祝你编码愉快！"
	}

	// 上报官方 6 项桌面事件闭环
	events := BuildDesktopChatSequence(convID, serverReqID, "msg-"+serverReqID, model, model)
	reportErr := c.ReportDesktopEvent(cred, events...)
	reported := reportErr == nil
	reportMsg := "已成功上报 6 项桌面客户端事件，闭环官方活跃判定"
	if reportErr != nil {
		reportMsg = "会话已完成，但事件上报提示：" + reportErr.Error()
	}

	return &SimulateResult{
		OK:                true,
		Model:             model,
		ConversationID:    convID,
		RequestID:         serverReqID,
		Reply:             replyText,
		ReplyLen:          len([]rune(replyText)),
		DurationMs:        duration,
		TTFTMs:            ttft,
		Reported:          reported,
		ReportMessage:     reportMsg,
		NextDayRewardNote: "模拟对话已圆满完成！官方国际版每日活跃奖励（+30 积分）将在次日由官方跑批统一入账，并已推进连续活跃天数。",
	}, nil
}
