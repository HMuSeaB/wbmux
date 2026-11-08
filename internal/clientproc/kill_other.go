//go:build !windows

package clientproc

import "fmt"

// Status 包含某个镜像的进程运行状态与 PID 列表。
type Status struct {
	Running bool  `json:"running"`
	PIDs    []int `json:"pids,omitempty"`
}

var inspectHook func(execName string) (Status, bool)

// SetInspectHookForTest 注册测试专用的进程探测钩子，返回清理函数。
func SetInspectHookForTest(hook func(execName string) (Status, bool)) func() {
	prev := inspectHook
	inspectHook = hook
	return func() { inspectHook = prev }
}

// Inspect 在非 Windows 平台如果未设置钩子则一律返回未运行。
func Inspect(execName string) Status {
	if inspectHook != nil {
		if st, ok := inspectHook(execName); ok {
			return st
		}
	}
	return Status{Running: false}
}

// Terminate 在非 Windows 平台不支持。
//
// 客户端只有 Windows 版；这里给明确的失败而不是让代码在编译期消失，
// 因为 CI 会交叉编译 darwin / linux，本包必须能编过。
func Terminate(execName string) (int, error) {
	return 0, fmt.Errorf("当前平台不支持关闭客户端进程（客户端只有 Windows 版）")
}

// TerminateAny 在非 Windows 平台同样不支持，理由见 Terminate。
func TerminateAny(execNames ...string) (int, error) {
	return 0, fmt.Errorf("当前平台不支持关闭客户端进程（客户端只有 Windows 版）")
}
