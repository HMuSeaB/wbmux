//go:build !windows

package netroute

// systemProxy 在非 Windows 上没有"注册表系统代理"这回事，
// macOS 的 NetworkExtension 配置与 Linux 的 gsettings 都不在这一层处理。
func systemProxy() (string, string) { return "", "" }
