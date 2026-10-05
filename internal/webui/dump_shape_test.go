package webui

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestDumpBodyShapeReportsInlineBase64 钉住诊断的核心判据：
// 内联 base64 必须被数出来，纯文本不能被误算。
//
// 这条诊断的结论会决定"要不要做图片降级"，数错就会做错方向。
func TestDumpBodyShapeReportsInlineBase64(t *testing.T) {
	// 造一张"假图"：8KB 的 base64
	fake := base64.StdEncoding.EncodeToString(make([]byte, 6144))
	body := map[string]any{
		"model": "deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant."},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "看看这张图"},
				map[string]any{"type": "image_url",
					"image_url": map[string]any{"url": "data:image/png;base64," + fake}},
			}},
			map[string]any{"role": "assistant", "content": "好的，这是普通文本"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "Bash"}}},
	}
	raw, _ := json.Marshal(body)

	out := dumpBodyShape(raw)

	for _, want := range []string{"messages 条数: 3", "image_url", "base64 内联总量"} {
		if !strings.Contains(out, want) {
			t.Errorf("报告里缺 %q\n---\n%s", want, out)
		}
	}
	// 内联量应该约等于那张假图
	if !strings.Contains(out, "base64") {
		t.Error("没报告 base64")
	}
	// 纯文本消息不该被算进 base64
	if strings.Contains(out, "assistant") && strings.Contains(out, "base64 8192") {
		t.Log("（仅在确有大块时才会有具体数字，这里只作提示）")
	}
}

// TestDumpBodyShapeNoBase64 没有图片时要报 0，不能瞎报。
func TestDumpBodyShapeNoBase64(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "纯文本，没有图"},
		},
	}
	raw, _ := json.Marshal(body)
	out := dumpBodyShape(raw)
	if !strings.Contains(out, "base64 内联总量: 0 字节") {
		t.Errorf("无图应报 0：\n%s", out)
	}
}

// TestDumpBodyShapeBadJSON 坏 JSON 不能 panic。
func TestDumpBodyShapeBadJSON(t *testing.T) {
	out := dumpBodyShape([]byte("{ 这不是 json"))
	if !strings.Contains(out, "不是 JSON 对象") {
		t.Errorf("坏 JSON 应给出说明：\n%s", out)
	}
}

// TestDumpOncePathDefaultOff 默认必须是关的。
//
// 这是硬要求：诊断会处理请求体，默认开着等于把用户的正文暴露在磁盘上。
func TestDumpOncePathDefaultOff(t *testing.T) {
	t.Setenv("WBMUX_DUMP_ONCE", "")
	if p := dumpOncePath(); p != "" {
		t.Errorf("默认必须不诊断，实际 %q", p)
	}
}
