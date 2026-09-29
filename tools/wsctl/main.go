// wsctl —— WorkBuddy 会话工作区维护（命令行）。
//
// 真正的逻辑在 internal/workspace，这里只做参数解析与打印，
// 与界面走同一套实现（两边各写一份迟早分叉）。
//
// 三类操作，约束完全不同：
//
//	wsctl list   只读：列出工作区与会话分布。随时可跑。
//	wsctl check  只读：体检（重复入口、写法不一致、目录不存在）。
//	wsctl set    写：改某个工作区的路径（含统一写法）。
//	wsctl move   写：把工作区整体挪到别的盘，并改写所有引用。
//
// 写操作全都要 --yes 才动，且事先备份数据库、客户端在跑就拒绝。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/workspace"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "list", "ls":
		err = cmdList(os.Args[2:])
	case "check":
		err = cmdCheck(os.Args[2:])
	case "set":
		err = cmdSet(os.Args[2:])
	case "move", "mv":
		err = cmdMove(os.Args[2:])
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`wsctl —— WorkBuddy 工作区维护

用法:
  wsctl list  [--host cn|intl]              列出工作区与会话分布（只读）
  wsctl check [--host cn|intl]              体检：重复入口、写法不一致、目录不存在（只读）
  wsctl set   <旧路径> <新路径> --yes        改一个工作区的路径，并统一写法
  wsctl move  <旧路径> <新路径> --yes        把工作区挪到别处，改写所有引用

写操作会先备份数据库；客户端在运行时会被拒绝。
`)
}

func parseArgs(args []string) (*flag.FlagSet, variant.ID, *bool, error) {
	fs := flag.NewFlagSet("wsctl", flag.ContinueOnError)
	host := fs.String("host", "cn", "cn|intl")
	yes := fs.Bool("yes", false, "确认执行写操作")
	if err := fs.Parse(args); err != nil {
		return nil, "", nil, err
	}
	return fs, variant.ID(*host), yes, nil
}

func cmdList(args []string) error {
	_, id, _, err := parseArgs(args)
	if err != nil {
		return err
	}
	p := variant.DefaultProbe()
	ov, err := workspace.Inspect(p, id)
	if err != nil {
		return err
	}

	fmt.Printf("%s 的工作区（%d 个，会话合计 %d）\n\n", ol(id), len(ov.Spaces), ov.TotalSess)
	for _, sp := range ov.Spaces {
		tail := ""
		if !sp.Exists {
			tail += "  **目录不存在**"
		}
		if !sp.InList {
			tail += "  （不在工作区清单里）"
		}
		if len(sp.Variants) > 1 {
			tail += fmt.Sprintf("  （%d 种写法）", len(sp.Variants))
		}
		fmt.Printf("  %-52s 会话 %2d  %s%s\n", sp.Path, sp.Sessions, sp.LastOpen, tail)
	}
	return nil
}

func cmdCheck(args []string) error {
	_, id, _, err := parseArgs(args)
	if err != nil {
		return err
	}
	p := variant.DefaultProbe()
	ov, err := workspace.Inspect(p, id)
	if err != nil {
		return err
	}

	fmt.Printf("%s 的工作区体检\n\n", ol(id))
	if len(ov.Problems) == 0 {
		fmt.Println("  没发现问题。")
		return nil
	}
	for _, pr := range ov.Problems {
		fmt.Printf("  [%s] %s\n", kindText(pr.Kind), pr.Path)
		if pr.Note != "" {
			fmt.Printf("        %s\n", pr.Note)
		}
		for _, v := range pr.Variants {
			fmt.Printf("        %q\n", v)
		}
	}
	fmt.Printf("\n合计 %d 处。用 set / move 处理。\n", len(ov.Problems))
	return nil
}

func cmdSet(args []string) error {
	fs, id, yes, err := parseArgs(args)
	if err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("用法: wsctl set <旧路径> <新路径> --yes")
	}
	return runPlan("set", id, fs.Arg(0), fs.Arg(1), *yes)
}

func cmdMove(args []string) error {
	fs, id, yes, err := parseArgs(args)
	if err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("用法: wsctl move <旧路径> <新路径> --yes")
	}
	return runPlan("move", id, fs.Arg(0), fs.Arg(1), *yes)
}

func runPlan(kind string, id variant.ID, from, to string, yes bool) error {
	p := variant.DefaultProbe()
	build := workspace.BuildSetPlan
	if kind == "move" {
		build = workspace.BuildMovePlan
	}
	pl, err := build(p, id, from, to)
	if err != nil {
		return err
	}
	fmt.Printf("计划（%s）\n%s", kind, pl.Summarize())
	for _, b := range pl.Blockers {
		fmt.Printf("\n  拦路：%s\n", b)
	}
	if !yes {
		fmt.Println("\n（这是写操作，加 --yes 才执行）")
		return nil
	}
	if !pl.Ready() {
		return fmt.Errorf("有拦路的原因，未执行")
	}
	if kind == "move" {
		err = pl.CommitMove(p, id)
	} else {
		err = pl.Commit(p, id)
	}
	if err != nil {
		return err
	}
	fmt.Println("\n完成。")
	if kind == "move" {
		fmt.Printf("源目录 %s 我没动——确认没问题后你自己删。\n", pl.From)
	}
	return nil
}

func ol(id variant.ID) string {
	if b, err := variant.Get(id); err == nil {
		return b.DisplayName
	}
	return string(id)
}

func kindText(k string) string {
	switch k {
	case "missing":
		return "目录不存在"
	case "noncanonical":
		return "写法非规范"
	case "duplicate":
		return "重复入口"
	}
	return k
}
