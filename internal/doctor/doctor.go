// Package doctor 提供机制自检。
//
// 本工具依赖客户端一个未公开的配置加载行为。该行为可能在任何版本中被
// 修改或移除，而失效的表现是"静默回落"——不报错，只是继续用自带配置连
// 原后端。用户会以为切换成功了，实际没有。
//
// 因此 doctor 的核心职责是：主动到安装包里确认这个机制是否还在。
package doctor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/product"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Level 是单项检查的结论级别。
type Level int

const (
	// OK 通过。
	OK Level = iota
	// Warn 可用但存在隐患。
	Warn
	// Fail 不可用。
	Fail
	// Info 仅作展示，不参与判定。
	Info
)

func (l Level) String() string {
	switch l {
	case OK:
		return "OK"
	case Warn:
		return "警告"
	case Fail:
		return "失败"
	default:
		return "信息"
	}
}

// Check 是一项检查的结论。
type Check struct {
	Name   string
	Level  Level
	Detail string
	// Hint 在非 OK 时给出可操作的建议。
	Hint string
}

// Options 控制自检范围。
type Options struct {
	// HostExe 为用户显式指定的宿主主程序路径，可为空。
	HostExe string
	// HostVariant 是宿主安装所属的档位。
	HostVariant variant.ID
	// Target 是准备切换到的后端。
	Target variant.ID
	// CacheDir 是生成配置的存放目录。
	CacheDir string
	// SkipAsar 跳过耗时的安装包扫描（用于快速模式）。
	SkipAsar bool

	Probe *variant.Probe
}

// Report 是一次自检的完整结果。
type Report struct {
	Host   variant.Install
	Target variant.Backend
	Checks []Check

	AsarPath string
	AsarSize int64
	// AsarHits 记录每个机制标记是否在安装包中找到。
	AsarHits map[string]bool
	AsarErr  error
}

// mechanismTokens 是客户端实现配置覆盖所必需的代码标记。
// 全部命中才认为机制可用。
var mechanismTokens = []string{
	// 读取覆盖文件路径的环境变量名。
	"ACC_PRODUCT_CONFIG_PATH",
	// 与前者互斥的内联变量。它的存在说明这套双通道机制仍然成立。
	"MUTUALLY_EXCLUSIVE_ENV_GROUPS",
}

// Failed 表示存在阻断性问题。
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Level == Fail {
			return true
		}
	}
	return false
}

// Warnings 统计警告数量。
func (r Report) Warnings() int {
	n := 0
	for _, c := range r.Checks {
		if c.Level == Warn {
			n++
		}
	}
	return n
}

// Run 执行自检。
func Run(opts Options) Report {
	probe := opts.Probe
	if probe == nil {
		probe = variant.DefaultProbe()
	}

	target, err := variant.Get(opts.Target)
	if err != nil {
		return Report{Checks: []Check{{
			Name: "目标后端", Level: Fail, Detail: err.Error(),
		}}}
	}

	rep := Report{Target: target}

	host, err := variant.Get(opts.HostVariant)
	if err != nil {
		rep.Checks = append(rep.Checks, Check{
			Name: "宿主档位", Level: Fail, Detail: err.Error(),
		})
		return rep
	}

	// 1. 定位宿主安装
	inst := probe.Detect(opts.HostVariant, opts.HostExe)
	rep.Host = inst

	if !inst.Found {
		rep.Checks = append(rep.Checks, Check{
			Name:   "定位宿主安装",
			Level:  Fail,
			Detail: strings.Join(inst.Problems, "; "),
			Hint:   "用 `wbmux config set-host --exe <主程序绝对路径>` 显式指定",
		})
		return rep
	}
	rep.Checks = append(rep.Checks, Check{
		Name:   "定位宿主安装",
		Level:  OK,
		Detail: fmt.Sprintf("%s（来源：%s）", inst.Executable, inst.Source),
	})

	// 2. 宿主版本
	if inst.Version != "" {
		rep.Checks = append(rep.Checks, Check{
			Name:   "宿主版本",
			Level:  Info,
			Detail: fmt.Sprintf("%s (build %s)", inst.Version, shortBuild(inst.Build)),
		})
	}

	// 3. 自带产品配置
	rep.Checks = append(rep.Checks, checkHostProduct(inst, host))

	// 4. 数据目录
	rep.Checks = append(rep.Checks, checkDataDir(inst))

	// 5. 生成配置的存放目录
	rep.Checks = append(rep.Checks, checkCacheDir(opts.CacheDir))

	// 6. 机制自检（核心）
	rep.Checks = append(rep.Checks, checkMechanism(probe, inst, &rep, opts.SkipAsar))

	// 7. 两套后端描述符的自洽性
	rep.Checks = append(rep.Checks, checkDescriptors(host, target))

	return rep
}

