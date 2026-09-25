// Package runner 把"切换后端并启动客户端"的编排逻辑集中到一处。
//
// 命令行与图形界面走的是同一条路径：先生成补丁配置，回读校验，
// 再拼装启动计划。逻辑放在这里而不是各写一份，是为了避免两边行为漂移——
// 尤其是回读校验这一步，漏掉就会静默连错后端，而失败表现是"看起来切换成功"。
package runner

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/launch"
	"github.com/HMuSeaB/wbmux/internal/product"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Options 描述一次切换准备的全部输入。
type Options struct {
	// Target 是要连接的后端。
	Target variant.ID

	// Native 为 true 时不做任何改写，直接用目标档位自己的安装启动。
	// 用途是留一条对照路径：覆盖模式异常时判断问题出在覆盖机制还是客户端本身。
	Native bool

	// HostFlag / ExeFlag 是命令行或设置文件给出的宿主选择，可为空。
	HostFlag string
	ExeFlag  string

	// ExtraEndpoints 追加进 officialEndpoints。
	ExtraEndpoints []string

	// ParentEnv 是父进程环境变量，通常传 os.Environ()。
	ParentEnv []string

	// Probe 可注入，便于测试。
	Probe *variant.Probe
}

// Result 是一次准备的结果。
//
// 它同时承担两个职责：给界面展示（字段都是可序列化的），以及执行启动。
// 界面先展示再调用 Launch，用户能看到究竟改了哪些字段。
type Result struct {
	Target variant.Backend
	Host   variant.Backend

	Native bool

	Executable  string
	ConfigPath  string
	DataDir     string
	Changes     []product.Change
	Stripped    []string
	Warnings    []string
	CommandLine string

	plan launch.Plan
}

// Prepare 生成配置并拼装启动计划，但不启动。
func Prepare(opts Options) (Result, error) {
	target, err := variant.Get(opts.Target)
	if err != nil {
		return Result{}, err
	}

	probe := opts.Probe
	if probe == nil {
		probe = variant.DefaultProbe()
	}

	if opts.Native {
		return prepareNative(target, probe, opts)
	}
	return prepareOverride(target, probe, opts)
}

// prepareNative 用目标档位自己的安装原样启动。
func prepareNative(target variant.Backend, probe *variant.Probe, opts Options) (Result, error) {
	inst := probe.Detect(target.ID, "")
	if !inst.Found {
		return Result{}, variant.NotFoundError(target.ID, inst)
	}

	// 即便不注入配置，也把残留的覆盖变量剥掉，
	// 否则用户环境里的旧值会让"原生"不再原生。
	env, stripped := launch.CleanEnv(opts.ParentEnv)

	return Result{
		Target:      target,
		Host:        target,
		Native:      true,
		Executable:  inst.Executable,
		DataDir:     inst.DataDir,
		Stripped:    stripped,
		CommandLine: inst.Executable,
		plan: launch.Plan{
			Variant:    target.ID,
			Executable: inst.Executable,
			DataDir:    inst.DataDir,
			Env:        env,
			Stripped:   stripped,
		},
	}, nil
}

// prepareOverride 是主路径：用宿主安装启动，但把后端指向 target。
func prepareOverride(target variant.Backend, probe *variant.Probe, opts Options) (Result, error) {
	cfg, err := config.Load()
	if err != nil {
		return Result{}, err
	}

	hostFlag, exeFlag := opts.HostFlag, opts.ExeFlag
	if exeFlag == "" {
		exeFlag = cfg.HostExe
	}
	if hostFlag == "" {
		hostFlag = cfg.HostVariant
	}

	hostID, hostInst, err := variant.Resolve(probe, hostFlag, exeFlag)
	if err != nil {
		return Result{}, err
	}
	host, err := variant.Get(hostID)
	if err != nil {
		return Result{}, err
	}

	cacheDir, err := config.CacheDir()
	if err != nil {
		return Result{}, err
	}
	outPath := filepath.Join(cacheDir, fmt.Sprintf("%s-to-%s.json", hostID, target.ID))

	patchOpts := product.Options{
		ExtraEndpoints: append(append([]string{}, cfg.ExtraEndpoints...), opts.ExtraEndpoints...),
	}
	changes, err := product.Generate(hostInst.ProductJSON, outPath, target, patchOpts)
	if err != nil {
		return Result{}, fmt.Errorf("生成产品配置失败: %w", err)
	}

	// 回读校验：写出去的配置必须真的指向目标后端。
	// 这一步几乎不花时间，却能挡住"补丁字段名写错"这类静默失败——
	// 那种情况下客户端会照常启动，只是继续连原后端。
	if err := verifyEndpoint(outPath, target); err != nil {
		return Result{}, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Result{}, fmt.Errorf("无法确定用户主目录: %w", err)
	}

	plan, err := launch.Build(launch.Options{
		Install:    hostInst,
		Target:     target,
		ConfigPath: outPath,
		DataDir:    filepath.Join(home, target.DataFolderName),
		ParentEnv:  opts.ParentEnv,
	})
	if err != nil {
		return Result{}, err
	}

	var warnings []string
	if hostID == target.ID {
		warnings = append(warnings, "目标后端与宿主档位相同，本次不会改变任何字段")
	}

	return Result{
		Target:      target,
		Host:        host,
		Executable:  plan.Executable,
		ConfigPath:  plan.ConfigPath,
		DataDir:     plan.DataDir,
		Changes:     changes,
		Stripped:    plan.Stripped,
		Warnings:    warnings,
		CommandLine: plan.CommandLine(),
		plan:        plan,
	}, nil
}

// Launch 执行启动计划。
func (r Result) Launch() error {
	return r.plan.Run()
}

// verifyEndpoint 回读生成配置，确认 endpoint 已指向目标后端。
func verifyEndpoint(path string, target variant.Backend) error {
	doc, err := product.Load(path)
	if err != nil {
		return fmt.Errorf("回读生成的配置失败: %w", err)
	}
	got, _ := doc["endpoint"].(string)
	if got != target.Endpoint {
		return fmt.Errorf("生成的配置校验失败：endpoint 为 %q，期望 %q", got, target.Endpoint)
	}
	return nil
}
