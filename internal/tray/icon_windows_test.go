//go:build windows

package tray

import "testing"

// TestCreateAppIconSucceeds 确认图标真的被系统接受了。
//
// 光有"字节布局对"不够：CreateIconFromResourceEx 会因为版本号、掩码长度
// 之类的细节直接返回 0，而代码里为稳妥起见失败会退回系统通用图标——
// 那样托盘照样有图标，只是又变回"认不出是谁"，正是这次要修的东西。
// 所以这里断言拿到的手柄**不是**那个通用图标。
func TestCreateAppIconSucceeds(t *testing.T) {
	got := createAppIcon()
	if got == 0 {
		t.Fatal("createAppIcon 返回了空手柄")
	}
	fallback, _, _ := procLoadIconW.Call(0, idiApplication)
	if got == fallback {
		t.Fatal("拿到的是系统通用图标：说明 CreateIconFromResourceEx 失败了，" +
			"图标字节大概有问题（检查 biHeight 是否两倍、掩码行是否对齐、版本号）")
	}
}
