package browser

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenRejectsEmptyURL(t *testing.T) {
	if err := Open(""); err == nil {
		t.Fatal("空地址应当报错，而不是去开一个空白窗口")
	}
}

func TestResolvable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-browser")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatalf("造测试文件: %v", err)
	}

	if !resolvable(exe) {
		t.Errorf("存在的绝对路径应当可解析: %s", exe)
	}
	if resolvable(filepath.Join(dir, "missing")) {
		t.Error("不存在的绝对路径不该可解析")
	}
	if resolvable(dir) {
		t.Error("目录不该被当成可执行文件")
	}
	if resolvable("") {
		t.Error("空名字不该可解析")
	}
}

// TestResolvablePrefersPathForBareNames 盯住一个容易写错的地方：
// 不带分隔符的名字才查 PATH，带分隔符的必须按路径查。
// 混用会让绝对路径意外命中 PATH 里的同名程序。
func TestResolvablePrefersPathForBareNames(t *testing.T) {
	if !resolvable("go") && runtime.GOOS != "windows" {
		t.Skip("PATH 里没有 go，跳过")
	}
	if resolvable("./definitely-not-a-real-binary-xyz") {
		t.Error("相对路径不该被当成 PATH 里的名字")
	}
}

func TestAppModeCommandsCarryAppFlag(t *testing.T) {
	cmds := appModeCommands("http://127.0.0.1:1234/?t=abc")
	if len(cmds) == 0 {
		t.Skip("该平台没有应用窗口模式的候选")
	}

	for _, c := range cmds {
		if len(c.argv) < 2 {
			t.Fatalf("候选命令缺少参数: %v", c.argv)
		}
		joined := strings.Join(c.argv, " ")
		if !strings.Contains(joined, "http://127.0.0.1:1234/?t=abc") {
			t.Errorf("候选命令未带上地址: %v", c.argv)
		}
		// 没有 --app= 就退化成普通标签页，界面的"像本地程序"效果就没了。
		if !strings.Contains(joined, "--app=") {
			t.Errorf("候选命令缺少 --app=: %v", c.argv)
		}
	}
}

// TestAppModeCandidatesAreAbsoluteOnWindows 确保 Windows 上给的是绝对路径：
// 拼出相对路径后，一旦当前目录恰好有同名文件就会被误判为可用。
func TestAppModeCandidatesAreAbsoluteOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只对 Windows 的候选做绝对路径要求")
	}
	for _, c := range appModeCommands("http://127.0.0.1:1/") {
		if !filepath.IsAbs(c.need) {
			t.Errorf("候选前提不是绝对路径: %q", c.need)
		}
		if !filepath.IsAbs(c.argv[0]) {
			t.Errorf("候选程序不是绝对路径: %q", c.argv[0])
		}
	}
}
