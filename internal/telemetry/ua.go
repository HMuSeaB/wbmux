package telemetry

import (
	"runtime"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// UserAgent 返回完全拟真官方 Electron/WorkBuddy 桌面端的完整 User-Agent。
//
// 彻底剥离任何包含 (wbmux) 或异常脚手架的特征，在网关与日志审计中呈现为标准的真实桌面端。
func UserAgent(goos string) string {
	if goos == "" {
		goos = runtime.GOOS
	}
	switch goos {
	case "windows":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) WorkBuddy/5.6.2 Chrome/128.0.6613.186 Electron/32.2.8 Safari/537.36"
	case "darwin":
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) WorkBuddy/5.6.2 Chrome/128.0.6613.186 Electron/32.2.8 Safari/537.36"
	default:
		return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) WorkBuddy/5.6.2 Chrome/128.0.6613.186 Electron/32.2.8 Safari/537.36"
	}
}

// Platform 返回与官方客户端 process.platform 语义一致的平台字符串。
func Platform(goos string) string {
	if goos == "" {
		goos = runtime.GOOS
	}
	switch goos {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	default:
		return "linux"
	}
}

// ClientVersion 返回官方客户端当前稳定版本号。
func ClientVersion(id variant.ID) string {
	if id == variant.Intl {
		return "5.5.2"
	}
	return "5.6.2"
}

// CommonHeaders 生成真实客户端外发请求的标准头集合。
func CommonHeaders(id variant.ID, homeDir string, goos string) map[string]string {
	if goos == "" {
		goos = runtime.GOOS
	}
	product := "workbuddy"
	if id == variant.Intl {
		product = "workbuddy-ai"
	}

	return map[string]string{
		"User-Agent":       UserAgent(goos),
		"X-Product":        product,
		"X-Device-Id":      DeviceID(homeDir),
		"X-Client-Version": ClientVersion(id),
		"X-Platform":       Platform(goos),
	}
}
