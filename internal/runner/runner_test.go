package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/product"
	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/variant/varianttest"
)

// prepareEnv 造出一套脱离真实机器的切换环境。
//
// 设置目录走 config.SetRoot，安装探测走注入的 Probe——否则在装了两套
// 客户端的开发机上会真的读到那些安装，测试也就失去意义了。
func prepareEnv(t *testing.T) varianttest.Env {
	t.Helper()
	t.Cleanup(config.SetRoot(t.TempDir()))
	return varianttest.New(t)
}

// backend 取档位描述符，取不到就让用例直接失败。
func backend(t *testing.T, id variant.ID) variant.Backend {
	t.Helper()
	b, err := variant.Get(id)
	if err != nil {
		t.Fatalf("variant.Get(%s): %v", id, err)
	}
	return b
}

func TestPrepareRejectsUnknownTarget(t *testing.T) {
	prepareEnv(t)

	_, err := Prepare(Options{Target: "nope"})
	if err == nil {
		t.Fatal("未知目标后端应当报错")
	}
}

func TestPrepareFailsWhenHostMissing(t *testing.T) {
	env := varianttest.New(t)
	t.Cleanup(config.SetRoot(t.TempDir()))

	// 把所有路径都判为不存在：模拟"本机没装客户端"。
	env.Probe.Exists = func(string) bool { return false }
	env.Probe.Registry = nil

	_, err := Prepare(Options{Target: variant.Intl, Probe: env.Probe})
	if err == nil {
		t.Fatal("找不到宿主安装时应当报错")
	}
}

func TestPrepareWritesConfigPointingAtTarget(t *testing.T) {
	env := prepareEnv(t)

	res, err := Prepare(Options{Target: variant.Intl, Probe: env.Probe})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	// 宿主仍是国内版，数据目录必须跟随目标档位——
	// 两套后端账号体系不互通，共用 profile 会互相冲掉登录态。
	if res.Host.ID != variant.CN {
		t.Errorf("宿主 = %q，期望国内版", res.Host.ID)
	}
	if !strings.Contains(res.DataDir, ".workbuddy-ai") {
		t.Errorf("数据目录 = %q，应当跟随国际版", res.DataDir)
	}
	if !strings.Contains(res.CommandLine, "--user-data-dir="+res.DataDir) {
		t.Errorf("命令行未指定数据目录: %q", res.CommandLine)
	}

	// 生成的配置必须真的落在磁盘上，且指向目标后端。
	raw, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatalf("读取生成的配置: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析生成的配置: %v", err)
	}
	if got := doc["endpoint"]; got != backend(t, variant.Intl).Endpoint {
		t.Errorf("endpoint = %v，期望 %s", got, backend(t, variant.Intl).Endpoint)
	}
	if got := doc["dataFolderName"]; got != backend(t, variant.Intl).DataFolderName {
		t.Errorf("dataFolderName = %v", got)
	}
	if got := doc["isOversea"]; got != true {
		t.Errorf("isOversea = %v，期望 true", got)
	}

	// 这几项绝不能动：改了会让客户端自身的更新与产品标识错乱。
	if got := doc["productName"]; got != backend(t, variant.CN).ProductName {
		t.Errorf("productName 被改成了 %v，应当保持宿主的值", got)
	}
	updates, ok := doc["updates"].(map[string]any)
	if !ok || updates["url"] != "https://keep-me.example" {
		t.Errorf("updates 被改动了: %v", doc["updates"])
	}
	if _, ok := doc["productFeatures"].(map[string]any); !ok {
		t.Errorf("productFeatures 丢失或被改动: %v", doc["productFeatures"])
	}
}

func TestPrepareNativeModeSkipsConfig(t *testing.T) {
	env := prepareEnv(t)

	res, err := Prepare(Options{Target: variant.Intl, Native: true, Probe: env.Probe})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !res.Native {
		t.Error("Native 应为 true")
	}
	if res.ConfigPath != "" {
		t.Errorf("原生模式不该生成配置，却得到 %q", res.ConfigPath)
	}
	if len(res.Changes) != 0 {
		t.Errorf("原生模式不该有改写项: %v", res.Changes)
	}
	// 原生模式用目标档位自己的安装。
	if res.Host.ID != variant.Intl {
		t.Errorf("宿主 = %q，期望国际版", res.Host.ID)
	}
	if res.Executable != env.Exe[variant.Intl] {
		t.Errorf("主程序 = %q，期望 %q", res.Executable, env.Exe[variant.Intl])
	}
}

