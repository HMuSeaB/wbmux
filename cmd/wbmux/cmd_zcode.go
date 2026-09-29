package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/zcode"
)

// cmdZCode 读 ZCode 的会话正文：列出、预览、导出成 Markdown。
//
// 只读 ZCode 的任何数据；导出写到用户指定的目录（默认当前目录）。
func cmdZCode(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	id := f.String("id", "")
	listOnly := f.Bool("list", false)
	limit := f.String("limit", "20")
	out := f.String("out", ".")
	reasoning := f.Bool("reasoning", false)
	noTools := f.Bool("no-tools", false)
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
  wbmux zcode --list              列出本机 ZCode 的会话
  wbmux zcode --id <会话id>       预览一个会话的对话（前若干条）
  wbmux zcode --id <会话id> --out <目录>
                                  导出成 Markdown（默认写到当前目录）

选项:
  --id <会话id>    要读的会话；用 --list 拿到 id
  --limit <n>      --list 显示多少条（默认 20）
  --out <目录>     导出目录（默认当前目录）
  --reasoning      导出时带上思考片段（默认不带）
  --no-tools       导出时不列工具名（默认列）

ZCode 的数据一个字节都不动，全程只读。
`)
		return nil
	}
	if len(f.Args) > 0 {
		return fmt.Errorf("zcode 不接受位置参数，收到 %q", strings.Join(f.Args, " "))
	}

	probe := variant.DefaultProbe()
	if !zcode.Available(probe) {
		return fmt.Errorf("本机没有 ZCode 的会话库（%s）", mustDBPath())
	}

	if *id == "" {
		// 没给 id 就当 --list 用：顺手列出来，省得用户先猜 id。
		return zcodeList(u, probe, mustParseInt(*limit, 20))
	}

	if !*listOnly && strings.TrimSpace(*id) != "" {
		tr, err := zcode.Load(probe, *id)
		if err != nil {
			return err
		}
		path, err := zcode.ExportFile(tr, *out, zcode.MarkdownOptions{
			WithReasoning: *reasoning,
			WithTools:     !*noTools,
		})
		if err != nil {
			return err
		}
		u.title("ZCode 会话导出")
		u.kv("标题", tr.Title)
		u.kv("项目", tr.Directory)
		u.kv("消息数", fmt.Sprintf("%d", len(tr.Msgs)))
		u.blank()
		u.ok("已导出：" + path)
		for _, w := range tr.Warns {
			u.warn(w)
		}
		return nil
	}
	return nil
}

func zcodeList(u *ui, probe *variant.Probe, limit int) error {
	rows, err := zcode.List(probe, limit)
	if err != nil {
		return err
	}
	u.title("ZCode 会话")
	if len(rows) == 0 {
		u.info("没有会话。")
		return nil
	}
	for _, r := range rows {
		title := r.Title
		if len([]rune(title)) > 40 {
			title = string([]rune(title)[:40]) + "…"
		}
		fmt.Fprintf(u.w, "  %s  %s\n", r.ID, title)
		fmt.Fprintf(u.w, "      %s · %s\n", r.Directory, r.When)
	}
	u.blank()
	u.info("读一个：wbmux zcode --id <会话id> --out <目录>")
	return nil
}

// mustParseInt 解析失败就用兜底值（命令行选项，不值得为它中止）。
func mustParseInt(s string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func mustDBPath() string {
	p, err := zcode.DBPath(variant.DefaultProbe())
	if err != nil {
		return "?"
	}
	return filepath.Clean(p)
}
