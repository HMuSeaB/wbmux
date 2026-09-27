package main

import (
	"testing"
	"time"
)

// TestParseIdle 钉住默认值这件事本身：**默认为 0（不自动退出）**。
//
// 2026-09-27 之前默认 30 分钟，靠页面心跳判活。问题是浏览器会冻结后台标签页
// （Edge 的"睡眠标签页"、Chrome 的节流），心跳一停，服务端就把"用户切去干别的"
// 误判成"没人用了"然后自我了断——用户看到的是"我没关它，它自己没了"，注入到
// 客户端的模型也跟着一起废。所以默认值必须是"不退出"，退出交给托盘与
// 界面里的「关闭界面」。
func TestParseIdle(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "", want: 0}, // 默认不自动退出
		{in: "off", want: 0},
		{in: "0", want: 0},
		{in: "never", want: 0},
		{in: "30m", want: 30 * time.Minute},
		{in: "1h30m", want: 90 * time.Minute},
		{in: " OFF ", want: 0},
		{in: "半小时", wantErr: true},
		{in: "-5m", wantErr: true},
	}
	for _, c := range cases {
		got, err := parseIdle(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseIdle(%q) 应当报错，却返回 %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseIdle(%q) 意外报错：%v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseIdle(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
	// 默认常量本身也要是 0：改回正数就等于把"会自己退出"重新变成默认行为。
	if defaultIdle != 0 {
		t.Errorf("默认空闲退出应为 0（不退出），得到 %v", defaultIdle)
	}
}
