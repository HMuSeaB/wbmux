package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// hermeticProbe 返回一个完全脱离真实机器的探测器。
//
// 关键在 Exists 只认假 HOME 底下的路径：否则 Detect 会顺着盘符根目录
// （D:\WorkBuddyAI 之类）找到开发机上真实存在的安装，测试结果就会随
// 机器不同而漂移。本包里凡是会读系统资源的路径都必须这样断掉。
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
		ReadFile: os.ReadFile,
		Registry: nil,
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录 %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写 %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return string(raw)
}

// TestCompressCwd 用本机实测到的 11 个目录名做回归。
//
// 这些样本是从国内版 9 个、国际版 2 个真实 projects 子目录里抄下来的，
// 覆盖了盘符大小写、正反斜杠、路径含空格、路径含全角冒号几种情况。
// 规则一旦被改坏，这里会立刻响。
func TestCompressCwd(t *testing.T) {
	cases := []struct{ cwd, want string }{
		{`c:\Users\36230\Desktop\Competition\竞赛汇总`, "c-Users-36230-Desktop-Competition-竞赛汇总"},
		{`c:\Users\36230\WorkBuddy AI`, "c-Users-36230-WorkBuddy AI"},
		{`c:\Users\36230\WorkBuddy\2026-09-19-17-39-54`, "c-Users-36230-WorkBuddy-2026-09-19-17-39-54"},
		{`c:\Users\36230\WorkBuddy\Claw`, "c-Users-36230-WorkBuddy-Claw"},
		{`d:\4rchive\Campus`, "d-4rchive-Campus"},
		{`d:\4rchive\Campus\jcg-q20-campus-net`, "d-4rchive-Campus-jcg-q20-campus-net"},
		{`d:\4rchive\Code\10：微信开发工具 解压密码：123`, "d-4rchive-Code-10：微信开发工具 解压密码：123"},
		{`d:\4rchive\Code\SlateTerm`, "d-4rchive-Code-SlateTerm"},
		{`d:\4rchive\Code\cc-switch`, "d-4rchive-Code-cc-switch"},
		{`c:\Users\36230\WorkBuddy AI\2026-09-25-11-15-46`, "c-Users-36230-WorkBuddy AI-2026-09-25-11-15-46"},
		{`d:\4rchive\Code\DOUZHANZHE-Control`, "d-4rchive-Code-DOUZHANZHE-Control"},
		// 数据库里存的是正斜杠写法，也要算出同一个目录名
		{`C:/Users/36230/WorkBuddy AI`, "c-Users-36230-WorkBuddy AI"},
		{``, ""},
	}
	for _, c := range cases {
		if got := CompressCwd(c.cwd); got != c.want {
			t.Errorf("CompressCwd(%q)\n  得到 %q\n  期望 %q", c.cwd, got, c.want)
		}
	}
}

// TestScanSessions 覆盖扫描的三种排除条件与文件收集。
func TestScanSessions(t *testing.T) {
	dir := t.TempDir()
	slug := "c-Users-me-proj"
	base := filepath.Join(dir, "projects", slug)

	// 正常会话：带 meta 与 rollback 两个附属文件
	mustWrite(t, filepath.Join(base, "aaaa.jsonl"),
		`{"type":"message","cwd":"c:\\Users\\me\\proj","timestamp":1}`+"\n"+
			`{"type":"ai-title","aiTitle":"标题甲"}`+"\n")
	mustWrite(t, filepath.Join(base, "aaaa.meta.json"), `{}`)
	mustWrite(t, filepath.Join(base, "aaaa.file-rollback.ndjson"), "")

	// 只有 jsonl，没有附属文件
	mustWrite(t, filepath.Join(base, "bbbb.jsonl"),
		`{"type":"message","cwd":"c:\\Users\\me\\proj","timestamp":2}`+"\n")

	// 空文件：客户端写了一半的残留，必须跳过
	mustWrite(t, filepath.Join(base, "cccc.jsonl"), "")

	// 带 .quickask 标记的划词临时会话，必须跳过
	mustWrite(t, filepath.Join(base, "dddd.jsonl"), `{"type":"message","cwd":"x"}`+"\n")
	mustWrite(t, filepath.Join(base, "dddd.quickask"), "")

	// 目录下的无关文件
	mustWrite(t, filepath.Join(base, "notes.txt"), "hello")

	got, err := scanSessions(dir)
	if err != nil {
		t.Fatalf("scanSessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应扫到 2 条会话，得到 %d 条：%v", len(got), keysOf(got))
	}

	a, ok := got["aaaa"]
	if !ok {
		t.Fatal("缺少会话 aaaa")
	}
	if a.Meta == "" || a.Rollback == "" {
		t.Errorf("aaaa 的附属文件没被收齐: meta=%q rollback=%q", a.Meta, a.Rollback)
	}
	if a.Head.Title != "标题甲" {
		t.Errorf("标题解析错误: %q", a.Head.Title)
	}
	if a.Head.Cwd != `c:\Users\me\proj` {
		t.Errorf("cwd 解析错误: %q", a.Head.Cwd)
	}
	if b := got["bbbb"]; b.Meta != "" || b.Rollback != "" {
		t.Errorf("bbbb 本不该有附属文件")
	}
}

