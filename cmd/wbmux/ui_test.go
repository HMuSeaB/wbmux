package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestDispWidthCountsCJKAsTwoColumns(t *testing.T) {
	cases := map[string]int{
		"":      0,
		"abc":   3,
		"目标后端":  8,
		"版本 v1": 7, // 版本(4) + 空格(1) + v1(2)
		"a目":    3,
	}
	for in, want := range cases {
		if got := dispWidth(in); got != want {
			t.Errorf("dispWidth(%q) = %d, 期望 %d", in, got, want)
		}
	}
}

// colOf 返回 sub 在 line 内的起始**显示列**。
//
// 不能直接比较 strings.Index：它返回字节偏移，而中文标签一个字符占
// 3 个字节、2 个显示列，两者并不相等。
func colOf(t *testing.T, line, sub string) int {
	t.Helper()
	i := strings.Index(line, sub)
	if i < 0 {
		t.Fatalf("行内找不到 %q: %q", sub, line)
	}
	return dispWidth(line[:i])
}

// 中文标签按显示宽度补齐后，值应当落在同一列上。
func TestKVAlignsCJKLabels(t *testing.T) {
	var buf bytes.Buffer
	u := &ui{w: &buf, color: false}
	u.kv("目标后端", "#A#")
	u.kv("宿主程序", "#B#")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d", len(lines))
	}
	a, b := colOf(t, lines[0], "#A#"), colOf(t, lines[1], "#B#")
	if a != b {
		t.Errorf("两行的值未对齐（第 %d 列 vs 第 %d 列）:\n%q\n%q", a, b, lines[0], lines[1])
	}
}

// 标签宽度不一（4 / 6 / 8 / 10 列）时仍要对齐。
//
// 回归测试：kvWidth 曾设为 10，遇到 10 列宽的"宿主主程序"会把值推右一格。
// 这里覆盖代码里实际用到的最宽与最窄标签，改动 kvWidth 时会被挡住。
func TestKVAlignsAcrossAllLabelWidths(t *testing.T) {
	labels := []string{
		"输出",    // 4 列
		"命令行",   // 6 列
		"目标后端",  // 8 列
		"宿主主程序", // 10 列，当前最长的 kv 标签
		"环境变量",  // 8 列
	}
	for _, l := range labels {
		if dispWidth(l) >= kvWidth {
			t.Errorf("标签 %q 的显示宽度 %d 已达 kvWidth=%d，会把值推右一格",
				l, dispWidth(l), kvWidth)
		}
	}

	var buf bytes.Buffer
	u := &ui{w: &buf, color: false}
	values := make([]string, len(labels))
	for i, l := range labels {
		values[i] = fmt.Sprintf("#%d#", i)
		u.kv(l, values[i])
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(labels) {
		t.Fatalf("行数 = %d, 期望 %d", len(lines), len(labels))
	}
	want := colOf(t, lines[0], values[0])
	for i, line := range lines {
		if got := colOf(t, line, values[i]); got != want {
			t.Errorf("第 %d 行的值在第 %d 列，期望第 %d 列:\n%q", i, got, want, line)
		}
	}
}

// 标签比 kvWidth 还宽时，只把值推右一格，不能挤成一团或截断。
func TestKVOverlongLabelDegradesGracefully(t *testing.T) {
	const label = "authentication.attributes.platform"

	var buf bytes.Buffer
	u := &ui{w: &buf, color: false}
	u.kv(label, "#v#")

	got := strings.TrimRight(buf.String(), "\n")
	if !strings.Contains(got, label) {
		t.Errorf("标签被截断: %q", got)
	}
	if !strings.Contains(got, label+" #v#") {
		t.Errorf("标签与值之间没有留出分隔: %q", got)
	}
}

func TestColorDisabledByDefaultOnBuffer(t *testing.T) {
	var buf bytes.Buffer
	u := &ui{w: &buf, color: supportsColor(&buf)}
	u.ok("done")
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("非终端输出不应带转义序列: %q", buf.String())
	}
}

func TestSetColorOverrides(t *testing.T) {
	var buf bytes.Buffer

	u := &ui{w: &buf}
	if err := u.setColor("always"); err != nil {
		t.Fatal(err)
	}
	if !u.color {
		t.Error("--color always 未启用着色")
	}

	if err := u.setColor("never"); err != nil {
		t.Fatal(err)
	}
	if u.color {
		t.Error("--color never 未关闭着色")
	}

	if err := u.setColor("auto"); err != nil {
		t.Fatal(err)
	}

	if err := u.setColor("rainbow"); err == nil {
		t.Error("非法模式应报错")
	}
}

func TestPaintEmitsResetOnlyWhenColored(t *testing.T) {
	plain := &ui{w: &bytes.Buffer{}, color: false}
	if got := plain.green("x"); got != "x" {
		t.Errorf("无色时 = %q", got)
	}

	colored := &ui{w: &bytes.Buffer{}, color: true}
	if got := colored.green("x"); got != ansiGreen+"x"+ansiReset {
		t.Errorf("有色时 = %q", got)
	}
}
