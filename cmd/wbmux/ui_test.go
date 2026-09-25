package main

import (
	"bytes"
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

// 中文标签按显示宽度补齐后，值应当落在同一列上。
func TestKVAlignsCJKLabels(t *testing.T) {
	var buf bytes.Buffer
	u := &ui{w: &buf, color: false}
	u.kv("目标后端", "A")
	u.kv("宿主程序", "B")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d", len(lines))
	}
	if strings.Index(lines[0], "A") != strings.Index(lines[1], "B") {
		t.Errorf("两行的值未对齐:\n%q\n%q", lines[0], lines[1])
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
