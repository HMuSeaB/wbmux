//go:build windows

// Package tray 在 Windows 通知区域（系统托盘）上放一个图标。
//
// # 为什么需要它
//
// 图形界面是"内嵌网页 + 系统浏览器"这条路（见 internal/browser 的说明）。
// 好处是保住了零 cgo、交叉编译；代价是**进程在不在跑，用户看不见**——
// 浏览器窗口关掉之后，光看桌面完全判断不出来。托盘图标补上这一块：
// 一眼看出在不在跑，右键能直接打开界面或退出。
//
// # 为什么直接调 Win32
//
// 本项目零第三方依赖，托盘也没有标准库支持。这里用 syscall 直调
// shell32 / user32，与 internal/console 的做法一致。
//
// 图标用系统自带的通用应用图标（LoadIconW(NULL, IDI_APPLICATION)）：
// 本项目的二进制没有嵌资源图标，硬去抽一个反而更脆。
package tray

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// Available 报告当前平台是否支持托盘。Windows 上恒为真。
func Available() bool { return true }

// Options 是托盘的行为回调。
type Options struct {
	// Tooltip 是鼠标悬停时显示的文字。
	Tooltip string
	// OnOpen 在"打开界面"被点击时调用。
	OnOpen func()
	// OnQuit 在"退出"被点击时调用。应当触发服务端关闭，
	// 从而让主流程的等待返回。
	OnQuit func()
	// Ready 在图标确实进入通知区域后被调用一次，可为 nil。
	//
	// 存在的理由是"没有报错"不等于"图标显示出来了"：Shell_NotifyIcon
	// 返回真才是真的注册成功。有了它，调用方能给出正面确认，
	// 而不是让用户对着一个不知道有没有生效的功能猜。
	Ready func()
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu  = user32.NewProc("CreatePopupMenu")
	procAppendMenuW      = user32.NewProc("AppendMenuW")
	procTrackPopupMenu   = user32.NewProc("TrackPopupMenu")
	procDestroyMenu      = user32.NewProc("DestroyMenu")
	procSetForegroundWnd = user32.NewProc("SetForegroundWindow")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procGetCursorPos     = user32.NewProc("GetCursorPos")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
)

const (
	// trayCallbackMessage 是托盘图标回传给窗口的自定义消息号。
	// 取 WM_APP+1，这是该用途的常见约定，避免与系统消息冲突。
	trayCallbackMessage = 0x8000 + 1

	nimAdd    = 0x00000000
	nimDelete = 0x00000002
	nifMsg    = 0x00000001
	nifIcon   = 0x00000002
	nifTip    = 0x00000004

	wmDestroy   = 0x0002
	wmClose     = 0x0010
	wmRButtonUp = 0x0205
	wmLButtonUp = 0x0202
	wmCommand   = 0x0111

	tpmRightButton = 0x0002
	tpmReturnCmd   = 0x0100
	tpmNonotify    = 0x0080

	mfString    = 0x00000000
	mfSeparator = 0x00000800

	// menuOpenID / menuQuitID 是右键菜单项的编号。
	menuOpenID = 1
	menuQuitID = 2

	// idiApplication 是系统通用应用图标。
	idiApplication = 32512

	// hwndMessage 不是这里的选项：托盘图标挂在"隐藏的普通顶层窗口"上，
	// 而不是 message-only 窗口。后者在部分 Windows 版本上收不到
	// 托盘回调，表现成"图标在但点不动"。
)

// wndClassExW 对应 Win32 的 WNDCLASSEXW。
//
// 字段顺序与类型必须与 C 结构逐字对齐，否则 RegisterClassExW 会失败。
// 4 字节字段用 uint32/int32、指针用 uintptr，让 Go 自己插入与 C 相同的
// 对齐填充。
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// notifyIconDataW 对应 Win32 的 NOTIFYICONDATAW。
//
// 同样必须逐字对齐。cbSize 用 unsafe.Sizeof 求得，不写死数字——
// 写死会在结构改动后静默失效。
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	ptX     int32
	ptY     int32
}

type point struct{ x, y int32 }

// state 持有窗口过程需要用到的回调。
//
// 窗口过程是由系统回调的裸函数，拿不到闭包，只能放到包级变量里。
// 因此同一进程只支持一个托盘。
var state struct {
	opts   Options
	hwnd   uintptr
	icon   uintptr
	inited bool
}

