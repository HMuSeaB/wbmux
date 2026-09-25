package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/doctor"
	"github.com/HMuSeaB/wbmux/internal/product"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func cmdDoctor(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	f.Alias("f", "fast")
	targetFlag := f.String("target", string(variant.Intl))
	fast := f.Bool("fast", false)
	help := f.Bool("help", false)

	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := newUIWith(c)
	if err != nil {
		return err
	}
	if *help {
		fmt.Fprint(u.w, `用法: wbmux doctor [选项]

体检：确认安装位置、自带配置、以及配置覆盖机制是否仍然有效。

选项:
  --target <cn|intl>   检查"切到该后端"这条路径（默认 intl）
  --fast               跳过安装包扫描，只做快速检查
  --host <cn|intl>     用哪一套安装作为宿主程序
  --exe <路径>         直接指定宿主主程序
  --color <模式>       auto（默认）/ always / never
  -h, --help           打印本帮助
`)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	hostFlag, exeFlag := *c.host, *c.exe
	if exeFlag == "" {
		exeFlag = cfg.HostExe
	}
	if hostFlag == "" {
		hostFlag = cfg.HostVariant
	}
	if len(f.Args) > 0 {
		hostFlag = f.Args[0]
	}

	// 宿主档位留空时让 doctor 自己去判定：它内部会做完整的探测，
	// 这样"一个都没装"这种情况也能给出比 CLI 层更细的说明。
	hostID := variant.ID("")
	if hostFlag != "" {
		hostID, err = variant.Parse(hostFlag)
		if err != nil {
			return err
		}
	}

	targetID, err := variant.Parse(*targetFlag)
	if err != nil {
		return err
	}

	cacheDir, err := config.CacheDir()
	if err != nil {
		return err
	}

	rep := doctor.Run(doctor.Options{
		HostExe:     exeFlag,
		HostVariant: hostID,
		Target:      targetID,
		CacheDir:    cacheDir,
		SkipAsar:    *fast,
	})

	printReport(u, rep)

	if rep.Failed() {
		return fmt.Errorf("体检未通过")
	}
	return nil
}

func printReport(u *ui, rep doctor.Report) {
	u.title("wbmux 体检")
	if rep.Host.Found {
		u.kv("宿主程序", rep.Host.Executable)
	}
	u.kv("目标后端", fmt.Sprintf("%s  %s", rep.Target.DisplayName, rep.Target.Endpoint))

	u.section("检查项")
	for _, c := range rep.Checks {
		line := fmt.Sprintf("%-4s %s", c.Level.String(), c.Name)
		switch c.Level {
		case doctor.OK:
			u.ok(line)
		case doctor.Warn:
			u.warn(line)
		case doctor.Fail:
			u.fail(line)
		default:
			u.info(line)
		}
		if c.Detail != "" {
			u.bullet(c.Detail)
		}
		if c.Hint != "" {
			u.hint(c.Hint)
		}
	}

	u.section("结论")
	switch {
	case rep.Failed():
		u.fail("存在阻断性问题，当前配置下无法可靠切换后端。")
	case rep.Warnings() > 0:
		u.warn(fmt.Sprintf("有 %d 项提示，功能可用但值得留意。", rep.Warnings()))
	default:
		u.ok("全部通过。")
	}
}

func cmdList(args []string) error {
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
		fmt.Fprint(u.w, `用法: wbmux list

列出本机探测到的客户端安装，以及可切换的后端。

选项:
  --host <cn|intl>   仅列出该档位
  --exe <路径>       直接指定宿主主程序
  --color <模式>     auto（默认）/ always / never
  -h, --help         打印本帮助
`)
		return nil
	}

	probe := variant.DefaultProbe()
	ids := []variant.ID{variant.CN, variant.Intl}
	if *c.host != "" {
		id, err := variant.Parse(*c.host)
		if err != nil {
			return err
		}
		ids = []variant.ID{id}
	}

	u.title("已探测到的安装")
	anyFound := false
	for _, id := range ids {
		b, err := variant.Get(id)
		if err != nil {
			return err
		}

		exe := ""
		if *c.exe != "" && len(ids) == 1 {
			exe = *c.exe
		}
		inst := probe.Detect(id, exe)

		u.blank()
		if !inst.Found {
			u.kv(b.DisplayName, u.dim("未找到"))
			for _, p := range inst.Problems {
				u.bullet(u.dim(p))
			}
			continue
		}

		anyFound = true
		u.kv(b.DisplayName, inst.Executable)
		u.bullet(u.dim("来源  ") + inst.Source)
		if inst.Version != "" {
			v := inst.Version
			if inst.Build != "" {
				v += "  build " + shortBuild(inst.Build)
			}
			u.bullet(u.dim("版本  ") + v)
		}
		u.bullet(u.dim("数据  ") + inst.DataDir)
		u.bullet(u.dim("配置  ") + inst.ProductJSON)
	}

	u.section("可切换的后端")
	for _, b := range variant.All() {
		u.kv(b.DisplayName, fmt.Sprintf("%-28s %s", b.Endpoint, u.dim("数据目录 "+b.DataFolderName)))
	}

	if !anyFound {
		u.blank()
		u.warn("没有探测到任何安装。")
		u.hint("用 `wbmux config set --exe <主程序绝对路径>` 显式指定")
	}
	return nil
}

// shortBuild 截短构建号，完整值对用户没有意义。
func shortBuild(b string) string {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}

