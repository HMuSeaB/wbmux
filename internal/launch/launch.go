// Package launch 负责按目标后端拼装并执行启动命令。
//
// 它只做两件事：把产品配置路径塞进环境变量，把数据目录作为参数传给客户端。
// 不修改安装目录里的任何文件。
package launch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// ConfigPathEnv 是客户端读取产品配置路径所用的环境变量。
const ConfigPathEnv = "ACC_PRODUCT_CONFIG_PATH"

// conflictingEnvs 与 ConfigPathEnv 互斥。
//
// 客户端源码里有一行显式声明：
//
//	MUTUALLY_EXCLUSIVE_ENV_GROUPS = [["ACC_PRODUCT_CONFIG_PATH", "ACC_PRODUCT_CONFIG_V3"]]
//
// 且解析逻辑为"先看 PATH，有值就完全忽略内联变量"。
// 但为了不依赖这个细节，我们主动把内联变量从子进程环境里剥掉，
// 免得残留值在将来某个版本里反过来压过我们的设置。
var conflictingEnvs = []string{
	"ACC_PRODUCT_CONFIG_V3",
	"ACC_PRODUCT_CONFIG_V2",
	"ACC_PRODUCT_CONFIG",
}

// UserDataDirFlag 是传给 Electron 的数据目录参数。
const UserDataDirFlag = "--user-data-dir"

// Plan 描述一次即将执行的启动，可先打印再执行。
type Plan struct {
	Variant    variant.ID
	Executable string
	ConfigPath string
	DataDir    string

	// Env 是完整的子进程环境变量列表。
	Env []string
	// Args 是不含可执行文件本身参数列表。
	Args []string
	// Stripped 记录被剥掉的变量名，便于诊断。
	Stripped []string
}

// Options 描述一次启动的全部输入。
type Options struct {
	// Install 提供要运行的主程序。
	Install variant.Install
	// Target 提供要连接的后端。
	Target variant.Backend
	// ConfigPath 是已生成好的产品配置文件路径。
	ConfigPath string

	// DataDir 是客户端应当使用的数据目录。
	//
	// 这里必须给**目标档位**的数据目录，而不是宿主的：两套后端的账号体系
	// 互不相通，让它们共用同一份 profile 会在来回切换时互相冲掉登录态。
	// 留空时按目标档位推导为 <主目录>/<DataFolderName>。
	DataDir string

	// ParentEnv 是父进程环境变量，通常传 os.Environ()。
	ParentEnv []string
}

// Build 依据宿主安装与目标后端拼装启动计划。
func Build(opts Options) (Plan, error) {
	inst, target := opts.Install, opts.Target

	if !inst.Found {
		return Plan{}, fmt.Errorf("宿主安装不可用: %s", strings.Join(inst.Problems, "; "))
	}
	if inst.Executable == "" {
		return Plan{}, fmt.Errorf("宿主安装缺少主程序路径")
	}
	if opts.ConfigPath == "" {
		return Plan{}, fmt.Errorf("缺少生成的产品配置文件路径")
	}
	if _, err := os.Stat(opts.ConfigPath); err != nil {
		return Plan{}, fmt.Errorf("生成的产品配置不存在: %w", err)
	}

	dataDir := opts.DataDir
	if dataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, target.DataFolderName)
		} else {
			dataDir = filepath.Join(filepath.Dir(inst.Executable), target.DataFolderName)
		}
	}

	env, stripped := FilteredEnv(opts.ParentEnv, opts.ConfigPath)

	return Plan{
		Variant:    target.ID,
		Executable: inst.Executable,
		ConfigPath: opts.ConfigPath,
		DataDir:    dataDir,
		Env:        env,
		Args:       []string{UserDataDirFlag + "=" + dataDir},
		Stripped:   stripped,
	}, nil
}

// CleanEnv 剥离会干扰后端选择的互斥变量，但不注入新值。
//
// 供 --native 模式使用：让客户端完全按它自己的默认配置启动，
// 同时避免父进程里残留的覆盖变量意外改变行为。
func CleanEnv(parent []string) ([]string, []string) {
	return stripEnv(parent, "")
}

// FilteredEnv 在 parent 的基础上剥离互斥变量并注入配置路径。
func FilteredEnv(parent []string, configPath string) ([]string, []string) {
	return stripEnv(parent, configPath)
}

// stripEnv 是 CleanEnv 与 FilteredEnv 的共同实现。
//
// 大小写不敏感：Windows 环境变量名不区分大小写，而 Go 的 os.Environ
// 保留原始大小写，因此不能直接做字符串比较。
func stripEnv(parent []string, configPath string) ([]string, []string) {
	drop := make(map[string]bool, len(conflictingEnvs)+1)
	for _, k := range conflictingEnvs {
		drop[strings.ToUpper(k)] = true
	}
	drop[strings.ToUpper(ConfigPathEnv)] = true

	out := make([]string, 0, len(parent)+1)
	var stripped []string

	for _, entry := range parent {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if drop[strings.ToUpper(key)] {
			stripped = append(stripped, key)
			continue
		}
		out = append(out, entry)
	}

	if configPath != "" {
		out = append(out, ConfigPathEnv+"="+configPath)
	}
	sort.Strings(stripped)
	return out, stripped
}

// CommandLine 返回便于展示与复制的命令行文本。
func (p Plan) CommandLine() string {
	parts := append([]string{quote(p.Executable)}, quoteAll(p.Args)...)
	return strings.Join(parts, " ")
}

// Run 启动子进程并立即返回，不等待其结束。
//
// 客户端是常驻 GUI，父进程退出不应带走它，因此使用平台相关的
// 分离标志启动（见 detach_*.go）。
func (p Plan) Run() error {
	cmd := exec.Command(p.Executable, p.Args...)
	cmd.Env = p.Env
	cmd.Dir = filepath.Dir(p.Executable)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	detach(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动失败: %w", err)
	}
	// 不等子进程，立即释放句柄，避免留下僵尸进程。
	go func() { _ = cmd.Wait() }()
	return nil
}

func quote(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\"") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, quote(s))
	}
	return out
}
