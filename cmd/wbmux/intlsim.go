package main

import (
	"context"
	"errors"
	"time"

	"github.com/HMuSeaB/wbmux/internal/usage"
	"github.com/HMuSeaB/wbmux/internal/webui"
)

// runIntlSimDaily 每天自动跑一次国际端"模拟活跃"。
//
// 与面板上那个"模拟活跃 (+30c)"按钮完全同一条路径（webui.AutoSimulateIntlActive），
// 区别只是不需要人点。早上 8 点前不跑：给官方日切留时间，也避免和手动操作撞车。
// 没配国际账号、没有限免模型都安静跳过——这不是用户必须知道的事。
//
// 成功后会写 usage.MarkIntlSimDay，面板读同一个文件显示"今日已模拟"，
// 这样定时跑的也能看见（2026-11-21 用户反馈"看不出今天国际版活跃了没"）。
func runIntlSimDaily(ctx context.Context, srv *webui.Server) {
	ticker := time.NewTicker(20 * time.Minute)
	defer ticker.Stop()

	try := func() {
		if usage.IntlSimDisabled() {
			return
		}
		now := time.Now()
		if now.Hour() < 8 {
			return
		}
		day := now.Format("2006-01-02")
		if usage.IntlSimLastDay() == day {
			return
		}
		if _, err := srv.AutoSimulateIntlActive(""); err != nil {
			if errors.Is(err, webui.ErrNoIntlCred) {
				return
			}
			guiLog("国际端模拟活跃（自动）失败：%v", err)
			return
		}
		usage.MarkIntlSimDay(day)
		guiLog("已自动完成国际端模拟活跃，官方次日发放 +30 积分")
	}

	try()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			try()
		}
	}
}
