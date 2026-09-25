package migrate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 本文件负责"内容类"配置与附件：
//
//   - KindContent：身份文件、设置、MCP、插件、任务等散落条目
//   - KindAssets：会话里实际引用到的图片等附件
//
// 附件这一项有个不显然的前提，见 pathRewrites 的说明——只复制图片是不够的。

// KindContent 是配置型文件与目录。
const KindContent Kind = "content"

// KindAssets 是会话引用到的附件。
const KindAssets Kind = "assets"

// contentFiles 按名字搬运的松散文件。
//
// 用白名单而不是"整个数据目录搬过去"：数据目录里混着大量设备本地状态与
// 缓存（keyblob / device-id / edge-sync-mapping*.db / binaries / logs / traces），
// 那些要么搬了没用，要么会出事。白名单是唯一能把"内容"和"设备"分开的办法。
var contentFiles = []struct{ name, note string }{
	{"SOUL.md", "身份"},
	{"IDENTITY.md", "身份"},
	{"USER.md", "身份"},
	{"MEMORY.md", "记忆"},
	{"settings.json", "设置"},
	{"models.json", "模型清单"},
	{"user-state.json", "用户状态"},
	{"workspace-state.json", "工作区状态"},
	{"mcp-approvals.json", "MCP 审批"},
	{"mcp-tool-list.json", "MCP 工具清单"},
}

// contentDirs 按目录搬运的内容。
var contentDirs = []struct{ name, note string }{
	{"connectors", "连接器"},
	{"connectors-marketplace", "连接器市场"},
	{"plugins", "插件"},
	{"plugin-account-state", "插件账号状态"},
	{"plugin-marketplace-state-new", "插件市场状态"},
	{"tasks", "任务"},
	{"plans", "计划"},
	{"file-history", "文件历史"},
	{"artifact-index", "产物索引"},
	{"changes-index", "改动索引"},
	{"changes-detail", "改动明细"},
}

// planContent 规划配置型文件与目录的搬运。
func planContent(srcDir, dstDir string) ([]workItem, []string) {
	var items []workItem
	var warns []string

	for _, f := range contentFiles {
		src := filepath.Join(srcDir, f.name)
		dst := filepath.Join(dstDir, f.name)
		if !fileExists(src) {
			continue
		}
		it := workItem{
			kind:  KindContent,
			id:    f.name,
			title: f.name,
			note:  f.note,
			size:  fileSize(src),
			files: []fileCopy{{src: src, dst: dst}},
		}
		if fileExists(dst) {
			// 两端都有这个文件时一律跳过，不覆盖。这些文件（尤其 settings.json
			// 与 user-state.json）是各端运行时自己写的，直接盖掉可能把目标端
			// 的可用状态换成来源端的，而来源端的不一定适用。
			it.skip = true
			it.skipWhy = "目标端已有同名文件，不覆盖"
		}
		items = append(items, it)
	}

	for _, d := range contentDirs {
		src := filepath.Join(srcDir, d.name)
		dst := filepath.Join(dstDir, d.name)
		if !dirExists(src) {
			continue
		}
		it := workItem{
			kind:  KindContent,
			id:    d.name,
			title: d.name + "/",
			note:  d.note,
			size:  dirSize(src),
			trees: []treeCopy{{srcDir: src, dstDir: dst}},
		}
		// 目录不整项目跳过：目录内是逐文件判定的，已存在的跳过、缺的补上。
		// 这类目录（插件、任务、文件历史）本来就是增量积累的。
		items = append(items, it)
	}
	return items, warns
}

// assetRefRe 匹配会话文本里的附件绝对路径。
//
// 要同时认三种差异，漏任何一种都会静默漏掉一部分附件：
//
//   - 分隔符：客户端自己写反斜杠，而正文里的路径是模型生成的，出现过正斜杠
//   - 路径形态：Windows 是 `C:\…`，macOS/Linux 是 `/Users/…`
//   - 转义：JSON 字符串里是双反斜杠，正文里是单反斜杠（调用方会先压平）
//
// 字符类刻意排除了引号、反引号、尖括号、逗号、分号与括号——路径在文本里
// 往往紧跟标点，不排掉会把标点吞进文件名。
var assetRefRe = regexp.MustCompile(
	`(?i)(?:[a-z]:[\\/]|/)[^\s"'<>|?*()\[\]{},;]*?[\\/](?:blobs|clipboard-images)[\\/][^\s"'<>|?*()\[\]{},;]+`)

