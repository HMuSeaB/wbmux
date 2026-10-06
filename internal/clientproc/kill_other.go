//go:build !windows

package clientproc

import "fmt"

// Terminate 在非 Windows 平台不支持。
//
// 客户端只有 Windows 版；这里给明确的失败而不是让代码在编译期消失，
// 因为 CI 会交叉编译 darwin / linux，本包必须能编过。
func Terminate(execName string) (int, error) {
	return 0, fmt.Errorf("当前平台不支持关闭客户端进程（客户端只有 Windows 版）")
}
