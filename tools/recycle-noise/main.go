// Command recycle-noise 诊断并清理回收站里的工具噪声。
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
// # 为什么不用脚本
//
// 早先那版 Python 脚本清理上千个条目要 10 分钟以上（每个条目一轮
// chmod + rmtree + 重试），而这里同样的量级只花 3 秒。差两百倍。
// 删掉重写的成本远低于继续维护两条慢路径。
//
// # 用法
//
//	go run ./tools/recycle-noise            # 报告，看是谁在塞
//	go run ./tools/recycle-noise clean      # 清掉已识别的噪声
//	go run ./tools/recycle-noise clean -all # 连用户自己的文件一起清
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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

func main() {
	all := flag.Bool("all", false, "clean 时连\"非噪声\"条目一起清掉（等于清空回收站）")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "用法: %s [report|clean] [-all]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "  report  列出回收站构成，指出是谁在塞（默认）")
		fmt.Fprintln(os.Stderr, "  clean   清掉已识别的工具噪声；你自己的文件不动")
		fmt.Fprintln(os.Stderr, "  -all    clean 时连你自己的文件也清掉")
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
		clean(entries, *all)
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

	if s.Noise > 0 {
		fmt.Println("=== 工具噪声（可以安全清掉）===")
		for _, k := range s.ByKind {
			fmt.Printf("  %-24s %5d 项\n", k.Kind, k.Count)
		}
		fmt.Println("  " + strings.Repeat("-", 34))
		fmt.Printf("  %-24s %5d 项\n\n", "小计", s.Noise)
	} else {
		fmt.Println("没有识别到工具噪声。")
		fmt.Println()
	}

	if s.OtherTotal > 0 {
		fmt.Printf("=== 其他 %d 项（你自己删的，我没动）===\n", s.OtherTotal)
		for _, e := range s.Other {
			fmt.Printf("  %10s  %s\n", human(e.Size), e.Original)
		}
		if s.OtherTotal > len(s.Other) {
			fmt.Printf("  …… 另有 %d 项（都很小）\n", s.OtherTotal-len(s.Other))
		}
		fmt.Println()
	}

	// 强调"条目数"而不是"体积"：这些条目每个只有几字节，
	// 但几千项就够让资源管理器卡死。别让用户按体积判断严重性。
	fmt.Printf("总体积 %s —— 体积不是问题，条目数才是。\n", human(s.NoiseBytes+s.OtherBytes))
	fmt.Println()
	fmt.Println("跑 `clean` 可以把噪声清掉；界面里的「回收站清理」页是同一个功能。")
}

func clean(entries []recyclenoise.Entry, all bool) {
	res := recyclenoise.Clean(entries, recyclenoise.CleanOptions{All: all})

	if res.Removed == 0 && res.Failed == 0 {
		fmt.Println("没有需要清理的条目。")
		fmt.Println("（要连你自己删的文件一起清掉，加 -all）")
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
	if after, err := recyclenoise.Scan(); err == nil {
		s := recyclenoise.Summarize(after)
		fmt.Printf("\n剩余 %d 项（噪声 %d / 其他 %d）\n", s.Total, s.Noise, s.OtherTotal)
		if s.OtherTotal > 0 {
			fmt.Println("剩下的都是你自己删的：")
			for _, e := range s.Other {
				fmt.Printf("  %s\n", e.Original)
			}
		}
	}
}
