package recyclenoise

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

// buildMeta 造一份版本 2 的 $I 元数据，用来测解析。
//
// 关键在于**路径前面那 4 个字节是长度字段**——不写这个字段就测不出
// "按 24 偏移读会多出怪字符"那个 bug。
func buildMeta(original string, size int64) []byte {
	buf := make([]byte, 28)
	binary.LittleEndian.PutUint64(buf[0:8], 2) // 版本
	binary.LittleEndian.PutUint64(buf[8:16], uint64(size))
	binary.LittleEndian.PutUint64(buf[16:24], 0) // 删除时间，测里不关心

	u16 := utf16.Encode([]rune(original))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(len(u16)))

	for _, u := range u16 {
		var tmp [2]byte
		binary.LittleEndian.PutUint16(tmp[:], u)
		buf = append(buf, tmp[:]...)
	}
	return buf
}

func TestParseMetaRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		original string
		size     int64
	}{
		{"中文路径", `C:\Users\36230\Desktop\浓度.docx`, 37982},
		{"临时目录随机名", `C:\Users\36230\AppData\Local\Temp\fxq_xbwd`, 4},
		{"带空格的路径", `C:\Program Files\Some App\x.exe`, 12345},
		{"根目录文件", `D:\a.txt`, 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "$IAAAAAAAA")
			if err := os.WriteFile(p, buildMeta(c.original, c.size), 0o600); err != nil {
				t.Fatal(err)
			}
			got, size, _, err := parseMeta(p)
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			if got != c.original {
				t.Errorf("路径不对\n  期望 %q\n  实际 %q", c.original, got)
			}
			if size != c.size {
				t.Errorf("大小不对: 期望 %d，实际 %d", c.size, size)
			}
			// 长度字段没被剥掉时，路径开头会多出一个怪字符。
			// 这里显式断言首字符是盘符，防止那个 bug 复发。
			if got[0] != 'C' && got[0] != 'D' {
				t.Errorf("路径开头不是盘符，长度字段可能没剥掉: %q", got)
			}
		})
	}
}

