//go:build windows

package variant

import (
	"syscall"
	"unsafe"
)

// 只用到四个注册表 API，因此直接用 syscall 调 advapi32，
// 不为一个探测功能引入 golang.org/x/sys 依赖。
const (
	hkeyCurrentUser  = 0x80000001
	hkeyLocalMachine = 0x80000002

	keyRead        = 0x20019
	keyWow64_64Key = 0x0100
	keyWow64_32Key = 0x0200
)

// uninstallPath 是卸载信息所在的位置。
//
// Electron 应用的安装器默认按当前用户安装（UninstallString 带 /currentuser），
// 记录落在 HKCU；机器级安装落在 HKLM，且 32/64 位视图要分别看。
const uninstallPath = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`

var (
	advapi32             = syscall.NewLazyDLL("advapi32.dll")
	procRegOpenKeyExW    = advapi32.NewProc("RegOpenKeyExW")
	procRegEnumKeyExW    = advapi32.NewProc("RegEnumKeyExW")
	procRegQueryValueExW = advapi32.NewProc("RegQueryValueExW")
	procRegCloseKey      = advapi32.NewProc("RegCloseKey")
)

type regKey uintptr

func regOpen(parent uintptr, sub string, access uint32) (regKey, bool) {
	p, err := syscall.UTF16PtrFromString(sub)
	if err != nil {
		return 0, false
	}
	var h regKey
	r, _, _ := procRegOpenKeyExW.Call(
		parent,
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(access),
		uintptr(unsafe.Pointer(&h)),
	)
	if r != 0 {
		return 0, false
	}
	return h, true
}

func (h regKey) close() {
	if h != 0 {
		_, _, _ = procRegCloseKey.Call(uintptr(h))
	}
}

// subKeys 枚举子键名。任何错误都视为枚举结束：
// 注册表在枚举过程中被改动是正常情况，不值得中断整个探测。
func (h regKey) subKeys() []string {
	var out []string
	buf := make([]uint16, 512)
	for i := uint32(0); ; i++ {
		n := uint32(len(buf))
		r, _, _ := procRegEnumKeyExW.Call(
			uintptr(h),
			uintptr(i),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(unsafe.Pointer(&n)),
			0, 0, 0, 0,
		)
		if r != 0 {
			return out
		}
		out = append(out, syscall.UTF16ToString(buf[:n]))
	}
}

// str 读取一个字符串值，不存在或读取失败时返回空串。
func (h regKey) str(name string) string {
	p, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return ""
	}

	// 第一次调用只取长度：lpData 传 0，系统会把所需字节数写回 size。
	var typ, size uint32
	r, _, _ := procRegQueryValueExW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(p)),
		0,
		uintptr(unsafe.Pointer(&typ)),
		0,
		uintptr(unsafe.Pointer(&size)),
	)
	if r != 0 || size == 0 {
		return ""
	}

	// size 是字节数，含结尾的 UTF-16 空字符，故多留一个 uint16 的余量。
	buf := make([]uint16, size/2+1)
	r, _, _ = procRegQueryValueExW.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(p)),
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

// registryInstalls 读取注册表里登记的已安装程序。
//
// 这是本机最权威的安装位置来源：它由安装器自己写下，不受安装盘符、
// 目录命名习惯影响。实测两套 WorkBuddy 都在 HKCU 下留有记录，
// 而其中国际版既没有数据目录线索、也不在常见安装目录里。
func registryInstalls() []RegistryEntry {
	roots := []struct {
		parent uintptr
		access uint32
	}{
		{hkeyCurrentUser, keyRead},
		{hkeyLocalMachine, keyRead | keyWow64_64Key},
		{hkeyLocalMachine, keyRead | keyWow64_32Key},
	}

	var out []RegistryEntry
	seen := map[string]bool{}

	for _, r := range roots {
		parent, ok := regOpen(r.parent, uninstallPath, r.access)
		if !ok {
			continue
		}
		for _, sub := range parent.subKeys() {
			ck, ok := regOpen(uintptr(parent), sub, r.access)
			if !ok {
				continue
			}
			e := RegistryEntry{
				DisplayName:     ck.str("DisplayName"),
				Publisher:       ck.str("Publisher"),
				InstallLocation: ck.str("InstallLocation"),
				DisplayIcon:     ck.str("DisplayIcon"),
			}
			ck.close()

			if e.DisplayName == "" {
				continue
			}
			// 同一个程序可能在多个视图里重复登记，去重避免候选列表变长。
			key := e.DisplayName + "\x00" + e.InstallLocation + "\x00" + e.DisplayIcon
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, e)
		}
		parent.close()
	}
	return out
}
