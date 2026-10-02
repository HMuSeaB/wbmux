// 把文件移进回收站（Windows）。
//
// # 为什么用 SHFileOperationW 而不是直接删
//
// 直接删没有退路。回收站是可恢复的，而"清会话"这种事用户事后后悔的概率不低。
// 系统自带的回收站机制还负责记录原始路径，右键"还原"就能回到原位。
//
// # 为什么不用 PowerShell / recycle 命令
//
// 走外部进程要处理路径转义、编码、以及"命令不存在"的降级，而 SHFileOperationW
// 是 shell32 里一个直调函数——syscall 就能到，符合本项目的零依赖约定
// （与 internal/tray 的 Shell_NotifyIconW 同一做法）。
package sessions

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	shell32              = syscall.NewLazyDLL("shell32.dll")
	procSHFileOperationW = shell32.NewProc("SHFileOperationW")
)

// SHFILEOPSTRUCTW 对应 Win32 的 SHFILEOPSTRUCTW。
//
// 字段顺序与类型必须与系统头文件一致——它是要按内存布局传给 C 的，
// 顺序错了不会报错，只会做出莫名其妙的事。
type SHFILEOPSTRUCTW struct {
	Hwnd                  uintptr
	WFunc                 uint32
	PFrom                 uintptr // 双 null 结尾的路径列表
	PTo                   uintptr // 不移动文件时留 0
	FFlags                uint16
	FAnyOperationsAborted int32
	HNameMappings         uintptr
	LpszProgressTitle     uintptr
}

const (
	FO_DELETE = 0x0003

	// FOF_ALLOWUNDO 是**关键的一位**：没有它，SHFileOperation 会把文件
	// 永久删除而不是放进回收站。
	FOF_ALLOWUNDO = 0x0040
	// 不给任何界面：静默进回收站，不弹确认框也不弹进度条。
	FOF_SILENT = 0x0004
	// 不弹"确认删除"对话框——确认由调用方负责（它已经让用户看过清单）。
	FOF_NOCONFIRMATION = 0x0010
	// 不弹"操作完成"提示。
	FOF_NOERRORUI = 0x0400
)

// moveToTrash 把一个文件移进回收站。
//
// # 关于返回值的坑（实测踩过）
//
// SHFileOperation 的返回值**不能直接当成功/失败用**。实测把一个临时文件
// 移进回收站：文件确实进去了（回收站的 $I 记录里有它），但返回值是 0x2
// （ERROR_FILE_NOT_FOUND）。所以 0x2 这个码**必须用"文件还在不在"来复核**，
// 否则会把成功的操作报成失败。
//
// 微软文档也提醒：pFrom 必须是**完整路径**（不带完整路径时 FO_DELETE 不会
// 进回收站，即使设了 FOF_ALLOWUNDO）；以及**带 `\\?\` 前缀的路径一律失败**。
// 调用方传进来的都是扫描得到的绝对路径，这两条在调用点另做检查。
func moveToTrash(path string) error {
	// 结构体必须**零值起步**。未用字段（pTo / HNameMappings）留成垃圾值会让
	// 系统读到随机内容——pTo 非 NULL 会被当成"要移动到的目标"。
	//
	// 这里用带字段名的字面量，Go 会把没写的字段置零，等价于 C 的 ZeroMemory。
	// 不要改成"手工给每个字段赋值"的形式，那样反而容易漏。
	var op SHFILEOPSTRUCTW

	from, err := syscall.UTF16FromString(path)
	if err != nil {
		return fmt.Errorf("路径无法转成 UTF-16: %w", err)
	}
	// PFrom 要的是"双 null 结尾"的宽字符串列表：路径 + '\0' + '\0'。
	// UTF16FromString 给的是单结尾，少一个 null 会让它把后面的内存也当路径读。
	from = append(from, 0)

	op.WFunc = FO_DELETE
	op.PFrom = uintptr(unsafe.Pointer(&from[0]))
	op.FFlags = FOF_ALLOWUNDO | FOF_NOCONFIRMATION | FOF_SILENT | FOF_NOERRORUI

	// 第二个参数 bMappings 传 0（false），否则它会尝试序列化映射表。
	ret, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)), 0, 0)

	if op.FAnyOperationsAborted != 0 {
		return fmt.Errorf("操作被中止")
	}
	if ret == 0 {
		return nil
	}

	// 非零返回：**先看文件是不是已经不在原处了**。
	//
	// 这是实测得来的：删成功后返回值仍可能是 0x2（文件找不到）。所以不能
	// 只看返回值——文件确实没了就是成功，还留着才算失败。
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	return fmt.Errorf("SHFileOperation 失败，错误码 %#x（文件仍在原处）", ret)
}
