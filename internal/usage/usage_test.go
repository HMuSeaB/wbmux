package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestSurveyLimitsPicksLatestPerSide 钉住"每侧最新一条"。
//
// 用户要的是"**现在**被限了吗、哪一侧"，不是一份历史清单。而清单的顺序
// 会随客户端持续写文件而变化——最关键的那条未必排在最前。有了 Latest，
// 界面可以先给一句当前状态，不必让用户自己在列表里翻。
func TestSurveyLimitsPicksLatestPerSide(t *testing.T) {
	probe := hermeticProbe(t)
	// 同一侧放两个文件：一个是旧的频率超限，一个是额度用尽。
	// 用 mtime 拉开先后，确保"最新"是按时间而不是碰巧的文件顺序。
	old := filepath.Join(probe.DataDir(variant.CN), "projects", "c-a")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	pOld := filepath.Join(old, "old.jsonl")
	if err := os.WriteFile(pOld, []byte(
		`{"content":"429 您的使用量已超出频率限制，将在 2026-09-18 21:27:01 UTC+8 重置"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pNew := filepath.Join(old, "new.jsonl")
	if err := os.WriteFile(pNew, []byte(
		`{"content":"429 Credits exhausted"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 让 new 明显更晚
	later := time.Now().Add(-time.Hour)
	if err := os.Chtimes(pOld, later.Add(-48*time.Hour), later.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(pNew, later, later); err != nil {
		t.Fatal(err)
	}

	s := SurveyLimits(probe)
	got, ok := s.Latest["cn"]
	if !ok {
		t.Fatalf("cn 侧没有给出最新状态；Limits=%d 条", len(s.Limits))
	}
	if got.Kind != "exhausted" {
		t.Errorf("cn 最新应是额度用尽，得到 %q", got.Kind)
	}
	if _, ok := s.Latest["intl"]; ok {
		t.Errorf("intl 侧没有任何会话，不该出现在 Latest 里")
	}
}

// TestParseResetMs 单独验时间解析。
//
// 解析错了不会报错，只会安静地把顺序排错——而顺序错了，"当前状态"就会
// 显示一个已经过去的限制，用户照着它等，白等。
func TestParseResetMs(t *testing.T) {
	cases := []struct {
		in   string
		want string // 期望的 UTC 时刻
	}{
		{"2026-09-26 13:40:52 UTC+8", "2026-09-26 05:40:52"},
		{"2026-09-26 13:40:52 UTC", "2026-09-26 13:40:52"},
		{"2026-09-18 21:27:01 UTC+8", "2026-09-18 13:27:01"},
		{"2026-09-20 10:16:13 UTC+8", "2026-09-20 02:16:13"},
	}
	for _, c := range cases {
		ms := parseResetMs(c.in)
		if ms == 0 {
			t.Errorf("解析失败：%q", c.in)
			continue
		}
		got := time.UnixMilli(ms).UTC().Format("2006-01-02 15:04:05")
		if got != c.want {
			t.Errorf("解析 %q\n  得到 %s\n  期望 %s", c.in, got, c.want)
		}
	}

	// 解析不了的要返回 0，好让上层退回文件 mtime，而不是塞个错值进去
	for _, bad := range []string{"", "很快恢复", "2026-13-45 99:99:99 UTC+8"} {
		if got := parseResetMs(bad); got != 0 {
			t.Errorf("%q 应解析失败返回 0，却得到 %d", bad, got)
		}
	}
}

// TestSurveyLimitsOrdersByEventTime 钉住排序依据。
//
// 曾经一律按文件修改时间排，结果"当前状态"显示 9-18，而历史里躺着更晚的
// 9-20——自相矛盾。排序必须看事件本身的时间。
func TestSurveyLimitsOrdersByEventTime(t *testing.T) {
	probe := hermeticProbe(t)
	dir := filepath.Join(probe.DataDir(variant.CN), "projects", "c-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "older.jsonl")
	newer := filepath.Join(dir, "newer.jsonl")
	mustWrite(t, older, `{"content":"429 您的使用量已超出频率限制，将在 2026-09-18 21:27:01 UTC+8 重置"}`+"\n")
	mustWrite(t, newer, `{"content":"429 您的使用量已超出频率限制，将在 2026-09-20 10:16:13 UTC+8 重置"}`+"\n")

	// 故意让"时间更早"的那个文件 mtime 更新——若还按 mtime 排序就会颠倒
	back := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, back, back); err != nil {
		t.Fatal(err)
	}
	earlier := back.Add(-48 * time.Hour)
	if err := os.Chtimes(newer, earlier, earlier); err != nil {
		t.Fatal(err)
	}

	s := SurveyLimits(probe)
	if len(s.Limits) != 2 {
		t.Fatalf("应找到 2 条，得到 %d 条", len(s.Limits))
	}
	if !strings.Contains(s.Limits[0].ResetAt, "2026-09-20") {
		t.Errorf("排序应按事件时间，最新的 9-20 要排第一；实际第一条是 %q", s.Limits[0].ResetAt)
	}
	got, ok := s.Latest["cn"]
	if !ok || !strings.Contains(got.ResetAt, "2026-09-20") {
		t.Errorf("cn 当前状态应是 9-20 那条，得到 %q", got.ResetAt)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", path, err)
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

// ---------- 消耗统计 ----------

// TestRowToCostSumsCredits 用真实库里的形状钉住解析。
//
// 实测（2026-09-26，真实 workbuddy.db）：credit_json 的键是不透明哈希，
// 值带 5.8900000000000015 这样的浮点尾巴。总量必须归整到两位小数，
// 否则界面和 JSON 里全是脏数。
func TestRowToCostSumsCredits(t *testing.T) {
	row := map[string]any{
		"session_id":  "00f182b4-568f-4cdd-8157-56effb5ccad8",
		"title":       "调试 wbmux 后台命令启动失败",
		"updated_at":  float64(1790398295856),
		"used":        float64(372571),
		"size":        float64(1000000),
		"credit_json": `{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":5.89,"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb":0.11}`,
	}
	c := rowToCost("cn", row)
	if c.Credits != 6 {
		t.Errorf("总量应为 6（尾巴已归整），得到 %v", c.Credits)
	}
	if c.Parts != 2 {
		t.Errorf("分项应为 2，得到 %d", c.Parts)
	}
	if c.Title != "调试 wbmux 后台命令启动失败" {
		t.Errorf("标题没取到：%q", c.Title)
	}
	if c.UpdatedAt != 1790398295856 {
		t.Errorf("毫秒时间戳没取到：%d", c.UpdatedAt)
	}
	if c.Used != 372571 || c.Size != 1000000 {
		t.Errorf("used/size 没取到：%d/%d", c.Used, c.Size)
	}
	if c.Side != "cn" {
		t.Errorf("归属判错：%q", c.Side)
	}
}

// TestRowToCostToleratesGarbage 钉住"取不到就留零值，绝不 panic"。
//
// 这张表是客户端内部实现，credit_json 实测可以为 NULL（intl 侧就有），
// 哪天列名一改、内容一变，解析会全方位失败——失败要退化成零值，
// 不能让整栏消失或者进程崩掉。
func TestRowToCostToleratesGarbage(t *testing.T) {
	cases := []struct {
		name string
		row  map[string]any
	}{
		{"credit_json 为 nil（SQL NULL）", map[string]any{"session_id": "s1"}},
		{"credit_json 是空串", map[string]any{"credit_json": ""}},
		{"credit_json 是 None", map[string]any{"credit_json": "None"}},
		{"credit_json 不是合法 JSON", map[string]any{"credit_json": "不是json"}},
		{"credit_json 是数组不是对象", map[string]any{"credit_json": `[1,2]`}},
		{"整行几乎为空", map[string]any{}},
	}
	for _, c := range cases {
		got := rowToCost("cn", c.row)
		if got.Credits != 0 || got.Parts != 0 {
			t.Errorf("%s：应得零值，得到 credits=%v parts=%d", c.name, got.Credits, got.Parts)
		}
		if got.Side != "cn" {
			t.Errorf("%s：归属丢了", c.name)
		}
	}
}

// TestRowToCostAcceptsIntAndFloatNumbers 钉住数值列的双重来源。
//
// SQLite 结果经 JSON 桥过来，整数可能被解成 float64，也可能保持 int64；
// 两边都必须接住，否则同一列一会儿有值一会儿是 0，用户看到的数字忽有忽无。
func TestRowToCostAcceptsIntAndFloatNumbers(t *testing.T) {
	base := map[string]any{
		"credit_json": `{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa":1.5}`,
	}
	asFloat := map[string]any{}
	for k, v := range base {
		asFloat[k] = v
	}
	asFloat["updated_at"] = float64(1790398295856)
	asFloat["used"] = float64(100)
	asFloat["size"] = float64(1000)

	asInt := map[string]any{}
	for k, v := range base {
		asInt[k] = v
	}
	asInt["updated_at"] = int64(1790398295856)
	asInt["used"] = int64(100)
	asInt["size"] = int64(1000)

	for name, row := range map[string]map[string]any{"float64": asFloat, "int64": asInt} {
		c := rowToCost("cn", row)
		if c.UpdatedAt != 1790398295856 || c.Used != 100 || c.Size != 1000 {
			t.Errorf("%s：数值列没接住 → updated=%d used=%d size=%d",
				name, c.UpdatedAt, c.Used, c.Size)
		}
	}
}

// TestAttachModels 钉住"模型信息从会话日志来"。
//
// credit_json 的分项键按请求记哈希，拆不到模型；会话日志的 "model"
// 字段是本机唯一的数据源。三个点都要钉住：去重按首次出现、
// 跨工作区目录也能找到、没有日志的会话留空而不是报错。
func TestAttachModels(t *testing.T) {
	probe := hermeticProbe(t)
	writeSession(t, probe, variant.CN, "c-a", "11111111-aaaa.jsonl",
		`{"type":"message","role":"assistant","model":"deepseek-v4.1-flash","content":"你好"}`+"\n"+
			`{"type":"message","role":"assistant","model":"glm-5.2","content":"继续"}`+"\n"+
			`{"type":"message","role":"assistant","model":"deepseek-v4.1-flash","content":"好"}`+"\n")
	writeSession(t, probe, variant.CN, "c-b", "22222222-bbbb.jsonl",
		`{"model":"hy4-preview-f"}`+"\n")
	// 真实日志里模型多在 providerData/model（嵌套），只看顶层一条都抓不到
	writeSession(t, probe, variant.CN, "c-c", "44444444-dddd.jsonl",
		`{"id":"x","providerData":{"model":"gpt-6-astra"},"content":"hi"}`+"\n"+
			`{"id":"y","providerData":{"model":"deepseek-v4.1-flash"},"content":"hi"}`+"\n")

	costs := []SessionCost{
		{Side: "cn", SessionID: "11111111-aaaa"},
		{Side: "cn", SessionID: "22222222-bbbb"},
		{Side: "cn", SessionID: "44444444-dddd"},
		{Side: "cn", SessionID: "33333333-cccc"}, // 没有日志文件
		{Side: "intl", SessionID: "11111111-aaaa"},
	}
	attachModels(probe, variant.CN, costs)

	if got := strings.Join(costs[0].Models, ","); got != "deepseek-v4.1-flash,glm-5.2" {
		t.Errorf("模型应按首次出现去重，得到 %q", got)
	}
	if len(costs[1].Models) != 1 || costs[1].Models[0] != "hy4-preview-f" {
		t.Errorf("跨工作区会话的模型没找到：%v", costs[1].Models)
	}
	if got := strings.Join(costs[2].Models, ","); got != "gpt-6-astra,deepseek-v4.1-flash" {
		t.Errorf("嵌套在 providerData 里的模型没抓到：%q", got)
	}
	if len(costs[3].Models) != 0 {
		t.Errorf("没有日志的会话应留空：%v", costs[3].Models)
	}
	// 关键防回归：attachModels 只填本侧。intl 轮进来时 cn 的已填好，
	// 不能因为 intl 日志里查不到就把它抹成空。
	if len(costs[0].Models) != 2 {
		t.Errorf("cn 条目的模型被另一轮扫描抹掉了：%v", costs[0].Models)
	}
	costs[3].Side = "intl"
	attachModels(probe, variant.Intl, costs)
	if len(costs[0].Models) != 2 {
		t.Errorf("intl 轮扫描后 cn 条目的模型被抹掉了：%v", costs[0].Models)
	}
}

// TestSurveyLimitsAttributesModelFromContext 钉住模型的滚动归属。
//
// 真实日志里 429 行自己往往不带 model（实测 45 条里 16 条没有），
// 被限的是"当时正在用的模型"——在 429 前面几行的请求里。
// 用真实事件钉住：2026-09-27 那次 Hy4 preview 超限，429 行无 model，
// 往前最近一次出现的就是 hy4-preview-f（与客户端横幅一致）。
func TestSurveyLimitsAttributesModelFromContext(t *testing.T) {
	probe := hermeticProbe(t)
	writeSession(t, probe, variant.CN, "c-a", "aaaa.jsonl",
		`{"id":"r1","providerData":{"model":"hy4-preview-f"}}`+"\n"+
			`{"id":"r2","providerData":{"model":"hy4-preview-f"}}`+"\n"+
			`{"content":[{"type":"output_text","text":"429 您的使用量已超出频率限制，将在 2026-09-27 11:19:18 UTC+8 重置，您也可以切换其他模型继续使用。"}]}`+"\n")
	// 前面完全没有 model 字段：宁可留空，不拿别的会话的模型充数
	writeSession(t, probe, variant.CN, "c-b", "bbbb.jsonl",
		`{"content":[{"type":"output_text","text":"429 您的使用量已超出频率限制，将在 2026-09-28 10:00:00 UTC+8 重置"}]}`+"\n")

	s := SurveyLimits(probe)
	if len(s.Limits) != 2 {
		t.Fatalf("应找到 2 条，得到 %d 条", len(s.Limits))
	}
	if !strings.Contains(s.Limits[0].ResetAt, "2026-09-28") {
		t.Fatalf("9-28 的事件应排最前，实际 %q", s.Limits[0].ResetAt)
	}
	if s.Limits[0].Model != "" {
		t.Errorf("前面没有 model 字段就该留空，得到 %q", s.Limits[0].Model)
	}
	if s.Limits[1].Model != "hy4-preview-f" {
		t.Errorf("模型应从 429 前面的请求行归属，得到 %q", s.Limits[1].Model)
	}
	// Latest 是最新一条（9-28，无模型），带模型的是历史里的 9-27 那条
	if got, ok := s.Latest["cn"]; !ok || !strings.Contains(got.ResetAt, "2026-09-28") {
		t.Errorf("Latest 应取最新事件：%+v", got)
	}
}

// TestSurveyLimitsByModel 钉住"每侧每模型各取最新"。
//
// 免费额度按模型单独计：hy4 和 deepseek 可以同时被限着，各有各的
// 重置时间。只看每侧最新一条会漏掉另一个模型的限制——用户就遇到过
// "deepseek 也超频了但界面上看不见"。
func TestSurveyLimitsByModel(t *testing.T) {
	probe := hermeticProbe(t)
	writeSession(t, probe, variant.CN, "c-a", "aaaa.jsonl",
		`{"providerData":{"model":"hy4-preview-f"}}`+"\n"+
			`{"content":[{"type":"output_text","text":"429 您的使用量已超出频率限制，将在 2026-09-27 11:19:18 UTC+8 重置"}]}`+"\n")
	// deepseek 被限得更晚，还故意在同侧留一条更早的同模型记录，验证去重
	writeSession(t, probe, variant.CN, "c-b", "bbbb.jsonl",
		`{"providerData":{"model":"deepseek-v4.1-flash"}}`+"\n"+
			`{"content":[{"type":"output_text","text":"429 您的使用量已超出频率限制，将在 2026-09-26 13:40:52 UTC+8 重置"}]}`+"\n"+
			`{"providerData":{"model":"deepseek-v4.1-flash"}}`+"\n"+
			`{"content":[{"type":"output_text","text":"429 您的使用量已超出频率限制，将在 2026-09-28 09:30:00 UTC+8 重置"}]}`+"\n")

	s := SurveyLimits(probe)
	if len(s.ByModel) != 2 {
		t.Fatalf("两个模型应各一条，得到 %d 条：%+v", len(s.ByModel), s.ByModel)
	}
	// 按时间倒序：9-28 的 deepseek 在前
	if s.ByModel[0].Model != "deepseek-v4.1-flash" || !strings.Contains(s.ByModel[0].ResetAt, "2026-09-28") {
		t.Errorf("第一条应是 deepseek 的 9-28：%+v", s.ByModel[0])
	}
	if s.ByModel[1].Model != "hy4-preview-f" || !strings.Contains(s.ByModel[1].ResetAt, "2026-09-27") {
		t.Errorf("第二条应是 hy4 的 9-27：%+v", s.ByModel[1])
	}
	// 同模型同侧只留最新：deepseek 的 9-26 那条不能出现
	for _, e := range s.ByModel {
		if strings.Contains(e.ResetAt, "2026-09-26") {
			t.Errorf("同模型的旧记录没去重：%+v", e)
		}
	}
	// ResetMs 供界面判断"重置时间是否已过"
	if s.ByModel[1].ResetMs == 0 {
		t.Errorf("resetMs 应解析出来，不能全靠原文：%+v", s.ByModel[1])
	}
}
