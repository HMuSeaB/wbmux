package variant

import (
	"path/filepath"
	"sort"
	"strings"
)

// pattern 是用于路径匹配的一个特征串及其归属档位。
type pattern struct {
	text string
	id   ID
}

// GuessFromPath 依据路径特征推测安装档位，无法判断时返回空 ID。
//
// 用于 `wbmux run --exe <路径>` 这类只给了路径、没给档位的场景：
// 有了它，用户不必同时指定 --host。
//
// 匹配按特征串长度从长到短进行。这是必要的：国内版的主程序名是
// "WorkBuddy"，而国际版是 "WorkBuddyAI"，前者是后者的子串。
// 若按短串优先，国际版的路径会被误判成国内版。
func GuessFromPath(p string) ID {
	low := strings.ToLower(filepath.ToSlash(strings.TrimSpace(p)))
	if low == "" {
		return ""
	}

	var pats []pattern
	for _, b := range All() {
		names := []string{
			b.WinExecutableName,
			b.MacAppName,
			b.MacBundleID,
			b.LinuxExecutableName,
			b.DataFolderName,
		}
		for _, n := range names {
			if n != "" {
				pats = append(pats, pattern{strings.ToLower(n), b.ID})
			}
		}
	}

	sort.SliceStable(pats, func(i, j int) bool {
		return len(pats[i].text) > len(pats[j].text)
	})

	for _, pat := range pats {
		if strings.Contains(low, pat.text) {
			return pat.id
		}
	}
	return ""
}