// checkHostProduct 校验宿主自带配置可读，并核对它自称的档位。
func checkHostProduct(inst variant.Install, host variant.Backend) Check {
	if inst.ProductJSON == "" {
		return Check{Name: "自带产品配置", Level: Fail, Detail: "路径未知"}
	}
	doc, err := product.Load(inst.ProductJSON)
	if err != nil {
		return Check{
			Name:   "自带产品配置",
			Level:  Fail,
			Detail: err.Error(),
			Hint:   "安装可能不完整，建议用客户端自带的修复程序重装",
		}
	}

	got, _ := doc["dataFolderName"].(string)
	gotEndpoint, _ := doc["endpoint"].(string)

	detail := fmt.Sprintf("%d 个顶层字段，endpoint=%s", len(doc), gotEndpoint)

	// 宿主自称的数据目录与用户设定的档位不符，说明路径指错了。
	if got != "" && got != host.DataFolderName {
		return Check{
			Name:   "自带产品配置",
			Level:  Warn,
			Detail: detail + fmt.Sprintf("；但 dataFolderName=%s，与 %s 档位预期的 %s 不一致", got, host.ID, host.DataFolderName),
			Hint:   "宿主路径可能指向了另一套安装，请核对 --exe",
		}
	}
	return Check{Name: "自带产品配置", Level: OK, Detail: detail}
}

// checkDataDir 检查宿主数据目录是否就绪。
func checkDataDir(inst variant.Install) Check {
	if inst.DataDir == "" {
		return Check{Name: "数据目录", Level: Fail, Detail: "无法推导"}
	}
	info, err := os.Stat(inst.DataDir)
	if err != nil {
		// 首次登录前不存在是正常的。
		return Check{
			Name:   "数据目录",
			Level:  Warn,
			Detail: inst.DataDir + "（尚不存在）",
			Hint:   "客户端首次登录后会自动创建，可忽略",
		}
	}
	if !info.IsDir() {
		return Check{Name: "数据目录", Level: Fail, Detail: inst.DataDir + " 不是目录"}
	}
	if !isWritable(inst.DataDir) {
		return Check{
			Name:   "数据目录",
			Level:  Fail,
			Detail: inst.DataDir + " 不可写",
			Hint:   "检查目录权限，或关闭正在运行的客户端",
		}
	}
	return Check{Name: "数据目录", Level: OK, Detail: inst.DataDir}
}

// checkCacheDir 检查生成配置的落盘位置。
func checkCacheDir(dir string) Check {
	if dir == "" {
		return Check{Name: "生成配置目录", Level: Fail, Detail: "未指定"}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Check{
			Name:   "生成配置目录",
			Level:  Fail,
			Detail: err.Error(),
			Hint:   "检查目录权限",
		}
	}
	if !isWritable(dir) {
		return Check{
			Name:   "生成配置目录",
			Level:  Fail,
			Detail: dir + " 不可写",
		}
	}
	return Check{Name: "生成配置目录", Level: OK, Detail: dir}
}

