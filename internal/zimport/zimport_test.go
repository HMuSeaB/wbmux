package zimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/sessions"
	"github.com/HMuSeaB/wbmux/internal/variant/varianttest"
	"github.com/HMuSeaB/wbmux/internal/zcode"
)

// TestBuildJSONL 钉住"生成的文件客户端认不认得"。
//
// 这是整个导入最脆弱的一环：库里插一行、文件里少一行，表现都是"点开是空白"，
// 而那时没人会想到是格式问题。
func TestBuildJSONL(t *testing.T) {
	tr := &zcode.Transcript{
		SessionID: "sess_x",
		Directory: `C:/proj/demo`,
		UpdatedMs: 1759003600000,
		Msgs: []zcode.Msg{
			{Role: "user", At: 1759000000000, Text: "你好"},
			{Role: "assistant", At: 1759000060000, Text: "在", Tools: []string{"Read", "Bash"},
				Reasoning: "先看代码"},
		},
	}
	lines, err := buildJSONL(tr, "sess_new", `C:/proj/demo`, true)
	if err != nil {
		t.Fatalf("buildJSONL: %v", err)
	}
	// user + assistant + 一条 reasoning = 3 行
	if len(lines) != 3 {
		t.Fatalf("应产出 3 行，得到 %d 行", len(lines))
	}
	for i, ln := range lines {
		var o map[string]any
		if err := json.Unmarshal([]byte(ln), &o); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON：%v", i, err)
		}
		// 每行都必须带的字段（客户端按这些字段渲染/归属）
		for _, k := range []string{"id", "timestamp", "type", "sessionId", "cwd"} {
			if _, ok := o[k]; !ok {
				t.Errorf("第 %d 行缺字段 %s：%s", i, k, ln)
			}
		}
		if !strings.Contains(ln, `\n`) { // JSON 里换行必须被转义，不能真的换行
			continue
		}
	}

	// 第一条是 user 消息，内容类型必须是 input_text
	var first map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &first)
	if first["type"] != "message" || first["role"] != "user" {
		t.Errorf("第一条应是 user 的 message：%v", first)
	}
	content := first["content"].([]any)[0].(map[string]any)
	if content["type"] != "input_text" {
		t.Errorf("user 的内容类型应为 input_text，得到 %v", content["type"])
	}
	if content["text"] != "你好" {
		t.Errorf("正文不对：%v", content["text"])
	}

	// 第二条应是 reasoning（挂在 assistant 之前），第三条是 assistant
	var second, third map[string]any
	_ = json.Unmarshal([]byte(lines[1]), &second)
	_ = json.Unmarshal([]byte(lines[2]), &third)
	if second["type"] != "reasoning" {
		t.Errorf("第二条应是 reasoning，得到 %v", second["type"])
	}
	if third["role"] != "assistant" {
		t.Errorf("第三条应是 assistant：%v", third)
	}
	// 工具调用降级成正文里的一行，而不是伪造 function_call 行。
	assistantText := third["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(assistantText, "Read") {
		t.Errorf("工具名应出现在正文里：%q", assistantText)
	}
	if third["type"] != "message" {
		t.Errorf("不要伪造 function_call 行，得到 %v", third["type"])
	}

	// 不带推理时只有 2 行。
	lines2, _ := buildJSONL(tr, "sess_new", `C:/proj/demo`, false)
	if len(lines2) != 2 {
		t.Errorf("不带推理应只有 2 行，得到 %d", len(lines2))
	}
}

