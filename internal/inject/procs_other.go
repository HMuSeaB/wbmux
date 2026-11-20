//go:build !windows

package inject

// clientListeningPorts 只在 Windows 上实现：那边 WorkBuddy 用 port=0 启动时
// 不写 DevToolsActivePort，需要枚举进程监听端口。其他平台仍依赖该文件。
func clientListeningPorts() []int { return nil }
