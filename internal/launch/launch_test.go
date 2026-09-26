package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestFilteredEnvStripsConflictingVariables(t *testing.T) {
	parent := []string{
		"PATH=C:\\Windows",
		"ACC_PRODUCT_CONFIG_V3={\"endpoint\":\"stale\"}",
		"ACC_PRODUCT_CONFIG_V2=stale",
		"ACC_PRODUCT_CONFIG=stale",
		"ACC_PRODUCT_CONFIG_PATH=C:\\old\\path.json",
		InternetEnvEnv + "=stale",
		"HOME=C:\\Users\\tester",
	}

	env, stripped := FilteredEnv(parent, `C:\new\path.json`)

	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			continue
		}
		for _, banned := range conflictingEnvs {
			if strings.HasPrefix(e, banned+"=") {
				t.Errorf("互斥变量未被剥离: %s", e)
			}
		}
	}

	var found string
	for _, e := range env {
		if strings.HasPrefix(e, ConfigPathEnv+"=") {
			found = strings.TrimPrefix(e, ConfigPathEnv+"=")
		}
	}
	if found != `C:\new\path.json` {
		t.Errorf("注入的配置路径 = %q, 期望 C:\\new\\path.json", found)
	}

	// 互斥变量 + 旧的 PATH 变量，共 4 项应被剥离。
	if len(stripped) != len(conflictingEnvs)+1 {
		t.Errorf("剥离项数 = %d, 期望 %d: %v", len(stripped), len(conflictingEnvs)+1, stripped)
	}
}

// Windows 环境变量名不区分大小写，而 os.Environ 保留原始大小写。
func TestFilteredEnvIsCaseInsensitive(t *testing.T) {
	parent := []string{
		"acc_product_config_path=C:\\old.json",
		"Acc_Product_Config_V3=stale",
	}
	env, stripped := FilteredEnv(parent, `C:\new.json`)

	if len(stripped) != 2 {
		t.Errorf("应剥离 2 项, 实际 %v", stripped)
	}
	count := 0
	for _, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), strings.ToUpper(ConfigPathEnv)+"=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("配置路径变量应恰好出现一次, 实际 %d 次", count)
	}
}

func TestFilteredEnvPreservesUnrelatedVariables(t *testing.T) {
	parent := []string{"PATH=C:\\bin", "TEMP=C:\\Temp", "USERPROFILE=C:\\Users\\tester"}
	env, _ := FilteredEnv(parent, `C:\cfg.json`)

	for _, want := range parent {
		found := false
		for _, e := range env {
			if e == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("无关变量被丢弃: %s", want)
		}
	}
}

func TestFilteredEnvStrippedListIsSorted(t *testing.T) {
	parent := []string{
		"ACC_PRODUCT_CONFIG_V3=x",
		"ACC_PRODUCT_CONFIG=x",
		"ACC_PRODUCT_CONFIG_PATH=x",
	}
	_, stripped := FilteredEnv(parent, "new")
	for i := 1; i < len(stripped); i++ {
		if stripped[i-1] > stripped[i] {
			t.Errorf("剥离清单未排序: %v", stripped)
		}
	}
}

// CleanEnv 用于 --native：只剥离，不注入。
func TestCleanEnvStripsWithoutInjecting(t *testing.T) {
	parent := []string{
		"PATH=C:\\Windows",
		"ACC_PRODUCT_CONFIG_PATH=C:\\old\\path.json",
		"ACC_PRODUCT_CONFIG_V3=stale",
	}
	env, stripped := CleanEnv(parent)

	if len(stripped) != 2 {
		t.Errorf("应剥离 2 项, 实际 %v", stripped)
	}
	for _, e := range env {
		if strings.HasPrefix(strings.ToUpper(e), "ACC_PRODUCT_CONFIG") {
			t.Errorf("残留了覆盖变量: %s", e)
		}
	}
	if len(env) != 1 || env[0] != "PATH=C:\\Windows" {
		t.Errorf("Env = %v", env)
	}
}

func TestBuildRejectsUnavailableInstall(t *testing.T) {
	intl, _ := variant.Get(variant.Intl)
	_, err := Build(Options{
		Install:    variant.Install{Found: false, Problems: []string{"找不到主程序"}},
		Target:     intl,
		ConfigPath: "x.json",
	})
	if err == nil {
		t.Fatal("宿主不可用时应报错")
	}
	if !strings.Contains(err.Error(), "找不到主程序") {
		t.Errorf("错误信息应带上原始问题: %v", err)
	}
}

func TestBuildRejectsMissingConfig(t *testing.T) {
	dir := t.TempDir()
	intl, _ := variant.Get(variant.Intl)
	inst := variant.Install{
		Found:      true,
		Executable: filepath.Join(dir, "WorkBuddy.exe"),
		DataDir:    filepath.Join(dir, "data"),
	}
	_, err := Build(Options{
		Install:    inst,
		Target:     intl,
		ConfigPath: filepath.Join(dir, "absent.json"),
	})
	if err == nil {
		t.Fatal("生成配置不存在时应报错")
	}
}

