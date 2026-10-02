// genicon 生成 wbmux.exe 的图标资源文件。
//
// 一次性的构建期工具：把托盘那把"运行时自画 ⇄"落成 wbmux.ico，再用
// goversioninfo 编成 .syso 放进 cmd/wbmux/，Windows 资源管理器、任务栏
// 与桌面快捷方式就都有品牌图标了（此前 exe 是 Go 默认的"白纸"图标，
// 2026-10-02 反馈"没图标啊"）。
//
// 用法：go run ./tools/genicon <输出路径>
package main

import (
	"fmt"
	"os"

	"github.com/HMuSeaB/wbmux/internal/tray"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "用法: genicon <wbmux.ico 输出路径>")
		os.Exit(2)
	}
	// 覆盖常用尺寸：16/24 是资源管理器小图与任务栏，32/48 是桌面与
	// 大图标视图，256 是超大图标视图。drawIcon 按尺寸参数化绘制。
	ico := tray.ICOFileBytes(16, 24, 32, 48, 64, 256)
	if err := os.WriteFile(os.Args[1], ico, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "写出失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s（%d 字节，6 个尺寸）\n", os.Args[1], len(ico))
}
