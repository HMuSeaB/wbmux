package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// hermeticProbe 造一个完全脱离真实机器的探测器。
//
// 与 internal/migrate 里的同名辅助一致：任何会碰真实系统状态的路径
// 都要可注入，否则在有真实客户端的机器上测试会互相干扰。
func hermeticProbe(t *testing.T) *variant.Probe {
	t.Helper()
	home := t.TempDir()
	return &variant.Probe{
		GOOS:   "windows",
		Home:   home,
		Getenv: func(string) string { return "" },
		Exists: func(p string) bool {
			if !strings.HasPrefix(p, home) {
				return false
			}
			_, err := os.Stat(p)
			return err == nil
		},
	}
}

// writeSession 在某个档位的 projects 目录下写一个会话文件。
func writeSession(t *testing.T, probe *variant.Probe, id variant.ID, slug, name, body string) {
	t.Helper()
	dir := filepath.Join(probe.DataDir(id), "projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
}

func TestSurveyLimitsFindsRateLimit(t *testing.T) {
	probe := hermeticProbe(t)
	writeSession(t, probe, variant.CN, "c-Users-me-proj", "aaaa.jsonl",
		`{"type":"message","role":"user","content":"你好"}`+"\n"+
			`{"type":"message","role":"assistant","model":"Deepseek-V4.1-Flash",`+
			`"content":"429 您的使用量已超出频率限制，将在 2026-09-26 13:40:52 UTC+8 重置，您也可以切换其他模型继续使用。"}`+"\n")

	s := SurveyLimits(probe)
	if len(s.Limits) != 1 {
		t.Fatalf("应找到 1 条限流，得到 %d 条；警告=%v", len(s.Limits), s.Warnings)
	}
	got := s.Limits[0]
	if got.Kind != "rate" {
		t.Errorf("类型应为 rate，得到 %q", got.Kind)
	}
	if got.Side != "cn" {
		t.Errorf("归属应为 cn，得到 %q —— 归属判错正是这个功能存在的意义", got.Side)
	}
	if !strings.Contains(got.ResetAt, "2026-09-26 13:40:52") {
		t.Errorf("重置时间没抓到：%q", got.ResetAt)
	}
	if got.Model != "Deepseek-V4.1-Flash" {
		t.Errorf("模型没取到：%q", got.Model)
	}
}

// TestSurveyLimitsDistinguishesExhausted 钉住最重要的区分。
//
// 「频率超限」和「额度用尽」都是 429，但对策完全不同：前者等重置就行，
// 后者得充值。混为一谈会让用户干等一个永远不会自己恢复的问题。
func TestSurveyLimitsDistinguishesExhausted(t *testing.T) {
	probe := hermeticProbe(t)
	writeSession(t, probe, variant.Intl, "c-Users-me-proj", "bbbb.jsonl",
		`{"type":"message","role":"assistant","model":"GPT-5.5",`+
			`"content":"429 Credits exhausted. Please visit the link below to purchase add-on packs and get more credits: https://www.codebuddy.ai/"}`+"\n")

	s := SurveyLimits(probe)
	if len(s.Limits) != 1 {
		t.Fatalf("应找到 1 条，得到 %d 条", len(s.Limits))
	}
	if s.Limits[0].Kind != "exhausted" {
		t.Errorf("类型应为 exhausted，得到 %q", s.Limits[0].Kind)
	}
	if s.Limits[0].Side != "intl" {
		t.Errorf("归属应为 intl，得到 %q", s.Limits[0].Side)
	}
	if s.Limits[0].ResetAt != "" {
		t.Errorf("额度用尽不该带重置时间，却给了 %q", s.Limits[0].ResetAt)
	}
}

// TestSurveyLimitsAttributesEachSide 覆盖用户最缺的那个信息：
// 同一个屏幕上分不清是国内被限还是国际被限。
func TestSurveyLimitsAttributesEachSide(t *testing.T) {
	probe := hermeticProbe(t)
	const rate = `{"type":"message","content":"429 您的使用量已超出频率限制，将在 2026-09-26 13:40:52 UTC+8 重置"}`
	writeSession(t, probe, variant.CN, "c-a", "cn.jsonl", rate+"\n")
	writeSession(t, probe, variant.Intl, "c-b", "intl.jsonl", rate+"\n")

	s := SurveyLimits(probe)
	if len(s.Limits) != 2 {
		t.Fatalf("两侧各一条，应共 2 条，得到 %d 条", len(s.Limits))
	}
	sides := map[string]bool{}
	for _, e := range s.Limits {
		sides[e.Side] = true
	}
	if !sides["cn"] || !sides["intl"] {
		t.Errorf("两侧都该各归一条，实际 %v", sides)
	}
}

func TestSurveyLimitsIgnoresCleanSessions(t *testing.T) {
	probe := hermeticProbe(t)
	writeSession(t, probe, variant.CN, "c-a", "clean.jsonl",
		`{"type":"message","content":"一切正常，没有任何限流"}`+"\n")

	s := SurveyLimits(probe)
	if len(s.Limits) != 0 {
		t.Errorf("没有限流时不该报出记录：%+v", s.Limits)
	}
}
