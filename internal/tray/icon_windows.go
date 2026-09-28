//go:build windows

package tray

import "unsafe"

// 把 icon.go 画出来的字节变成 HICON。

// createAppIcon 生成 wbmux 自己的托盘图标。
//
// 失败一律退回系统通用图标：图标丑一点也比没有图标好——托盘是用户判断
// "进程还在不在"的唯一入口，不能因为它而整个托盘起不来。
func createAppIcon() uintptr {
	bits := iconResourceBytes(iconSize)
	// CreateIconFromResourceEx(presbits, dwResSize, fIcon, dwVer, cx, cy, flags)
	// dwVer 用 0x00030000（图标资源版本），flags 0 = LR_DEFAULTCOLOR。
	h, _, _ := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&bits[0])),
		uintptr(len(bits)),
		1, // fIcon
		0x00030000,
		iconSize, iconSize,
		0,
	)
	if h != 0 {
		return h
	}
	icon, _, _ := procLoadIconW.Call(0, idiApplication)
	return icon
}
