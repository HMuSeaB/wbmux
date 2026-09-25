package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// ANSI 转义序列。仅在终端支持且未禁用时使用。
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
)

// ui 负责所有面向用户的输出。
//
// 集中在一处的好处是着色开关只需要判断一次，且管道输出时自动退化为纯文本。
type ui struct {
	w     io.Writer
	color bool
}

func newUI(w io.Writer) *ui {
	return &ui{w: w, color: supportsColor(w)}
}

// supportsColor 判断是否值得输出 ANSI 转义。
//
// 遵循 NO_COLOR 约定（https://no-color.org），并在输出被重定向到文件或
// 管道时关闭着色——否则日志里会混进一堆转义序列。
func supportsColor(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// setColor 依据 --color 选项覆盖自动判断。
func (u *ui) setColor(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		return nil
	case "always", "yes", "on":
		u.color = true
		return nil
	case "never", "no", "off":
		u.color = false
		return nil
	}
	return fmt.Errorf("--color 只接受 auto/always/never，收到 %q", mode)
}

func (u *ui) paint(code, s string) string {
	if !u.color {
		return s
	}
	return code + s + ansiReset
}

func (u *ui) bold(s string) string   { return u.paint(ansiBold, s) }
func (u *ui) dim(s string) string    { return u.paint(ansiDim, s) }
func (u *ui) red(s string) string    { return u.paint(ansiRed, s) }
func (u *ui) green(s string) string  { return u.paint(ansiGreen, s) }
func (u *ui) yellow(s string) string { return u.paint(ansiYellow, s) }
func (u *ui) cyan(s string) string   { return u.paint(ansiCyan, s) }

// title 打印一段主标题。
func (u *ui) title(s string) {
	fmt.Fprintf(u.w, "%s\n", u.bold(s))
}

// section 在空行之后打印一个小标题。
func (u *ui) section(s string) {
	fmt.Fprintf(u.w, "\n%s\n", u.bold(s))
}

func (u *ui) blank() {
	fmt.Fprintln(u.w)
}

// kv 打印"标签 值"，标签按显示宽度补齐，中文也能对齐。
func (u *ui) kv(label, value string) {
	pad := 10 - dispWidth(label)
	if pad < 1 {
		pad = 1
	}
	fmt.Fprintf(u.w, "  %s%s%s\n", u.dim(label), strings.Repeat(" ", pad), value)
}

// bullet 打印一条缩进的项目符号。
func (u *ui) bullet(s string) {
	fmt.Fprintf(u.w, "    %s %s\n", u.dim("·"), s)
}

func (u *ui) ok(s string)   { fmt.Fprintf(u.w, "%s %s\n", u.green("✓"), s) }
func (u *ui) warn(s string) { fmt.Fprintf(u.w, "%s %s\n", u.yellow("!"), s) }
func (u *ui) fail(s string) { fmt.Fprintf(u.w, "%s %s\n", u.red("×"), s) }
func (u *ui) info(s string) { fmt.Fprintf(u.w, "%s %s\n", u.dim("·"), s) }

// hint 打印一条建议，用于失败项的后续指引。
func (u *ui) hint(s string) {
	fmt.Fprintf(u.w, "    %s\n", u.dim("→ "+s))
}

// dispWidth 估算字符串在终端中占用的列数，CJK 字符按两列计。
//
// 终端里中日韩字符通常是半角字符的两倍宽，不区分就会让对齐错位。
func dispWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			w++
		case isWide(r):
			w += 2
		default:
			w++
		}
	}
	return w
}

// isWide 判断一个字符是否占两列。
//
// 覆盖常见的宽字符区间即可，不追求与 Unicode East Asian Width 完全一致。
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // 谚文字母
		r >= 0x2E80 && r <= 0x303E,   // CJK 部首、标点
		r >= 0x3041 && r <= 0x33FF,   // 假名、注音、CJK 兼容
		r >= 0x3400 && r <= 0x4DBF,   // CJK 扩展 A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK 统一表意文字
		r >= 0xA000 && r <= 0xA4CF,   // 彝文
		r >= 0xAC00 && r <= 0xD7A3,   // 谚文音节
		r >= 0xF900 && r <= 0xFAFF,   // CJK 兼容表意文字
		r >= 0xFE30 && r <= 0xFE6F,   // CJK 兼容形式
		r >= 0xFF00 && r <= 0xFF60,   // 全角形式
		r >= 0xFFE0 && r <= 0xFFE6,   // 全角符号
		r >= 0x1F300 && r <= 0x1FAFF, // 表情符号
		r >= 0x20000 && r <= 0x3FFFD: // CJK 扩展 B 及以上
		return true
	}
	return false
}
