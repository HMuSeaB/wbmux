// Package varianttest 提供脱离真实机器的探测环境。
//
// 单独成包是因为不止一个包的测试需要它：variant 的探测结果同时被
// runner（生成配置）与 webui（界面状态）消费，两边都得在"本机装了什么"
// 完全可控的前提下跑。放在这里比在各家测试文件里复制一份强。
//
// 这个包只被 _test.go 引用，不会进入发布产物。
package varianttest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Env 是一套假安装环境。
type Env struct {
	// Home 是假的用户主目录。
	Home string
	// Root 是这套环境的根目录，临时目录。
	Root string
	// Exe 是各档位的主程序路径。
	Exe map[variant.ID]string
	// DataDir 是各档位的数据目录。
	DataDir map[variant.ID]string

	Probe *variant.Probe
}

// New 在临时目录里造出两套"已安装"的客户端，返回可注入的探测器。
//
// 刻意让两套安装走不同的定位路径：国内版靠数据目录线索、国际版靠注册表。
// 这与真实机器上的情况一致，顺带把两条探测策略也覆盖了。
func New(t testing.TB) Env {
	t.Helper()

	root := t.TempDir()
	home := filepath.Join(root, "home")

	env := Env{
		Home:    home,
		Root:    root,
		Exe:     map[variant.ID]string{},
		DataDir: map[variant.ID]string{},
	}

	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		b, err := variant.Get(id)
		if err != nil {
			t.Fatalf("variant.Get(%s): %v", id, err)
		}

		dir := filepath.Join(root, "apps", string(id))
		env.Exe[id] = writeInstall(t, dir, b)
		env.DataDir[id] = filepath.Join(home, b.DataFolderName)
	}

	// 国内版的数据目录线索：客户端注册文件关联时写下的标记，
	// 第 4 段是主程序绝对路径。
	//
	// 用 json.Marshal 而不是拼字符串：标记里含 Windows 路径的反斜杠，
	// 手工拼接会写出非法转义（\U 不是合法转义序列），探测会静默失败——
	// 这个坑在真实开发中踩过一次。
	cn, err := variant.Get(variant.CN)
	if err != nil {
		t.Fatalf("variant.Get(cn): %v", err)
	}
	marker, err := json.Marshal(map[string]string{
		"officeFileAssociationsRepairMarker": fmt.Sprintf("v3:win32:5.6.2:%s:%s:aac,avif",
			env.Exe[variant.CN], cn.ProductName),
	})
	if err != nil {
		t.Fatalf("构造标记失败: %v", err)
	}
	mustWrite(t, filepath.Join(env.DataDir[variant.CN], "settings.json"), marker)

	intl, err := variant.Get(variant.Intl)
	if err != nil {
		t.Fatalf("variant.Get(intl): %v", err)
	}

	env.Probe = &variant.Probe{
		GOOS: "windows",
		Home: home,
		// 一律返回空：让常见安装目录与 PATH 两条策略都落空，
		// 从而确保命中的确实是上面埋的那两条路径。
		Getenv: func(string) string { return "" },
		Exists: func(p string) bool {
			if p == "" {
				return false
			}
			_, err := os.Stat(p)
			return err == nil
		},
		ReadFile: os.ReadFile,
		Registry: func() []variant.RegistryEntry {
			return []variant.RegistryEntry{{
				DisplayName: intl.ProductName,
				// Publisher 留空即视为通过（真实记录里是腾讯）。
				DisplayIcon: env.Exe[variant.Intl] + ",0",
			}}
		},
	}
	return env
}

// writeInstall 造出一套最小但完整的安装目录，返回主程序路径。
func writeInstall(t testing.TB, dir string, b variant.Backend) string {
	t.Helper()

	exe := filepath.Join(dir, b.WinExecutableName+".exe")
	mustWrite(t, exe, []byte("stub"))

	// 自带产品配置：够 Patch 用即可。updates 与 productFeatures 是故意放的，
	// 用来断言切换后它们原样不动。
	doc := map[string]any{
		"productName":       b.ProductName,
		"endpoint":          b.Endpoint,
		"stagingEndpoint":   b.StagingEndpoint,
		"dataFolderName":    b.DataFolderName,
		"officialEndpoints": b.OfficialEndpoints,
		"updates":           map[string]any{"url": "https://keep-me.example"},
		"productFeatures":   map[string]any{"ChannelSlack": true},
		"authentication": map[string]any{
			"id":         b.AuthID,
			"label":      b.AuthLabel,
			"attributes": map[string]any{"platform": b.AuthPlatform},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("序列化 product.json 失败: %v", err)
	}

	resources := filepath.Join(dir, "resources")
	mustWrite(t, filepath.Join(resources, "app.asar"), []byte("stub"))
	mustWrite(t, filepath.Join(resources, filepath.FromSlash(variant.ProductJSONRel)), raw)

	return exe
}

func mustWrite(t testing.TB, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}
