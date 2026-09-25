package variant

import (
	"path/filepath"
	"strings"
	"testing"
)

// fakeFS 构造一个只存在于内存里的文件系统，供探测逻辑测试使用。
type fakeFS struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string][]byte{}, dirs: map[string]bool{}}
}

func (f *fakeFS) addFile(p string, content []byte) {
	f.files[p] = content
	f.dirs[filepath.Dir(p)] = true
}

func (f *fakeFS) addDir(p string) { f.dirs[p] = true }

func (f *fakeFS) probe(goos, home string, env map[string]string) *Probe {
	return &Probe{
		GOOS:   goos,
		Home:   home,
		Getenv: func(k string) string { return env[k] },
		Exists: func(p string) bool {
			if p == "" {
				return false
			}
			if _, ok := f.files[p]; ok {
				return true
			}
			return f.dirs[p]
		},
		ReadFile: func(p string) ([]byte, error) {
			if b, ok := f.files[p]; ok {
				return b, nil
			}
			return nil, errNotFound
		},
	}
}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var errNotFound = notFoundErr{}

func TestDataDirUsesHomePlusFolderName(t *testing.T) {
	p := newFakeFS().probe("windows", `C:\Users\tester`, nil)
	if got, want := p.DataDir(CN), filepath.Join(`C:\Users\tester`, ".workbuddy"); got != want {
		t.Errorf("DataDir(CN) = %q, 期望 %q", got, want)
	}
	if got, want := p.DataDir(Intl), filepath.Join(`C:\Users\tester`, ".workbuddy-ai"); got != want {
		t.Errorf("DataDir(Intl) = %q, 期望 %q", got, want)
	}
}

func TestExplicitPathWinsOverEverything(t *testing.T) {
	fs := newFakeFS()
	exe := `X:\Custom\WorkBuddy.exe`
	fs.addFile(exe, nil)
	fs.addDir(`X:\Custom\resources`)
	fs.addFile(`X:\Custom\resources\app.asar`, nil)
	fs.addFile(filepath.Join(`X:\Custom\resources`, filepath.FromSlash(ProductJSONRel)), []byte(`{}`))

	p := fs.probe("windows", `C:\Users\tester`, nil)
	inst := p.Detect(CN, exe)

	if !inst.Found {
		t.Fatalf("未找到安装: %v", inst.Problems)
	}
	if inst.Source != "显式配置" {
		t.Errorf("Source = %q, 期望 显式配置", inst.Source)
	}
	// 用 filepath.Dir 而非字面量比较：路径分隔符的语义随平台而变，
	// 断言"安装目录等于主程序所在目录"这个事实本身即可。
	if want := filepath.Dir(exe); inst.InstallDir != want {
		t.Errorf("InstallDir = %q, 期望 %q", inst.InstallDir, want)
	}
}

// 客户端把本程序注册为 Office 关联时会写下带盘符冒号的标记，
// 解析必须能还原完整路径。
func TestDataDirHintParsesDriveLetterColon(t *testing.T) {
	fs := newFakeFS()
	exe := `D:\Tools\WorkBuddy\WorkBuddy.exe`
	fs.addFile(exe, nil)
	fs.addDir(`D:\Tools\WorkBuddy\resources`)
	fs.addFile(`D:\Tools\WorkBuddy\resources\app.asar`, nil)
	fs.addFile(filepath.Join(`D:\Tools\WorkBuddy\resources`, filepath.FromSlash(ProductJSONRel)), []byte(`{}`))

	dataDir := filepath.Join(`C:\Users\tester`, ".workbuddy")
	fs.addDir(dataDir)
	fs.addFile(filepath.Join(dataDir, "settings.json"), []byte(
		`{"officeFileAssociationsRepairMarker":"v3:win32:5.6.2:D:\\Tools\\WorkBuddy\\WorkBuddy.exe:WorkBuddy:aac,avif,bmp"}`))

	p := fs.probe("windows", `C:\Users\tester`, nil)
	inst := p.Detect(CN, "")

	if !inst.Found {
		t.Fatalf("未找到安装: %v", inst.Problems)
	}
	if inst.Executable != exe {
		t.Errorf("Executable = %q, 期望 %q", inst.Executable, exe)
	}
	if inst.Source != "数据目录线索" {
		t.Errorf("Source = %q, 期望 数据目录线索", inst.Source)
	}
}

