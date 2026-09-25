package variant

import "testing"

// 国内版产品名是国际版的前缀，这是最容易出错的地方。
func TestMatchProductRejectsPrefixCollision(t *testing.T) {
	if matchProduct("WorkBuddy AI 5.5.2", "WorkBuddy") {
		t.Error("国际版的记录不该算作国内版")
	}
	if !matchProduct("WorkBuddy 5.6.2", "WorkBuddy") {
		t.Error("国内版的记录应匹配成功")
	}
	if !matchProduct("WorkBuddy AI 5.5.2", "WorkBuddy AI") {
		t.Error("国际版的记录应匹配成功")
	}
}

func TestMatchProductAcceptsCommonSuffixes(t *testing.T) {
	for _, name := range []string{
		"WorkBuddy",
		"WorkBuddy 5.6.2",
		"WorkBuddy v5.6.2",
		"workbuddy 5.6.2 (x64)",
		"WorkBuddy-5.6.2",
		"  WorkBuddy  ",
	} {
		if !matchProduct(name, "WorkBuddy") {
			t.Errorf("应匹配: %q", name)
		}
	}
}

func TestMatchProductRejectsUnrelated(t *testing.T) {
	for _, name := range []string{
		"",
		"WorkBuddy Helper",
		"WorkBuddyOld",
		"NotWorkBuddy",
		"CodeBuddy 1.0",
	} {
		if matchProduct(name, "WorkBuddy") {
			t.Errorf("不该匹配: %q", name)
		}
	}
}

func TestParseDisplayIcon(t *testing.T) {
	cases := map[string]string{
		`D:\Tools\WorkBuddy\WorkBuddy.exe,0`: `D:\Tools\WorkBuddy\WorkBuddy.exe`,
		`D:\WorkBuddyAI\WorkBuddyAI.exe,0`:   `D:\WorkBuddyAI\WorkBuddyAI.exe`,
		`"C:\App\App.exe",0`:                 `C:\App\App.exe`,
		`"C:\Program Files\App\App.exe"`:     `C:\Program Files\App\App.exe`,
		`C:\App\App.exe`:                     `C:\App\App.exe`,
		// 路径里本身含逗号，末尾没有图标序号时不应被截断。
		`C:\My,App\App.exe`: `C:\My,App\App.exe`,
		``:                  ``,
		`   `:               ``,
	}
	for in, want := range cases {
		if got := parseDisplayIcon(in); got != want {
			t.Errorf("parseDisplayIcon(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

func TestPublisherMatches(t *testing.T) {
	for _, p := range []string{
		"Tencent Technology (Shenzhen) Company Limited",
		"tencent",
		"腾讯科技（深圳）有限公司",
		"", // 缺失时不否决
	} {
		if !publisherMatches(p) {
			t.Errorf("应通过: %q", p)
		}
	}
	for _, p := range []string{"Microsoft Corporation", "Some Other Vendor"} {
		if publisherMatches(p) {
			t.Errorf("应拒绝: %q", p)
		}
	}
}

// 用本机实测到的两条记录做端到端匹配。
func TestRegistryCandidatesAgainstRealEntries(t *testing.T) {
	entries := []RegistryEntry{
		{
			DisplayName: "WorkBuddy 5.6.2",
			Publisher:   "Tencent Technology (Shenzhen) Company Limited",
			DisplayIcon: `D:\Tools\WorkBuddy\WorkBuddy.exe,0`,
		},
		{
			DisplayName: "WorkBuddy AI 5.5.2",
			Publisher:   "Tencent Technology (Shenzhen) Company Limited",
			DisplayIcon: `D:\WorkBuddyAI\WorkBuddyAI.exe,0`,
		},
	}

	cn, _ := Get(CN)
	intl, _ := Get(Intl)

	gotCN := registryCandidates(entries, cn)
	if len(gotCN) != 1 || gotCN[0] != `D:\Tools\WorkBuddy\WorkBuddy.exe` {
		t.Errorf("国内版候选 = %v", gotCN)
	}

	gotIntl := registryCandidates(entries, intl)
	if len(gotIntl) != 1 || gotIntl[0] != `D:\WorkBuddyAI\WorkBuddyAI.exe` {
		t.Errorf("国际版候选 = %v", gotIntl)
	}
}

func TestRegistryCandidatesUsesInstallLocation(t *testing.T) {
	entries := []RegistryEntry{
		{
			DisplayName:     "WorkBuddy 5.6.2",
			InstallLocation: `C:\Program Files\WorkBuddy\`,
		},
	}
	cn, _ := Get(CN)
	got := registryCandidates(entries, cn)
	if len(got) != 1 || got[0] != `C:\Program Files\WorkBuddy\WorkBuddy.exe` {
		t.Errorf("候选 = %v", got)
	}
}

// DisplayIcon 指向别的程序时不能采纳。
func TestRegistryCandidatesIgnoresForeignDisplayIcon(t *testing.T) {
	entries := []RegistryEntry{
		{
			DisplayName: "WorkBuddy 5.6.2",
			DisplayIcon: `C:\Windows\System32\uninstall.exe,0`,
		},
	}
	cn, _ := Get(CN)
	if got := registryCandidates(entries, cn); len(got) != 0 {
		t.Errorf("不该采纳无关的 DisplayIcon: %v", got)
	}
}

// 注册表探测必须能被注入替换，否则测试会读到真实注册表。
func TestDetectSkipsRegistryWhenNotInjected(t *testing.T) {
	fs := newFakeFS()
	p := fs.probe("windows", `C:\Users\tester`, nil)
	if p.Registry != nil {
		t.Fatal("测试用探测器默认不应绑定真实注册表")
	}
	inst := p.Detect(CN, "")
	if inst.Found {
		t.Error("没有注入注册表且文件系统为空时不该判定为找到")
	}
}

func TestDetectUsesInjectedRegistry(t *testing.T) {
	fs := newFakeFS()
	exe := `D:\WorkBuddyAI\WorkBuddyAI.exe`
	fs.addFile(exe, nil)
	fs.addDir(`D:\WorkBuddyAI\resources`)
	fs.addFile(`D:\WorkBuddyAI\resources\app.asar`, nil)
	fs.addFile(`D:\WorkBuddyAI\resources\app.asar.unpacked\cli\product.json`, []byte(`{}`))

	p := fs.probe("windows", `C:\Users\tester`, nil)
	p.Registry = func() []RegistryEntry {
		return []RegistryEntry{{
			DisplayName: "WorkBuddy AI 5.5.2",
			Publisher:   "Tencent Technology (Shenzhen) Company Limited",
			DisplayIcon: exe + ",0",
		}}
	}

	inst := p.Detect(Intl, "")
	if !inst.Found {
		t.Fatalf("未找到: %v", inst.Problems)
	}
	if inst.Source != "注册表" {
		t.Errorf("Source = %q, 期望 注册表", inst.Source)
	}
	if inst.Executable != exe {
		t.Errorf("Executable = %q", inst.Executable)
	}
}
