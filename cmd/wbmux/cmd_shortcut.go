package main

import (
	"fmt"

	"github.com/HMuSeaB/wbmux/internal/shortcut"
)

// cmdShortcut 创建桌面快捷方式。
//
// 界面里有同一个功能的按钮（/api/shortcut）；这里留一条命令入口，
// 是给脚本化的安装/升级流程用的——下载完 exe 顺手建好快捷方式，两步并一步。
func cmdShortcut(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	help := f.Bool("help", false)
	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := newUIWith(c)
	if err != nil {
		return err
	}
	if *help {
		fmt.Fprint(u.w, `用法：
  wbmux shortcut

在桌面创建（或覆盖）wbmux.lnk，指向当前正在使用的 exe。
exe 挪了位置后重新执行一次，快捷方式即自动跟上。
`)
		return nil
	}
	path, err := shortcut.Create()
	if err != nil {
		return err
	}
	u.ok("已创建桌面快捷方式：" + path)
	return nil
}
