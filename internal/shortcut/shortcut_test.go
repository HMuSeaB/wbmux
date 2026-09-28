package shortcut

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/HMuSeaB/wbmux/internal/config"
)

// TestLooksTemporary 钉住"这个 exe 位置能不能写进快捷方式"。
//
// 背景（2026-09-28 实测）：用 go-tmp 里编译出来的 exe 建快捷方式，.lnk 指向
// `%LOCALAPPDATA%\go-tmp\go-build*\b001\exe\wbmux.exe`——那个目录早就被清了，
// 用户双击图标毫无反应，而从图上看不出任何异常。所以这条判据必须有测试，
// 不能靠"当时环境恰好是对的"。
func TestLooksTemporary(t *testing.T) {
	tmp := os.TempDir()
	cases := []struct {
		path string
		want bool
		why  string
	}{
		{`D:\4rchive\Code\wbmux\dist\wbmux.exe`, false, "正常构建目录要放行"},
		{`C:\Users\me\Downloads\wbmux.exe`, false, "下载目录也是稳定的"},
		{`D:\Tools\wbmux\wbmux.exe`, false, "用户自己放的位置"},
		{`C:\Users\me\AppData\Local\go-tmp\go-build831825957\b001\exe\wbmux.exe`, true, "go build 的临时产物"},
		{`C:\Users\me\AppData\Local\Temp\go-build123\b001\exe\wbmux.exe`, true, "临时目录里的 go-build"},
		{filepath.Join(tmp, "whatever", "wbmux.exe"), true, "%TEMP% 下的一律不稳"},
	}
	for _, c := range cases {
		if got := looksTemporary(c.path); got != c.want {
			t.Errorf("looksTemporary(%q) = %v，期望 %v（%s）", c.path, got, c.want, c.why)
		}
	}
}

// TestSelfCopyLandsInBin 复制目标必须在设置目录下的 bin/，
// 而且内容与源文件**逐字节一致**（半截的 exe 比没有更糟）。
func TestSelfCopyLandsInBin(t *testing.T) {
	root := t.TempDir()
	restore := config.SetRoot(root)
	defer restore()

	src := filepath.Join(t.TempDir(), "wbmux.exe")
	want := []byte("MZ fake pe bytes for test")
	if err := os.WriteFile(src, want, 0o755); err != nil {
		t.Fatal(err)
	}
	dst, err := selfCopy(src)
	if err != nil {
		t.Fatalf("selfCopy: %v", err)
	}
	if filepath.Dir(dst) != filepath.Join(root, "bin") {
		t.Errorf("应复制到 <设置目录>/bin 下，得到 %s", dst)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("复制内容不一致：%d 字节 vs %d 字节", len(got), len(want))
	}
	// 源文件要留着：调用方可能正在运行（界面里的按钮），移动会在 Windows 上失败。
	if _, err := os.Stat(src); err != nil {
		t.Errorf("源文件不该被动过：%v", err)
	}
}

// TestSelfCopyIsIdempotent 重复执行要能覆盖（升级后的新版本要能顶上）。
func TestSelfCopyIsIdempotent(t *testing.T) {
	root := t.TempDir()
	restore := config.SetRoot(root)
	defer restore()

	dir := t.TempDir()
	src := filepath.Join(dir, "wbmux.exe")
	for _, content := range []string{"first-version", "second-version-longer"} {
		if err := os.WriteFile(src, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		dst, err := selfCopy(src)
		if err != nil {
			t.Fatalf("selfCopy: %v", err)
		}
		got, _ := os.ReadFile(dst)
		if string(got) != content {
			t.Errorf("第二次应覆盖成 %q，得到 %q", content, got)
		}
	}
}

// TestCreateEndToEnd 真正建一次快捷方式并读回内容。
//
// 这条测试的价值全在"失败必须是响的"：2026-09-28 踩到的坑正是
// **命令报成功、lnk 却没被写**（PowerShell 的 COM 报错没人接，
// 而且 IconLocation 在 Save 之前根本不存在）。所以这里断言的是
// 文件真的存在、真的引用了我们的 exe 与 .ico。
//
// 用 t.TempDir() 当设置目录、并跳过桌面不可写的环境（CI 上多半没有桌面）。
func TestCreateEndToEnd(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("快捷方式是 Windows 专属")
	}
	if _, err := exec.LookPath("powershell"); err != nil {
		t.Skip("没有 powershell")
	}
	restore := config.SetRoot(t.TempDir())
	defer restore()

	// 当前测试进程的 exe 在临时目录里，Create 会拒绝——先自己复制到
	// 一个"稳定"的临时目录，再用它冒充正式安装位置。
	stableDir := t.TempDir()
	// t.TempDir() 在 %TEMP% 下，会被判成临时目录，所以这里刻意用
	// 设置目录下的 bin/ 冒充（selfCopy 的落点）。
	cfgDir, err := config.Dir()
	if err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(cfgDir, "bin", "wbmux.exe")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = stableDir
	self, _ := os.Executable()
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Skipf("读不到自身：%v", err)
	}
	if err := os.WriteFile(stable, raw, 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := createFor(stable)
	if err != nil {
		// 桌面对某些环境（无桌面会话的 CI）不可写，不算代码的问题。
		if strings.Contains(err.Error(), "找不到桌面目录") {
			t.Skipf("这个环境没有桌面：%v", err)
		}
		t.Fatalf("Create 失败：%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Create 报告成功但文件不存在：%v", err)
	}
	if info.Size() == 0 {
		t.Fatal("快捷方式是空文件")
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	body, _ := os.ReadFile(path)
	text := string(body) + decodeUTF16(body)
	if !strings.Contains(text, "wbmux.exe") {
		t.Errorf("快捷方式里没有我们的 exe 路径")
	}
	if !strings.Contains(text, "wbmux.ico") {
		t.Errorf("快捷方式没引用自画图标（那就会是白纸图标）")
	}
}

// decodeUTF16 把二进制里的 UTF-16 串解出来，便于在测试里做包含判断。
func decodeUTF16(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return string(utf16.Decode(u))
}

// TestExePathRejectsTemporary 当前测试进程就跑在临时目录里（go test 的产物），
// 所以 ExePath 应该**明确拒绝**，而不是给出一个骗人的路径。
//
// 这条在 Windows 上才有意义（go test 的临时目录路径形态如此），
// 但它同时也是"判据别误伤正常路径"的回归：如果哪天 looksTemporary 变成
// 恒真，正常目录的用例会先失败。
func TestExePathRejectsTemporary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("临时目录判据是按 Windows 的布局写的")
	}
	exe, _ := os.Executable()
	if !strings.Contains(strings.ToLower(exe), "go-build") && !strings.Contains(strings.ToLower(exe), "temp") {
		t.Skipf("测试进程不在临时目录里（%s），跳过", exe)
	}
	path, ok := ExePath()
	if ok {
		if looksTemporary(path) {
			t.Errorf("ExePath 报告可用，却仍指向临时目录：%s", path)
		}
	}
}