func TestBuildProducesExpectedArgsAndEnv(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "generated.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	intl, _ := variant.Get(variant.Intl)
	dataDir := filepath.Join(dir, ".workbuddy-ai")
	inst := variant.Install{
		Found:      true,
		Executable: filepath.Join(dir, "WorkBuddy.exe"),
		DataDir:    filepath.Join(dir, ".workbuddy"),
	}

	plan, err := Build(Options{
		Install:    inst,
		Target:     intl,
		ConfigPath: cfg,
		DataDir:    dataDir,
		ParentEnv:  []string{"ACC_PRODUCT_CONFIG_V3=stale", "PATH=/usr/bin"},
	})
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}

	if plan.Variant != variant.Intl {
		t.Errorf("Variant = %q", plan.Variant)
	}
	if len(plan.Args) != 1 || plan.Args[0] != UserDataDirFlag+"="+dataDir {
		t.Errorf("Args = %v", plan.Args)
	}
	if plan.ConfigPath != cfg {
		t.Errorf("ConfigPath = %q", plan.ConfigPath)
	}
	if len(plan.Stripped) != 1 || plan.Stripped[0] != "ACC_PRODUCT_CONFIG_V3" {
		t.Errorf("Stripped = %v", plan.Stripped)
	}

	var injected bool
	for _, e := range plan.Env {
		if e == ConfigPathEnv+"="+cfg {
			injected = true
		}
	}
	if !injected {
		t.Error("环境变量未注入配置路径")
	}
}

// 数据目录必须跟随**目标**档位。
//
// 回归测试：早期实现沿用了宿主安装的数据目录，会让两套后端共用同一份
// profile，来回切换时互相冲掉登录态。
func TestBuildDefaultsToTargetDataDirNotHostDataDir(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "generated.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法确定主目录: %v", err)
	}

	intl, _ := variant.Get(variant.Intl)
	hostDataDir := filepath.Join(dir, ".workbuddy") // 宿主是国内版
	inst := variant.Install{
		Found:      true,
		Executable: filepath.Join(dir, "WorkBuddy.exe"),
		DataDir:    hostDataDir,
	}

	plan, err := Build(Options{Install: inst, Target: intl, ConfigPath: cfg})
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}

	want := filepath.Join(home, intl.DataFolderName)
	if plan.DataDir != want {
		t.Errorf("DataDir = %q, 期望目标档位的 %q", plan.DataDir, want)
	}
	if plan.DataDir == hostDataDir {
		t.Error("数据目录不应沿用宿主安装的")
	}
}

func TestCommandLineQuotesPathsWithSpaces(t *testing.T) {
	p := Plan{
		Executable: `C:\Program Files\WorkBuddy\WorkBuddy.exe`,
		Args:       []string{`--user-data-dir=C:\Users\a b\.workbuddy`},
	}
	got := p.CommandLine()
	if !strings.HasPrefix(got, `"C:\Program Files\WorkBuddy\WorkBuddy.exe"`) {
		t.Errorf("可执行文件路径未加引号: %s", got)
	}
	if !strings.Contains(got, `"--user-data-dir=C:\Users\a b\.workbuddy"`) {
		t.Errorf("含空格参数未加引号: %s", got)
	}
}

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"":            `""`,
		"plain":       "plain",
		"with space":  `"with space"`,
		`a"b`:         `"a\"b"`,
		"tab\there":   "\"tab\there\"",
		`C:\Users\x`:  `C:\Users\x`,
		"two  spaces": `"two  spaces"`,
	}
	for in, want := range cases {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %q, 期望 %q", in, got, want)
		}
	}
}

// TestBuildInjectsInternetEnvironment 钉住网络环境注入。
//
// 5.6 的客户端按登录会话推导网络环境并发布 ACC_PRODUCT_CONFIG_V3，
// 推导值会压过 ACC_PRODUCT_CONFIG_PATH 的文件覆盖（实测静默回落）。
// 修复 = 预设官方开关变量，且有 !process.env 守卫保证预设值优先。
func TestBuildInjectsInternetEnvironment(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		id   variant.ID
		want string
	}{
		{variant.Intl, "external"},
		{variant.CN, "internal"},
	} {
		target, _ := variant.Get(c.id)
		inst := variant.Install{
			Found:      true,
			Executable: filepath.Join(dir, "host.exe"),
			DataDir:    filepath.Join(dir, "data"),
		}
		cfgPath := filepath.Join(dir, "cfg-"+string(c.id)+".json")
		if err := os.WriteFile(cfgPath, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		plan, err := Build(Options{
			Install:    inst,
			Target:     target,
			ConfigPath: cfgPath,
			DataDir:    filepath.Join(dir, "dd"),
		})
		if err != nil {
			t.Fatalf("%s: Build: %v", c.id, err)
		}
		want := InternetEnvEnv + "=" + c.want
		found := false
		for _, e := range plan.Env {
			if strings.HasPrefix(e, InternetEnvEnv+"=") && e != want {
				t.Errorf("%s: 环境变量错位 %q", c.id, e)
			}
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: 环境里没有 %q，实际 %v", c.id, want, plan.Env)
		}
	}
}

// TestCleanEnvStripsInternetEnvironment 钉住原生模式的纯净性：
// 用户环境里残留的开关值会把原生启动悄悄切到别的后端，必须剥离。
func TestCleanEnvStripsInternetEnvironment(t *testing.T) {
	env, stripped := CleanEnv([]string{
		"PATH=x",
		InternetEnvEnv + "=external",
	})
	for _, e := range env {
		if strings.HasPrefix(e, InternetEnvEnv+"=") {
			t.Errorf("原生模式不应保留网络环境开关：%q", e)
		}
	}
	found := false
	for _, k := range stripped {
		if k == InternetEnvEnv {
			found = true
		}
	}
	if !found {
		t.Errorf("剥离清单里应记录被剥的变量：%v", stripped)
	}
}
