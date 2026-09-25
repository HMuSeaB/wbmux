package variant

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// RegistryEntry 是注册表里一条已安装程序的记录。
//
// 单独抽成与平台无关的结构，是为了让匹配逻辑能脱离 Windows 单测；
// 真正的注册表读取在 registry_windows.go，其它平台返回空。
type RegistryEntry struct {
	DisplayName     string
	Publisher       string
	InstallLocation string
	DisplayIcon     string
}

var (
	registryOnce sync.Once
	registryList []RegistryEntry
)

// registryInstallsCached 只读一次注册表。
//
// Detect 一次命令里可能被调用两遍（国内版 + 国际版），而枚举卸载项要
// 遍历数百个子键，重复读取纯属浪费。
func registryInstallsCached() []RegistryEntry {
	registryOnce.Do(func() { registryList = registryInstalls() })
	return registryList
}

// registryCandidates 从注册表记录里挑出属于某个档位的主程序路径候选。
func registryCandidates(entries []RegistryEntry, b Backend) []string {
	var out []string
	for _, e := range entries {
		if !publisherMatches(e.Publisher) {
			continue
		}
		if !matchProduct(e.DisplayName, b.ProductName) {
			continue
		}
		out = append(out, exeFromEntry(e, b)...)
	}
	return dedupe(out)
}

// matchProduct 判断 DisplayName 是否指的就是 productName 这个产品。
//
// 不能简单用前缀匹配：国内版产品名 "WorkBuddy" 恰好是国际版
// "WorkBuddy AI" 的前缀，只做前缀判断会把国际版的记录算到国内版头上。
// 因此要求匹配之后紧跟的必须是版本号或括号说明。
func matchProduct(displayName, productName string) bool {
	d := strings.ToLower(strings.TrimSpace(displayName))
	p := strings.ToLower(strings.TrimSpace(productName))
	if p == "" || !strings.HasPrefix(d, p) {
		return false
	}
	rest := strings.TrimSpace(d[len(p):])
	if rest == "" {
		return true
	}
	switch c := rest[0]; {
	case c >= '0' && c <= '9', c == 'v', c == '(', c == '-', c == '_':
		return true
	}
	return false
}

// publisherMatches 做一次宽松的厂商校验，挡掉同名的不相关程序。
// Publisher 缺失时不作否决，以免误伤精简过的安装记录。
func publisherMatches(p string) bool {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return true
	}
	return strings.Contains(p, "tencent") || strings.Contains(p, "腾讯")
}

// exeFromEntry 从一条注册表记录里推导主程序路径。
func exeFromEntry(e RegistryEntry, b Backend) []string {
	wantExe := b.WinExecutableName + ".exe"
	var out []string

	if loc := strings.TrimSpace(e.InstallLocation); loc != "" {
		loc = strings.TrimRight(loc, `\/`)
		if loc != "" {
			out = append(out, filepath.Join(loc, wantExe))
		}
	}

	if icon := parseDisplayIcon(e.DisplayIcon); icon != "" {
		// 只有文件名对得上才采纳：DisplayIcon 有时指向别的东西。
		if strings.EqualFold(filepath.Base(icon), wantExe) {
			out = append(out, icon)
		}
	}
	return out
}

// parseDisplayIcon 从 DisplayIcon 里剥出可执行文件路径。
//
// 该值的常见形式是 "D:\App\App.exe,0"，也可能带引号。
func parseDisplayIcon(s string) string {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s == "" {
		return ""
	}
	// 去掉结尾的 ",<图标序号>"。路径本身可能含逗号，所以只认
	// 最后一段能被解析成整数的情况。
	if i := strings.LastIndexByte(s, ','); i > 0 {
		if _, err := strconv.Atoi(strings.TrimSpace(s[i+1:])); err == nil {
			s = s[:i]
		}
	}
	return strings.Trim(strings.TrimSpace(s), `"`)
}