// checkMechanism 是本工具的核心检查：到安装包里确认配置覆盖机制仍然存在。
func checkMechanism(probe *variant.Probe, inst variant.Install, rep *Report, skip bool) Check {
	if skip {
		return Check{
			Name:   "配置覆盖机制",
			Level:  Warn,
			Detail: "已跳过（--fast）",
			Hint:   "去掉 --fast 可执行完整检查",
		}
	}
	if inst.ResourcesDir == "" {
		return Check{Name: "配置覆盖机制", Level: Fail, Detail: "无法定位 resources 目录"}
	}

	asar := filepath.Join(inst.ResourcesDir, filepath.FromSlash(variant.AsarRel))
	rep.AsarPath = asar

	info, err := os.Stat(asar)
	if err != nil {
		rep.AsarErr = err
		return Check{
			Name:   "配置覆盖机制",
			Level:  Fail,
			Detail: "无法读取 " + asar,
			Hint:   "安装可能不完整",
		}
	}
	rep.AsarSize = info.Size()

	hits, err := ScanTokens(asar, mechanismTokens)
	rep.AsarHits = hits
	rep.AsarErr = err
	if err != nil {
		return Check{
			Name:   "配置覆盖机制",
			Level:  Fail,
			Detail: "扫描安装包失败: " + err.Error(),
		}
	}

	var missing []string
	for _, t := range mechanismTokens {
		if !hits[t] {
			missing = append(missing, t)
		}
	}

	if len(missing) == 0 {
		return Check{
			Name:   "配置覆盖机制",
			Level:  OK,
			Detail: fmt.Sprintf("在 %s 中确认 %d 项标记齐备", filepath.Base(asar), len(mechanismTokens)),
		}
	}

	return Check{
		Name:  "配置覆盖机制",
		Level: Fail,
		Detail: fmt.Sprintf("安装包中找不到 %s —— 官方可能已改版，覆盖机制大概率失效",
			strings.Join(missing, "、")),
		Hint: "升级 wbmux 或改用账号切换方案；继续启动会静默回落，仍连原后端",
	}
}

// checkDescriptors 确认两套后端描述符确实互不相同，
// 否则"切换"只是换了个名字。
func checkDescriptors(host, target variant.Backend) Check {
	if host.ID == target.ID {
		return Check{
			Name:   "后端描述符",
			Level:  Warn,
			Detail: fmt.Sprintf("宿主与目标同为 %s，无需切换", host.ID),
		}
	}
	if host.Endpoint == target.Endpoint {
		return Check{
			Name:   "后端描述符",
			Level:  Fail,
			Detail: "两套后端的 endpoint 相同",
		}
	}
	if host.DataFolderName == target.DataFolderName {
		return Check{
			Name:   "后端描述符",
			Level:  Fail,
			Detail: "两套后端的数据目录相同，切换会污染同一份数据",
		}
	}
	return Check{
		Name:   "后端描述符",
		Level:  OK,
		Detail: fmt.Sprintf("%s → %s（%s）", host.Endpoint, target.Endpoint, target.DisplayName),
	}
}

// ScanTokens 在文件里流式查找一组标记。
//
// 安装包有数百 MB，不能整体读入内存。分块扫描，块间保留
// (最长标记长度 - 1) 字节的重叠，避免标记恰好跨块而被漏掉。
func ScanTokens(path string, tokens []string) (map[string]bool, error) {
	hits := make(map[string]bool, len(tokens))
	maxLen := 0
	for _, t := range tokens {
		hits[t] = false
		if len(t) > maxLen {
			maxLen = len(t)
		}
	}
	if maxLen == 0 {
		return hits, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	const chunkSize = 4 << 20
	buf := make([]byte, chunkSize+maxLen)
	carry := 0

	for {
		n, readErr := f.Read(buf[carry:])
		total := carry + n

		if total > 0 {
			window := buf[:total]
			for _, t := range tokens {
				if hits[t] {
					continue
				}
				if bytes.Contains(window, []byte(t)) {
					hits[t] = true
				}
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return hits, readErr
		}
		if n == 0 {
			break
		}

		// 把末尾 maxLen-1 字节挪到块首，作为下一轮的重叠区。
		keep := maxLen - 1
		if keep > total {
			keep = total
		}
		copy(buf[:keep], buf[total-keep:total])
		carry = keep

		// 全部命中就没必要继续扫了。
		if allTrue(hits) {
			break
		}
	}
	return hits, nil
}

func allTrue(m map[string]bool) bool {
	for _, v := range m {
		if !v {
			return false
		}
	}
	return true
}

func isWritable(dir string) bool {
	probe := filepath.Join(dir, ".wbmux-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(probe)
	return true
}

func shortBuild(b string) string {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}
