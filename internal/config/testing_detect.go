package config

import (
	"os"
	"strings"
)

// testing 报告"当前进程是不是测试二进制"。
//
// 判据取 os.Args[0] 是否以 .test 结尾（`go test` 编译出的可执行文件名），
// 这是最省事也最准的：正常构建的 wbmux 永远不会叫这个名字。
func inTestBinary() bool {
	if len(os.Args) == 0 {
		return false
	}
	base := strings.ToLower(filepathBase(os.Args[0]))
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe")
}

func filepathBase(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