func TestPrepareNativeModeStripsOverrideEnv(t *testing.T) {
	env := prepareEnv(t)

	// 父进程里残留的覆盖变量会让原生模式失去"对照"的意义：
	// 它会照常连到被覆盖的后端，用户看到的现象就无从判断。
	res, err := Prepare(Options{
		Target: variant.Intl,
		Native: true,
		Probe:  env.Probe,
		ParentEnv: []string{
			"ACC_PRODUCT_CONFIG_PATH=/somewhere/else.json",
			"ACC_PRODUCT_CONFIG_V3={}",
			"PATH=/usr/bin",
		},
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	for _, name := range res.Stripped {
		if strings.EqualFold(name, "PATH") {
			t.Errorf("PATH 不该被剥离")
		}
	}
	if len(res.Stripped) != 2 {
		t.Errorf("应当剥离 2 个覆盖变量，实际 %v", res.Stripped)
	}
}

// ---------- 回读校验 ----------
//
// 这一步几乎不花时间，却是唯一能挡住"补丁字段名写错"的防线：
// 那种情况下客户端会照常启动，只是继续连原后端，表现是"看起来切换成功"。

func TestVerifyEndpoint(t *testing.T) {
	intl := backend(t, variant.Intl)

	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "product.json")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("写入测试配置: %v", err)
		}
		return p
	}

	t.Run("指向目标后端时通过", func(t *testing.T) {
		p := write(t, `{"endpoint":"`+intl.Endpoint+`"}`)
		if err := verifyEndpoint(p, intl); err != nil {
			t.Fatalf("校验不该失败: %v", err)
		}
	})

	t.Run("仍指向原后端时报错", func(t *testing.T) {
		p := write(t, `{"endpoint":"https://www.workbuddy.cn"}`)
		err := verifyEndpoint(p, intl)
		if err == nil {
			t.Fatal("配置仍指向国内版，校验却通过了")
		}
		if !strings.Contains(err.Error(), intl.Endpoint) {
			t.Errorf("错误信息里应当带上期望值，实际: %v", err)
		}
	})

	t.Run("缺少 endpoint 时报错", func(t *testing.T) {
		p := write(t, `{"productName":"WorkBuddy"}`)
		if err := verifyEndpoint(p, intl); err == nil {
			t.Fatal("缺少 endpoint 时校验应当失败")
		}
	})

	t.Run("文件不存在时报错", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "missing.json")
		if err := verifyEndpoint(p, intl); err == nil {
			t.Fatal("文件不存在时校验应当失败")
		}
	})

	t.Run("内容非法时报错", func(t *testing.T) {
		p := write(t, `{not json`)
		if err := verifyEndpoint(p, intl); err == nil {
			t.Fatal("内容非法时校验应当失败")
		}
	})
}

// TestPrepareOutputSurvivesRoundTrip 确认生成的配置能被原样读回。
//
// product.Patch 用的是 map[string]any，数字若被解码成 float64 会在
// 回写时变成 1.5e+06 这类形态，把客户端的配置改坏。这条用例盯住这一点。
func TestPrepareOutputSurvivesRoundTrip(t *testing.T) {
	env := prepareEnv(t)

	// 在宿主自带配置里塞一个会暴露精度问题的数字。
	b, err := variant.Get(variant.CN)
	if err != nil {
		t.Fatalf("variant.Get: %v", err)
	}
	hostJSON := filepath.Join(filepath.Dir(env.Exe[variant.CN]), "resources",
		filepath.FromSlash(variant.ProductJSONRel))
	doc, err := product.Load(hostJSON)
	if err != nil {
		t.Fatalf("载入宿主配置: %v", err)
	}
	doc["someBigNumber"] = json.Number("1234567890123")
	raw, err := doc.Bytes()
	if err != nil {
		t.Fatalf("序列化宿主配置: %v", err)
	}
	if err := os.WriteFile(hostJSON, raw, 0o644); err != nil {
		t.Fatalf("写回宿主配置: %v", err)
	}
	_ = b

	res, err := Prepare(Options{Target: variant.Intl, Probe: env.Probe})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	out, err := os.ReadFile(res.ConfigPath)
	if err != nil {
		t.Fatalf("读取生成配置: %v", err)
	}
	if !strings.Contains(string(out), "1234567890123") {
		t.Errorf("大整数被改写成了别的形态:\n%s", out)
	}
}
