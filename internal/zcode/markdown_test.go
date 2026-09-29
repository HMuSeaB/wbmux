package zcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample() *Transcript {
	return &Transcript{
		SessionID: "sess_abc123456789",
		Title:     "为啥托盘图标 #右键 没用 / 求解",
		Directory: `C:/Users/me/proj`,
		CreatedMs: 1759000000000,
		UpdatedMs: 1759003600000,
		Msgs: []Msg{
			{Role: "user", At: 1759000000000, Text: "托盘图标右键没用"},
			{Role: "assistant", At: 1759000060000, Text: "我查一下托盘代码。", Tools: []string{"Read", "Bash"}},
			{Role: "assistant", At: 1759000120000, Text: "找到了。", Reasoning: "先看 wndProc……", Files: []string{`internal\tray\tray_windows.go`}},
		},
	}
}

// TestToMarkdown 导出的 Markdown 要"人能读、别的工具能喂"。
func TestToMarkdown(t *testing.T) {
	md := ToMarkdown(sample(), MarkdownOptions{WithTools: true})

	// 路径里的反斜杠在 Go 字符串字面量里要转义，容易把测试写歪；
	// 这里只断言"能看出那是项目路径"的关键片段。
	for _, want := range []string{
		"proj",
		"# 为啥托盘图标 \\#右键 没用 / 求解", // 标题里的 # 要转义，否则顶乱层级
		"## 我",
		"## 助手",
		"托盘图标右键没用",
		"工具：Read、Bash",
		"消息数：3",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("导出内容缺少 %q\n---\n%s", want, md)
		}
	}
	// 默认不带思考过程：它是过程不是对话，一股脑塞进去反而没人看。
	if strings.Contains(md, "先看 wndProc") {
		t.Error("默认不该导出思考片段")
	}
	md2 := ToMarkdown(sample(), MarkdownOptions{WithTools: true, WithReasoning: true})
	if !strings.Contains(md2, "先看 wndProc") {
		t.Error("显式要求时应当带上思考片段")
	}
	// 不要工具名时不该出现那一行。
	md3 := ToMarkdown(sample(), MarkdownOptions{})
	if strings.Contains(md3, "工具：") {
		t.Error("NoTools 时不该列工具")
	}
}

// TestExportFileNaming 文件名要能落在磁盘上：标题里全是非法字符时也得有个名字。
func TestExportFileNaming(t *testing.T) {
	dir := t.TempDir()

	tr := sample()
	p1, err := ExportFile(tr, dir, MarkdownOptions{})
	if err != nil {
		t.Fatalf("ExportFile: %v", err)
	}
	base := filepath.Base(p1)
	if strings.ContainsAny(base, `\/:*?"<>|`) {
		t.Errorf("文件名含非法字符：%s", base)
	}
	if !strings.HasPrefix(base, "2025-") && !strings.HasPrefix(base, "2026-") {
		t.Errorf("文件名应以日期开头，得到 %s", base)
	}

	// 同名再来一次：不能覆盖，要另起一个。
	p2, err := ExportFile(tr, dir, MarkdownOptions{})
	if err != nil {
		t.Fatalf("ExportFile 第二次: %v", err)
	}
	if p1 == p2 {
		t.Fatal("同名导出不该覆盖已有文件")
	}
	if _, err := os.Stat(p1); err != nil {
		t.Errorf("第一个文件不该被删：%v", err)
	}

	// 标题全是非法字符时，仍要生成一个可用的名字。
	weird := sample()
	weird.Title = `///:::???`
	weird.SessionID = "sess_zzzzzzzzzzzz"
	p3, err := ExportFile(weird, dir, MarkdownOptions{})
	if err != nil {
		t.Fatalf("ExportFile 怪标题: %v", err)
	}
	if filepath.Base(p3) == ".md" || strings.TrimSuffix(filepath.Base(p3), ".md") == "" {
		t.Errorf("怪标题也要有名字，得到 %s", filepath.Base(p3))
	}
}

// TestToMarkdownKeepsWarns 读取过程中的警告要写进导出文件——
// 导出是事后才看的，那时没人能回到现场。
func TestToMarkdownKeepsWarns(t *testing.T) {
	tr := sample()
	tr.Warns = []string{"消息 msg_x 的元数据无法解析，已跳过"}
	md := ToMarkdown(tr, MarkdownOptions{})
	if !strings.Contains(md, "已跳过") {
		t.Error("警告应当写进导出内容")
	}
}

// TestStamp 时间格式化（防止把毫秒当秒用，那会显示成 1970 年）。
func TestStamp(t *testing.T) {
	got := stamp(1759000000000)
	if !strings.HasPrefix(got, "2025-") && !strings.HasPrefix(got, "2026-") {
		t.Errorf("毫秒当秒用会得到上古年份，实际得到 %s", got)
	}
	if _, err := time.Parse("2006-01-02 15:04", got); err != nil {
		t.Errorf("时间格式不对：%s", got)
	}
}
