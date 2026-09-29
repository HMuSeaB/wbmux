package main

import (
	"fmt"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/sessions"
	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/zimport"
)

// cmdZImport 把 ZCode 的会话导入 WorkBuddy，让它在客户端列表里能看见、点开能读。
//
// 这是**写**操作：会往目标档位的数据目录里写一份 jsonl、并插一行索引。
// 只增不改，且要求目标客户端已退出。
func cmdZImport(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	id := f.String("id", "")
	to := f.String("to", "cn")
	project := f.String("project", "")
	title := f.String("title", "")
	reasoning := f.Bool("reasoning", false)
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
		fmt.Fprint(u.w, `用法：
  wbmux zimport --id <ZCode 会话id> [--to cn|intl] [--yes]

把一个 ZCode 会话导入 WorkBuddy，之后在客户端的会话列表里能直接看见它。
请在**目标客户端完全退出**后执行（它握着列表缓存，边跑边写会看不到）。

选项:
  --id <会话id>    要导入的 ZCode 会话（wbmux zcode --list 拿 id）
  --to cn|intl     目标档位（默认 cn = 国内）
  --project <路径> 指定项目路径（默认用 ZCode 会话里的）
  --title <标题>   指定标题（默认用 ZCode 会话标题）
  --reasoning      把思考片段也导入（默认不带）
  --yes            跳过确认，直接导入

这是写操作：会在目标档位写一份对话文件 + 一行索引。只增不改，
已存在的 id 一律不覆盖；ZCode 那边的数据一个字节都不动。
`)
		return nil
	}
	if len(f.Args) > 0 {
		return fmt.Errorf("zimport 不接受位置参数，收到 %q", strings.Join(f.Args, " "))
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("缺少 --id；先用 wbmux zcode --list 拿到会话 id")
	}

	v := sessions.VendorWBCN
	switch strings.ToLower(strings.TrimSpace(*to)) {
	case "cn", "china", "":
		v = sessions.VendorWBCN
	case "intl", "international", "ai":
		v = sessions.VendorWBIntl
	default:
		return fmt.Errorf("--to 只支持 cn 或 intl，收到 %q", *to)
	}

	if !*yes {
		u.title("导入 ZCode 会话到 WorkBuddy")
		u.kv("目标档位", v.Label())
		u.blank()
		u.warn("这是写操作：会在目标档位的数据目录里新建一份对话文件并插一行索引。")
		u.info("确认无误后加 --yes 再执行一次。")
		return nil
	}

	res, err := zimport.Import(variant.DefaultProbe(), *id, zimport.Options{
		Vendor:          v,
		WithReasoning:   *reasoning,
		ProjectOverride: *project,
		TitleOverride:   *title,
	})
	if err != nil {
		return err
	}
	u.title("导入完成")
	u.kv("标题", res.Title)
	u.kv("项目", res.Project)
	u.kv("消息数", fmt.Sprintf("%d", res.Msgs))
	u.kv("会话 id", res.SessionID)
	u.kv("对话文件", res.JSONLPath)
	u.blank()
	u.ok("启动 " + v.Label() + " 客户端即可在列表里看到它。")
	if res.Skipped != "" {
		u.warn(res.Skipped)
	}
	return nil
}
