// Package recyclenoise 诊断并清理回收站里的"工具噪声"。
//
// # 为什么要有它
//
// 用户反馈回收站被灌满、打开就卡死。查下来（读回收站 $I 元数据、逐条对过
// 原始路径）发现：真凶不是用户自己的文件，而是**工具侧每次调用产生的
// 小文件**——
//
//   - PowerShell 每次启动都做一次 AppLocker 策略探测，往临时目录写
//     __PSScriptPolicyTest_*.ps1 / *.psm1，每次 1~4 个
//   - 开发工具每次调用在临时目录下建一个 6~8 位随机名的目录
//   - 工具的安全删除中转目录 codebuddy-safe-delete*
//   - go build 被中断时留下的 go-buildNNNNNNN（已由 GOTMPDIR 治掉）
//
// 关键认识：**卡死的原因是「条目数」而不是「体积」**。这些条目每个只有
// 几字节到几十字节，但堆到几千个之后，资源管理器要逐个读元数据才能画出
// 列表，于是就卡住了。所以判断严重性不能看体积。
//
// 而且这**不是一次性泄漏**：只要在对话里干活，回收站就会持续增长。
// 实测两分钟内从 13 项涨到 34 项。
//
// # 为什么根治不了
//
// 产生方是工具侧（PowerShell 的策略探测、工具自身的临时目录），本程序
// 改不了它们的行为。能做的只有"把已有的清掉"，所以这里提供的是清理，
// 而不是拦截。
//
// # 安全边界
//
// 只删**匹配已知噪声特征**的条目，用户自己删的文件一律不动。要连用户文件
// 一起清需要显式要求（CleanOptions.All）。
package recyclenoise

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
)

// ---------- 回收站定位 ----------

// Entry 是回收站里的一条记录。
type Entry struct {
	// Original 是它被删除前的完整路径。这是判断"谁产生的"唯一可靠依据。
	Original string `json:"original"`
	// Size 是原始大小（字节）。注意：判断噪声不能只看大小，见包注释。
	Size int64 `json:"size"`
	// DeletedAt 是删除时间。
	DeletedAt time.Time `json:"deletedAt"`
	// Kind 是识别出的噪声类别；不是噪声则为空。
	Kind string `json:"kind,omitempty"`

	// metaPath 是 $I 元数据文件，bodyPath 是 $R 内容本体。
	// 删除时两者必须成对处理，否则会留下"幽灵条目"——回收站里有个名字，
	// 但点还原会报错（内容已经不在了）。
	metaPath string
	bodyPath string
}

// recycleDirs 返回所有盘符下当前用户的回收站目录。
//
// 每个盘是 <盘>:\$Recycle.Bin\<SID>\。SID 目录名以 S-1- 开头。
// 读不到就跳过——有的盘没有回收站目录，或者没有权限。
func recycleDirs() []string {
	var out []string
	for _, drive := range "CDEFGHIJ" {
		base := string(drive) + `:\$Recycle.Bin`
		kids, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, k := range kids {
			if !k.IsDir() || !strings.HasPrefix(k.Name(), "S-1-") {
				continue
			}
			out = append(out, filepath.Join(base, k.Name()))
		}
	}
	return out
}

// ---------- $I 元数据解析 ----------

