package webui

import (
	"encoding/json"
	"fmt"
)

// DefaultSlimmingThreshold 是触发自适应智能瘦身的报文门槛（默认 300 KB，约 60k tokens）。
// 正常小请求绝对不触碰，仅在请求过大且上游报错拒载时兜底介入。
const DefaultSlimmingThreshold = 300 * 1024

// slimLargeChatBody 对大上下文请求进行自适应安全瘦身。
//
// 核心设计原则（Safety First）：
//  1. 绝对保留所有 role: "system" 消息（系统提示词、Agent 规则与人设）；
//  2. 严格维护 OpenAI tool_calls 与 tool_call_id 的成对完整性，绝不断头或悬挂；
//  3. 针对早期历史轮次中累积的大量过期 tool content（终端日志、大文件读取），
//     安全替换为紧凑的执行成功占位摘要；
//  4. 绝对完整保留尾部最近的活跃对话轮次（思维现场与最新输入）。
//
// 返回瘦身后的 JSON 报文、是否执行了瘦身、瘦身前字节数、瘦身后字节数。
func slimLargeChatBody(raw []byte, thresholdBytes int64) ([]byte, bool, int64, int64) {
	origLen := int64(len(raw))
	if origLen < thresholdBytes {
		return raw, false, origLen, origLen
	}

	var req map[string]any
	if err := json.Unmarshal(raw, &req); err != nil {
		return raw, false, origLen, origLen
	}

	rawMsgs, ok := req["messages"].([]any)
	if !ok || len(rawMsgs) <= 4 {
		return raw, false, origLen, origLen
	}

	totalMsgs := len(rawMsgs)
	// 保留尾部最近 6 条消息完整不折叠（保证最近一整套任务现场与思考链完好无损）
	recentKeepCount := 6
	if totalMsgs < recentKeepCount {
		recentKeepCount = totalMsgs
	}
	recentThresholdIdx := totalMsgs - recentKeepCount

	modified := false
	newMsgs := make([]any, 0, totalMsgs)

	for i, m := range rawMsgs {
		msgMap, isMap := m.(map[string]any)
		if !isMap {
			newMsgs = append(newMsgs, m)
			continue
		}

		role, _ := msgMap["role"].(string)

		// 1. 系统提示词：无论多长，100% 完整保留
		if role == "system" {
			newMsgs = append(newMsgs, msgMap)
			continue
		}

		// 2. 尾部活跃消息：100% 完整保留
		if i >= recentThresholdIdx {
			newMsgs = append(newMsgs, msgMap)
			continue
		}

		// 3. 早期历史工具响应 (role == "tool")：
		// 历史工具执行结果是会话体积膨胀的头号元凶（占 85% 以上），
		// 只要其长度超过 300 字符，安全折叠为紧凑成功摘要。
		if role == "tool" {
			if contentStr, ok := msgMap["content"].(string); ok && len(contentStr) > 300 {
				cloneMsg := make(map[string]any, len(msgMap))
				for k, v := range msgMap {
					cloneMsg[k] = v
				}
				cloneMsg["content"] = fmt.Sprintf(
					"[wbmux 网关自适应保活：该历史工具执行成功，早期输出已自动折叠 (原大小 %d 字节)]",
					len(contentStr),
				)
				newMsgs = append(newMsgs, cloneMsg)
				modified = true
				continue
			}
		}

		// 4. 早期超长用户输入或助手大段输出：若超过 4000 字符，保留前后段
		if role == "user" || role == "assistant" {
			if contentStr, ok := msgMap["content"].(string); ok && len(contentStr) > 4000 {
				cloneMsg := make(map[string]any, len(msgMap))
				for k, v := range msgMap {
					cloneMsg[k] = v
				}
				head := contentStr[:1000]
				tail := contentStr[len(contentStr)-800:]
				cloneMsg["content"] = fmt.Sprintf("%s\n\n... [中间 %d 字符已由网关自适应紧凑折叠] ...\n\n%s", head, len(contentStr)-1800, tail)
				newMsgs = append(newMsgs, cloneMsg)
				modified = true
				continue
			}
		}

		newMsgs = append(newMsgs, msgMap)
	}

	if !modified {
		return raw, false, origLen, origLen
	}

	req["messages"] = newMsgs
	out, err := json.Marshal(req)
	if err != nil {
		return raw, false, origLen, origLen
	}

	afterLen := int64(len(out))
	// 确保确实瘦身了才返回成功
	if afterLen < origLen {
		return out, true, origLen, afterLen
	}

	return raw, false, origLen, origLen
}
