package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/runner"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func cmdRun(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("n", "dry-run")
	f.Alias("h", "help")
	dryRun := f.Bool("dry-run", false)
	native := f.Bool("native", false)
	extraEndpoints := f.List("extra-endpoint")
	help := f.Bool("help", false)

	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := newUIWith(c)
	if err != nil {
		return err
	}
	if *help {
		printRunHelp(u)
		return nil
	}

	targetID, err := singleTarget(f.Args)
	if err != nil {
		return err
	}

	res, err := runner.Prepare(runner.Options{
		Target:         targetID,
		Native:         *native,
		HostFlag:       *c.host,
		ExeFlag:        *c.exe,
		ExtraEndpoints: *extraEndpoints,
		ParentEnv:      os.Environ(),
	})
	if err != nil {
		return err
	}

	printRunPlan(u, res)

	if *dryRun {
		u.section("将要执行")
		u.kv("命令行", res.CommandLine)
		if res.ConfigPath != "" {
			u.kv("环境变量", "ACC_PRODUCT_CONFIG_PATH="+res.ConfigPath)
		}
		u.blank()
		u.info("--dry-run：未真正启动")
		return nil
	}

	if err := res.Launch(); err != nil {
		return err
	}
	u.section("已启动")
	u.info("客户端可能需要几秒才显示窗口。")
	return nil
}

// printRunPlan 打印一次切换的完整预览。
//
// 两种模式的展示结构不同：原生模式没有改写项，硬套同一套输出会留下一片空白。
func printRunPlan(u *ui, res runner.Result) {
	if res.Native {
		u.title("原生启动（不做任何改写）")
		u.kv("目标后端", fmt.Sprintf("%s  %s", res.Target.DisplayName, res.Target.Endpoint))
		u.kv("宿主程序", res.Executable)
		u.kv("数据目录", res.DataDir)
		reportStripped(u, res.Stripped)
		return
	}

	u.title(fmt.Sprintf("%s → %s", res.Host.DisplayName, res.Target.DisplayName))
	u.kv("目标后端", fmt.Sprintf("%s  %s", res.Target.DisplayName, res.Target.Endpoint))
	u.kv("宿主程序", fmt.Sprintf("%s  %s", res.Host.DisplayName, res.Executable))
	u.kv("生成配置", res.ConfigPath)
	u.kv("数据目录", res.DataDir)
	reportStripped(u, res.Stripped)

	for _, w := range res.Warnings {
		u.blank()
		u.warn(w)
	}

	u.section(fmt.Sprintf("配置改写 %d 项", len(res.Changes)))
	for _, ch := range res.Changes {
		u.bullet(ch.String())
	}
	if len(res.Changes) == 0 {
		u.bullet(u.dim("（无）"))
	}
}

// singleTarget 从位置参数里取出唯一的目标后端。
func singleTarget(args []string) (variant.ID, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("%w，例如 `wbmux run intl`", errNoTarget)
	}
	if len(args) > 1 {
		return "", fmt.Errorf("只接受一个目标后端，收到 %d 个：%s", len(args), strings.Join(args, " "))
	}
	return variant.Parse(args[0])
}

func reportStripped(u *ui, stripped []string) {
	if len(stripped) == 0 {
		return
	}
	u.kv("已剥离", strings.Join(stripped, ", "))
}

func printRunHelp(u *ui) {
	fmt.Fprint(u.w, `用法: wbmux run <后端> [选项]

用宿主安装启动客户端，但让它连接指定的后端。

选项:
  --dry-run               只打印将要执行的内容，不启动
  --native                用目标后端自己的安装原生启动，不做任何改写
  --extra-endpoint <URL>  追加进 officialEndpoints，可重复
  --host <cn|intl>        用哪一套安装作为宿主程序
  --exe <路径>            直接指定宿主主程序
  --color <模式>          auto（默认）/ always / never
  -h, --help              打印本帮助

示例:
  wbmux run intl
  wbmux run cn --dry-run
  wbmux run intl --host cn --exe "D:\Tools\WorkBuddy\WorkBuddy.exe"
`)
}
