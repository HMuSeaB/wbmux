package webui

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSlimLargeChatBody(t *testing.T) {
	// 1. 小报文不触碰
	smallPayload := map[string]any{
		"model": "deepseek-v4.1-flash",
		"messages": []map[string]string{
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "hello world"},
		},
	}
	smallBytes, _ := json.Marshal(smallPayload)
	out, slimmed, _, _ := slimLargeChatBody(smallBytes, 600*1024)
	if slimmed {
		t.Fatalf("小报文不应该被瘦身")
	}
	if string(out) != string(smallBytes) {
		t.Fatalf("小报文内容被篡改")
	}

	// 2. 模拟真实长会话大报文 (包含大量工具调用)
	longOutput := strings.Repeat("console.log('huge output from cat command line');\n", 1000) // 约 50KB

	var msgs []map[string]any
	// 系统提示词
	msgs = append(msgs, map[string]any{
		"role":    "system",
		"content": "CRITICAL_SYSTEM_PROMPT_DO_NOT_LOSE",
	})
	// 模拟前 10 轮交互，中间带有巨大工具响应
	for i := 0; i < 10; i++ {
		msgs = append(msgs, map[string]any{
			"role":    "user",
			"content": "read file batch " + string(rune('0'+i)),
		})
		msgs = append(msgs, map[string]any{
			"role": "assistant",
			"content": "",
			"tool_calls": []map[string]any{
				{
					"id":   "call_id_" + string(rune('0'+i)),
					"type": "function",
					"function": map[string]any{
						"name": "read_file",
					},
				},
			},
		})
		msgs = append(msgs, map[string]any{
			"role":         "tool",
			"tool_call_id": "call_id_" + string(rune('0'+i)),
			"content":      longOutput, // 巨大工具输出
		})
	}
	// 尾部最新问题
	msgs = append(msgs, map[string]any{
		"role":    "user",
		"content": "LATEST_USER_QUESTION_NEED_REPLY",
	})

	largePayload := map[string]any{
		"model":    "deepseek-v4.1-flash",
		"messages": msgs,
	}
	largeBytes, _ := json.Marshal(largePayload)
	origLen := int64(len(largeBytes))

	// 阈值设为 100KB，确保触发
	slimmedBytes, slimmed, before, after := slimLargeChatBody(largeBytes, 100*1024)
	if !slimmed {
		t.Fatalf("大报文应该被成功瘦身")
	}
	if after >= before {
		t.Fatalf("瘦身后体积应显著减小，实际 before=%d, after=%d", before, after)
	}

	// 瘦身率应大于 70%
	ratio := (1.0 - float64(after)/float64(before)) * 100
	t.Logf("原始体积: %d 字节, 瘦身后: %d 字节, 压缩率: %.1f%%", before, after, ratio)
	if ratio < 70 {
		t.Errorf("压缩率偏低: %.1f%%", ratio)
	}

	// 校验 JSON 结构与关键字段完整性
	var res map[string]any
	if err := json.Unmarshal(slimmedBytes, &res); err != nil {
		t.Fatalf("瘦身后 JSON 非法: %v", err)
	}

	resMsgs := res["messages"].([]any)
	// 验证系统提示词仍在
	firstMsg := resMsgs[0].(map[string]any)
	if firstMsg["role"] != "system" || firstMsg["content"] != "CRITICAL_SYSTEM_PROMPT_DO_NOT_LOSE" {
		t.Errorf("系统提示词被破坏: %v", firstMsg)
	}

	// 验证最新用户问题仍在
	lastMsg := resMsgs[len(resMsgs)-1].(map[string]any)
	if lastMsg["role"] != "user" || lastMsg["content"] != "LATEST_USER_QUESTION_NEED_REPLY" {
		t.Errorf("最新用户问题被破坏: %v", lastMsg)
	}

	// 验证所有 tool_call_id 依然完整匹配
	for _, m := range resMsgs {
		mMap := m.(map[string]any)
		if mMap["role"] == "tool" {
			callID, ok := mMap["tool_call_id"].(string)
			if !ok || !strings.HasPrefix(callID, "call_id_") {
				t.Errorf("tool_call_id 丢失或异常: %v", mMap)
			}
		}
	}
	_ = origLen
}