// parseMeta 从 $I 文件读出一条回收站记录的元数据。
//
// Windows Vista+ 的版本 2 布局（实测确认，网上不少说法是错的）：
//
//	0..8     版本号（2）
//	8..16    原始文件大小
//	16..24   删除时间（FILETIME）
//	24..28   路径长度（字符数，小端 uint32）
//	28..     路径，UTF-16LE
//
// **偏移 24 那 4 个字节是长度字段，不是路径的一部分。** 按"路径从 24 开始"
// 去读，会在每个路径前面多出一个怪字符（那其实是长度值的首字节）。
// 这里用长度字段来切，既准确又能顺带做校验。
func parseMeta(path string) (original string, size int64, deleted time.Time, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, time.Time{}, err
	}
	if len(raw) < 28 {
		return "", 0, time.Time{}, fmt.Errorf("元数据太短（%d 字节）", len(raw))
	}

	size = int64(binary.LittleEndian.Uint64(raw[8:16]))

	// FILETIME：从 1601-01-01 UTC 起的 100 纳秒数。
	ft := binary.LittleEndian.Uint64(raw[16:24])
	if ft > 0 {
		const (
			// 1601 到 1970 之间的 100ns 数
			epochDiff = 116444736000000000
			tick      = 100 * time.Nanosecond
		)
		deleted = time.Unix(0, int64(ft-epochDiff)*int64(tick)).Local()
	}

	nchars := binary.LittleEndian.Uint32(raw[24:28])
	body := raw[28:]
	if nchars > 0 {
		// 按声明的字符数切，避免把结尾的 NUL 也算进路径。
		want := int(nchars) * 2
		if want <= len(body) {
			body = body[:want]
		}
	}

	u16 := make([]uint16, 0, len(body)/2)
	for i := 0; i+1 < len(body); i += 2 {
		u16 = append(u16, binary.LittleEndian.Uint16(body[i:i+2]))
	}
	original = string(utf16.Decode(u16))
	original = strings.TrimRight(original, "\x00")
	if original == "" {
		return "", 0, time.Time{}, fmt.Errorf("路径为空")
	}
	return original, size, deleted, nil
}

// ---------- 噪声识别 ----------

// noiseRule 是一条噪声识别规则。
type noiseRule struct {
	Kind string
	// Re 匹配"原始路径"。用正向写法，注意 Windows 路径里的反斜杠在正则里
	// 有特殊含义，必须写成 [\\/] 或双写。
	Re *regexp.Regexp
	// MaxSize 大于 0 时，只把不超过这个大小的条目当噪声。
	// 用于那些"模式比较宽"的规则，避免误伤同名的正常文件。
	MaxSize int64
}

