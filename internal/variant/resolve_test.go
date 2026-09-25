package variant

import (
	"path/filepath"
	"strings"
	"testing"
)

// 装好两套安装的假环境，用于验证 Resolve 的优先级。
func twoInstallFS() *fakeFS {
	fs := newFakeFS()
	for _, e := range []string{
		`D:\Tools\WorkBuddy\WorkBuddy.exe`,
		`D:\WorkBuddyAI\WorkBuddyAI.exe`,
	} {
		fs.addFile(e, nil)
		dir := filepath.Dir(e)
		fs.addDir(filepath.Join(dir, "resources"))
		fs.addFile(filepath.Join(dir, "resources", "app.asar"), nil)
		fs.addFile(filepath.Join(dir, "resources", "app.asar.unpacked", "cli", "product.json"), []byte(`{}`))
	}
	return fs
}

func withRegistry(fs *fakeFS) *Probe {
	p := fs.probe("windows", `C:\Users\tester`, nil)
	p.Registry = func() []RegistryEntry {
		return []RegistryEntry{
			{DisplayName: "WorkBuddy 5.6.2", DisplayIcon: `D:\Tools\WorkBuddy\WorkBuddy.exe,0`},
			{DisplayName: "WorkBuddy AI 5.5.2", DisplayIcon: `D:\WorkBuddyAI\WorkBuddyAI.exe,0`},
		}
	}
	return p
}

// 不指定任何东西时，自动探测应当国内版优先。
func TestResolveAutoPrefersCN(t *testing.T) {
	id, inst, err := Resolve(withRegistry(twoInstallFS()), "", "")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if id != CN {
		t.Errorf("id = %q, 期望 cn", id)
	}
	if inst.Executable != `D:\Tools\WorkBuddy\WorkBuddy.exe` {
		t.Errorf("Executable = %q", inst.Executable)
	}
}

func TestResolveHonoursExplicitVariant(t *testing.T) {
	id, inst, err := Resolve(withRegistry(twoInstallFS()), "intl", "")
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if id != Intl {
		t.Errorf("id = %q", id)
	}
	if inst.Executable != `D:\WorkBuddyAI\WorkBuddyAI.exe` {
		t.Errorf("Executable = %q", inst.Executable)
	}
}

// --exe 给国际版路径、不给档位时，应当靠路径推断出国际版。
func TestResolveInfersVariantFromExePath(t *testing.T) {
	id, _, err := Resolve(withRegistry(twoInstallFS()), "", `D:\WorkBuddyAI\WorkBuddyAI.exe`)
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if id != Intl {
		t.Errorf("id = %q, 期望 intl", id)
	}
}