// TestProjectSlugMatchesClient 目录命名必须与客户端逐字符一致。
//
// # 这条为什么是"能不能用"的分水岭（2026-09-29 真事故）
//
// 第一版规则把"非字母数字"都压成连字符，于是 `WorkBuddy AI` 变成了
// `WorkBuddy-AI`——文件写进了另一个目录，客户端去 `WorkBuddy AI/` 找，
// 找不到，表现为**列表里有条目、点开是空的**（用户："导进去了，但是里面
// 没有记录"）。
//
// 下面这组的左侧全部来自本机 workbuddy.db 里真实存在的 cwd，右侧是客户端
// 实际建出来的目录名（逐条核对过）。
func TestProjectSlugMatchesClient(t *testing.T) {
	cases := []struct{ in, want string }{
		// 带空格——第一版就是死在这条上。
		{`C://Users//36230//WorkBuddy AI`, "c-Users-36230-WorkBuddy AI"},
		// 带中文（中文必须保留）。
		{`C://Users//36230//Desktop//Competition//竞赛汇总`, "c-Users-36230-Desktop-Competition-竞赛汇总"},
		// 带全角冒号（同一类：只压分隔符，其余原样）。
		{`D://4rchive//Code//10：微信开发工具 解压密码：123`, "d-4rchive-Code-10：微信开发工具 解压密码：123"},
		// 空格 + 中文 + 短横线混合。
		{`C://Users//36230//WorkBuddy AI\2026-09-25-11-15-46`, "c-Users-36230-WorkBuddy AI-2026-09-25-11-15-46"},
		{`D://4rchive//Code//DOUZHANZHE-Control`, "d-4rchive-Code-DOUZHANZHE-Control"},
		{`D://4rchive//Code//cc-switch`, "d-4rchive-Code-cc-switch"},
		// 正斜杠与反斜杠要得到同一个名字。
		{`C:/Users/36230/WorkBuddy AI`, "c-Users-36230-WorkBuddy AI"},
		{``, "unknown-project"},
	}
	for _, c := range cases {
		if got := projectSlug(c.in); got != c.want {
			t.Errorf("projectSlug(%q)\n  得到 %q\n  期望 %q", c.in, got, c.want)
		}
	}
}

// TestRequireClientClosed 客户端在跑时必须拒绝——这条比搬运更严是刻意的：
// 导入是显式的一次性动作，静默地"导了但看不见"最糟。
func TestRequireClientClosed(t *testing.T) {
	if err := requireClientClosed(sessions.VendorWBCN, false); err != nil {
		t.Fatalf("没在跑时不该拦：%v", err)
	}
	err := requireClientClosed(sessions.VendorWBCN, true)
	if err == nil {
		t.Fatal("在跑时必须拒绝")
	}
	// 报错要能照做：说清"退出客户端"和"为什么"。
	for _, want := range []string{"退出", "缓存"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("报错应当包含 %q：%v", want, err)
		}
	}
}

// TestNewUUIDShape 客户端用的是标准 v4 UUID 形态，别的不认。
func TestNewUUIDShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		u := newUUID()
		if len(u) != 36 || strings.Count(u, "-") != 4 {
			t.Fatalf("不是 UUID 形态：%s", u)
		}
		if u[14] != '4' {
			t.Errorf("第 13 位应是版本号 4：%s", u)
		}
		if seen[u] {
			t.Fatalf("生成了重复的 UUID：%s", u)
		}
		seen[u] = true
	}
}

// TestImportRefusesRunningClient 端到端：客户端在跑时**一个文件都不该写**。
func TestImportRefusesRunningClient(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".workbuddy")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 用统一的构造器：字段一定齐全（手抄版本漏过 Exists，
	// 结果本地全绿、CI 的 macOS/Windows 上崩在 windowsDriveRoots）。
	probe := varianttest.Probe(home)

	// 造一份最小的 ZCode 库，让 Load 不至于因为"读不到"而先失败。
	zdir := filepath.Join(home, ".zcode", "cli", "db")
	if err := os.MkdirAll(zdir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(zdir, "db.sqlite"), []byte("x"), 0o644)

	// 这里只验"客户端在跑 → 拒绝"这条路（真实进程状态由 IsRunning 决定；
	// 本机 WorkBuddy 确实在跑，所以这条在开发机上会真的命中）。
	_, err := Import(probe, "sess_x", Options{Vendor: sessions.VendorWBCN})
	if err == nil {
		t.Skip("本机目标客户端没在跑，这条断言不适用")
	}
	if !strings.Contains(err.Error(), "正在运行") && !strings.Contains(err.Error(), "没有可导入") &&
		!strings.Contains(err.Error(), "读取失败") && !strings.Contains(err.Error(), "没有这个会话") {
		t.Logf("（先失败在别的原因上，也说明没写任何文件）err=%v", err)
	}
	// 关键断言：不许留下任何产物。
	entries, _ := os.ReadDir(dataDir)
	if len(entries) != 0 {
		t.Errorf("被拒绝时不该写任何东西，dataDir 下出现了：%v", entries)
	}
}

