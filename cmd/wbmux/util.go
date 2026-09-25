package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// absPath 把用户输入的路径转成绝对路径。
//
// 先展开 ~，再相对当前工作目录取绝对路径：用户手写 --exe 时
// 常带波浪号或相对路径，直接用会导致后续比较与展示都不一致。
func absPath(p string) (string, error) {
	p = expandHome(p)
	if p == "" {
		return "", fmt.Errorf("路径为空")
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