// collectAssetRefs 找出会话实际引用到的附件，返回"相对数据目录的路径 → 绝对路径"。
//
// 只收被引用到的：数据目录下的 blobs/ 可能堆了几百 MB 的历史垃圾，
// 全搬毫无意义，引用是唯一有意义的判据。
func collectAssetRefs(srcDataDir string, sessions map[string]sessionFiles) (map[string]string, error) {
	out := map[string]string{}

	// 一律转成正斜杠再比较：会话里两种分隔符都可能出现，
	// 而数据目录本身在 Windows 用 \、在 macOS/Linux 用 /，直接比会全错。
	root := strings.TrimRight(filepath.ToSlash(srcDataDir), "/")
	if root == "" {
		return out, nil
	}
	rootLower := strings.ToLower(root)

	for _, sf := range sessions {
		raw, err := os.ReadFile(sf.JSONL)
		if err != nil {
			// 单个文件读不动不该让整次扫描失败，跳过它继续。
			continue
		}
		// 压平转义：把 \\ 变成 \，这样一套正则可以同时命中两种形态。
		// 只是用来找路径，找到的位置另外处理，所以不会污染原文。
		text := strings.ReplaceAll(string(raw), `\\`, `\`)

		for _, m := range assetRefRe.FindAllString(text, -1) {
			norm := strings.ReplaceAll(m, `\`, "/")
			// 大小写不敏感地判断归属，但切的时候用原串的偏移，
			// 以保留路径真实的大小写。
			if !strings.HasPrefix(strings.ToLower(norm), rootLower+"/") {
				continue // 引用的不是本数据目录里的东西（比如别的盘）
			}
			rel := norm[len(root)+1:]
			if rel == "" {
				continue
			}
			abs := filepath.FromSlash(norm)
			if !fileExists(abs) {
				// 引用还在但文件已被清理，属正常情况，不报错也不收。
				continue
			}
			out[filepath.FromSlash(rel)] = abs
		}
	}
	return out, nil
}

// planAssets 规划附件的搬运。
func planAssets(srcDir, dstDir string, sessions map[string]sessionFiles) ([]workItem, []string) {
	refs, err := collectAssetRefs(srcDir, sessions)
	if err != nil {
		return nil, []string{"扫描会话引用的附件失败：" + err.Error()}
	}
	if len(refs) == 0 {
		return nil, nil
	}

	// 排序保证输出稳定（map 遍历顺序是随机的），否则每次扫描的清单顺序都不同。
	rels := make([]string, 0, len(refs))
	for rel := range refs {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	items := make([]workItem, 0, len(rels))
	for _, rel := range rels {
		src := refs[rel]
		dst := filepath.Join(dstDir, rel)
		it := workItem{
			kind:  KindAssets,
			id:    rel,
			title: filepath.Base(rel),
			note:  filepath.Dir(rel),
			size:  fileSize(src),
			files: []fileCopy{{src: src, dst: dst}},
		}
		if fileExists(dst) {
			it.skip = true
			it.skipWhy = "目标端已有同名附件"
		}
		items = append(items, it)
	}
	return items, nil
}

// pathRewrites 返回把会话里引用的附件路径从来源端数据目录改到目标端的替换表。
//
// # 为什么必须改写
//
// 会话里的图片存的是**绝对路径**，直指 `<来源数据目录>\blobs\…`。
// 所以只把图片文件复制到目标端是没用的——引用依然指向来源端，
// 只要用户卸载对方客户端或删掉那个目录，图就全裂了。
// 要让迁移真正独立，必须连路径前缀一起改。
//
// 代价是搬过去的 jsonl 不再是逐字节副本。这是有意的取舍：用户选了"完整"，
// 而"完整"和"字节一致"在这里不可兼得。
//
// # 为什么是三条规则
//
// 同一个路径在文件里可能以三种形态出现，漏掉任何一种都会有一半引用改不到：
//
//	C:\\Users\\x\\.workbuddy-ai\\blobs\\…    JSON 字符串里的转义形态
//	C:\Users\x\.workbuddy-ai\blobs\…        正文里的原样形态
//	C:/Users/x/.workbuddy-ai/blobs/…        正斜杠形态
func pathRewrites(srcDataDir, dstDataDir string) [][2]string {
	src := strings.TrimRight(srcDataDir, `\/`)
	dst := strings.TrimRight(dstDataDir, `\/`)
	if src == "" || dst == "" || src == dst {
		return nil
	}

	esc := func(s string) string { return strings.ReplaceAll(s, `\`, `\\`) }
	slash := func(s string) string { return strings.ReplaceAll(s, `\`, `/`) }

	return [][2]string{
		{esc(src) + `\\`, esc(dst) + `\\`},
		{src + `\`, dst + `\`},
		{slash(src) + `/`, slash(dst) + `/`},
	}
}

// copyFileWithRewrites 复制文件，并在写入前按 replacements 改写内容。
//
// 目标已存在时返回 errExists，绝不覆盖——与 copyFile 同一约束。
func copyFileWithRewrites(src, dst string, replacements [][2]string) (int, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	text := string(raw)

	changed := 0
	for _, r := range replacements {
		if r[0] == "" || r[0] == r[1] {
			continue
		}
		n := strings.Count(text, r[0])
		if n == 0 {
			continue
		}
		text = strings.ReplaceAll(text, r[0], r[1])
		changed += n
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return 0, errExists
		}
		return 0, err
	}
	if _, err := out.WriteString(text); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, err
	}
	return changed, nil
}