// TestApplySkillsNeverOverwrites 验证技能搬运的"只新增"约束。
//
// 这是本包最不能出错的一条：目标端已有的技能文件一个字节都不能变。
func TestApplySkillsNeverOverwrites(t *testing.T) {
	probe := hermeticProbe(t)
	src := filepath.Join(probe.Home, ".workbuddy-ai")
	dst := filepath.Join(probe.Home, ".workbuddy")

	mustWrite(t, filepath.Join(src, "skills", "alpha", "SKILL.md"), "# alpha 来自国际版\n")
	mustWrite(t, filepath.Join(src, "skills", "beta", "SKILL.md"), "# beta\n")
	mustWrite(t, filepath.Join(src, "skills", "beta", "scripts", "run.py"), "print(1)\n")

	// 目标端已有同名技能，内容不同
	const mine = "# 我自己改过的 alpha\n"
	mustWrite(t, filepath.Join(dst, "skills", "alpha", "SKILL.md"), mine)

	// 管理用文件不是技能目录，不该被搬
	mustWrite(t, filepath.Join(src, "skills", "_bm_skillid_migration.json"), `{"x":1}`)

	opts := Options{Source: variant.Intl, Target: variant.CN, Probe: probe}
	rep, err := Apply(opts, Selection{Kinds: []Kind{KindSkills}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := mustRead(t, filepath.Join(dst, "skills", "alpha", "SKILL.md")); got != mine {
		t.Errorf("目标端已有技能被改动了！\n  得到 %q\n  期望 %q", got, mine)
	}
	if got := mustRead(t, filepath.Join(dst, "skills", "beta", "SKILL.md")); got != "# beta\n" {
		t.Errorf("beta 没被正确搬运: %q", got)
	}
	if got := mustRead(t, filepath.Join(dst, "skills", "beta", "scripts", "run.py")); got != "print(1)\n" {
		t.Errorf("子目录文件没被搬运: %q", got)
	}
	if fileExists(filepath.Join(dst, "skills", "_bm_skillid_migration.json")) {
		t.Error("管理用文件被当成技能搬过去了")
	}
	if rep.SkippedItems != 1 {
		t.Errorf("整项跳过应为 1，得到 %d", rep.SkippedItems)
	}
	if rep.CopiedFiles != 2 {
		t.Errorf("复制文件数应为 2，得到 %d", rep.CopiedFiles)
	}
}

// TestApplySkillsIsIdempotent 连跑两次不该产生任何新增。
func TestApplySkillsIsIdempotent(t *testing.T) {
	probe := hermeticProbe(t)
	mustWrite(t, filepath.Join(probe.Home, ".workbuddy-ai", "skills", "alpha", "SKILL.md"), "# alpha\n")

	opts := Options{Source: variant.Intl, Target: variant.CN, Probe: probe}
	first, err := Apply(opts, Selection{Kinds: []Kind{KindSkills}})
	if err != nil {
		t.Fatalf("第一次 Apply: %v", err)
	}
	if first.CopiedFiles != 1 {
		t.Fatalf("第一次应复制 1 个文件，得到 %d", first.CopiedFiles)
	}

	second, err := Apply(opts, Selection{Kinds: []Kind{KindSkills}})
	if err != nil {
		t.Fatalf("第二次 Apply: %v", err)
	}
	if second.CopiedFiles != 0 || second.SkippedItems != 1 {
		t.Errorf("第二次应整项跳过，得到 copied=%d skippedItems=%d",
			second.CopiedFiles, second.SkippedItems)
	}
}

// TestApplyMemoryRules 覆盖记忆搬运的三条判定。
func TestApplyMemoryRules(t *testing.T) {
	const uidSrc = "11111111-1111-1111-1111-111111111111"
	const uidDst = "22222222-2222-2222-2222-222222222222"

	memFile := func(uid, block string) string {
		return "# User Memory Profile\n\n## Memory Block\n\n" + block +
			"\n\n<!-- RAW_JSON_START\n{\"uid\":\"" + uid + "\",\"memoryBlock\":\"" + block +
			"\"}\nRAW_JSON_END -->\n"
	}

	t.Run("来源为空则跳过", func(t *testing.T) {
		probe := hermeticProbe(t)
		mustWrite(t, filepath.Join(probe.Home, ".workbuddy-ai", "memory", uidSrc+"_memory.md"), memFile(uidSrc, ""))
		mustWrite(t, filepath.Join(probe.Home, ".workbuddy", "memory", uidDst+"_memory.md"), memFile(uidDst, ""))

		rep, err := Apply(Options{Source: variant.Intl, Target: variant.CN, Probe: probe},
			Selection{Kinds: []Kind{KindMemory}})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if rep.CopiedFiles != 0 || rep.SkippedItems != 1 {
			t.Errorf("来源为空时不该搬运，得到 copied=%d skipped=%d", rep.CopiedFiles, rep.SkippedItems)
		}
	})

	t.Run("目标非空则不覆盖", func(t *testing.T) {
		probe := hermeticProbe(t)
		mustWrite(t, filepath.Join(probe.Home, ".workbuddy-ai", "memory", uidSrc+"_memory.md"), memFile(uidSrc, "国际版的内容"))
		dstPath := filepath.Join(probe.Home, ".workbuddy", "memory", uidDst+"_memory.md")
		mustWrite(t, dstPath, memFile(uidDst, "国内版本来就有内容"))

		rep, err := Apply(Options{Source: variant.Intl, Target: variant.CN, Probe: probe},
			Selection{Kinds: []Kind{KindMemory}})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if rep.CopiedFiles != 0 || rep.SkippedItems != 1 {
			t.Errorf("两端都有内容时不该搬运，得到 copied=%d skipped=%d", rep.CopiedFiles, rep.SkippedItems)
		}
		if !strings.Contains(mustRead(t, dstPath), "国内版本来就有内容") {
			t.Error("目标端记忆被覆盖了")
		}
	})

	t.Run("目标为空则搬入并换成目标端 uid", func(t *testing.T) {
		probe := hermeticProbe(t)
		mustWrite(t, filepath.Join(probe.Home, ".workbuddy-ai", "memory", uidSrc+"_memory.md"), memFile(uidSrc, "值得搬的内容"))
		dstPath := filepath.Join(probe.Home, ".workbuddy", "memory", uidDst+"_memory.md")
		mustWrite(t, dstPath, memFile(uidDst, ""))

		rep, err := Apply(Options{Source: variant.Intl, Target: variant.CN, Probe: probe},
			Selection{Kinds: []Kind{KindMemory}})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if rep.CopiedFiles != 1 {
			t.Fatalf("应复制 1 个文件，得到 %d", rep.CopiedFiles)
		}
		// 必须落在目标端自己的 uid 文件名上，否则客户端不会读它
		if !strings.Contains(mustRead(t, dstPath), "值得搬的内容") {
			t.Error("记忆内容没被搬过来")
		}
		if fileExists(filepath.Join(probe.Home, ".workbuddy", "memory", uidSrc+"_memory.md")) {
			t.Error("不该按来源端 uid 落文件")
		}
	})
}

// TestSurveyRejectsSameVariant 挡掉"从自己搬到自己"。
func TestSurveyRejectsSameVariant(t *testing.T) {
	probe := hermeticProbe(t)
	if _, err := Survey(Options{Source: variant.CN, Target: variant.CN, Probe: probe}); err == nil {
		t.Error("来源与目标相同时应当报错")
	}
}

// TestBuildWorkSessionsKeepsFilesOutOfSoftSkip 覆盖"目标端有文件但没索引行"
// 这一种症状——那正是"手动拷过文件、列表却空着"的情形，必须仍然补索引。
func TestBuildWorkSessionsKeepsFilesOutOfSoftSkip(t *testing.T) {
	probe := hermeticProbe(t)
	slug := "c-Users-me-proj"
	// 这里用正斜杠是有意的：JSON 字符串里 `\U` 不是合法转义，
	// 反斜杠路径必须先转义才能进 JSON，写错了会让整行解析失败、
	// 表现为"读不到 cwd"。正斜杠同样能被 CompressCwd 归一到这个目录名。
	cwd := "c:/Users/me/proj"

	mustWrite(t, filepath.Join(probe.Home, ".workbuddy-ai", "projects", slug, "aaaa.jsonl"),
		`{"type":"message","cwd":"`+cwd+`","timestamp":1}`+"\n"+`{"type":"ai-title","aiTitle":"标题甲"}`+"\n")

	// 目标端已经有同名文件，但没有数据库（读索引会失败并降级为警告）
	mustWrite(t, filepath.Join(probe.Home, ".workbuddy", "projects", slug, "aaaa.jsonl"), "{}\n")

	items, warns, err := buildWork(
		Options{Source: variant.Intl, Target: variant.CN, Probe: probe},
		Selection{Kinds: []Kind{KindSessions}})
	if err != nil {
		t.Fatalf("buildWork: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应有 1 个待办项，得到 %d", len(items))
	}
	it := items[0]
	if it.skip {
		t.Fatalf("不该整项跳过，理由：%s", it.skipWhy)
	}
	if it.row == nil {
		t.Fatal("应当生成了索引行")
	}
	if it.row.UserID != "" {
		t.Errorf("索引行的 user_id 必须置空串才能对当前用户可见，得到 %q", it.row.UserID)
	}
	if it.row.ID != "aaaa" {
		t.Errorf("索引行 id 错误: %q", it.row.ID)
	}
	// 目标目录不对时不该瞎猜，这里确认落点是按 cwd 算出来的
	wantDst := filepath.Join(probe.Home, ".workbuddy", "projects", slug, "aaaa.jsonl")
	if len(it.files) == 0 || it.files[0].dst != wantDst {
		t.Errorf("文件落点错误，期望 %s", wantDst)
	}
	if len(warns) == 0 {
		t.Error("读不到索引时应当给出警告")
	}
}

// TestBuildWorkSessionsSkipsWhenNoCwd 读不到 cwd 的会话必须跳过并说明原因，
// 而不是凭猜造一个工作区目录出来。
func TestBuildWorkSessionsSkipsWhenNoCwd(t *testing.T) {
	probe := hermeticProbe(t)
	mustWrite(t, filepath.Join(probe.Home, ".workbuddy-ai", "projects", "some-slug", "aaaa.jsonl"),
		`{"type":"message","timestamp":1}`+"\n")

	items, _, err := buildWork(
		Options{Source: variant.Intl, Target: variant.CN, Probe: probe},
		Selection{Kinds: []Kind{KindSessions}})
	if err != nil {
		t.Fatalf("buildWork: %v", err)
	}
	if len(items) != 1 || !items[0].skip {
		t.Fatalf("缺 cwd 的会话应当被跳过，得到 %+v", items)
	}
	if items[0].skipWhy == "" {
		t.Error("跳过时必须写明原因")
	}
}

func keysOf(m map[string]sessionFiles) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPathRewritesCoversAllThreeForms 验证三种书写形态都能被覆盖。
//
// 同一个路径在会话文件里可能以三种形态出现，漏掉任何一种都会有一半引用
// 改不到——而漏改的表现是"图片静默裂掉"，不会报错，所以这里必须逐条验。
func TestPathRewritesCoversAllThreeForms(t *testing.T) {
	src := `C:\Users\me\.workbuddy-ai`
	dst := `C:\Users\me\.workbuddy`
	rs := pathRewrites(src, dst)
	if len(rs) != 3 {
		t.Fatalf("应给出 3 条替换规则，得到 %d", len(rs))
	}

	cases := []struct{ in, want string }{
		{
			// JSON 字符串里的转义形态
			`"p":"C:\\Users\\me\\.workbuddy-ai\\blobs\\ab\\x.png"`,
			`"p":"C:\\Users\\me\\.workbuddy\\blobs\\ab\\x.png"`,
		},
		{
			// 正文里的原样形态
			`见 C:\Users\me\.workbuddy-ai\clipboard-images\y.png 这张图`,
			`见 C:\Users\me\.workbuddy\clipboard-images\y.png 这张图`,
		},
		{
			// 正斜杠形态
			`path=C:/Users/me/.workbuddy-ai/blobs/cd/z.png`,
			`path=C:/Users/me/.workbuddy/blobs/cd/z.png`,
		},
	}

	for _, c := range cases {
		got := c.in
		for _, r := range rs {
			got = strings.ReplaceAll(got, r[0], r[1])
		}
		if got != c.want {
			t.Errorf("改写不完整\n  输入 %s\n  得到 %s\n  期望 %s", c.in, got, c.want)
		}
	}

	// 不该动到别的目录
	other := `C:\Users\me\.workbuddy-ai-backup\x.png`
	got := other
	for _, r := range rs {
		got = strings.ReplaceAll(got, r[0], r[1])
	}
	if got != other {
		t.Errorf("误改了前缀相近但不同的目录: %s", got)
	}
}

// TestCopyFileWithRewrites 验证改写落盘、不覆盖、且回报改写次数。
func TestCopyFileWithRewrites(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "in.jsonl")
	dst := filepath.Join(dir, "out", "in.jsonl")
	src2 := `C:\Users\me\.workbuddy-ai`

	// 三种形态各出现一次
	mustWrite(t, src,
		`{"a":"C:\\Users\\me\\.workbuddy-ai\\blobs\\ab\\x.png"}`+"\n"+
			"见 C:\\Users\\me\\.workbuddy-ai\\clipboard-images\\y.png\n"+
			"path=C:/Users/me/.workbuddy-ai/blobs/cd/z.png\n")

	rs := pathRewrites(src2, `C:\Users\me\.workbuddy`)
	n, err := copyFileWithRewrites(src, dst, rs)
	if err != nil {
		t.Fatalf("copyFileWithRewrites: %v", err)
	}
	if n != 3 {
		t.Errorf("应改写 3 处，得到 %d", n)
	}

	out := mustRead(t, dst)
	if strings.Contains(out, ".workbuddy-ai") {
		t.Errorf("仍残留来源端路径:\n%s", out)
	}
	if !strings.Contains(out, `.workbuddy\\blobs\\ab\\x.png`) {
		t.Errorf("转义形态没改对:\n%s", out)
	}

	// 再写一次必须被挡下，绝不覆盖
	if _, err := copyFileWithRewrites(src, dst, rs); !errors.Is(err, errExists) {
		t.Errorf("目标已存在时应返回 errExists，得到 %v", err)
	}
}

// TestCollectAssetRefs 验证只收被引用到的附件，且忽略不存在与越界的。
func TestCollectAssetRefs(t *testing.T) {
	probe := hermeticProbe(t)
	dataDir := filepath.Join(probe.Home, ".workbuddy-ai")
	dir := filepath.Join(dataDir, "projects", "c-Users-me-proj")

	// 真的存在、且被引用
	mustWrite(t, filepath.Join(dataDir, "blobs", "ab", "used.png"), "PNG")
	mustWrite(t, filepath.Join(dataDir, "clipboard-images", "clip.png"), "PNG")
	// 存在但没被引用 —— 不该被收
	mustWrite(t, filepath.Join(dataDir, "blobs", "cd", "unused.png"), "PNG")

	// 会话文件里写的是**真实的**数据目录路径，不能写死一个假的——
	// collectAssetRefs 只收落在来源数据目录下的引用，路径对不上就会收成 0 个。
	// 转义形态（双反斜杠）正是它在 JSON 字符串里的样子。
	escDir := strings.ReplaceAll(dataDir, `\`, `\\`)

	mustWrite(t, filepath.Join(dir, "aaaa.jsonl"),
		`{"type":"message","cwd":"c:/Users/me/proj","timestamp":1}`+"\n"+
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"`+
			`抄送 `+escDir+`\\blobs\\ab\\used.png 与 `+
			escDir+`\\clipboard-images\\clip.png `+
			`还有个不存在的 `+escDir+`\\blobs\\ff\\gone.png `+
			`以及别处的 D:\\other\\blobs\\zz\\n.png `+
			// 正斜杠写法：正文里的路径是模型生成的，实测出现过这种形态。
			// 收不到就说明正则只认反斜杠，在 macOS/Linux 上会漏掉全部附件。
			`再来一次正斜杠写法 `+strings.ReplaceAll(escDir, `\`, `/`)+`/blobs/ab/used.png"}]}`+"\n")

	sessions, err := scanSessions(dataDir)
	if err != nil {
		t.Fatalf("scanSessions: %v", err)
	}
	refs, err := collectAssetRefs(dataDir, sessions)
	if err != nil {
		t.Fatalf("collectAssetRefs: %v", err)
	}

	// 期望值必须按当前平台的分隔符拼，不能写死反斜杠——
	// 否则这个用例在 macOS/Linux 上会假失败。
	want := []string{
		filepath.Join("blobs", "ab", "used.png"),
		filepath.Join("clipboard-images", "clip.png"),
	}
	if len(refs) != len(want) {
		t.Fatalf("应收 %d 个附件，得到 %d 个：%v", len(want), len(refs), refs)
	}
	for _, w := range want {
		if _, ok := refs[w]; !ok {
			t.Errorf("缺少附件 %s；实际收到 %v", w, refs)
		}
	}
	if _, ok := refs[filepath.Join("blobs", "cd", "unused.png")]; ok {
		t.Error("没被引用到的附件不该被收")
	}
}

// TestApplyContentCopiesFilesAndSkipsExisting 验证内容类文件的只新增语义。
func TestApplyContentCopiesFilesAndSkipsExisting(t *testing.T) {
	probe := hermeticProbe(t)
	src := filepath.Join(probe.Home, ".workbuddy-ai")
	dst := filepath.Join(probe.Home, ".workbuddy")

	mustWrite(t, filepath.Join(src, "SOUL.md"), "国际版的人格\n")
	mustWrite(t, filepath.Join(src, "models.json"), `{"m":1}`)
	mustWrite(t, filepath.Join(src, "connectors", "a.json"), `{"c":1}`)

	// 目标端已有 SOUL.md，且内容不同 —— 绝不能被盖掉
	const mine = "国内版的人格\n"
	mustWrite(t, filepath.Join(dst, "SOUL.md"), mine)

	// 设备绑定文件不该被搬
	mustWrite(t, filepath.Join(src, "keyblob"), "SECRET")

	rep, err := Apply(Options{Source: variant.Intl, Target: variant.CN, Probe: probe},
		Selection{Kinds: []Kind{KindContent}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := mustRead(t, filepath.Join(dst, "SOUL.md")); got != mine {
		t.Errorf("目标端已有文件被覆盖了！得到 %q", got)
	}
	if got := mustRead(t, filepath.Join(dst, "models.json")); got != `{"m":1}` {
		t.Errorf("models.json 没搬过来: %q", got)
	}
	if got := mustRead(t, filepath.Join(dst, "connectors", "a.json")); got != `{"c":1}` {
		t.Errorf("目录内文件没搬过来: %q", got)
	}
	if fileExists(filepath.Join(dst, "keyblob")) {
		t.Error("设备绑定文件 keyblob 被搬过去了——这是个严重问题")
	}
	if rep.SkippedItems != 1 {
		t.Errorf("应整项跳过 1 个（SOUL.md），得到 %d", rep.SkippedItems)
	}
}
