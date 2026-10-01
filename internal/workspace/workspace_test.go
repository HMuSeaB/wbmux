package workspace

import (
	"strings"
	"testing"
)

// 这个包里最不能错的几件事：路径规范化、slug 规则、以及"客户端在跑就拒绝"。
// 它们错了的后果不是报错，而是**静默地把用户的会话归错地方**——所以逐条钉住。

func TestUnify(t *testing.T) {
	cases := []struct{ in, want string }{
		// 最常见的一种：正斜杠写法要归一成反斜杠
		{`C:/Users/36230/WorkBuddy AI`, `c:\Users\36230\WorkBuddy AI`},
		// 盘符大写要统一：库里两种写法并存，不统一就会分裂成两个入口
		{`D:\4rchive\Campus`, `d:\4rchive\Campus`},
		// 结尾分隔符要去掉，否则 `C:\a\` 与 `C:\a` 会被当成两个目录
		{`C:\Users\36230\`, `c:\Users\36230`},
		{`C:/Users/36230/`, `c:\Users\36230`},
		// **分隔符之外的一切原样保留**，尤其是空格与中文
		{`C:\Users\36230\WorkBuddy AI`, `c:\Users\36230\WorkBuddy AI`},
		{`D:\4rchive\Code\10：微信开发工具 解压密码：123`,
			`d:\4rchive\Code\10：微信开发工具 解压密码：123`},
		// 纯盘符
		{`C:`, `c:\`},
		{`C:\`, `c:\`},
		// 空串与空白
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := Unify(tc.in); got != tc.want {
			t.Errorf("Unify(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestSlugMatchesRealClientOutput 钉住 slug 规则。
//
// **期望值全部来自实测**：把库里 `distinct cwd` 逐条算出来，再与
// `<数据目录>/projects/` 下的实际目录名比对（13/13 对上）。
// 两条容易凭直觉写错的：
//   - 整串是**全小写**的（`c-Users-…` 是错的）；
//   - 冒号也压成连字符，所以盘符后会**多一个连字符**（`c--users-…`）。
//
// 空格与中文原样保留——这是最容易被"顺手规范化"掉的一点。
func TestSlugMatchesRealClientOutput(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Users\36230\WorkBuddy AI`, `c--users-36230-workbuddy ai`},
		{`D:\4rchive\Code\co-switch`, `d--4rchive-code-co-switch`},
		{`D:/4rchive/Campus`, `d--4rchive-campus`},
		{`C:\Users\36230\WorkBuddy AI\2026-09-25-11-15-46`,
			`c--users-36230-workbuddy ai-2026-09-25-11-15-46`},
		{`C:\Users\36230\Desktop\Competition\竞赛汇总`,
			`c--users-36230-desktop-competition-竞赛汇总`},
		{`D:\4rchive\Code\10：微信开发工具 解压密码：123`,
			`d--4rchive-code-10：微信开发工具 解压密码：123`},
	}
	for _, tc := range cases {
		if got := Slug(tc.in); got != tc.want {
			t.Errorf("Slug(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
	// 同一目录的两种写法必须得到同一个 slug，否则合并写法时会漏改目录。
	a := Slug(`C:/Users/36230/WorkBuddy AI`)
	b := Slug(`C:\Users\36230\WorkBuddy AI`)
	if a != b {
		t.Fatalf("两种写法应得到同一 slug：%q vs %q", a, b)
	}
}

func TestSamePath(t *testing.T) {
	same := [][2]string{
		{`C:/Users/x`, `c:\Users\x`},
		{`C:\Users\X\`, `c:/users/x`},
		{`D:\4rchive\Campus`, `d:\4rchive\campus`}, // Windows 路径大小写不敏感
	}
	for _, p := range same {
		if !SamePath(p[0], p[1]) {
			t.Errorf("应视为同一路径: %q vs %q", p[0], p[1])
		}
	}
	diff := [][2]string{
		{`C:\Users\a`, `C:\Users\b`},
		{`C:\Users\a`, `C:\Users\a\x`}, // 父目录与子目录是两个工作区
	}
	for _, p := range diff {
		if SamePath(p[0], p[1]) {
			t.Errorf("不该视为同一路径: %q vs %q", p[0], p[1])
		}
	}
}

func TestReplacementsLongestFirst(t *testing.T) {
	pairs := replacements(`C:\work\proj`, `D:\work\proj`)
	if len(pairs) < 3 {
		t.Fatalf("应给出三种写法的替换对，得到 %d 组", len(pairs))
	}
	// 必须按旧串长度降序：否则 `C:\work` 会先把 `C:\\work`（转义形式）
	// 切碎，留下半截路径。
	for i := 1; i < len(pairs); i++ {
		if len(pairs[i-1][0]) < len(pairs[i][0]) {
			t.Fatalf("替换对没有按长度降序：第 %d 组比前一组长", i)
		}
	}
	// 转义形式要真的存在（会话文件是 JSON，反斜杠会翻倍）
	found := false
	for _, p := range pairs {
		if p[0] == `C:\\work\\proj` {
			found = true
			if p[1] != `D:\\work\\proj` {
				t.Fatalf("转义形式的新值不对: %q", p[1])
			}
		}
	}
	if !found {
		t.Fatal("缺少 JSON 转义形式的替换对——漏了它会让会话里的路径改不掉")
	}
}

func TestContainsPathCatchesAllSpellings(t *testing.T) {
	content := `{"text":"见 D:\\4rchive\\Code\\proj\\a.png 与 D:/4rchive/Code/proj/b.png"}`
	if !containsPath(content, `D:\4rchive\Code\proj`) {
		t.Fatal("应能认出 JSON 转义与正斜杠两种写法")
	}
	if containsPath(content, `D:\4rchive\Code\other`) {
		t.Fatal("不该把无关路径算进来")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
	}
	for _, tc := range cases {
		if got := humanBytes(tc.n); got != tc.want {
			t.Errorf("humanBytes(%d) = %q，期望 %q", tc.n, got, tc.want)
		}
	}
}

// TestPlanReadyIsFalsyWhenBlocked 钉住"有拦路原因就不能执行"这条。
//
// 这是整个功能的安全底线：客户端在跑时改库会被它的内存缓存回写覆盖，
// 表现是"改了没生效"或更严重——索引与正文对不上，列表里点开是空白。
func TestPlanReadyIsFalsyWhenBlocked(t *testing.T) {
	pl := &Plan{}
	if !pl.Ready() {
		t.Fatal("没有拦路原因时应当 ready")
	}
	pl.Blockers = []string{"客户端在跑"}
	if pl.Ready() {
		t.Fatal("有拦路原因时不该 ready")
	}
}

// TestSummarizeMentionsBothEnds 保证给人看的摘要不会漏掉"从哪到哪"。
//
// 摘要存在的意义就是让用户在动手前看清楚，漏掉一端等于没说明白。
func TestSummarizeMentionsBothEnds(t *testing.T) {
	pl := &Plan{
		Kind: "move", From: `c:\old\place`, To: `d:\new\place`,
		CwdVariants: []string{`C:\old\place`}, CwdRows: 3,
		FileCount: 10, TotalBytes: 2048,
	}
	s := pl.Summarize()
	for _, want := range []string{`c:\old\place`, `d:\new\place`, "3 条", "10 个"} {
		if !strings.Contains(s, want) {
			t.Fatalf("摘要里应提到 %q，实际：\n%s", want, s)
		}
	}
}

// TestJudgeSession 钉住"哪些索引条目算坏的"。
//
// 这块最要紧的一条是**不许误伤**：删索引意味着用户再也看不到那个会话，
// 而正文文件还躺在磁盘上没人去找。所以每条反例都要显式测。
func TestJudgeSession(t *testing.T) {
	cases := []struct {
		name       string
		facts      sessionFacts
		wantAction string
		wantReason string // 为空表示"不该有问题"
	}{
		{
			name: "正常会话：什么都不做",
			facts: sessionFacts{
				cwd: `D:/4rchive/Code/wbmux`, status: "completed",
				transport: "local", hasBody: true,
			},
			wantAction: "",
		},
		{
			name: "实测那条云端残留：cwd 空 + 无正文 + cloud",
			facts: sessionFacts{
				cwd: "", status: "completed", transport: "cloud",
				deletedAt: -1, hasBody: false,
			},
			wantAction: "delete",
			wantReason: "cwd 为空且磁盘上没有正文文件",
		},
		{
			name: "实测那条大写 status",
			facts: sessionFacts{
				cwd: `C:/Users/36230/WorkBuddy AI`, status: "Completed",
				transport: "local", hasBody: true,
			},
			wantAction: "fix",
			wantReason: `status="Completed"`,
		},

		// ---------- 反例：不许误伤 ----------

		{
			// 只 cwd 空但有正文：那是"归属丢了"，该走 set/move 修，
			// 删掉的话会话就真没了。
			name: "cwd 空但有正文文件 → 不删",
			facts: sessionFacts{
				cwd: "", status: "completed", transport: "local", hasBody: true,
			},
			wantAction: "",
		},
		{
			// 实测那两条：正数时间戳 + 删除时间与最后活动时间一致 = 正常删除。
			// 客户端自己会隐藏，轮不到我们动。
			name: "deleted_at 是合法正数（用户正常删的）→ 不动",
			facts: sessionFacts{
				cwd: `C:/Users/36230/WorkBuddy AI`, status: "completed",
				transport: "local", deletedAt: 1785335121258, hasBody: true,
			},
			wantAction: "",
		},
		{
			name: "正常的小写 status（working 也要认）",
			facts: sessionFacts{
				cwd: `D:/4rchive/Code/slate`, status: "working",
				transport: "local", hasBody: true,
			},
			wantAction: "",
		},
		{
			name: "status 为空不当异常（老数据可能没这个字段）",
			facts: sessionFacts{
				cwd: `D:/4rchive/Code/slate`, status: "",
				transport: "local", hasBody: true,
			},
			wantAction: "",
		},
		{
			// deleted_at=0 是"未删除"的正常写法，不是脏值。
			name: "deleted_at=0（未删除）不算脏值",
			facts: sessionFacts{
				cwd: `D:/4rchive/Code/slate`, status: "completed",
				transport: "local", deletedAt: 0, hasBody: true,
			},
			wantAction: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reasons, action := judgeSession(c.facts)
			if action != c.wantAction {
				t.Errorf("action = %q，期望 %q（reasons=%v）", action, c.wantAction, reasons)
			}
			if c.wantReason == "" {
				if len(reasons) != 0 {
					t.Errorf("不该有问题，实际 %v", reasons)
				}
				return
			}
			joined := strings.Join(reasons, "；")
			if !strings.Contains(joined, c.wantReason) {
				t.Errorf("原因里应含 %q，实际 %q", c.wantReason, joined)
			}
		})
	}
}

// TestJudgeSessionDeleteWinsOverFix 确认"要删的条目不会被降级成修正"。
//
// 优先级有意义：云端残留那条同时命中了判据一与判据二（deleted_at=-1）。
// 若 fix 抢先，我们会去"修一个没有正文的条目"，改完它还在侧栏上——
// 点开照样是空白。
func TestJudgeSessionDeleteWinsOverFix(t *testing.T) {
	_, action := judgeSession(sessionFacts{
		cwd: "", status: "Completed", transport: "cloud",
		deletedAt: -1, hasBody: false,
	})
	if action != "delete" {
		t.Fatalf("action = %q，期望 delete（delete 应当优先于 fix）", action)
	}
}
