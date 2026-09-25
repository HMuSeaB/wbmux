// Package version 保存构建期注入的版本信息。
package version

import "runtime"

// 以下变量通过 -ldflags "-X ..." 在构建时注入。
var (
	// Version 是语义化版本号。
	Version = "dev"
	// Commit 是构建时的提交哈希。
	Commit = "none"
	// Date 是构建时间。
	Date = "unknown"
)

// String 返回一行可读的版本描述。
func String() string {
	return Version + " (" + short(Commit) + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}

func short(s string) string {
	if s == "" || s == "none" {
		return "none"
	}
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