// verifyEndpoint 回读生成配置，确认 endpoint 已指向目标后端。
//
// export 走的是自己的路径（不经过 runner），所以这里单独保留一份。
// 少这一步就会漏掉"补丁字段名写错"这类静默失败。
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

func cmdExport(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	f.Alias("o", "out")
	out := f.String("out", "")
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
		fmt.Fprint(u.w, `用法: wbmux export <后端> [选项]

只生成合并后的产品配置并写出，不启动客户端。
适合在脚本里预先生成，或拿来人工比对。

选项:
  -o, --out <路径>        输出路径（默认写到 wbmux 缓存目录）
  --extra-endpoint <URL>  追加进 officialEndpoints，可重复
  --host <cn|intl>        用哪一套安装作为宿主程序
  --exe <路径>            直接指定宿主主程序
  --color <模式>          auto（默认）/ always / never
  -h, --help              打印本帮助
`)
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

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	probe := variant.DefaultProbe()
	hostID, hostInst, err := resolveHost(cfg, *c.host, *c.exe, probe)
	if err != nil {
		return err
	}

	outPath := *out
	if outPath == "" {
		cacheDir, err := config.CacheDir()
		if err != nil {
			return err
		}
		outPath = filepath.Join(cacheDir, fmt.Sprintf("%s-to-%s.json", hostID, targetID))
	} else {
		abs, err := absPath(outPath)
		if err != nil {
			return err
		}
		outPath = abs
	}

	opts := product.Options{
		ExtraEndpoints: append(append([]string{}, cfg.ExtraEndpoints...), *extraEndpoints...),
	}
	changes, err := product.Generate(hostInst.ProductJSON, outPath, target, opts)
	if err != nil {
		return fmt.Errorf("生成产品配置失败: %w", err)
	}
	if err := verifyEndpoint(outPath, target); err != nil {
		return err
	}

	u.title(fmt.Sprintf("已生成 %s → %s 的配置", hostID, targetID))
	u.kv("源配置", hostInst.ProductJSON)
	u.kv("输出", outPath)
	u.section(fmt.Sprintf("配置改写 %d 项", len(changes)))
	for _, ch := range changes {
		u.bullet(ch.String())
	}
	return nil
}

func cmdConfig(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	host := f.String("host", "")
	exe := f.String("exe", "")
	help := f.Bool("help", false)

	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := newUIWith(c)
	if err != nil {
		return err
	}

	sub := "show"
	if len(f.Args) > 0 {
		sub = f.Args[0]
	}
	if *help {
		sub = "help"
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	switch sub {
	case "show", "list":
		path, err := config.Path()
		if err != nil {
			return err
		}
		u.title("当前设置")
		u.kv("设置文件", path)
		if cfg.HostVariant == "" {
			u.kv("宿主档位", u.dim("自动探测"))
		} else {
			u.kv("宿主档位", cfg.HostVariant)
		}
		if cfg.HostExe == "" {
			u.kv("宿主主程序", u.dim("自动探测"))
		} else {
			u.kv("宿主主程序", cfg.HostExe)
		}
		if len(cfg.ExtraEndpoints) == 0 {
			u.kv("附加端点", u.dim("无"))
		} else {
			u.kv("附加端点", strings.Join(cfg.ExtraEndpoints, ", "))
		}
		return nil

	case "set":
		if *host == "" && *exe == "" {
			return fmt.Errorf("至少要指定 --host 或 --exe 之一")
		}
		if *host != "" {
			id, err := variant.Parse(*host)
			if err != nil {
				return err
			}
			cfg.HostVariant = string(id)
		}
		if *exe != "" {
			abs, err := absPath(*exe)
			if err != nil {
				return err
			}
			if _, err := os.Stat(abs); err != nil {
				return fmt.Errorf("主程序不存在: %s", abs)
			}
			cfg.HostExe = abs
			if cfg.HostVariant == "" {
				if g := variant.GuessFromPath(abs); g != "" {
					cfg.HostVariant = string(g)
					u.info(fmt.Sprintf("据路径推断宿主档位为 %s", g))
				}
			}
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		u.ok("已保存。")
		return nil

	case "add-endpoint":
		if len(f.Args) < 2 {
			return fmt.Errorf("用法: wbmux config add-endpoint <URL>")
		}
		cfg.ExtraEndpoints = append(cfg.ExtraEndpoints, f.Args[1])
		if err := config.Save(cfg); err != nil {
			return err
		}
		u.ok("已追加 " + f.Args[1])
		return nil

	case "reset":
		if err := config.Save(config.Config{}); err != nil {
			return err
		}
		u.ok("设置已恢复默认。")
		return nil

	case "path":
		path, err := config.Path()
		if err != nil {
			return err
		}
		fmt.Fprintln(u.w, path)
		return nil

	case "help":
		fmt.Fprint(u.w, `用法: wbmux config [子命令]

子命令:
  （无）                            查看当前设置
  set --host <cn|intl>              指定宿主档位
  set --exe <主程序绝对路径>        指定宿主主程序
  add-endpoint <URL>                追加进 officialEndpoints
  reset                             恢复默认
  path                              打印设置文件路径

设置存放在 ~/.wbmux/config.json，不写入任何客户端目录。
`)
		return nil
	}

	return fmt.Errorf("未知的 config 子命令 %q，试试 `wbmux config help`", sub)
}