func TestDetectReportsMissingProductJSON(t *testing.T) {
	fs := newFakeFS()
	exe := `X:\Custom\WorkBuddy.exe`
	fs.addFile(exe, nil)
	fs.addDir(`X:\Custom\resources`)
	fs.addFile(`X:\Custom\resources\app.asar`, nil)
	// 故意不放 product.json

	p := fs.probe("windows", `C:\Users\tester`, nil)
	inst := p.Detect(CN, exe)

	if !inst.Found {
		t.Fatal("主程序存在时应判定为找到")
	}
	joined := strings.Join(inst.Problems, " | ")
	if !strings.Contains(joined, ProductJSONRel) {
		t.Errorf("问题列表应指出缺少 product.json，实际: %s", joined)
	}
}

func TestDetectFailsCleanlyWhenNothingExists(t *testing.T) {
	fs := newFakeFS()
	p := fs.probe("windows", `C:\Users\tester`, map[string]string{
		"LOCALAPPDATA": `C:\Users\tester\AppData\Local`,
		"ProgramFiles": `C:\Program Files`,
	})
	inst := p.Detect(CN, "")

	if inst.Found {
		t.Fatal("不该判定为找到")
	}
	if len(inst.Problems) == 0 {
		t.Fatal("未找到时应给出可操作的提示")
	}
	if !strings.Contains(strings.Join(inst.Problems, " "), "wbmux config") {
		t.Errorf("提示应引导用户使用 wbmux config，实际: %v", inst.Problems)
	}
}

func TestExeInDirPerPlatform(t *testing.T) {
	cn, _ := Get(CN)
	intl, _ := Get(Intl)
	fs := newFakeFS()

	win := fs.probe("windows", `C:\Users\tester`, nil)
	if got := win.exeInDir(cn, `C:\Program Files\WorkBuddy`); got != filepath.Join(`C:\Program Files\WorkBuddy`, "WorkBuddy.exe") {
		t.Errorf("windows CN = %q", got)
	}
	if got := win.exeInDir(intl, `C:\Program Files\WorkBuddy AI`); got != filepath.Join(`C:\Program Files\WorkBuddy AI`, "WorkBuddyAI.exe") {
		t.Errorf("windows Intl = %q", got)
	}

	mac := fs.probe("darwin", "/Users/tester", nil)
	got := mac.exeInDir(intl, "/Applications/WorkBuddy AI.app")
	want := filepath.Join("/Applications/WorkBuddy AI.app", "Contents", "MacOS", "WorkBuddyAI")
	if got != want {
		t.Errorf("darwin Intl = %q, 期望 %q", got, want)
	}

	lin := fs.probe("linux", "/home/tester", nil)
	if got := lin.exeInDir(cn, "/usr/lib/workbuddy"); got != filepath.Join("/usr/lib/workbuddy", "workbuddy") {
		t.Errorf("linux CN = %q", got)
	}
}

func TestPathLookup(t *testing.T) {
	fs := newFakeFS()
	exe := filepath.Join(`C:\bin`, "WorkBuddyAI.exe")
	fs.addFile(exe, nil)

	p := fs.probe("windows", `C:\Users\tester`, map[string]string{
		"PATH": `C:\other;C:\bin`,
	})
	inst := p.Detect(Intl, "")
	if !inst.Found {
		t.Fatalf("未找到: %v", inst.Problems)
	}
	if inst.Source != "PATH" {
		t.Errorf("Source = %q, 期望 PATH", inst.Source)
	}
}