// noiseRules 只列**明确**是工具反复产生的模式。不匹配的一律留给用户。
var noiseRules = []noiseRule{
	// PowerShell 的执行策略探测文件，每次启动都建 1~4 个。
	// 内容是固定的一句注释，纯粹给 AppLocker 探测用。
	{Kind: "PowerShell 策略探测", Re: regexp.MustCompile(`__PSScriptPolicyTest_`)},

	// go build 被中断留下的中间产物（正常情况下会自删）。
	//
	// 这里必须匹配**路径中任意位置**的 go-buildNNN，不能只在结尾。
	// 原因：一次被中断的构建会在回收站里炸出上百个条目——不只是那个
	// go-buildNNN 目录本身，它下面的每个子目录（b001 / b002 / exe / …）
	// 都会各自成为一条独立记录。实测一次构建平均炸出 77 条，最多见过 245 条。
	// 只认结尾的话，就只有那 1 条顶层目录被识别，其余上百条全被当成
	// "用户自己的文件"，等于没治好。
	{Kind: "go build 残渣", Re: regexp.MustCompile(`[\\/]go-build\d+[\\/]`)},
	{Kind: "go build 残渣", Re: regexp.MustCompile(`[\\/]go-build\d+$`)},

	// 工具自身的安全删除中转与临时目录。
	{Kind: "工具临时目录", Re: regexp.MustCompile(`codebuddy-safe-delete`)},

	// 构建/运行本项目留下的备份文件：`wbmux.exe~` 这种是编辑器或
	// 构建脚本改写可执行文件时的临时副本，出现在 dist/ 下。
	// 用结尾的波浪号判定——那是"这一份是被替换掉的旧版本"的通用约定。
	{Kind: "构建临时文件", Re: regexp.MustCompile(`[\\/]wbmux\.exe~$`)},

	// 工具链改写源文件时留下的中间副本：`<名字>.<随机数字>`，
	// 紧挨着原文件放在同一个目录里（如 recyclenoise.go.1415197015836502699）。
	// 这是 gofmt / go test / 编辑器保存时的原子替换残留。
	//
	// 位数放宽到 10~25：实测见过 18 位（zimport_test.go.688348325700437376）
	// 和 19 位（recyclenoise.go.1415197015836502699）两种，原来只认 19 位漏掉了前者。
	{Kind: "工具链临时副本", Re: regexp.MustCompile(`\.\d{10,25}$`), MaxSize: 1 << 20},

	// 工具每次调用建的短随机名目录：6~8 位小写字母/数字/下划线，
	// 如 rj5ot4gb / _xcafdc2 / fxq_xbwd。
	// 模式较宽，所以限制体积很小才算——工具只往里放一个几百字节的脚本。
	{Kind: "工具临时目录", Re: regexp.MustCompile(`[\\/][Tt]emp[\\/]_?[a-z0-9_]{6,8}$`), MaxSize: 4096},

	// go test 的临时目录。Go 的 testing 包会在临时目录下建一个以测试函数名
	// 开头、末尾带随机数的目录，比如 TestLaunchBothReportsEachSideIndependent1106365132。
	// 测试里的 t.TempDir() 会往里铺一棵假的安装树（apps/cn/、apps/intl/…），
	// 跑完清理时**每个子目录都会变成回收站里的一条独立记录**——
	// 实测一次 go test ./... 能塞进几百条。这是本地跑测试的正常代价，
	// 不是用户删了东西。
	//
	// 注意必须连**子目录和里面的文件**一起匹配（所以要求 Test… 出现在
	// 路径中任意一段 temp 之下，且不限定结尾）。只匹配顶层目录名的话，
	// 那棵树里的几十个子路径又会被漏掉。
	// 测试名的随机尾巴有的跟在 ASCII 名后面（…Independent1106365132），
	// 有的跟在中文名后面（…中文路径2900747262），所以用"临时目录段后
	// 紧跟 Test 开头"作为判据，不强制全 ASCII。
	{Kind: "go test 临时目录", Re: regexp.MustCompile(`[\\/][Tt]emp[\\/]Test[^\\/]*\d{6,}[\\/]`)},
	{Kind: "go test 临时目录", Re: regexp.MustCompile(`[\\/][Tt]emp[\\/]Test[^\\/]*\d{6,}$`)},

	// ---------- wbmux 自己产生的（2026-09-29 实测：它才是最大头）----------
	//
	// 用户反馈"有些工作区的回收站的文件没有被命中"。实测本机回收站 228 条里
	// 只认出 11 条，漏掉的 217 条有 **117 条是 wbmux 自己写的**。
	//
	// sqlite 桥每次查库都会在 ~/.wbmux/tmp 下写一对脚本与结果：
	// `sqlite-N.js` / `sqlite-N.json`（N 是并发序号）。用完即弃，但
	// **正常删除也会进回收站**——查几十次就攒出上百条，而且它们体积是 0
	// 或几百字节，最典型的"条目数拖垮资源管理器"来源。
	//
	// 为什么之前没被"工具临时目录"那条命中：那条要求 Temp 段后紧跟
	// 6~8 位随机名，而这里是 `sqlite-1.js` 这种**固定前缀 + 序号 + 扩展名**。
	{Kind: "wbmux sqlite 桥临时件",
		Re:      regexp.MustCompile(`[\\/]\.wbmux[\\/]tmp[\\/]sqlite-\d+\.(js|json)$`),
		MaxSize: 1 << 20},

	// 构建产物与被替换掉的旧版本：`wbmux\dist\wbmux.exe`、`wbmux\wbmux.exe`。
	//
	// 这些看着"像程序"，但都在源码树里——真正的稳定安装位是
	// `~/.wbmux/bin/wbmux.exe`，源码树里的那份随时可以重新编出来。
	// 限定在 wbmux 源码树的 dist/ 或仓库根下，避免误伤别处同名的 exe。
	{Kind: "wbmux 构建产物",
		Re: regexp.MustCompile(`[\\/](wbmux|wbmux-[^\\/]*)[\\/](dist[\\/])?wbmux\.exe$`)},

	// ---------- Python 工具链的临时件 ----------
	//
	// pip 每次装包会建一串临时目录，名字是固定的几种前缀 + 随机后缀：
	// pip-install- / pip-unpack- / pip-ephem-wheel-cache- / pip-build-tracker-
	// / pip-download-，都在 Temp 下。装完即删，同样会进回收站。
	//
	// 必须连**里面的内容**一起匹配（如 pip-unpack-xxx\py7zr-1.1.3-...whl）：
	// 那棵树里每个文件都是一条独立记录。所以用"路径中任意位置的 pip- 临时段"
	// 作判据，而不是只认结尾。
	//
	// **别改成只列举已知前缀**：pip 的临时目录前缀随版本增加
	// （download 这类就是后来补的），用 `pip-[a-z-]+-` 让新前缀自动覆盖。
	{Kind: "pip 安装临时件",
		Re: regexp.MustCompile(`[\\/][Tt]emp[\\/]pip-[a-z][a-z-]*-[^\\/]*`), MaxSize: 512 << 20},

	// Python 自测用的可删文件（CPython 自己的 access 测试会建，名字很长且带随机尾巴）。
	{Kind: "Python 测试残留", Re: regexp.MustCompile(`accesstest_deleteme_`)},

	// SQLite 的 WAL / 共享内存边车文件。
	//
	// **这是每次客户端正常退出都会产生的**：SQLite 在最后一个连接关闭时会
	// 删掉 <库>.db-wal 与 <库>.db-shm（属于正常收尾），而删除动作会进回收站。
	// 于是每开关一次 WorkBuddy，回收站里就多两三条。它们**纯瞬态**——
	// 客户端下次启动会重新建，删掉不影响任何数据。
	//
	// 判据限定在 .db-wal / .db-shm / .db-journal 结尾（而不是所有 -wal），
	// 避免误伤用户自己起的同后缀文件。
	{Kind: "SQLite 边车文件",
		Re: regexp.MustCompile(`\.(db|sqlite|sqlite3)-(wal|shm|journal)$`), MaxSize: 64 << 20},
}

