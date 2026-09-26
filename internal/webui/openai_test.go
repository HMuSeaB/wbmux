package webui

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnsureSystemFirst 钉住官方接口的硬要求：第一条必须是 system
// （实测 code 11128）。四种形态：缺 system 要补、system 不在首位要挪、
// 已在首位不动、空消息列表给默认。
func TestEnsureSystemFirst(t *testing.T) {
	mk := func(role, content string) openaiMessage {
		return openaiMessage{Role: role, Content: json.RawMessage(`"` + content + `"`)}
	}
	firstIsSystem := func(msgs []openaiMessage) bool {
		return len(msgs) > 0 && strings.EqualFold(msgs[0].Role, "system")
	}

	got := ensureSystemFirst([]openaiMessage{mk("user", "hi")})
	if !firstIsSystem(got) {
		t.Errorf("缺 system 时应补默认 system")
	}
	if len(got) != 2 {
		t.Errorf("补默认后应共 2 条，得到 %d", len(got))
	}

	got = ensureSystemFirst([]openaiMessage{mk("user", "hi"), mk("system", "sys")})
	if !firstIsSystem(got) || got[0].Role != "system" {
		t.Errorf("不在首位的 system 应挪到首位")
	}

	orig := []openaiMessage{mk("system", "sys"), mk("user", "hi")}
	got = ensureSystemFirst(orig)
	if len(got) != 2 || got[1].Role != "user" {
		t.Errorf("首位已是 system 不应改动：%v", got)
	}

	got = ensureSystemFirst(nil)
	if !firstIsSystem(got) {
		t.Errorf("空列表应给默认 system")
	}
}
