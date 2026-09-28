package update

import "testing"

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
