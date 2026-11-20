package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/webui"
)

// intlSimDayFile 记录"哪一天已经自动跑过模拟活跃"。
//
// 官方是按自然日跑批：当天有一次成功的免积分对话，次日发 +30 积分。
// 所以一天一次就够，写进文件后重启、多开都不会重复触发。
const intlSimDayFile = "intl-sim-day"

// runIntlSimDaily 每天自动跑一次国际端"模拟活跃"。
//
// 与面板上那个"模拟活跃 (+30c)"按钮完全同一条路径（webui.AutoSimulateIntlActive），
// 区别只是不需要人点。早上 8 点前不跑：给官方日切留时间，也避免和手动操作撞车。
// 没配国际账号、没有限免模型都安静跳过——这不是用户必须知道的事。
func runIntlSimDaily(ctx context.Context, srv *webui.Server) {
	ticker := time.NewTicker(20 * time.Minute)
	defer ticker.Stop()

	try := func() {
		// 退出开关：在 ~/.wbmux/intl-sim-off 放个空文件就不再自动跑。
		// 不加 UI 开关是有意的——这条逻辑够小，一个文件最不容易出错。
		if off := intlSimOffPath(); off != "" {
			if _, err := os.Stat(off); err == nil {
				return
			}
		}
		now := time.Now()
		if now.Hour() < 8 {
			return
		}
		day := now.Format("2006-01-02")
		if lastIntlSimDay() == day {
			return
		}
		if _, err := srv.AutoSimulateIntlActive(""); err != nil {
			if errors.Is(err, webui.ErrNoIntlCred) {
				return
			}
			guiLog("国际端模拟活跃（自动）失败：%v", err)
			return
		}
		markIntlSimDay(day)
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

func intlSimPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".wbmux", intlSimDayFile)
}

// intlSimOffPath 是"关闭自动模拟"的哨兵文件路径。
func intlSimOffPath() string {
	p := intlSimPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "intl-sim-off")
}

func lastIntlSimDay() string {
	p := intlSimPath()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func markIntlSimDay(day string) {
	p := intlSimPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	_ = os.WriteFile(p, []byte(day), 0o644)
}