func TestParseMetaTooShort(t *testing.T) {
	p := filepath.Join(t.TempDir(), "$Ishort")
	if err := os.WriteFile(p, []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := parseMeta(p); err == nil {
		t.Error("过短的元数据应该报错")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		original string
		size     int64
		wantKind string
	}{
		// 该认出来的
		{"PowerShell 策略探测 ps1",
			`C:\Users\36230\AppData\Local\Temp\__PSScriptPolicyTest_w0fzll2g.ram.ps1`, 68, "PowerShell 策略探测"},
		{"PowerShell 策略探测 psm1",
			`C:\Users\36230\AppData\Local\Temp\__PSScriptPolicyTest_u4h0wdbr.dd3.psm1`, 68, "PowerShell 策略探测"},
		{"go build 残渣",
			`C:\Users\36230\AppData\Local\Temp\go-build1288296383`, 16500000, "go build 残渣"},
		{"工具随机名目录",
			`C:\Users\36230\AppData\Local\Temp\rj5ot4gb`, 4, "工具临时目录"},
		{"工具随机名目录带下划线",
			`C:\Users\36230\AppData\Local\Temp\_xcafdc2`, 4, "工具临时目录"},
		{"工具随机名目录含下划线",
			`C:\Users\36230\AppData\Local\Temp\fxq_xbwd`, 4, "工具临时目录"},
		{"safe-delete",
			`C:\Users\36230\AppData\Local\Temp\codebuddy-safe-delete-bulk`, 0, "工具临时目录"},

		// go build 的**嵌套子路径**也要认出来。这是这次最要紧的一条：
		// 一次被中断的构建会把上百个子目录各自变成一条回收站记录，
		// 只匹配结尾的 go-buildNNN 就只能认出顶层那一条。
		{"go build 的一级子目录",
			`C:\Users\36230\AppData\Local\go-tmp\go-build79954058\b001`, 0, "go build 残渣"},
		{"go build 的深层子目录",
			`C:\Users\36230\AppData\Local\go-tmp\go-build3426418836\b001\exe`, 0, "go build 残渣"},
		{"go build 里的文件",
			`C:\Users\36230\AppData\Local\go-tmp\go-build465982355\b001\exe\a.out.exe`, 11000000, "go build 残渣"},

		// Go 构建缓存（GOCACHE）淘汰的条目。2026-09-30 实测它是本机最大的一头：
		// 8794 条 / 498 MB，而当时一条规则都没命中它——用户说的"回收站还有东西
		// 没清理干净"就是指这个。
		//
		// 形态是 `<GOCACHE>\<2位十六进制>\<64位十六进制>-{a,d}`，-a 是 action
		// 记录、-d 是数据。Go 自己按容量淘汰缓存时是**移进回收站**而不是直接删。
		{"Go 缓存淘汰 action 记录",
			`C:\Users\36230\AppData\Local\go-build\ff\fff31923861dfbeba1293322160f78282a7975db4e38de1282b2158c9025ce68-a`,
			175, "Go 构建缓存淘汰"},
		{"Go 缓存淘汰 数据块",
			`C:\Users\36230\AppData\Local\go-build\ff\ffe754bc5a4cd8fc873493e7fcacb52cd7523ca59841ecb6710e5e91fc6664d3-d`,
			13700000, "Go 构建缓存淘汰"},
		// 大小不设上限：实测有 13.7 MB 的 -d 条目，它们正是占体积的那批。
		{"Go 缓存淘汰 大块也要认",
			`C:\Users\36230\AppData\Local\go-build\a1\` +
				`1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef-d`,
			13700000, "Go 构建缓存淘汰"},

		// go test 的临时树（t.TempDir 会铺出多级子目录）。
		{"go test 顶层",
			`C:\Users\36230\AppData\Local\Temp\TestURLContainsToken790468319`, 0, "go test 临时目录"},
		{"go test 的子目录",
			`C:\Users\36230\AppData\Local\Temp\TestURLContainsToken790468319\002\apps\intl`, 0, "go test 临时目录"},
		{"go test 里的文件",
			`C:\Users\36230\AppData\Local\Temp\TestURLContainsToken790468319\002\apps\cn\resources\app.asar`, 4, "go test 临时目录"},
		{"go test 中文测试名",
			`C:\Users\36230\AppData\Local\Temp\TestParseMetaRoundTrip中文路径2900747262\001\$IAAAAAAAA`, 88, "go test 临时目录"},

		// 构建/工具链残留
		{"构建备份",
			`D:\4rchive\Code\wbmux\dist\wbmux.exe~`, 7905792, "构建临时文件"},
		{"工具链临时副本",
			`D:\4rchive\Code\wbmux\internal\recyclenoise\recyclenoise.go.1415197015836502699`, 12124, "工具链临时副本"},
		// 位数不止 19：这条是 18 位（实测遇到），原来的 {19} 会漏掉它。
		{"工具链临时副本（18 位）",
			`D:\4rchive\Code\wbmux\internal\zimport\zimport_test.go.688348325700437376`, 4096, "工具链临时副本"},

		// ---------- 2026-09-29 补的规则（用户反馈"有些回收站文件没被命中"）----------
		//
		// 实测本机回收站 228 条里只认出 11 条，漏掉的绝大部分是下面这几类。
		// 其中 **wbmux 自己的 sqlite 桥是最大头**（117 条）—— 它每次查库都写
		// 一对脚本与结果，用完即弃，但正常删除也会进回收站。

		{"wbmux sqlite 桥脚本",
			`C:\Users\36230\.wbmux\tmp\sqlite-1.js`, 6991, "wbmux sqlite 桥临时件"},
		{"wbmux sqlite 桥结果",
			`C:\Users\36230\.wbmux\tmp\sqlite-3.json`, 483, "wbmux sqlite 桥临时件"},
		{"wbmux sqlite 桥（两位数序号）",
			`C:\Users\36230\.wbmux\tmp\sqlite-12.json`, 483, "wbmux sqlite 桥临时件"},

		{"wbmux 构建产物 dist",
			`D:\4rchive\Code\wbmux\dist\wbmux.exe`, 11645952, "wbmux 构建产物"},
		{"wbmux 构建产物 仓库根",
			`D:\4rchive\Code\wbmux\wbmux.exe`, 11645952, "wbmux 构建产物"},

		{"pip install 临时目录",
			`C:\Users\36230\AppData\Local\Temp\pip-install-d_6t02ll`, 0, "pip 安装临时件"},
		{"pip unpack 临时目录",
			`C:\Users\36230\AppData\Local\Temp\pip-unpack-orgg1t1r`, 0, "pip 安装临时件"},
		{"pip 缓存目录",
			`C:\Users\36230\AppData\Local\Temp\pip-ephem-wheel-cache-1nd8ouih`, 0, "pip 安装临时件"},
		// 后来才出现的 prefix，规则要能自动覆盖（别写成只列举已知几种）。
		{"pip download 临时目录（新前缀）",
			`C:\Users\36230\AppData\Local\Temp\pip-download-253a8j7w`, 0, "pip 安装临时件"},
		// 那棵树里每个文件都是一条独立记录，必须一起认出来。
		{"pip unpack 里的 whl",
			`C:\Users\36230\AppData\Local\Temp\pip-unpack-orgg1t1r\py7zr-1.1.3-py3-none-any.whl`, 72242, "pip 安装临时件"},

		// SQLite 边的 WAL/SHM：**每次客户端正常退出都会产生**（SQLite 收尾时
		// 删掉它们，删除动作进回收站）。纯瞬态，客户端下次启动重建。
		{"SQLite 边车 -wal",
			`C:\Users\36230\.workbuddy-ai\workbuddy.db-wal`, 0, "SQLite 边车文件"},
		{"SQLite 边车 -shm",
			`C:\Users\36230\.workbuddy-ai\workbuddy.db-shm`, 32768, "SQLite 边车文件"},
		{"SQLite 边车 -journal",
			`C:\Users\36230\.workbuddy\workbuddy.db-journal`, 1024, "SQLite 边车文件"},

		{"Python access 测试残留",
			`C:\Users\36230\.workbuddy\binaries\python\versions\3.13.12\Lib\site-packages\accesstest_deleteme_fishfingers_custard_s446au`, 0, "Python 测试残留"},

		// 不该认出来的——用户自己的东西
		{"用户删的安装包",
			`C:\Users\36230\Downloads\oopz_setup_v1.4.6.1.exe`, 253019040, ""},
		{"用户删的文档",
			`C:\Users\36230\Desktop\浓度.docx`, 37982, ""},
		{"临时目录里的大目录（名字像但不是）",
			`C:\Users\36230\AppData\Local\Temp\kcmoewz5`, 3200000000, ""},
		{"临时目录里的 oopz",
			`C:\Users\36230\AppData\Local\Temp\oopz`, 296000000, ""},
		{"普通文件",
			`C:\Users\36230\Documents\report.pdf`, 1024, ""},
		// 用户自己的数据库（不是边车文件）——别被 -wal 那条规则误伤。
		{"用户自己的 sqlite 库",
			`C:\Users\36230\Documents\我的数据.db`, 102400, ""},
		// 名字里有 pip 但不在临时目录、也不是 pip 的临时目录格式。
		{"文档里的 pip 笔记",
			`C:\Users\36230\Documents\pip-notes.md`, 2048, ""},

		// ---------- 反例：Go 缓存那条规则**不许**命中这些 ----------
		//
		// 那条规则用了 40+ 位十六进制的宽模式，最容易误伤，所以逐条钉住边界。
		// 判断原则：只有"go-build 下、恰好两级十六进制名、末段是长哈希 + -a/-d"
		// 才算；任何一条不满足都交还给用户。
		{"反例：普通 go-buildN 临时目录（归上一条规则，不是缓存）",
			`C:\Users\36230\AppData\Local\go-tmp\go-build4146152272\b001`, 0, "go build 残渣"},
		{"反例：分片段不是 2 位十六进制",
			`C:\Users\36230\AppData\Local\go-build\zzz\` +
				`1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef-a`, 100, ""},
		{"反例：哈希太短（不足 40 位）",
			`C:\Users\36230\AppData\Local\go-build\ff\abc-a`, 100, ""},
		{"反例：后缀不是 -a/-d",
			`C:\Users\36230\AppData\Local\go-build\ff\` +
				`1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef-x`, 100, ""},
		{"反例：只是哈希结尾，但不在 go-build 下",
			`C:\Users\36230\Documents\` +
				`1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef-a`, 100, ""},
		{"反例：go-build 下但只有一级（缺分片目录）",
			`C:\Users\36230\AppData\Local\go-build\` +
				`1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef-a`, 100, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(Entry{Original: c.original, Size: c.size})
			if got != c.wantKind {
				t.Errorf("类别不对\n  路径 %s\n  期望 %q\n  实际 %q",
					c.original, c.wantKind, got)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	entries := []Entry{
		{Original: `C:\T\__PSScriptPolicyTest_a.ps1`, Size: 68, Kind: "PowerShell 策略探测"},
		{Original: `C:\T\__PSScriptPolicyTest_b.ps1`, Size: 68, Kind: "PowerShell 策略探测"},
		{Original: `C:\T\rj5ot4gb`, Size: 4, Kind: "工具临时目录"},
		{Original: `C:\D\mine.exe`, Size: 253019040},
	}

	s := Summarize(entries)

	if s.Total != 4 {
		t.Errorf("总数应为 4，实际 %d", s.Total)
	}
	if s.Noise != 3 {
		t.Errorf("噪声应为 3，实际 %d", s.Noise)
	}
	if s.OtherTotal != 1 {
		t.Errorf("其他应为 1，实际 %d", s.OtherTotal)
	}
	if s.NoiseBytes != 140 {
		t.Errorf("噪声体积应为 140，实际 %d", s.NoiseBytes)
	}
	if s.OtherBytes != 253019040 {
		t.Errorf("其他体积应为 253019040，实际 %d", s.OtherBytes)
	}
	if len(s.ByKind) != 2 {
		t.Fatalf("应有 2 个类别，实际 %d", len(s.ByKind))
	}
	// 按条数降序：PowerShell 策略探测(2) 应排在 工具临时目录(1) 前面
	if s.ByKind[0].Kind != "PowerShell 策略探测" || s.ByKind[0].Count != 2 {
		t.Errorf("排序不对，第一项应为 PowerShell 策略探测×2，实际 %+v", s.ByKind[0])
	}
	if len(s.Other) != 1 || s.Other[0].Original != `C:\D\mine.exe` {
		t.Errorf("其他条目列表不对: %+v", s.Other)
	}
}

func TestSummarizeOtherLimit(t *testing.T) {
	var entries []Entry
	for i := 0; i < otherPreviewLimit+10; i++ {
		entries = append(entries, Entry{
			Original: `C:\D\file` + string(rune('a'+i%26)) + `.txt`,
			Size:     1,
		})
	}
	s := Summarize(entries)
	if len(s.Other) != otherPreviewLimit {
		t.Errorf("其他条目预览应截断到 %d 条，实际 %d", otherPreviewLimit, len(s.Other))
	}
	if s.OtherTotal != otherPreviewLimit+10 {
		t.Errorf("其他条目总数应为 %d，实际 %d", otherPreviewLimit+10, s.OtherTotal)
	}
}

// TestCleanKeepsUserFiles 是这次改动里最要紧的一条：
// 默认清理**绝对不能**碰用户自己删的文件。
func TestCleanKeepsUserFiles(t *testing.T) {
	dir := t.TempDir()

	mk := func(suffix, original string, size int64) Entry {
		meta := filepath.Join(dir, "$I"+suffix)
		body := filepath.Join(dir, "$R"+suffix)
		if err := os.WriteFile(meta, buildMeta(original, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(body, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return Entry{
			Original: original, Size: size,
			Kind:     Classify(Entry{Original: original, Size: size}),
			metaPath: meta, bodyPath: body,
		}
	}

	noise := mk("NOISE", `C:\Users\36230\AppData\Local\Temp\__PSScriptPolicyTest_a.ps1`, 68)
	user := mk("USERX", `C:\Users\36230\Downloads\setup.exe`, 999)

	res := Clean([]Entry{noise, user}, CleanOptions{})

	if res.Removed != 1 {
		t.Errorf("应只清 1 项，实际 %d", res.Removed)
	}
	if _, err := os.Stat(noise.metaPath); !os.IsNotExist(err) {
		t.Error("噪声条目没被清掉")
	}
	if _, err := os.Stat(noise.bodyPath); !os.IsNotExist(err) {
		t.Error("噪声的 $R 没被清掉（会留幽灵条目）")
	}
	if _, err := os.Stat(user.metaPath); err != nil {
		t.Error("用户文件的 $I 被误删了")
	}
	if _, err := os.Stat(user.bodyPath); err != nil {
		t.Error("用户文件的 $R 被误删了")
	}
}

// TestCleanAllRemovesEverything 验证显式要求时才动用户文件。
func TestCleanAllRemovesEverything(t *testing.T) {
	dir := t.TempDir()
	meta := filepath.Join(dir, "$IUSERX")
	body := filepath.Join(dir, "$RUSERX")
	if err := os.WriteFile(meta, buildMeta(`C:\D\mine.exe`, 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(body, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := Entry{Original: `C:\D\mine.exe`, Size: 1, metaPath: meta, bodyPath: body}
	res := Clean([]Entry{e}, CleanOptions{All: true})

	if res.Removed != 1 {
		t.Errorf("--all 应清 1 项，实际 %d", res.Removed)
	}
	if _, err := os.Stat(meta); !os.IsNotExist(err) {
		t.Error("--all 时用户条目也应被清掉")
	}
}

// TestRemoveEntryPairwise 单独确认成对删除：
// 只删 $I 会留下幽灵条目，所以两个都要没。
func TestRemoveEntryPairwise(t *testing.T) {
	dir := t.TempDir()
	meta := filepath.Join(dir, "$IAAAA")
	body := filepath.Join(dir, "$RAAAA")
	for _, p := range []string{meta, body} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := Entry{Original: "x", metaPath: meta, bodyPath: body}
	if err := removeEntry(e); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	for _, p := range []string{meta, body} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s 应该已不存在", p)
		}
	}
}

// TestRemoveEntryToleratesMissing 缺文件不该算失败（用户可能已经手动清过）。
func TestRemoveEntryToleratesMissing(t *testing.T) {
	dir := t.TempDir()
	e := Entry{
		Original: "x",
		metaPath: filepath.Join(dir, "$Inone"),
		bodyPath: filepath.Join(dir, "$Rnone"),
	}
	if err := removeEntry(e); err != nil {
		t.Errorf("文件不存在不该报错，实际: %v", err)
	}
}
