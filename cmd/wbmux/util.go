package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// absPath 把用户输入的路径转成绝对路径。
//
// 先展开 ~，再对相对路径取绝对路径：用户手写 --exe 时常用波浪号或相对路径，
// 直接拿去做后续比较与展示都不一致。
//
// 是否"绝对"由 variant.IsAbsolutePath 判断而不是 filepath.IsAbs：
// 后者按当前平台判断，在 Linux/macOS 上会把 `D:\App\App.exe` 当成相对路径
// 并拼上工作目录，把路径悄悄改坏。
func absPath(p string) (string, error) {
	p = expandHome(strings.TrimSpace(p))
	if p == "" {
		return "", fmt.Errorf("路径为空")
	}
	if variant.IsAbsolutePath(p) {
		return filepath.Clean(p), nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("解析路径 %q 失败: %w", p, err)
	}
	return filepath.Clean(abs), nil
}

// expandHome 把开头的 ~ 替换为用户主目录。
func expandHome(p string) string {
	if p != "~" && !hasHomePrefix(p) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}

func hasHomePrefix(p string) bool {
	return len(p) >= 2 && p[0] == '~' && (p[1] == '/' || p[1] == '\\')
}
