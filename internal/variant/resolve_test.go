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