// Classify 判断一条记录是不是工具噪声，是则返回类别名。
func Classify(e Entry) string {
	for _, r := range noiseRules {
		if !r.Re.MatchString(e.Original) {
			continue
		}
		if r.MaxSize > 0 && e.Size > r.MaxSize {
			continue
		}
		return r.Kind
	}
	return ""
}

// ---------- 扫描 ----------

// Scan 列出回收站里所有条目，并标注哪些是工具噪声。
func Scan() ([]Entry, error) {
	dirs := recycleDirs()
	if len(dirs) == 0 {
		return nil, fmt.Errorf("找不到回收站目录")
	}

	var out []Entry
	for _, d := range dirs {
		kids, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, k := range kids {
			name := k.Name()
			// $I 是元数据；$R 是内容本体，与 $I 同后缀。
			if !strings.HasPrefix(name, "$I") {
				continue
			}
			metaPath := filepath.Join(d, name)
			original, size, deleted, err := parseMeta(metaPath)
			if err != nil {
				continue
			}
			body := filepath.Join(d, "$R"+strings.TrimPrefix(name, "$I"))
			e := Entry{
				Original:  original,
				Size:      size,
				DeletedAt: deleted,
				metaPath:  metaPath,
			}
			if _, err := os.Stat(body); err == nil {
				e.bodyPath = body
			}
			e.Kind = Classify(e)
			out = append(out, e)
		}
	}

	// 稳定排序：先按类别（噪声在前），再按时间倒序。
	sort.SliceStable(out, func(i, j int) bool {
		ni, nj := out[i].Kind != "", out[j].Kind != ""
		if ni != nj {
			return ni
		}
		return out[i].DeletedAt.After(out[j].DeletedAt)
	})
	return out, nil
}

// ---------- 汇总 ----------

