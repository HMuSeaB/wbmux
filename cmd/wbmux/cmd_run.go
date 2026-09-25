package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/launch"
	"github.com/HMuSeaB/wbmux/internal/product"
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
	target, err := variant.Get(targetID)
	if err != nil {
		return err
	}

	probe := variant.DefaultProbe()
	if *native {
		return runNative(u, target, probe, *dryRun)
	}
	return runOverride(u, target, probe, *dryRun, *extraEndpoints, *c.host, *c.exe)
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

// runNative 用目标档位自己的安装原生启动，不做任何改写。
//
// 存在的意义是留一条对照路径：当覆盖模式出现异常时，可以立刻判断
// 是"覆盖机制的问题"还是"客户端本身的问题"。
func runNative(u *ui, target variant.Backend, probe *variant.Probe, dryRun bool) error {
	inst, err := installOf(target.ID, probe)
	if err != nil {
		return err
	}

	// 即便不注入配置，也把残留的覆盖变量剥掉，
	// 否则用户环境里的旧值会让"原生"不再原生。
	env, stripped := launch.CleanEnv(os.Environ())
	plan := launch.Plan{
		Variant:    target.ID,
		Executable: inst.Executable,
		DataDir:    inst.DataDir,
		Env:        env,
		Stripped:   stripped,
	}

	u.title("原生启动（不做任何改写）")
	u.kv("目标后端", fmt.Sprintf("%s  %s", target.DisplayName, target.Endpoint))
	u.kv("宿主程序", inst.Executable)
	u.kv("数据目录", inst.DataDir)
	reportStripped(u, stripped)

	if dryRun {
		u.section("将要执行")
		u.kv("命令行", plan.CommandLine())
		u.blank()
		u.info("--dry-run：未真正启动")
		return nil
	}

	if err := plan.Run(); err != nil {
		return err
	}
	u.section("已启动")
	return nil
}

// runOverride 是主路径：用宿主安装启动，但把后端指向 target。
func runOverride(u *ui, target variant.Backend, probe *variant.Probe, dryRun bool, extraEndpoints []string, hostFlag, exeFlag string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	hostID, hostInst, err := resolveHost(cfg, hostFlag, exeFlag, probe)
	if err != nil {
		return err
	}
	host, err := variant.Get(hostID)
	if err != nil {
		return err
	}

	cacheDir, err := config.CacheDir()
	if err != nil {
		return err
	}
	outPath := filepath.Join(cacheDir, fmt.Sprintf("%s-to-%s.json", hostID, target.ID))

	opts := product.Options{
		ExtraEndpoints: append(append([]string{}, cfg.ExtraEndpoints...), extraEndpoints...),
	}
	changes, err := product.Generate(hostInst.ProductJSON, outPath, target, opts)
	if err != nil {
		return fmt.Errorf("生成产品配置失败: %w", err)
	}
	// 回读校验：写出去的配置必须真的指向目标后端。
	// 这一步几乎不花时间，却能挡住"补丁字段名写错"这类静默失败。
	if err := verifyEndpoint(outPath, target); err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("无法确定用户主目录: %w", err)
	}

	plan, err := launch.Build(launch.Options{
		Install:    hostInst,
		Target:     target,
		ConfigPath: outPath,
		DataDir:    filepath.Join(home, target.DataFolderName),
		ParentEnv:  os.Environ(),
	})
	if err != nil {
		return err
	}

	u.title(fmt.Sprintf("%s → %s", host.DisplayName, target.DisplayName))
	u.kv("目标后端", fmt.Sprintf("%s  %s", target.DisplayName, target.Endpoint))
	u.kv("宿主程序", fmt.Sprintf("%s  %s", host.DisplayName, plan.Executable))
	u.kv("生成配置", outPath)
	u.kv("数据目录", plan.DataDir)
	reportStripped(u, plan.Stripped)

	if hostID == target.ID {
		u.blank()
		u.warn("目标后端与宿主档位相同，本次不会改变任何字段")
	}

	u.section(fmt.Sprintf("配置改写 %d 项", len(changes)))
	for _, ch := range changes {
		u.bullet(ch.String())
	}
	if len(changes) == 0 {
		u.bullet(u.dim("（无）"))
	}

	if dryRun {
		u.section("将要执行")
		u.kv("命令行", plan.CommandLine())
		u.kv("环境变量", launch.ConfigPathEnv+"="+outPath)
		u.blank()
		u.info("--dry-run：未真正启动")
		return nil
	}

	if err := plan.Run(); err != nil {
		return err
	}
	u.section("已启动")
	u.info("客户端可能需要几秒才显示窗口。")
	return nil
}

// verifyEndpoint 回读生成配置，确认 endpoint 已指向目标后端。
func verifyEndpoint(path string, target variant.Backend) error {
	doc, err := product.Load(path)
	if err != nil {
		return fmt.Errorf("回读生成的配置失败: %w", err)
	}
	got, _ := doc["endpoint"].(string)
	if got != target.Endpoint {
		return fmt.Errorf("生成的配置校验失败：endpoint 为 %q，期望 %q", got, target.Endpoint)
	}
	return nil
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