// Run 显示托盘图标并进入消息循环，直到退出项被点击。
//
// 会阻塞，且**必须运行在锁定后的 OS 线程上**：Win32 的消息循环要求创建
// 窗口的线程来跑。调用方负责 runtime.LockOSThread()。
func Run(opts Options) error {
	if state.inited {
		return fmt.Errorf("tray: 已经有一个托盘在运行")
	}
	state.opts = opts

	hInst, _, _ := procGetModuleHandleW.Call(0)

	className, err := syscall.UTF16PtrFromString("wbmuxTrayWnd")
	if err != nil {
		return err
	}
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     hInst,
		lpszClassName: className,
	}
	if ret, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); ret == 0 {
		return fmt.Errorf("tray: RegisterClassExW 失败: %v", callErr)
	}

	// 建一个不显示的顶层窗口来收托盘消息。全程不调 ShowWindow。
	hwnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		0, 0, 0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("tray: CreateWindowExW 失败: %v", callErr)
	}
	state.hwnd = hwnd

	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	state.icon = icon

	nid := notifyIconDataW{
		cbSize:           uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:             hwnd,
		uID:              1,
		uFlags:           nifMsg | nifIcon | nifTip,
		uCallbackMessage: trayCallbackMessage,
		hIcon:            icon,
	}
	copy(nid.szTip[:], syscall.StringToUTF16(opts.Tooltip))

	if ret, _, callErr := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); ret == 0 {
		return fmt.Errorf("tray: Shell_NotifyIcon(NIM_ADD) 失败（图标未能加入通知区域）: %v", callErr)
	}

	state.inited = true
	if opts.Ready != nil {
		opts.Ready()
	}
	defer func() {
		ret, _, _ := procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
		_ = ret
		_, _, _ = procDestroyWindow.Call(hwnd)
		state.inited = false
	}()

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		// GetMessage 返回 0 表示 WM_QUIT，-1 表示出错；两者都该结束。
		if int32(ret) <= 0 {
			return nil
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// wndProc 是窗口过程。
func wndProc(hwnd uintptr, m uint32, wParam, lParam uintptr) uintptr {
	switch m {
	case trayCallbackMessage:
		// 左键或右键都弹菜单：单靠左键直接开界面会让"退出"没地方放，
		// 而托盘的通用习惯就是右键出菜单。
		if lParam == wmRButtonUp || lParam == wmLButtonUp {
			showMenu(hwnd)
		}
		return 0

	case wmCommand:
		switch wParam & 0xffff {
		case menuOpenID:
			if state.opts.OnOpen != nil {
				state.opts.OnOpen()
			}
		case menuQuitID:
			if state.opts.OnQuit != nil {
				state.opts.OnQuit()
			}
			_, _, _ = procPostQuitMessage.Call(0)
		}
		return 0

	case wmClose, wmDestroy:
		_, _, _ = procPostQuitMessage.Call(0)
		return 0
	}

	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(m), wParam, lParam)
	return ret
}

// showMenu 在光标处弹出右键菜单。
func showMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer func() { _, _, _ = procDestroyMenu.Call(menu) }()

	openLabel, _ := syscall.UTF16PtrFromString("打开界面")
	quitLabel, _ := syscall.UTF16PtrFromString("退出")

	_, _, _ = procAppendMenuW.Call(menu, mfString, menuOpenID, uintptr(unsafe.Pointer(openLabel)))
	_, _, _ = procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	_, _, _ = procAppendMenuW.Call(menu, mfString, menuQuitID, uintptr(unsafe.Pointer(quitLabel)))

	var pt point
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// TrackPopupMenu 前必须先 SetForegroundWindow，否则菜单会"粘住"
	// ——点别处不消失。这是该 API 的既有怪癖，官方文档也这么要求。
	_, _, _ = procSetForegroundWnd.Call(hwnd)

	_, _, _ = procTrackPopupMenu.Call(
		menu,
		tpmRightButton|tpmReturnCmd|tpmNonotify,
		uintptr(pt.x), uintptr(pt.y),
		0, hwnd, 0,
	)
	// 之后补一个 WM_NULL，同样是官方建议的收尾动作。
	_, _, _ = procPostMessageW.Call(hwnd, 0, 0, 0)
}

// 保证 LockOSThread 被引用到，提示调用方的义务。
var _ = runtime.LockOSThread
