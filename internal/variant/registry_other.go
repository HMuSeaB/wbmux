//go:build !windows

package variant

// registryInstalls 在非 Windows 平台没有对应机制。
//
// macOS / Linux 上安装位置更规范（/Applications、包管理器路径），
// 常见目录探测已足够。
func registryInstalls() []RegistryEntry { return nil }