// 显式路径不存在时必须硬失败。
//
// 回归测试：早期实现会静默跳过不存在的显式路径，退回自动探测，
// 结果用户指定了 A 却启动了 B——而且可能因此连上非预期的后端。
func TestExplicitPathMustExist(t *testing.T) {
	fs := newFakeFS()
	// 让自动探测有东西可捡：注册表里有一条国内版记录。
	realExe := `D:\Tools\WorkBuddy\WorkBuddy.exe`
	fs.addFile(realExe, nil)
	fs.addDir(`D:\Tools\WorkBuddy\resources`)
	fs.addFile(`D:\Tools\WorkBuddy\resources\app.asar`, nil)
	fs.addFile(`D:\Tools\WorkBuddy\resources\app.asar.unpacked\cli\product.json`, []byte(`{}`))

	p := fs.probe("windows", `C:\Users\tester`, nil)
	p.Registry = func() []RegistryEntry {
		return []RegistryEntry{{DisplayName: "WorkBuddy 5.6.2", DisplayIcon: realExe + ",0"}}
	}

	inst := p.Detect(CN, `D:\nope\Nope.exe`)
	if inst.Found {
		t.Fatalf("显式路径不存在时不该退回自动探测，却找到了 %q", inst.Executable)
	}
	joined := strings.Join(inst.Problems, " | ")
	if !strings.Contains(joined, "不存在") {
		t.Errorf("问题说明应指出路径不存在，实际: %s", joined)
	}
}

// 传了国际版主程序却按国内版探测，必须拒绝。
func TestExplicitPathRejectsWrongVariant(t *testing.T) {
	fs := newFakeFS()
	exe := `D:\WorkBuddyAI\WorkBuddyAI.exe`
	fs.addFile(exe, nil)
	fs.addDir(`D:\WorkBuddyAI\resources`)

	p := fs.probe("windows", `C:\Users\tester`, nil)

	inst := p.Detect(CN, exe)
	if inst.Found {
		t.Fatal("国际版主程序不该被当作国内版接受")
	}
	if !strings.Contains(strings.Join(inst.Problems, " "), "国际版") {
		t.Errorf("提示应说明档位不符，实际: %v", inst.Problems)
	}

	// 反过来，按国际版探测应当接受。
	ok := p.Detect(Intl, exe)
	if !ok.Found {
		t.Fatalf("国际版主程序按国际版探测应成功: %v", ok.Problems)
	}
}

// 被重命名过的副本不做档位判断，交给 --host 决定。
func TestExplicitPathAcceptsRenamedBinary(t *testing.T) {
	fs := newFakeFS()
	exe := `D:\portable\wb.exe`
	fs.addFile(exe, nil)
	fs.addDir(`D:\portable\resources`)

	p := fs.probe("windows", `C:\Users\tester`, nil)
	if inst := p.Detect(CN, exe); !inst.Found {
		t.Fatalf("重命名的副本应被接受: %v", inst.Problems)
	}
}

func TestReadLaunchInfo(t *testing.T) {
	fs := newFakeFS()
	dataDir := filepath.Join(`C:\Users\tester`, ".workbuddy")
	fs.addDir(dataDir)
	fs.addFile(filepath.Join(dataDir, "last-launch.json"),
		[]byte(`{"version":"5.6.2","build":"37a65c0b","timestamp":"2026-09-25T03:14:10.761Z"}`))

	p := fs.probe("windows", `C:\Users\tester`, nil)
	inst := Install{Variant: CN, DataDir: dataDir}
	p.readLaunchInfo(&inst)

	if inst.Version != "5.6.2" || inst.Build != "37a65c0b" {
		t.Errorf("版本信息解析错误: version=%q build=%q", inst.Version, inst.Build)
	}
}

func TestFromDataDirHintIgnoresMalformedMarkers(t *testing.T) {
	for _, marker := range []string{
		``,
		`not-enough:parts`,
		`v3:win32:5.6.2:no-extension-here:WorkBuddy:aac`,
	} {
		fs := newFakeFS()
		dataDir := filepath.Join(`C:\Users\tester`, ".workbuddy")
		fs.addDir(dataDir)
		fs.addFile(filepath.Join(dataDir, "settings.json"),
			[]byte(`{"officeFileAssociationsRepairMarker":"`+marker+`"}`))

		p := fs.probe("windows", `C:\Users\tester`, nil)
		if got := p.fromDataDirHint(CN); got != "" {
			t.Errorf("标记 %q 应被忽略, 实际返回 %q", marker, got)
		}
	}
}
