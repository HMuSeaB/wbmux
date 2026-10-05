//go:build windows

package webui

// 读写用户级环境变量（HKCU\Environment）。
//
// # 为什么直接摸注册表，而不是 os.Getenv / os.Setenv
//
// os.* 只作用于**本进程**的环境块快照。而这里要影响的是**另一个进程**
// （客户端），而且环境变量是它**启动时**读的。所以要跨进程、跨重启生效，
// 只有写进注册表这一条路（Windows 就是从这里加载用户环境块的）。
//
// 读同理：写完之后本进程的 os.Getenv 仍是旧值，**注册表才是真相**。
// 实测踩过：写完读回来还是旧的，界面看着"没生效"。
//
// # 为什么用 syscall 手写，而不是起一个子进程
//
// 本包历史上用过 PowerShell / reg.exe 起子进程，但：
//   * reg.exe 在本机被安全策略拉黑；
//   * 起子进程慢（每次几百毫秒），而且双击启动时还要小心别弹黑窗口。
// 这里只用到四个注册表 API，直接调 advapi32 最干净，
// 与 internal/variant/registry_windows.go 的做法一致（零第三方依赖）。

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	advapi32            = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyEx    = advapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueEx = advapi32.NewProc("RegQueryValueExW")
	procRegSetValueEx   = advapi32.NewProc("RegSetValueExW")
	procRegDeleteValue  = advapi32.NewProc("RegDeleteValueW")
	procRegCloseKey     = advapi32.NewProc("RegCloseKey")
)

const (
	hkeyCurrentUser = 0x80000001
	keySetValue     = 0x0002
	keyQueryValue   = 0x0001
	regSZ           = 1
	regExpandSZ     = 2
	errorFileNotFnd = 2
)

// userEnvKey 是用户环境变量所在的注册表路径。
const userEnvKey = `Environment`

// utf16Ptr 把字符串转成以 NUL 结尾的 UTF-16 指针。
func utf16Ptr(s string) (*uint16, error) {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// openUserEnv 打开 HKCU\Environment。
func openUserEnv(access uint32) (syscall.Handle, error) {
	sub, err := utf16Ptr(userEnvKey)
	if err != nil {
		return 0, err
	}
	var h syscall.Handle
	r, _, _ := procRegOpenKeyEx.Call(
		uintptr(hkeyCurrentUser),
		uintptr(unsafe.Pointer(sub)),
		0,
		uintptr(access),
		uintptr(unsafe.Pointer(&h)),
	)
	if r != 0 {
		return 0, fmt.Errorf("打开 HKCU\\Environment 失败（错误码 %d）", r)
	}
	return h, nil
}

// readUserEnv 读一个用户级环境变量；不存在或读不到时返回空串。
//
// 刻意不返回 error：调用方（界面）在"没设过"和"读失败"时表现一样
// ——都按"走默认"处理。真出错时硬报错反而会让整页打不开。
func readUserEnv(name string) string {
	h, err := openUserEnv(keyQueryValue)
	if err != nil {
		return ""
	}
	defer procRegCloseKey.Call(uintptr(h))

	n, err := utf16Ptr(name)
	if err != nil {
		return ""
	}

	// 先问长度
	var typ, size uint32
	r, _, _ := procRegQueryValueEx.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(n)),
		0, 0,
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 || size == 0 {
		return ""
	}

	buf := make([]uint16, size/2+1)
	r, _, _ = procRegQueryValueEx.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(n)),
		0,
		uintptr(unsafe.Pointer(&typ)),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}

// setUserEnv 设一个用户级环境变量；value 为空串表示删除它。
func setUserEnv(name, value string) error {
	h, err := openUserEnv(keySetValue)
	if err != nil {
		return err
	}
	defer procRegCloseKey.Call(uintptr(h))

	n, err := utf16Ptr(name)
	if err != nil {
		return err
	}

	if value == "" {
		r, _, _ := procRegDeleteValue.Call(
			uintptr(h), uintptr(unsafe.Pointer(n)))
		// 本来就不存在也算成功——"删掉它"这个意图已经达成。
		if r != 0 && r != errorFileNotFnd {
			return fmt.Errorf("删除环境变量 %s 失败（错误码 %d）", name, r)
		}
		broadcastEnvChange()
		return nil
	}

	v, err := utf16Ptr(value)
	if err != nil {
		return err
	}
	// 字节长度要含结尾的 NUL
	size := uint32((len(value) + 1) * 2)
	r, _, _ := procRegSetValueEx.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(n)),
		0,
		regSZ,
		uintptr(unsafe.Pointer(v)),
		uintptr(size),
	)
	if r != 0 {
		return fmt.Errorf("写入环境变量 %s 失败（错误码 %d）", name, r)
	}
	broadcastEnvChange()
	return nil
}
