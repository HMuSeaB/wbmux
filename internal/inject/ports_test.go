package inject

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReadDevToolsActivePort 钉住解析口径：只认第一行、越界与坏格式都当没有。
func TestReadDevToolsActivePort(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if got := readDevToolsActivePort(write("ok", "54321\n/devtools/browser/abc\n")); got != 54321 {
		t.Errorf("正常文件应解析出 54321，得到 %d", got)
	}
	if got := readDevToolsActivePort(write("bad", "not-a-port\n")); got != 0 {
		t.Errorf("坏格式应返回 0，得到 %d", got)
	}
	if got := readDevToolsActivePort(write("range", "70000\n")); got != 0 {
		t.Errorf("越界端口应返回 0，得到 %d", got)
	}
	if got := readDevToolsActivePort(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("文件不存在应返回 0，得到 %d", got)
	}
}

// TestDiscoverPorts 验证发现顺序与去重：DevToolsActivePort 的端口在前，
// 固定端口兜底，重复的只留一个。
func TestDiscoverPorts(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "DevToolsActivePort"), []byte("51000\nabc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// dirB 没有 DevToolsActivePort（客户端未以 port=0 启动）

	got := discoverPorts([]string{dirA, dirB}, []int{15462}, []int{9222, 51000, 9223})
	want := []int{51000, 15462, 9222, 9223}
	if len(got) != len(want) {
		t.Fatalf("端口数不对，得到 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("端口顺序不对，得到 %v，期望 %v", got, want)
		}
	}
}
