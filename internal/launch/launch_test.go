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

func TestBuildRejectsUnavailableInstall(t *testing.T) {
	intl, _ := variant.Get(variant.Intl)
	_, err := Build(variant.Install{Found: false, Problems: []string{"找不到主程序"}}, intl, "x.json", nil)
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
	_, err := Build(inst, intl, filepath.Join(dir, "absent.json"), nil)
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
		DataDir:    dataDir,
	}

	plan, err := Build(inst, intl, cfg, []string{"ACC_PRODUCT_CONFIG_V3=stale", "PATH=/usr/bin"})
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

func TestBuildFallsBackWhenDataDirUnknown(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "generated.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cn, _ := variant.Get(variant.CN)
	inst := variant.Install{Found: true, Executable: filepath.Join(dir, "WorkBuddy.exe")}

	plan, err := Build(inst, cn, cfg, nil)
	if err != nil {
		t.Fatalf("Build 失败: %v", err)
	}
	want := filepath.Join(dir, cn.DataFolderName)
	if plan.DataDir != want {
		t.Errorf("DataDir = %q, 期望 %q", plan.DataDir, want)
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
