package update

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"v0.5.0", "0.4.2", true},       // 正常升级
		{"v0.5.0", "0.5.0", false},      // 同版本
		{"v0.5.0", "0.6.0", false},      // 本地更新
		{"v0.5.0", "0.4.9", true},       // 补丁位
		{"v0.5.0", "0.5.0-beta1", true}, // 预发布尾巴按数字截断
		{"v0.5.0", "dev", true},         // 开发版：有正式发布就提示
		{"v0.5.0", "", true},            // 空版本同上
		{"garbage", "0.4.0", false},     // latest 解析不了：不乱推荐
		{"v0.5", "0.4.9", true},         // 两段版本也认
	}
	for _, c := range cases {
		if got := isNewer(c.latest, c.current); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestSemverParts(t *testing.T) {
	cases := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"v0.5.0", [3]int{0, 5, 0}, true},
		{"0.4.2", [3]int{0, 4, 2}, true},
		{"v1", [3]int{1, 0, 0}, true},
		{"v0.5.0-beta1", [3]int{0, 5, 0}, true},
		{"dev", [3]int{}, false},
		{"", [3]int{}, false},
	}
	for _, c := range cases {
		got, ok := semverParts(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("semverParts(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestPickWindowsAsset(t *testing.T) {
	assets := []ghAsset{
		{Name: "checksums.txt", BrowserDownloadURL: "https://example.com/checksums.txt"},
		{Name: "wbmux_0.5.0_Linux_amd64.zip", BrowserDownloadURL: "https://example.com/linux.zip"},
		{Name: "wbmux_0.5.0_windows_arm64.zip", BrowserDownloadURL: "https://example.com/arm64.zip"},
		{Name: "wbmux_0.5.0_windows_amd64.zip", BrowserDownloadURL: "https://example.com/win64.zip"},
	}
	got := pickWindowsAsset(assets)
	if got != "https://example.com/win64.zip" {
		t.Errorf("挑错资产：%q", got)
	}
	if pickWindowsAsset(nil) != "" {
		t.Error("空资产应返回空串")
	}
}

func TestDisplayVersion(t *testing.T) {
	if displayVersion("") != "dev" || displayVersion("  ") != "dev" {
		t.Error("空版本应显示 dev")
	}
	if displayVersion("0.5.0") != "0.5.0" {
		t.Error("非空版本应原样返回")
	}
}

// TestParseChecksums 解析 goreleaser 的 checksums.txt。
func TestParseChecksums(t *testing.T) {
	raw := "3ac66ff62260d48a72c9c8f57ee1d9eff5750662452331a59121cf034d44d8f6  wbmux_0.6.0_windows_amd64.zip\n" +
		"\n" +
		"abc123  *binary-with-star-prefix\n" +
		"这行不是校验和\n"
	got := parseChecksums(raw)
	if got["wbmux_0.6.0_windows_amd64.zip"] != "3ac66ff62260d48a72c9c8f57ee1d9eff5750662452331a59121cf034d44d8f6" {
		t.Errorf("没解出 windows 包: %v", got)
	}
	if got["binary-with-star-prefix"] != "abc123" {
		t.Errorf("应当剥掉 * 前缀（二进制模式的标记）: %v", got)
	}
	if len(got) != 2 {
		t.Errorf("只应解出 2 条，得到 %d 条: %v", len(got), got)
	}
}

// TestVerifyChecksum 校验和是"下载到的到底是不是官方那个包"的唯一可靠判据。
//
// 背景：2026-09-28，~/.wbmux/bin/wbmux.exe（升级要替换的目标位置）被写成了
// 一个测试二进制。它同样有 MZ 头、体量也够，所以"像不像 PE"完全拦不住；
// 而按字符串猜身份试过三组判据、全部不可靠（见 checkPE 的注释）。
func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "wbmux_0.9.9_windows_amd64.zip")
	content := []byte("pretend this is a zip")
	if err := os.WriteFile(zipPath, content, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	// ① 一致 → 通过
	note, err := verifyChecksum(zipPath, "wbmux_0.9.9_windows_amd64.zip",
		map[string]string{"wbmux_0.9.9_windows_amd64.zip": good})
	if err != nil {
		t.Fatalf("校验和一致时不该报错：%v", err)
	}
	if !strings.Contains(note, "核对") {
		t.Errorf("应当说明已核对，得到 %q", note)
	}

	// ② 不一致 → 拒绝，且报错要说人话（别只说"失败"）
	_, err = verifyChecksum(zipPath, "wbmux_0.9.9_windows_amd64.zip",
		map[string]string{"wbmux_0.9.9_windows_amd64.zip": strings.Repeat("0", 64)})
	if err == nil {
		t.Fatal("校验和不符必须拒绝")
	}
	if !strings.Contains(err.Error(), "校验和不符") {
		t.Errorf("报错应当点明校验和不符，得到：%v", err)
	}

	// ③ release 没提供该文件的校验和 → 放行但如实说明
	note, err = verifyChecksum(zipPath, "wbmux_0.9.9_windows_amd64.zip", map[string]string{})
	if err != nil {
		t.Fatalf("没有校验和时不该拦（老 release 可能没带）：%v", err)
	}
	if !strings.Contains(note, "未校验") {
		t.Errorf("应当如实说明未校验，得到 %q", note)
	}
}

// TestCheckPE 只做最低标准的体检（MZ 头 + 体量）。
func TestCheckPE(t *testing.T) {
	dir := t.TempDir()
	notPE := filepath.Join(dir, "x.exe")
	_ = os.WriteFile(notPE, bytes.Repeat([]byte("A"), 8<<20), 0o644)
	if err := checkPE(notPE); err == nil {
		t.Error("没有 MZ 头应当拒绝")
	}
	small := filepath.Join(dir, "y.exe")
	_ = os.WriteFile(small, append([]byte("MZ"), bytes.Repeat([]byte("A"), 1024)...), 0o644)
	if err := checkPE(small); err == nil {
		t.Error("体量过小应当拒绝")
	}
}

// repoRoot 找到仓库根（go.mod 所在处）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("找不到仓库根")
		}
		dir = parent
	}
}
