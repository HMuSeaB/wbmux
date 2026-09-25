package variant

import "testing"

func TestGuessFromPath(t *testing.T) {
	cases := []struct {
		path string
		want ID
	}{
		// 国内版
		{`D:\Tools\WorkBuddy\WorkBuddy.exe`, CN},
		{`C:\Program Files\WorkBuddy\WorkBuddy.exe`, CN},
		{`C:\Users\tester\.workbuddy\settings.json`, CN},
		{`/Applications/WorkBuddy.app/Contents/MacOS/WorkBuddy`, CN},
		{`/usr/lib/workbuddy/workbuddy`, CN},

		// 国际版
		{`D:\WorkBuddyAI\WorkBuddyAI.exe`, Intl},
		{`C:\Users\tester\.workbuddy-ai\last-launch.json`, Intl},
		{`/Applications/WorkBuddy AI.app/Contents/MacOS/WorkBuddy AI`, Intl},
		{`/opt/workbuddy-ai/workbuddy-ai`, Intl},
		{`C:\Users\tester\Library\com.workbuddy.workbuddy-ai\cache`, Intl},

		// 判不出来
		{``, ""},
		{`C:\Windows\System32\notepad.exe`, ""},
		{`/tmp/random/path`, ""},
	}
	for _, c := range cases {
		if got := GuessFromPath(c.path); got != c.want {
			t.Errorf("GuessFromPath(%q) = %q, 期望 %q", c.path, got, c.want)
		}
	}
}

// 国内版主程序名是国际版的子串，必须靠"长串优先"来区分。
func TestGuessFromPathPrefersMoreSpecificName(t *testing.T) {
	if got := GuessFromPath(`D:\WorkBuddyAI\WorkBuddyAI.exe`); got != Intl {
		t.Fatalf("国际版路径被误判为 %q", got)
	}
	// 反过来：国际版目录下放着国内版主程序名，仍应按更具体的目录名判为国际版。
	if got := GuessFromPath(`D:\WorkBuddyAI\WorkBuddy.exe`); got != Intl {
		t.Errorf("应以更具体的路径特征为准, 实际 %q", got)
	}
}

func TestGuessFromPathIsCaseInsensitive(t *testing.T) {
	if got := GuessFromPath(`d:\tools\workbuddy\WORKBUDDY.EXE`); got != CN {
		t.Errorf("大小写不应影响判断, 实际 %q", got)
	}
}