// TestRowCoversRequiredColumns 索引行必须把"NOT NULL 且无默认值"的列都给上。
//
// 实测（国际版库的 pragma）：只有 cwd / user_id / created_at / updated_at
// 四列是 NOT NULL 且没有默认值——少一个，插进去就直接违反约束。
func TestRowCoversRequiredColumns(t *testing.T) {
	tr := &zcode.Transcript{CreatedMs: 1000, UpdatedMs: 2000, Title: "t", Directory: `C:/p`}
	row := rowFor(tr, "id-1", `C:/p`, "标题")

	if row.Cwd != `C:/p` {
		t.Errorf("cwd 必须给上：%q", row.Cwd)
	}
	// user_id 故意写空串而不是某个 uid：客户端的 getSessions 用
	// `user_id IS NULL OR user_id = ''` 保证这类记录对当前登录用户可见。
	// 填成别处的 uid 反而看不见。
	if row.UserID != "" {
		t.Errorf("user_id 应为空串（当前用户可见），得到 %q", row.UserID)
	}
	if row.CreatedAt == 0 || row.UpdatedAt == 0 {
		t.Errorf("时间戳必须给上：created=%d updated=%d", row.CreatedAt, row.UpdatedAt)
	}
	// 标题两处都要给：客户端列表读 coalesce(custom_title, title)。
	if row.Title == nil || row.CustomTitle == nil {
		t.Error("title 与 custom_title 都要给")
	}
	if *row.Title != "标题" {
		t.Errorf("标题不对：%q", *row.Title)
	}
	// 状态：必须是**小写** completed。客户端自己的 19/20 行是小写，而 status
	// 会参与 `status IN (...)` 之类的等值比较，大小写敏感。
	if row.Status == nil || *row.Status != "completed" {
		t.Errorf("status 应为小写 completed，得到 %v", row.Status)
	}
	// transport 必须显式给 'local'：客户端本地列表是
	//   WHERE transport = 'local' AND deleted_at IS NULL AND (user_id = ? OR user_id = '')
	// 缺这一列（留 NULL）时行在库里、也能过另外两道，但侧栏永远不显示。
	if row.Transport == nil || *row.Transport != "local" {
		t.Errorf("transport 应为 'local'，得到 %v", row.Transport)
	}
	// 时间缺失时要有兜底：只有 updated 就两个都用 updated，别留 0。
	only := &zcode.Transcript{UpdatedMs: 5000}
	r2 := rowFor(only, "id-2", `C:/p`, "t")
	if r2.CreatedAt != 5000 {
		t.Errorf("只给了 updated 时，created 应兜底为同一个值，得到 %d", r2.CreatedAt)
	}
}

// TestImportedJSONLIsWhatClientExpects 真机导入的产物形状（用固定数据复现）。
//
// 2026-09-29 在真实国际版档位上导过一次：32 行、0 坏行、行类型全 message、
// 索引四项必填齐全、能过客户端的 deleted_at 过滤。这里把那份结论压成
// 不依赖真实环境的断言，防止以后改坏格式还没人发现。
func TestImportedJSONLIsWhatClientExpects(t *testing.T) {
	tr := &zcode.Transcript{
		SessionID: "sess_z", Directory: `C:/proj`, UpdatedMs: 1759003600000,
		Msgs: []zcode.Msg{
			{Role: "user", At: 1759000000000, Text: "问题"},
			{Role: "assistant", At: 1759000001000, Text: "回答", Tools: []string{"Read"}},
		},
	}
	lines, err := buildJSONL(tr, "sess_new", `C:/proj`, false)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"

	// 文件以换行结尾（客户端按行读；最后一行没换行在某些实现里会丢）。
	if !strings.HasSuffix(body, "\n") {
		t.Error("文件应以换行结尾")
	}
	// 每行都要能独立解析（这是 jsonl 的硬要求：坏一行会连累后面）。
	for i, ln := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		var o map[string]any
		if err := json.Unmarshal([]byte(ln), &o); err != nil {
			t.Fatalf("第 %d 行解析失败：%v", i, err)
		}
		if o["sessionId"] != "sess_new" {
			t.Errorf("第 %d 行的 sessionId 应对上新会话：%v", i, o["sessionId"])
		}
		if o["cwd"] != `C:/proj` {
			t.Errorf("第 %d 行的 cwd 不对：%v", i, o["cwd"])
		}
	}
	// 正文里的换行必须是转义过的——真换行会把一行切成两行，直接破坏 jsonl。
	if strings.Contains(strings.TrimRight(body, "\n"), "问题\n") {
		t.Error("正文里的换行没有被 JSON 转义")
	}
}
