package variant

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Install 描述本机一处客户端安装的探测结果。
type Install struct {
	Variant ID
	// Found 为 false 时，其余字段可能为空，Problems 说明原因。
	Found bool
	// Source 记录是哪条策略定位到的，便于 doctor 输出与排错。
	Source string

	Executable   string
	InstallDir   string
	ResourcesDir string
	ProductJSON  string
	DataDir      string

	// Version / Build 来自数据目录的 last-launch.json，可能为空。
	Version string
	Build   string

	Problems []string
}

// ProductJSONRel 是 product.json 在 resources 下的相对路径。
const ProductJSONRel = "app.asar.unpacked/cli/product.json"

// AsarRel 是主程序包在 resources 下的相对路径。
const AsarRel = "app.asar"

// Probe 承载探测所需的环境访问，便于在测试里替换。
type Probe struct {
	GOOS     string
	Home     string
	Getenv   func(string) string
	Exists   func(string) bool
	ReadFile func(string) ([]byte, error)
	// Registry 返回注册表里登记的已安装程序。
	// 为 nil 时跳过注册表探测；测试里注入固定数据即可脱离真实注册表。
	Registry func() []RegistryEntry
}

// DefaultProbe 返回绑定真实环境的探测器。
func DefaultProbe() *Probe {
	home, _ := os.UserHomeDir()
	return &Probe{
		GOOS:     runtime.GOOS,
		Home:     home,
		Getenv:   os.Getenv,
		Exists:   exists,
		ReadFile: os.ReadFile,
		Registry: registryInstallsCached,
	}
}

func exists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// DataDir 返回该档位的数据目录绝对路径。
func (p *Probe) DataDir(id ID) string {
	b, err := Get(id)
	if err != nil {
		return ""
	}
	return filepath.Join(p.Home, b.DataFolderName)
}

// Detect 按优先级依次尝试多种策略定位安装。
// explicitExe 非空时优先采用（来自用户配置或命令行）。
func (p *Probe) Detect(id ID, explicitExe string) Install {
	b, err := Get(id)
	if err != nil {
		return Install{Variant: id, Problems: []string{err.Error()}}
	}

	inst := Install{Variant: id, DataDir: p.DataDir(id)}
	p.readLaunchInfo(&inst)

	// 显式路径是权威的，必须单独处理：不存在就直接失败，
	// 绝不能悄悄退回自动探测——用户明确指定了路径，却启动了另一处安装，
	// 是最坏的一类失败（会连到非预期的后端）。
	if explicitExe != "" {
		if !p.Exists(explicitExe) {
			inst.Problems = append(inst.Problems, "指定的主程序不存在: "+explicitExe)
			return inst
		}
		if other, ok := exeBelongsToOther(id, explicitExe); ok {
			inst.Problems = append(inst.Problems, fmt.Sprintf(
				"指定的主程序属于%s，与当前档位（%s）不符", other.DisplayName, b.DisplayName))
			return inst
		}
		p.fill(&inst, b, explicitExe, "显式配置")
		return inst
	}

	// 其余策略按可靠性排序，逐个惰性求值：
	// 前一条命中就不必再查后面的，注册表枚举这种开销较大的动作因此可以省掉。
	resolvers := []struct {
		name string
		find func() []string
	}{
		{"数据目录线索", func() []string { return one(p.fromDataDirHint(id)) }},
		{"注册表", func() []string {
			if p.Registry == nil {
				return nil
			}
			return registryCandidates(p.Registry(), b)
		}},
		{"常见安装位置", func() []string { return p.commonDirCandidates(b) }},
		{"PATH", func() []string { return one(p.fromPathLookup(b)) }},
	}

	for _, r := range resolvers {
		for _, exe := range r.find() {
			if exe == "" || !p.Exists(exe) {
				continue
			}
			p.fill(&inst, b, exe, r.name)
			return inst
		}
	}

	inst.Problems = append(inst.Problems,
		"未找到该档位的安装，可用 `wbmux config set --exe <主程序绝对路径>` 指定")
	return inst
}

// exeBelongsToOther 判断某个可执行文件是否明显属于另一个档位。
//
// 用于在用户传了 --exe 却没说档位时挡住张冠李戴。只认文件名完全一致的
// 情况：被重命名过的副本不做判断，交给用户用 --host 明确指定。
func exeBelongsToOther(id ID, path string) (Backend, bool) {
	base := strings.ToLower(filepath.Base(path))
	for _, b := range All() {
		if b.ID == id {
			continue
		}
		for _, name := range []string{b.WinExecutableName + ".exe", b.LinuxExecutableName} {
			if name == ".exe" || name == "" {
				continue
			}
			if base == strings.ToLower(name) {
				return b, true
			}
		}
	}
	return Backend{}, false
}

