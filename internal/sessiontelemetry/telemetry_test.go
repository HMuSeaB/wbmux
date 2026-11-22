package sessiontelemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestTailReadUsage(t *testing.T) {
	tmpDir := t.TempDir()
	sessFile := filepath.Join(tmpDir, "test-sess.jsonl")

	// 1. 模拟写入普通对话与带 usage 的行
	lines := []string{
		`{"id":"1","type":"message","content":[{"type":"input_text","text":"hello"}]}`,
		`{"id":"2","type":"message","timestamp":1700000000000,"providerData":{"requestModelName":"混元-Standard","rawUsage":{"prompt_tokens":1234,"completion_tokens":56,"total_tokens":1290,"credit":0.05}},"content":[{"type":"output_text","text":"hi"}]}`,
		`{"id":"3","type":"function_call","name":"bash"}`, // 后面跟一行没有 usage 的
	}
	if err := os.WriteFile(sessFile, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := TailReadUsage(sessFile, "cn")
	if err != nil {
		t.Fatalf("TailReadUsage: %v", err)
	}
	if m == nil {
		t.Fatal("期望解析出 metric，但得到 nil")
	}
	if m.PromptTokens != 1234 {
		t.Errorf("PromptTokens 期望 1234，得到 %d", m.PromptTokens)
	}
	if m.CompletionTokens != 56 {
		t.Errorf("CompletionTokens 期望 56，得到 %d", m.CompletionTokens)
	}
	if m.TotalTokens != 1290 {
		t.Errorf("TotalTokens 期望 1290，得到 %d", m.TotalTokens)
	}
	if m.Model != "混元-Standard" {
		t.Errorf("Model 期望 混元-Standard，得到 %q", m.Model)
	}
	if m.Credit != 0.05 {
		t.Errorf("Credit 期望 0.05，得到 %f", m.Credit)
	}
}

func TestTailReadUsageTruncatedLine(t *testing.T) {
	tmpDir := t.TempDir()
	sessFile := filepath.Join(tmpDir, "test-trunc.jsonl")

	// 末尾故意放一行写了一半被截断的 JSON
	content := `{"id":"1","timestamp":1700000000000,"providerData":{"model":"hunyuan","rawUsage":{"prompt_tokens":500,"completion_tokens":50,"total_tokens":550}}}` + "\n" +
		`{"id":"2","timestamp":1700000001000,"providerData":{"model":"hunyuan","rawUsa` // 截断

	if err := os.WriteFile(sessFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := TailReadUsage(sessFile, "cn")
	if err != nil {
		t.Fatalf("TailReadUsage on truncated: %v", err)
	}
	if m == nil {
		t.Fatal("期望跳过截断行解析出倒数第二行，但得到 nil")
	}
	if m.PromptTokens != 500 {
		t.Errorf("PromptTokens 期望 500，得到 %d", m.PromptTokens)
	}
}

func TestTailReadUsageLargeFile(t *testing.T) {
	tmpDir := t.TempDir()
	sessFile := filepath.Join(tmpDir, "test-large.jsonl")

	f, err := os.Create(sessFile)
	if err != nil {
		t.Fatal(err)
	}

	// 写入超过 70KB 的无关日志
	dummy := strings.Repeat(`{"type":"thought","text":"thinking very hard..."}`+"\n", 1500)
	if _, err := f.WriteString(dummy); err != nil {
		t.Fatal(err)
	}

	// 在文件最末尾写入真实的 usage
	target := `{"id":"final","timestamp":1700000099000,"providerData":{"requestModelName":"DeepSeek-V4.1-Flash","rawUsage":{"prompt_tokens":408000,"completion_tokens":300,"total_tokens":408300,"credit":0.31}}}` + "\n"
	if _, err := f.WriteString(target); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	m, err := TailReadUsage(sessFile, "intl")
	if err != nil {
		t.Fatalf("TailReadUsage on large file: %v", err)
	}
	if m == nil {
		t.Fatal("大文件 Seek 尾读期望命中，但得到 nil")
	}
	if m.PromptTokens != 408000 {
		t.Errorf("PromptTokens 期望 408000，得到 %d", m.PromptTokens)
	}
	if m.Model != "DeepSeek-V4.1-Flash" {
		t.Errorf("Model 期望 DeepSeek-V4.1-Flash，得到 %q", m.Model)
	}
}

func TestTailReadUsageSecondTier(t *testing.T) {
	tmpDir := t.TempDir()
	sessFile := filepath.Join(tmpDir, "test-second-tier.jsonl")

	// 用量记录先写，随后追加约 100KB 不带 usage/tokens 关键字的填充行，
	// 把它顶出 64KB 快路径窗口、留在 512KB 慢路径窗口内。
	usage := `{"id":"u1","timestamp":1700000000000,"providerData":{"requestModelName":"Kimi-K3.1-Pro","rawUsage":{"prompt_tokens":61000,"completion_tokens":120,"total_tokens":61120}}}` + "\n"
	filler := strings.Repeat(`{"type":"thought","text":"thinking very hard, filling the log line by line..."}`+"\n", 1500)
	if err := os.WriteFile(sessFile, []byte(usage+filler), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() <= 64*1024 || fi.Size() >= 512*1024 {
		t.Fatalf("测试前置：文件应为 64KB–512KB，实际 %d 字节", fi.Size())
	}

	m, err := TailReadUsage(sessFile, "cn")
	if err != nil {
		t.Fatalf("TailReadUsage: %v", err)
	}
	if m == nil {
		t.Fatal("用量记录在 64KB 外、512KB 内，第二档应找得到，但得到 nil")
	}
	if m.PromptTokens != 61000 || m.Model != "Kimi-K3.1-Pro" {
		t.Errorf("期望 61000/Kimi-K3.1-Pro，得到 %d/%q", m.PromptTokens, m.Model)
	}
}

func TestTailReadUsageBeyondSlowWindow(t *testing.T) {
	tmpDir := t.TempDir()
	sessFile := filepath.Join(tmpDir, "test-beyond-slow.jsonl")

	// 用量记录距文件尾超过 512KB：两档都扫不到，返回 nil（有界，不扫全文件）。
	usage := `{"id":"u1","timestamp":1700000000000,"providerData":{"requestModelName":"Kimi-K3.1-Pro","rawUsage":{"prompt_tokens":61000,"completion_tokens":120,"total_tokens":61120}}}` + "\n"
	filler := strings.Repeat(`{"type":"thought","text":"thinking very hard, filling the log line by line..."}`+"\n", 9000)
	if err := os.WriteFile(sessFile, []byte(usage+filler), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() <= 512*1024 {
		t.Fatalf("测试前置：文件应超过 512KB，实际 %d 字节", fi.Size())
	}

	m, err := TailReadUsage(sessFile, "cn")
	if err != nil {
		t.Fatalf("TailReadUsage: %v", err)
	}
	if m != nil {
		t.Errorf("用量记录在 512KB 外，应返回 nil，得到 %+v", m)
	}
}

func TestFindLatestActiveSession(t *testing.T) {
	tmpDir := t.TempDir()
	cnDir := filepath.Join(tmpDir, ".workbuddy", "projects", "proj1")
	intlDir := filepath.Join(tmpDir, ".workbuddy-ai", "projects", "proj2")
	_ = os.MkdirAll(cnDir, 0o755)
	_ = os.MkdirAll(intlDir, 0o755)

	oldFile := filepath.Join(cnDir, "old.jsonl")
	newFile := filepath.Join(intlDir, "new.jsonl")

	_ = os.WriteFile(oldFile, []byte("old"), 0o644)
	time.Sleep(20 * time.Millisecond)
	_ = os.WriteFile(newFile, []byte("new"), 0o644)

	probe := &variant.Probe{
		Home: tmpDir,
	}

	gotPath, gotSide, _ := FindLatestActiveSession(probe)
	if !strings.EqualFold(gotPath, newFile) {
		t.Errorf("期望最新文件 %s，得到 %s", newFile, gotPath)
	}
	if gotSide != "intl" {
		t.Errorf("期望最新档位 intl，得到 %s", gotSide)
	}
}

func TestRealWorkBuddySessionRead(t *testing.T) {
	probe := variant.DefaultProbe()
	m := GetLatestSessionUsage(probe)
	if m != nil {
		t.Logf("实测命中用户真实活跃会话：%s (%s), PromptTokens=%d, Model=%s, Credit=%.2f, Time=%s",
			m.SessionID, m.Side, m.PromptTokens, m.Model, m.Credit, m.Timestamp.Format("2006-01-02 15:04:05"))
	} else {
		t.Log("当前环境未检测到活跃会话文件")
	}
}