// Summary 是给界面看的一份概览。
type Summary struct {
	// Total 是回收站条目总数。
	Total int `json:"total"`
	// Noise 是可以安全清掉的工具噪声条数。
	Noise int `json:"noise"`
	// NoiseBytes 是这些噪声的体积。通常很小——卡死看的是条目数不是体积，
	// 这个字段只是顺带给出，别拿它判断严重性。
	NoiseBytes int64 `json:"noiseBytes"`
	// ByKind 按类别分组计数，让用户一眼看出是谁在塞。
	ByKind []KindCount `json:"byKind"`
	// Other 是"非噪声"条目（多半是用户自己删的），只取前若干条给界面展示。
	Other []Entry `json:"other"`
	// OtherTotal 是其他条目的总数（Other 只是其中一部分）。
	OtherTotal int `json:"otherTotal"`
	// OtherBytes 是其他条目的总体积。
	OtherBytes int64 `json:"otherBytes"`
}

// KindCount 是某一类噪声的计数。
type KindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// otherPreviewLimit 限制返回给界面的"其他条目"条数，避免一次渲染几百行。
const otherPreviewLimit = 20

// Summarize 把扫描结果汇总成界面用的概览。
func Summarize(entries []Entry) Summary {
	var s Summary
	s.Total = len(entries)

	counts := map[string]int{}
	for _, e := range entries {
		if e.Kind == "" {
			s.OtherTotal++
			s.OtherBytes += e.Size
			if len(s.Other) < otherPreviewLimit {
				s.Other = append(s.Other, e)
			}
			continue
		}
		s.Noise++
		s.NoiseBytes += e.Size
		counts[e.Kind]++
	}

	// 按条数从多到少，让最大的来源排最前。
	for k, n := range counts {
		s.ByKind = append(s.ByKind, KindCount{Kind: k, Count: n})
	}
	sort.Slice(s.ByKind, func(i, j int) bool {
		if s.ByKind[i].Count != s.ByKind[j].Count {
			return s.ByKind[i].Count > s.ByKind[j].Count
		}
		return s.ByKind[i].Kind < s.ByKind[j].Kind
	})
	return s
}

// ---------- 清理 ----------

// CleanOptions 控制清理范围。
type CleanOptions struct {
	// All 为 true 时连"非噪声"条目一起清掉（等于清空回收站）。
	// 默认 false——用户自己删的东西可能还想恢复，不该替他决定。
	All bool
}

// CleanResult 是一次清理的结果。
type CleanResult struct {
	Removed  int      `json:"removed"`
	Failed   int      `json:"failed"`
	Freed    int64    `json:"freed"`
	Failures []string `json:"failures,omitempty"`
}

// Clean 彻底删除匹配到的条目。
//
// 注意这里不是"移到回收站"——东西本来就在回收站里，这一步是把它们真正
// 从磁盘上抹掉。之所以不复用系统的删除接口，是因为那样又会绕回"进回收站"，
// 等于什么都没做。
//
// $I 与 $R 必须成对删除。只删一个会留下"幽灵条目"：回收站里还显示着名字，
// 但点还原会失败，因为内容已经不在了。
func Clean(entries []Entry, opts CleanOptions) CleanResult {
	var res CleanResult
	for _, e := range entries {
		if e.Kind == "" && !opts.All {
			continue
		}
		if err := removeEntry(e); err != nil {
			res.Failed++
			if len(res.Failures) < 5 {
				res.Failures = append(res.Failures, fmt.Sprintf("%s: %v", e.Original, err))
			}
			continue
		}
		res.Removed++
		res.Freed += e.Size
	}
	return res
}

// removeEntry 删掉一条记录（$I 与 $R 一起）。
func removeEntry(e Entry) error {
	var lastErr error
	for _, p := range []string{e.metaPath, e.bodyPath} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			continue // 本来就不在，不算失败
		}
		// 回收站里的文件常带着只读属性，先清掉再删。
		_ = os.Chmod(p, 0o700)
		if err := os.RemoveAll(p); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// CleanNoise 只清噪声，是最常用的一步到位入口。
func CleanNoise() (CleanResult, Summary, error) {
	entries, err := Scan()
	if err != nil {
		return CleanResult{}, Summary{}, err
	}
	before := Summarize(entries)
	res := Clean(entries, CleanOptions{})

	after, err := Scan()
	if err != nil {
		return res, before, nil
	}
	return res, Summarize(after), nil
}