// one 把可能为空的单个候选包装成列表，便于统一处理。
func one(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// fill 补全一处已确认存在的安装信息，并校验关键文件。
func (p *Probe) fill(inst *Install, b Backend, exe, source string) {
	inst.Found = true
	inst.Source = source
	inst.Executable = exe
	inst.InstallDir = filepath.Dir(exe)
	inst.ResourcesDir = filepath.Join(inst.InstallDir, "resources")
	inst.ProductJSON = filepath.Join(inst.ResourcesDir, filepath.FromSlash(ProductJSONRel))

	if !p.Exists(inst.ResourcesDir) {
		inst.Problems = append(inst.Problems, "缺少 resources 目录，可能不是完整安装")
	}
	if !p.Exists(inst.ProductJSON) {
		inst.Problems = append(inst.Problems,
			"缺少 "+ProductJSONRel+"，无法读取自带产品配置")
	}
	if !p.Exists(filepath.Join(inst.ResourcesDir, filepath.FromSlash(AsarRel))) {
		inst.Problems = append(inst.Problems,
			"缺少 "+AsarRel+"，无法执行机制自检")
	}
	if inst.DataDir != "" && !p.Exists(inst.DataDir) {
		inst.Problems = append(inst.Problems,
			"数据目录尚不存在（首次登录后才会创建）: "+inst.DataDir)
	}
}

// fromDataDirHint 从数据目录的 settings.json 里挖主程序路径。
//
// 客户端在把本程序注册为 Office 文件关联时，会写下一条形如
//
//	v3:win32:5.6.2:D:\Tools\WorkBuddy\WorkBuddy.exe:WorkBuddy:aac,avif,...
//
// 的标记。其中第 4 段就是主程序绝对路径。这是本机唯一一处由客户端自己
// 记录安装位置的地方，因此优先级仅次于用户显式指定。
func (p *Probe) fromDataDirHint(id ID) string {
	dir := p.DataDir(id)
	if dir == "" {
		return ""
	}
	raw, err := p.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return ""
	}
	var doc struct {
		Marker string `json:"officeFileAssociationsRepairMarker"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Marker == "" {
		return ""
	}
	parts := strings.Split(doc.Marker, ":")
	if len(parts) < 4 {
		return ""
	}
	// 路径本身可能含盘符冒号，因此只认第 4 段及其后到可执行文件名结束的部分。
	candidate := parts[3]
	if !strings.HasSuffix(strings.ToLower(candidate), ".exe") {
		// 兼容盘符被切走的情况：把后续段拼回来直到遇到 .exe
		rebuilt := candidate
		for _, seg := range parts[4:] {
			rebuilt += ":" + seg
			if strings.HasSuffix(strings.ToLower(seg), ".exe") {
				candidate = rebuilt
				break
			}
		}
	}
	if !strings.HasSuffix(strings.ToLower(candidate), ".exe") {
		return ""
	}
	return candidate
}

// commonDirCandidates 展开常见安装目录下的主程序候选路径。
func (p *Probe) commonDirCandidates(b Backend) []string {
	dirs := p.commonDirs(b)
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		out = append(out, p.exeInDir(b, dir))
	}
	return dedupe(out)
}

// commonDirs 返回各平台上的常见安装目录候选。
func (p *Probe) commonDirs(b Backend) []string {
	switch p.GOOS {
	case "windows":
		var dirs []string
		for _, base := range []string{
			p.Getenv("LOCALAPPDATA") + `\Programs`,
			p.Getenv("LOCALAPPDATA"),
			p.Getenv("ProgramFiles"),
			p.Getenv("ProgramFiles(x86)"),
			p.Getenv("ProgramW6432"),
		} {
			if base == "" {
				continue
			}
			dirs = append(dirs,
				filepath.Join(base, b.ProductName),
				filepath.Join(base, b.WinExecutableName),
			)
		}
		// 盘根布局：实测存在直接装在盘根的情况（如 D:\WorkBuddyAI），
		// 上面那些基于环境变量的位置覆盖不到。每盘符两次 stat，开销可忽略。
		// 注册表探测通常已经命中，这一层是给"免安装解压"这类
		// 没有登记卸载信息的情况兜底。
		for _, root := range p.windowsDriveRoots() {
			dirs = append(dirs,
				filepath.Join(root, b.WinExecutableName),
				filepath.Join(root, b.ProductName),
			)
		}
		return dedupe(dirs)
	case "darwin":
		return []string{
			filepath.Join("/Applications", b.MacAppName),
			filepath.Join(p.Home, "Applications", b.MacAppName),
		}
	default:
		return []string{
			filepath.Join("/opt", b.LinuxExecutableName),
			filepath.Join(p.Home, ".local", "share", b.LinuxExecutableName),
		}
	}
}

// windowsDriveRoots 返回本机存在的盘符根目录。
func (p *Probe) windowsDriveRoots() []string {
	var roots []string
	for c := 'C'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		if p.Exists(root) {
			roots = append(roots, root)
		}
	}
	return roots
}

// exeInDir 在给定安装目录里推导主程序路径；找不到返回空串。
func (p *Probe) exeInDir(b Backend, dir string) string {
	if dir == "" {
		return ""
	}
	var rel string
	switch p.GOOS {
	case "windows":
		rel = b.WinExecutableName + ".exe"
	case "darwin":
		// /Applications/WorkBuddy.app/Contents/MacOS/<可执行名>
		rel = filepath.Join("Contents", "MacOS", b.WinExecutableName)
	default:
		rel = b.LinuxExecutableName
	}
	return filepath.Join(dir, rel)
}

// fromPathLookup 在 PATH 里找可执行文件。
func (p *Probe) fromPathLookup(b Backend) string {
	names := []string{b.LinuxExecutableName}
	if p.GOOS == "windows" {
		names = []string{b.WinExecutableName + ".exe"}
	}
	for _, dir := range filepath.SplitList(p.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, n := range names {
			full := filepath.Join(dir, n)
			if p.Exists(full) {
				return full
			}
		}
	}
	return ""
}

// readLaunchInfo 从数据目录读取版本号与构建号，用于 doctor 展示。
func (p *Probe) readLaunchInfo(inst *Install) {
	if inst.DataDir == "" {
		return
	}
	raw, err := p.ReadFile(filepath.Join(inst.DataDir, "last-launch.json"))
	if err != nil {
		return
	}
	var doc struct {
		Version string `json:"version"`
		Build   string `json:"build"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	inst.Version = doc.Version
	inst.Build = doc.Build
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
