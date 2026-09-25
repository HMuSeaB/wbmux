// Command wbmux 用一份安装连接 WorkBuddy 的两套后端。
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/console"
	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/version"
)

func main() {
	console.EnableUTF8()

	args := os.Args[1:]
	// 无参数时区分两种来源：双击启动（Windows 会新开一个只有自己的控制台）
	// 说明用户不是想敲命令，直接开图形界面；从终端里裸跑则打印帮助。
	if len(args) == 0 && console.IsExclusiveConsole() {
		args = []string{"gui"}
	}

	if err := dispatch(args); err != nil {
		u := newUI(os.Stderr)
		u.fail(err.Error())
		os.Exit(1)
	}
}

func dispatch(args []string) error {
	if len(args) == 0 {
		printHelp(newUI(os.Stdout))
		return nil
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "gui", "ui":
		return cmdGUI(rest)
	case "run", "r":
		return cmdRun(rest)
	case "doctor", "check":
		return cmdDoctor(rest)
	case "list", "ls":
		return cmdList(rest)
	case "export":
		return cmdExport(rest)
	case "migrate", "sync":
		return cmdMigrate(rest)
	case "config", "cfg":
		return cmdConfig(rest)
	case "help", "-h", "--help":
		printHelp(newUI(os.Stdout))
		return nil
	case "version", "-V", "--version":
		fmt.Println(version.String())
		return nil
	}

	return fmt.Errorf("未知命令 %q，试试 `wbmux help`", cmd)
}

// commonOpts 是各子命令共用的宿主选择选项。
type commonOpts struct {
	host  *string
	exe   *string
	color *string
}

func addCommon(f *flags) commonOpts {
	return commonOpts{
		host:  f.String("host", ""),
		exe:   f.String("exe", ""),
		color: f.String("color", "auto"),
	}
}

// newUIWith 建立输出器并应用 --color。
func newUIWith(c commonOpts) (*ui, error) {
	u := newUI(os.Stdout)
	if err := u.setColor(*c.color); err != nil {
		return nil, err
	}
	return u, nil
}

// resolveHost 在设置文件之上叠加命令行选项，然后交给底层解析。
//
// 优先级：命令行 > 设置文件 > 自动探测。
func resolveHost(cfg config.Config, hostFlag, exeFlag string, probe *variant.Probe) (variant.ID, variant.Install, error) {
	if exeFlag == "" {
		exeFlag = cfg.HostExe
	}
	if hostFlag == "" {
		hostFlag = cfg.HostVariant
	}
	return variant.Resolve(probe, hostFlag, exeFlag)
}

// installOf 探测指定档位的安装，找不到时返回可直接展示的错误。
func installOf(id variant.ID, probe *variant.Probe) (variant.Install, error) {
	inst := probe.Detect(id, "")
	if !inst.Found {
		return inst, variant.NotFoundError(id, inst)
	}
	return inst, nil
}

func printHelp(u *ui) {
	cn, _ := variant.Get(variant.CN)
	intl, _ := variant.Get(variant.Intl)

	u.title("wbmux —— 用一份安装，连两套后端。")
	fmt.Fprint(u.w, `
用法:
  wbmux <命令> [选项]

命令:
  gui              打开图形界面（双击 wbmux.exe 同此）
  run <后端>       用指定后端启动客户端
  doctor           体检：安装位置、配置、覆盖机制是否仍有效
  list             列出本机探测到的安装与可用后端
  export <后端>    只生成合并配置，不启动
  migrate          把一侧的会话/技能/记忆搬到另一侧
  config           查看或修改设置
  version          打印版本
  help             打印本帮助

后端:
`)
	u.kv(cn.DisplayName, cn.Endpoint)
	u.kv(intl.DisplayName, intl.Endpoint)
	fmt.Fprint(u.w, `
通用选项:
  --host <cn|intl>      用哪一套安装作为宿主程序
  --exe <路径>          直接指定宿主主程序
  --color <模式>        auto（默认）/ always / never
  -h, --help            打印本帮助
  -V, --version         打印版本

示例:
  wbmux gui
  wbmux list
  wbmux doctor
  wbmux run intl
  wbmux run cn --dry-run
  wbmux run intl --native     用国际版自己的安装原生启动，作对照
  wbmux migrate               看看另一侧有什么可以搬过来
  wbmux migrate intl cn --yes 把国际版的会话搬进国内版
`)
}

// 供 main.go 内其它文件复用的哨兵错误。
var errNoTarget = errors.New("缺少目标后端")