func TestResolveRejectsMissingExeWithoutFallback(t *testing.T) {
	_, _, err := Resolve(withRegistry(twoInstallFS()), "", `D:\nope\Nope.exe`)
	if err == nil {
		t.Fatal("路径不存在时应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "不存在") {
		t.Errorf("错误信息应指出路径不存在: %s", msg)
	}
	// 不应出现"不可用：不存在"这类重复表述。
	if strings.Contains(msg, "不可用") {
		t.Errorf("错误信息重复啰嗦: %s", msg)
	}
}

func TestResolveRejectsVariantMismatch(t *testing.T) {
	_, _, err := Resolve(withRegistry(twoInstallFS()), "cn", `D:\WorkBuddyAI\WorkBuddyAI.exe`)
	if err == nil {
		t.Fatal("档位不符时应报错")
	}
	if !strings.Contains(err.Error(), "国际版") {
		t.Errorf("错误信息应指出实际档位: %s", err)
	}
}

func TestResolveReportsNothingInstalled(t *testing.T) {
	p := newFakeFS().probe("windows", `C:\Users\tester`, nil)
	_, _, err := Resolve(p, "", "")
	if err == nil {
		t.Fatal("一个安装都没有时应报错")
	}
	if !strings.Contains(err.Error(), "wbmux config") {
		t.Errorf("错误信息应给出可操作建议: %s", err)
	}
}

func TestResolveRejectsUnknownVariantName(t *testing.T) {
	if _, _, err := Resolve(withRegistry(twoInstallFS()), "nonsense", ""); err == nil {
		t.Fatal("无法识别的档位名应报错")
	}
}

func TestExeBelongsToOther(t *testing.T) {
	other, ok := exeBelongsToOther(CN, `D:\WorkBuddyAI\WorkBuddyAI.exe`)
	if !ok || other.ID != Intl {
		t.Errorf("应识别为国际版: ok=%v id=%v", ok, other.ID)
	}
	if _, ok := exeBelongsToOther(CN, `D:\Tools\WorkBuddy\WorkBuddy.exe`); ok {
		t.Error("国内版主程序不该被判为其它档位")
	}
	if _, ok := exeBelongsToOther(CN, `D:\portable\wb.exe`); ok {
		t.Error("无法识别的文件名不该被下判断")
	}
}

// 绝对路径判断必须同时认两种平台的写法。
//
// 回归测试：原先直接用 filepath.IsAbs，在 Linux/macOS 上会把
// `D:\App\App.exe` 判为相对路径，随后 filepath.Abs 把它拼到工作目录后面，
// 路径被悄悄改坏，用户只看到"指定的主程序不存在"。
func TestIsAbsolutePath(t *testing.T) {
	cases := map[string]bool{
		// Windows 盘符形式
		`D:\App\App.exe`:   true,
		`d:/App/App.exe`:   true,
		`C:\`:              true,
		`D:App`:            false, // 盘符相对路径，不是绝对路径
		`D:App\App.exe`:    false,
		`\\server\share\x`: true,  // UNC
		`\App\App.exe`:     false, // 仅根相对

		// 类 Unix 形式
		"/opt/app/app": true,
		"/":            true,
		"relative/x":   false,
		"./x":          false,
		"../x":         false,
		"":             false,
	}
	for in, want := range cases {
		if got := IsAbsolutePath(in); got != want {
			t.Errorf("IsAbsolutePath(%q) = %v, 期望 %v", in, got, want)
		}
	}
}

// Windows 路径在任何平台上都不该被拼上工作目录。
//
// 不断言字符串完全相等：filepath.Clean 会把分隔符归一到当前平台的风格
// （`D:/App/App.exe` 在 Windows 上变成 `D:\App\App.exe`），这属于正常行为。
// 真正要守住的是"没被拼上工作目录"。
func TestNormalizePathKeepsForeignAbsolutePathsIntact(t *testing.T) {
	cases := map[string]string{
		`D:\App\App.exe`:    "App.exe",
		`D:/App/App.exe`:    "App.exe",
		`\\srv\share\a.exe`: "a.exe",
		`/opt/app/tool`:     "tool",
	}
	for in, wantBase := range cases {
		got, err := normalizePath(in)
		if err != nil {
			t.Fatalf("normalizePath(%q) 失败: %v", in, err)
		}
		if !IsAbsolutePath(got) {
			t.Errorf("normalizePath(%q) = %q, 不再是绝对路径", in, got)
		}
		if base := baseName(got); base != wantBase {
			t.Errorf("normalizePath(%q) = %q, 文件名 %q 期望 %q", in, got, base, wantBase)
		}
		// 被拼上工作目录的典型特征是路径里出现了 `..` 或层级变多。
		if strings.Contains(got, "..") {
			t.Errorf("normalizePath(%q) = %q, 疑似被拼上了工作目录", in, got)
		}
	}
}

func TestNormalizePathResolvesRelative(t *testing.T) {
	got, err := normalizePath("relative/x.exe")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("相对路径未被解析为绝对路径: %q", got)
	}

	if _, err := normalizePath(""); err == nil {
		t.Error("空路径应报错")
	}
	if _, err := normalizePath("   "); err == nil {
		t.Error("纯空白路径应报错")
	}
}

// 端到端：给 Windows 路径、不给档位，应当靠路径推断出国际版。
func TestResolveFromExeWithWindowsPathOnAnyPlatform(t *testing.T) {
	fs := twoInstallFS()
	p := withRegistry(fs)

	id, inst, err := Resolve(p, "", `D:\WorkBuddyAI\WorkBuddyAI.exe`)
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if id != Intl {
		t.Errorf("id = %q, 期望 intl", id)
	}
	if inst.Executable != `D:\WorkBuddyAI\WorkBuddyAI.exe` {
		t.Errorf("Executable = %q", inst.Executable)
	}
}
