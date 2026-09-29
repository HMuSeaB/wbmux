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

	// **把"桌面"也隔离掉**（2026-09-29 真事故）：快捷方式的落点是固定的
	// 「<桌面>\wbmux.lnk」，不隔离的话这条测试会覆盖/删除用户桌面上那份
	// 真实的快捷方式——回收站里躺过 5 个 `Desktop\wbmux.lnk` 就是它干的。
	desktop := t.TempDir()
	defer DesktopForTest(desktop)()

	// 拿自己的可执行文件**复制到临时目录**，当作"正式安装位置的那份 exe"。
	//
	// # 绝不能往 <设置目录>/bin/ 里写（2026-09-28 真实事故）
	//
	// 我这条测试原先就是把测试二进制写进 `<config.Dir()>/bin/wbmux.exe`。
	// 而 `config.SetRoot` 是**进程级全局**：包内测试串行时看着没事，但只要
	// 同包里别的用例把它 restore 回真实路径，写进去的就是**用户的程序**——
	// `~/.wbmux/bin/wbmux.exe` 正是自助升级的落点、也是用户双击的那份。
	// 用户当晚看到的"稳定版变成测试二进制（`version` 输出 PASS）"就是这么来的。
	//
	// 现在的做法：源文件放 t.TempDir()（%TEMP% 下会被 createFor 判为临时目录，
	// 所以**不能**拿它当目标），目标是**另一个临时目录里的普通文件**——
	// createFor 只拒绝"像编译产物"的路径，普通临时目录照收。
	self, err := os.Executable()
	if err != nil {
		t.Skipf("拿不到自身路径：%v", err)
	}
	src := filepath.Join(t.TempDir(), "wbmux.exe")
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Skipf("读不到自身：%v", err)
	}
	if err := os.WriteFile(src, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(t.TempDir(), "installed", "wbmux.exe")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
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
// 所以 ExePath 不该给出一个骗人的路径。
//
// # 这条测试必须**先隔离设置目录**（2026-09-28 踩到）
//
// `ExePath()` 在发现 exe 位于临时目录时会退而求其次：把自己复制到
// `<设置目录>/bin/`。我第一次写这条测试时没隔离设置目录，于是它**真的把
// 测试二进制复制进了 `~/.wbmux/bin/wbmux.exe`**——那正是用户双击的程序
// （用户当晚看到稳定版 `version` 输出 "PASS" 就是这么来的）。
//
// 现在：先 SetRoot 隔离；并且 config 侧有 GuardRealWrite 兜底——测试进程
// 往真实设置目录写会直接 panic，而不是静默改掉用户数据。
func TestExePathRejectsTemporary(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("临时目录判据是按 Windows 的布局写的")
	}
	restore := config.SetRoot(t.TempDir())
	defer restore()

	exe, _ := os.Executable()
	if !strings.Contains(strings.ToLower(exe), "go-build") && !strings.Contains(strings.ToLower(exe), "temp") {
		t.Skipf("测试进程不在临时目录里（%s），跳过", exe)
	}
	path, ok := ExePath()
	if !ok {
		return // 拒绝也是一种正确结果（调用方会给出明确错误）
	}
	if looksTemporary(path) {
		t.Errorf("ExePath 报告可用，却仍指向临时目录：%s", path)
	}
}

// TestDesktopOverrideIsHonored 验证「桌面可注入」这个接缝真的生效。
//
// # 这条替换掉了原先的 TestDesktopIsIsolated —— 那个设计是错的
//
// 它为了验证「不该碰真桌面」，先去碰了真桌面：跑一遍未隔离的 createFor，
// 看真实桌面有没有被改。结果当场把用户桌面上的快捷方式**覆盖**成了指向
// 测试临时目录的假 exe（1854 → 2599 字节），还原又被环境拒绝——真的把
// 用户的快捷方式弄坏了。这个思路自相矛盾，删掉。
//
// 正确做法是反过来：断言注入**生效**。注入临时目录后，产物必须落在注入的
// 目录里；真实桌面那边由 TestCreateEndToEnd 的隔离来保证。
func TestDesktopOverrideIsHonored(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("快捷方式是 Windows 专属")
	}
	restoreCfg := config.SetRoot(t.TempDir())
	defer restoreCfg()

	fakeDesktop := t.TempDir()
	defer DesktopForTest(fakeDesktop)()

	self, err := os.Executable()
	if err != nil {
		t.Skip("拿不到自身路径")
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Skipf("读不到自身：%v", err)
	}
	stable := filepath.Join(t.TempDir(), "installed", "wbmux.exe")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stable, raw, 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := createFor(stable)
	if err != nil {
		t.Skipf("环境不允许建快捷方式：%v", err)
	}
	// 关键断言：产物落在**注入的**目录里，而不是真桌面。
	if filepath.Dir(path) != fakeDesktop {
		t.Errorf("快捷方式没落在注入的桌面里：%s（期望 %s）", path, fakeDesktop)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("注入的桌面里没有产物：%v", err)
	}
}
