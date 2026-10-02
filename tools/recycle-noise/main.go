// Command recycle-noise 诊断并清理回收站里的垃圾。
//
// # 为什么有这个命令行入口
//
// 界面上已经有「回收站清理」页（internal/recyclenoise + internal/webui/recycle.go），
// 那条路是首选。这个命令行入口是给"界面没开着"的场景用的：
// CI、脚本、或者只想敲一条命令的时候。
//
// 它**复用同一个 internal/recyclenoise 包**，不另写一份模式表。
// 这一点很要紧——最早有个 Python 版脚本，模式表是抄的，两边很快就漂了：
// 面板能认出的，脚本认不出。
//
// # 分档
//
// 界面和这里都按"能不能放心删"分三档，顺序即危险度：
//
//	noise    工具反复产生的垃圾（被中断的构建、测试临时树、探测文件）
//	scratch  开发草稿（项目 tmp/dist 下的产物、临时目录里的散落文件）
//	keep     用户自己删的文件、wbmux 下载的升级包
//
// 不加 -through 时只清第一档，跟自动清理的行为一致。
//
// # 为什么不用脚本
//
// 早先那版 Python 脚本清理上千个条目要 10 分钟以上（每个条目一轮
// chmod + rmtree + 重试），而这里同样的量级只花 3 秒。差两百倍。
// 删掉重写的成本远低于继续维护两条慢路径。
//
// # 用法
//
//	go run ./tools/recycle-noise                        # 报告，看是哪几档
//	go run ./tools/recycle-noise clean                  # 只清工具噪声
//	go run ./tools/recycle-noise clean -through scratch # 连开发草稿一起清
//	go run ./tools/recycle-noise clean -all             # 清空（含用户文件）
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/HMuSeaB/wbmux/internal/recyclenoise"
)

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// tierLabels 是各档的中文名，界面与命令行用同一套说法。
var tierLabels = map[recyclenoise.Category]string{
	recyclenoise.CategoryNoise:   "工具噪声",
	recyclenoise.CategoryScratch: "开发草稿",
	recyclenoise.CategoryKeep:    "保留（你自己的文件）",
}

func main() {
	all := flag.Bool("all", false, "清空回收站，含用户自己删的文件")
	through := flag.String("through", "",
		"清到哪一档：noise（默认）/ scratch / keep")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: %s [report|clean] [-through noise|scratch|keep] [-all]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "  report  列出回收站分档构成（默认）")
		fmt.Fprintln(os.Stderr, "  clean   清到指定的那一档；默认只清工具噪声")
		flag.PrintDefaults()
	}
	flag.Parse()

	action := "report"
	if flag.NArg() > 0 {
		action = flag.Arg(0)
	}

	entries, err := recyclenoise.Scan()
	if err != nil {
		fmt.Fprintf(os.Stderr, "读回收站失败: %v\n", err)
		os.Exit(1)
	}

	switch action {
	case "report":
		report(entries)
	case "clean":
		runClean(entries, *through, *all)
	default:
		fmt.Fprintf(os.Stderr, "不认识的命令: %s\n\n", action)
		flag.Usage()
		os.Exit(2)
	}
}

func report(entries []recyclenoise.Entry) {
	s := recyclenoise.Summarize(entries)

	if s.Total == 0 {
		fmt.Println("回收站是空的。")
		return
	}

	fmt.Printf("回收站共 %d 项\n\n", s.Total)
	for _, t := range s.Tiers {
		fmt.Printf("=== %s：%d 项，%s ===\n", tierLabels[t.Category], t.Count, human(t.Bytes))
		for _, k := range t.ByKind {
			fmt.Printf("    %-24s %5d 项\n", k.Kind, k.Count)
		}
		// 只有"保留"档值得列样本：那一档是用户自己的东西，
		// 得让人看清楚哪些被留下了。其它档删了就是垃圾，不必刷屏。
		if t.Category == recyclenoise.CategoryKeep {
			for _, e := range t.Sample {
				fmt.Printf("      %9s  %s\n", human(e.Size), e.Original)
			}
		}
		fmt.Println()
	}

	total := s.NoiseBytes + s.ScratchBytes + s.OtherBytes
	fmt.Printf("总体积 %s —— 体积不是问题，条目数才是。\n", human(total))
	fmt.Println()
	fmt.Println("clean 默认只清「工具噪声」；加 -through scratch 可连开发草稿一起清。")
	fmt.Println("界面上是同一个功能：面板 →「回收站清理」。")
}

func runClean(entries []recyclenoise.Entry, through string, all bool) {
	opts := recyclenoise.CleanOptions{All: all}
	switch through {
	case "", "noise":
		opts.Through = recyclenoise.CategoryNoise
	case "scratch":
		opts.Through = recyclenoise.CategoryScratch
	case "keep":
		opts.Through = recyclenoise.CategoryKeep
	default:
		fmt.Fprintf(os.Stderr, "不认识的档: %s（可选 noise / scratch / keep）\n", through)
		os.Exit(2)
	}

	// 动手前先报一遍要删多少。别让人稀里糊涂把几百 MB 抹掉。
	s := recyclenoise.Summarize(entries)
	var n int
	limit := opts.Through
	if all {
		limit = recyclenoise.CategoryKeep
	}
	for _, t := range s.Tiers {
		if t.Category.Severity() <= limit.Severity() {
			n += t.Count
		}
	}
	fmt.Printf("将清理「%s」及以下各档，共 %d 项。\n", tierLabels[limit], n)

	res := recyclenoise.Clean(entries, opts)
	if res.Removed == 0 && res.Failed == 0 {
		fmt.Println("没有需要清理的条目。")
		fmt.Println("（想连你自己的文件一起清，加 -all）")
		return
	}

	fmt.Printf("已清 %d 项，释放约 %s\n", res.Removed, human(res.Freed))
	if res.Failed > 0 {
		fmt.Printf("有 %d 项失败——多半是被别的程序占用，关掉资源管理器再试。\n", res.Failed)
		for _, f := range res.Failures {
			fmt.Printf("  %s\n", f)
		}
	}

	// 清完再报一次，让用户看到剩下什么。
	after, err := recyclenoise.Scan()
	if err != nil {
		return
	}
	s2 := recyclenoise.Summarize(after)
	fmt.Printf("\n剩余 %d 项\n", s2.Total)
	for _, t := range s2.Tiers {
		fmt.Printf("  %-24s %4d 项  %s\n", tierLabels[t.Category], t.Count, human(t.Bytes))
		if t.Category == recyclenoise.CategoryKeep {
			for _, e := range t.Sample {
				fmt.Printf("      留下：%s\n", e.Original)
			}
		}
	}
}
