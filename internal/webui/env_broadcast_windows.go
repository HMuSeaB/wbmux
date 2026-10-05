//go:build windows

package webui

// 通知系统"环境变量变了"。
//
// # 为什么必须做这一步
//
// 改完注册表后，**已经开着的程序**不会自动知道。资源管理器（Explorer）负责
// 广播 `WM_SETTINGCHANGE`，收到它的程序才会重新读环境块。
// 少这一步的后果很具体：用户设完值、重启客户端，客户端是从 explorer 启动的，
// 而 explorer 手上还是旧环境 —— **新值传不下去，看起来就是"设了没用"**。
//
// 所以这里主动广播一次。失败不影响主流程：最坏情况是"要重新登录或重启才生效"，
// 那也比"静默无效"好，而且我们在界面上已经说了要重启客户端。

import (
	"syscall"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procSendMessageTime = user32.NewProc("SendMessageTimeoutW")
)

const (
	hwndBroadcast   = 0xFFFF
	wmSettingChange = 0x001A
	smtoAbortIfHung = 0x0002
)

// broadcastEnvChange 广播"环境变量已变"。
//
// 用 SendMessageTimeout 而不是 SendMessage：后者会**阻塞等到所有窗口响应**，
// 万一某个程序卡住，我们的 HTTP 请求就跟着卡死。给个 1 秒上限，超时就算了。
func broadcastEnvChange() {
	param, err := syscall.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	procSendMessageTime.Call(
		uintptr(hwndBroadcast),
		uintptr(wmSettingChange),
		0,
		uintptr(unsafe.Pointer(param)),
		uintptr(smtoAbortIfHung),
		uintptr(1000), // 1 秒超时
		uintptr(unsafe.Pointer(&result)),
	)
}
