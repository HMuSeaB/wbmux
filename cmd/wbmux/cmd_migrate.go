package main

import (
	"fmt"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

func cmdMigrate(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	f.Alias("n", "dry-run")

	fromFlag := f.String("from", "")
	toFlag := f.String("to", "")
	kinds := f.List("kind")
	dryRun := f.Bool("dry-run", false)
	yes := f.Bool("yes", false)
	help := f.Bool("help", false)

	if err := f.Parse(args); err != nil {
		return err
	}
	u, err := newUIWith(c)
	if err != nil {
		return err
	}

	if *help {
		fmt.Fprint(u.w, `用法: wbmux migrate [来源] [目标] [选项]

把一侧数据目录里的会话、技能与记忆搬到另一侧。

默认是只读的：不加 --yes 只列出将要搬运的东西，一个字节都不会动。

选项:
  --from <cn|intl>     来源档位（默认 intl）
  --to <cn|intl>       目标档位（默认 cn）
  --kind <种类>        只搬某一类，可重复：sessions / skills / memory
  --dry-run            预演，只列清单
  --yes                真正执行（会先备份目标端数据库）
  --color <模式>       auto（默认）/ always / never
  -h, --help           打印本帮助

为什么不能只复制文件:
  会话列表读的是数据目录下 workbuddy.db 的 sessions 表。客户端那个
  "从 projects/ 重建索引"的函数只在数据库损坏自愈时才跑，正常启动不执行。
  所以只拷 jsonl 过去，列表里什么都不会出现——必须同时写索引行。

示例:
  wbmux migrate                      看看国际版有什么可以搬到国内版
  wbmux migrate intl cn --yes        真正执行
  wbmux migrate --kind skills --yes  只搬技能
`)
		return nil
	}

	// 位置参数与 --from/--to 等价，方便 `wbmux migrate intl cn` 这种写法。
	from, to := *fromFlag, *toFlag
	if from == "" && len(f.Args) > 0 {
		from = f.Args[0]
	}
	if to == "" && len(f.Args) > 1 {
		to = f.Args[1]
	}
	// 默认方向取"国际版 → 国内版"：这是用户手上两份安装最常见的用法，
	// 但仍然会把方向完整打印出来，不让人误以为搬错了边。
	if from == "" {
		from = string(variant.Intl)
	}
	if to == "" {
		to = string(variant.CN)
	}

	srcID, err := variant.Parse(from)
	if err != nil {
		return err
	}
	dstID, err := variant.Parse(to)
	if err != nil {
		return err
	}
	opts := migrate.Options{Source: srcID, Target: dstID}

	survey, err := migrate.Survey(opts)
	if err != nil {
		return err
	}
	printSurvey(u, survey)

	if *dryRun {
		u.blank()
		u.info("这是预演，没有任何改动。去掉 --dry-run 后再加 --yes 才会执行。")
		return nil
	}
	if !*yes {
		u.blank()
		u.warn("还没有执行。确认清单无误后加 --yes。")
		return nil
	}

	sel := migrate.Selection{}
	for _, k := range *kinds {
		sel.Kinds = append(sel.Kinds, migrate.Kind(k))
	}

	rep, err := migrate.Apply(opts, sel)
	if err != nil {
		return err
	}
	printMigrateReport(u, rep)
	return nil
}

func printSurvey(u *ui, s migrate.SurveyResult) {
	u.title(fmt.Sprintf("wbmux migrate —— 把%s的东西搬到%s", s.SourceName, s.TargetName))
	u.blank()
	u.kv("来源", fmt.Sprintf("%s  %s", s.SourceName, s.SourceDataDir))
	u.kv("目标", fmt.Sprintf("%s  %s", s.TargetName, s.TargetDataDir))

	if s.TargetRunning {
		u.blank()
		u.warn("目标端客户端正在运行：搬运可以照做，但会话列表要重启后才会刷新。")
	}
	for _, w := range s.Warnings {
		u.warn(w)
	}

	printGroup(u, "会话", s.Sessions)
	printGroup(u, "技能", s.Skills)
	printGroup(u, "记忆", s.Memory)
}

func printGroup(u *ui, label string, list []migrate.Candidate) {
	if len(list) == 0 {
		return
	}

	fresh := 0
	for _, c := range list {
		if c.State == "new" {
			fresh++
		}
	}

	u.section(fmt.Sprintf("%s（%d 项，其中 %d 项待搬）", label, len(list), fresh))
	for _, c := range list {
		line := c.Title
		if c.Size > 0 {
			line = fmt.Sprintf("%-44s %9s", line, humanSize(c.Size))
		}
		if c.State == "exists" {
			u.info(line + "   跳过：" + c.Note)
			continue
		}
		u.ok(line)
		if c.Subtitle != "" && c.Subtitle != c.Title {
			u.hint(c.Subtitle)
		}
	}
}

func printMigrateReport(u *ui, r migrate.Report) {
	u.section("完成")
	if r.SkippedItems > 0 {
		u.kv("整项跳过", fmt.Sprintf("%d 项（目标端已有）", r.SkippedItems))
	}
	u.kv("复制文件", fmt.Sprintf("%d 个（跳过已存在 %d 个）", r.CopiedFiles, r.SkippedFiles))
	if r.RowsInserted > 0 || r.RowsSkipped > 0 {
		u.kv("写入索引", fmt.Sprintf("%d 行（已存在 %d 行）", r.RowsInserted, r.RowsSkipped))
	}
	if r.BackupDir != "" {
		u.kv("数据库备份", r.BackupDir)
	}
	for _, w := range r.Warnings {
		u.warn(w)
	}
	u.blank()
	u.ok("搬运完成。重新启动目标端客户端即可在列表里看到。")
}

// humanSize 把字节数格式化成人看的单位。
//
// 与界面里的做法保持一致：都用 1024 进制，免得同一个文件在命令行和
// 图形界面显示成两个数。
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
