//go:build !windows

package webui

import "fmt"

// readUserEnv 在非 Windows 平台没有"用户级环境变量"这个概念
// （客户端本来也只有 Windows 版），一律返回空串按"未设置"处理。
func readUserEnv(name string) string { return "" }

// setUserEnv 在非 Windows 平台明确失败。
//
// 代码要能在三平台编过（CI 会交叉编译 darwin/linux），所以给一个清楚的错误，
// 而不是让它在编译期消失。
func setUserEnv(name, value string) error {
	return fmt.Errorf("当前平台不支持修改客户端环境变量（客户端只有 Windows 版）")
}
