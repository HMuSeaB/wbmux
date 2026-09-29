package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/checkin"
	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/credential"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// cmdCheckin 查签到状态、领今天的积分。
//
// # 为什么"领取"要单独敲一个词
//
// 查询是只读的，随便跑几遍都没事；领取是**写操作**，会真的动账号状态。
// 如果让两者共用一个不带参数的默认命令，就等于把写操作藏在"我只看一眼"里。
// 所以默认只查，要领必须显式敲 `claim`——和界面上那个按钮是同一个道理。
//
// 另外：默认只看国内侧。签到只在国内侧开放（国际侧接口在但 active=false），
// 默认走"自动探测到哪套就用哪套"会在只装了国际版的机器上给出一句
// "没有活动"，让人以为是功能坏了。要查国际侧得显式 --host intl。
func cmdCheckin(args []string) error {
	f := newFlags()
	c := addCommon(f)
	f.Alias("h", "help")
	jsonOut := f.Bool("json", false)
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
  wbmux checkin              查看签到状态（只读）
  wbmux checkin claim        领取今天的积分（会真的动账号状态）

选项:
  --host <cn|intl>   查哪一侧（默认 cn）。签到只在国内侧开放。
  --json             原样输出 JSON，便于脚本消费

说明:
  · 领取是幂等的：今天已经签过时如实回报"已签过"，不会多领，也不算失败。
  · 国内版凭据是加密信封，本命令会启动一次客户端主程序借它的运行时解开。
  · 国际侧没有这个活动（接口在，active=false）。
`)
		return nil
	}

	claim := false
	switch len(f.Args) {
	case 0:
	case 1:
		if strings.ToLower(strings.TrimSpace(f.Args[0])) != "claim" {
			return fmt.Errorf("checkin 只接受 claim 这一个位置参数，收到 %q", f.Args[0])
		}
		claim = true
	default:
		return fmt.Errorf("checkin 最多接受一个位置参数，收到 %q", strings.Join(f.Args, " "))
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	probe := variant.DefaultProbe()

	// 目标档位：默认国内侧，显式指定才换。
	id := variant.CN
	if h := strings.TrimSpace(*c.host); h != "" {
		parsed, _, err := resolveHost(cfg, h, *c.exe, probe)
		if err != nil {
			return err
		}
		id = parsed
	}

	deps := checkin.Deps{Probe: probe}
	// 用户钉过客户端位置时，解信封也用同一处——否则会出现"查的是这份账号、
	// 解的却是另一处安装的凭据"这种谁也看不出来的错配。
	if exe := pinnedExe(cfg, *c.exe); exe != "" {
		deps.Cred = func(p *variant.Probe, target variant.ID) (credential.Credential, error) {
			return credential.ResolveWith(p, target, exe)
		}
	}

	if !claim {
		st, err := checkin.Query(deps, id)
		if err != nil {
			return err
		}
		if *jsonOut {
			return writeJSONTo(u, st)
		}
		printCheckinStatus(u, id, st)
		return nil
	}

	res, err := checkin.Claim(deps, id)
	if err != nil {
		return err
	}
	if *jsonOut {
		return writeJSONTo(u, res)
	}

	u.title("签到")
	switch {
	case res.AlreadyCheckedIn:
		msg := res.Message
		if msg == "" {
			msg = "今天已经签过了。"
		}
		u.info(msg + "（已签过不会重复领，这是正常结果）")
	case res.Credit > 0:
		u.ok(fmt.Sprintf("已领取 %d 积分。", res.Credit))
	default:
		// 既不是已签、积分又是 0：接口回了个我们没见过的形状，
		// 如实说，不要编一个"成功"出来。
		u.warn("后端没有回报积分数（回话形状可能变了），请到客户端确认。")
	}
	if res.Status != nil {
		u.blank()
		printCheckinStatus(u, id, *res.Status)
	}
	return nil
}

// pinnedExe 返回用户钉住的客户端主程序（命令行优先于设置文件）。
func pinnedExe(cfg config.Config, fromFlag string) string {
	if strings.TrimSpace(fromFlag) != "" {
		return fromFlag
	}
	return cfg.HostExe
}

// printCheckinStatus 打印一份状态快照。
func printCheckinStatus(u *ui, id variant.ID, st checkin.Status) {
	side := string(id)
	if b, err := variant.Get(id); err == nil {
		side = b.DisplayName
	}
	u.title("签到状态 · " + side)

	if !st.Active {
		u.warn("这一侧没有开签到活动。")
		// 国际侧就是这个情况，说清"不是坏了"，免得用户去折腾凭据。
		u.hint("签到目前只在国内版开放；国际版接口在，但活动没开。")
		return
	}

	if st.TodayCheckedIn {
		u.kv("今日", "已签到（"+fmt.Sprintf("%d", st.TodayCredit)+" 积分）")
	} else {
		u.kv("今日", "还没签，跑 `wbmux checkin claim` 领取")
	}
	u.kv("连签", fmt.Sprintf("%d 天", st.StreakDays))
	u.kv("每日积分", fmt.Sprintf("%d", st.DailyCredit))
	if st.WeekCheckinDays > 0 {
		u.kv("本周已签", fmt.Sprintf("%d 天", st.WeekCheckinDays))
	}
	// 连签奖励只在真的进入奖励周期时才提，否则每行都挂一串 0 反而看不清重点。
	if st.StreakBonusDays > 0 || st.StreakBonusCredit > 0 {
		u.kv("连签奖励", fmt.Sprintf("还差 %d 天，送 %d 天 %d 积分",
			st.NextStreakDay, st.StreakBonusDays, st.StreakBonusCredit))
	}
	if len(st.CheckinDates) > 0 {
		u.kv("最近一次", st.CheckinDates[0])
	}
}

// writeJSONTo 把任意结构原样输出成 JSON。
//
// 单独抽出来是因为脚本消费时希望拿到**后端语义**的字段，而不是给人看的排版。
func writeJSONTo(u *ui, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(u.w, string(raw))
	return nil
}
