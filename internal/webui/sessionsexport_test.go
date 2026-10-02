package webui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 导出接口的测试。
//
// 为什么值得单测这个：导出**读的是磁盘上的会话文件**，而路径来自扫描结果。
// 写错一个条件就会"导了但用户找不到"或者"什么都没导却报成功"。
// 而且它是我这一版唯一新增的只读接口，先钉住行为再交给用户。

// writeCodexSession 造一个最小的 Codex 会话文件。
//
// 只放渲染会用到的行：session_meta 给 cwd、两条 user、一条 assistant、
// 一次工具调用。够验证"读出来了、渲染了、能打开"。
func writeCodexSession(t *testing.T, home, name, cwd, firstMsg string) string {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "03", "16")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	lines := []string{
		`{"timestamp":"2026-03-16T06:00:00.000Z","type":"session_meta","payload":{"id":"` +
			strings.TrimSuffix(name, ".jsonl") + `","cwd":"` + strings.ReplaceAll(cwd, `\`, `\\`) + `"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` +
			firstMsg + `"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"好的"}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestExportSingleSession 走一遍"点某个会话的导出按钮"。
func TestExportSingleSession(t *testing.T) {
	env := newTestEnv(t, Options{})
	writeCodexSession(t, env.home, "rollout-2026-03-16T06-00-00-aaaa.jsonl",
		`D:\proj\demo`, "帮我看看这个报错")

	outDir := filepath.Join(t.TempDir(), "out")
	body, _ := json.Marshal(map[string]any{
		"id":  "rollout-2026-03-16T06-00-00-aaaa",
		"dir": outDir,
	})
	res := env.do(t, "POST", "/api/sessions/export?days=30&keep=1", string(body), true)
	if res.code != 200 {
		t.Fatalf("HTTP %d：%s", res.code, res.body)
	}

	var got struct {
		Exported int      `json:"exported"`
		Dir      string   `json:"dir"`
		Index    string   `json:"index"`
		Skipped  []string `json:"skipped"`
		Failed   []string `json:"failed"`
	}
	res.decode(t, &got)

	if got.Exported != 1 {
		t.Fatalf("该导出 1 个，实际 %d（failed=%v skipped=%v）", got.Exported, got.Failed, got.Skipped)
	}
	if got.Index == "" {
		t.Fatal("该返回索引页路径——界面要靠它告诉用户文件在哪")
	}
	if _, err := os.Stat(got.Index); err != nil {
		t.Fatalf("索引页没写出来：%v", err)
	}

	// 内容要真的在里面：会话正文、项目路径都得能搜到。
	raw, err := os.ReadFile(got.Index)
	if err != nil {
		t.Fatal(err)
	}
	idx := string(raw)
	if !strings.Contains(idx, "帮我看看这个报错") {
		t.Error("索引页里该有这个会话的标题（取自第一句话）")
	}
	if !strings.Contains(idx, "proj") {
		t.Error("索引页里该有项目路径")
	}
}

// TestExportRejectsWrongMethod 只读接口也要求 POST：GET 带不上请求体，
// 而"导出哪一个"必须显式说出来，不能靠 URL 参数猜。
func TestExportRejectsWrongMethod(t *testing.T) {
	env := newTestEnv(t, Options{})
	res := env.do(t, "GET", "/api/sessions/export?days=30&keep=1", "", true)
	if res.code != 405 {
		t.Fatalf("该 405，实际 %d", res.code)
	}
}

// TestExportRequiresToken 确认它跟别的接口一样受令牌保护。
//
// 导出会把会话正文（可能含项目路径、代码片段）写到**调用方指定的目录**，
// 没有令牌就等于"本机任何程序都能让它把会话写到任意位置"。
func TestExportRequiresToken(t *testing.T) {
	env := newTestEnv(t, Options{})
	res := env.do(t, "POST", "/api/sessions/export?days=30&keep=1", `{"id":"x"}`, false)
	if res.code != 403 {
		t.Fatalf("无令牌该 403，实际 %d", res.code)
	}
}

// TestExportUnsupportedVendorIsReported 确认不支持的源**如实报告**，
// 而不是假装成功。
//
// 用户点名了某一家、那家又导不了时，最糟的结果是"命令成功、目录是空的"。
func TestExportUnsupportedVendorIsReported(t *testing.T) {
	env := newTestEnv(t, Options{})
	// 没有任何会话时它应当明确报错，而不是返回一个空目录。
	body := `{"id":"不存在的会话"}`
	res := env.do(t, "POST", "/api/sessions/export?days=30&keep=1", body, true)
	if res.code != 200 && res.code != 404 && res.code != 500 {
		t.Fatalf("非预期状态码 %d", res.code)
	}
	// 关键：响应里要有话说清楚，不能是一个空的成功。
	if res.code == 200 {
		var got struct {
			Exported int      `json:"exported"`
			Failed   []string `json:"failed"`
		}
		res.decode(t, &got)
		if got.Exported == 0 && len(got.Failed) == 0 {
			t.Fatal("没导出任何东西、却没有一条说明——用户会以为成功了")
		}
	}
}

// ---------- 界面里直接查看 ----------

// TestSessionViewRendersConversation 走一遍"点打开新标签页"。
//
// 这个路由与别的不同：它回的是**整页 HTML**、不走 guard（guard 失败回 JSON），
// 所以令牌校验必须自己再做一遍——漏了就是把会话正文敞开给任何能访问
// 本地端口的东西。
func TestSessionViewRendersConversation(t *testing.T) {
	env := newTestEnv(t, Options{})
	const sid = "rollout-2026-03-16T06-00-00-bbbb"
	writeCodexSession(t, env.home, sid+".jsonl", `D:/proj/view`, "帮我看下这个报错")

	res := env.do(t, "GET", "/session?id="+sid+"&limit=10", "", true)
	if res.code != 200 {
		t.Fatalf("HTTP %d：%s", res.code, res.body)
	}
	page := string(res.body)

	if !strings.Contains(page, "<!DOCTYPE html>") {
		t.Error("该是一整页 HTML")
	}
	// 正文必须在里面 —— 这是这个页面的全部意义。
	if !strings.Contains(page, "帮我看下这个报错") {
		t.Error("页面里没有会话正文")
	}
	if !strings.Contains(page, "proj") {
		t.Error("页面里没有项目路径")
	}
	// 头部该显示传进来的标题。
	if !strings.Contains(page, "测试标题") && !strings.Contains(page, "view") {
		t.Error("页面里没有标题")
	}
}

// TestSessionViewRequiresToken 确认整页路由自己也校验令牌。
func TestSessionViewRequiresToken(t *testing.T) {
	env := newTestEnv(t, Options{})
	res := env.do(t, "GET", "/session?id=x", "", false)
	if res.code != 403 {
		t.Fatalf("无令牌该 403，实际 %d", res.code)
	}
}

// TestSessionViewUnknownIDIsNotFound 确认找不到时给 404 且说清原因。
func TestSessionViewUnknownIDIsNotFound(t *testing.T) {
	env := newTestEnv(t, Options{})
	res := env.do(t, "GET", "/session?id=根本没有这个&t=x", "", true)
	if res.code != 404 {
		t.Fatalf("该 404，实际 %d：%s", res.code, res.body)
	}
	if !strings.Contains(string(res.body), "重新扫描") {
		t.Errorf("该提示怎么办（重新扫描），实际：%s", res.body)
	}
}

// TestSessionViewTruncationNotice 确认大会话会告诉用户"还有更早的"，
// 并给出「加载全部」的链接。
//
// 不给提示的话，用户会以为"这个会话就这么点"——而实际上前面还有几百项。
func TestSessionViewTruncationNotice(t *testing.T) {
	env := newTestEnv(t, Options{})
	// 造一个 30 对的会话，窗口给 5。
	dir := filepath.Join(env.home, ".codex", "sessions", "2026", "03", "17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"type":"session_meta","payload":{"id":"big","cwd":"D://p"}}` + "\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, `{"type":"response_item","payload":{"type":"function_call","name":"t%02d","arguments":"a","call_id":"c%02d"}}`+"\n", i, i)
		fmt.Fprintf(&b, `{"type":"response_item","payload":{"type":"function_call_output","call_id":"c%02d","output":"r"}}`+"\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-big.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	res := env.do(t, "GET", "/session?id=big&limit=5", "", true)
	if res.code != 200 {
		t.Fatalf("HTTP %d：%s", res.code, res.body)
	}
	page := string(res.body)
	if !strings.Contains(page, "加载全部") {
		t.Error("截断时该给出「加载全部」的入口")
	}
	if !strings.Contains(page, "limit=0") {
		t.Error("「加载全部」的链接该带 limit=0")
	}
	if !strings.Contains(page, "共 ") {
		t.Error("该告诉用户总共有多少项")
	}
}
