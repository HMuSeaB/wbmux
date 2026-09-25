package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}
	return p
}

func TestScanTokensFindsPresentTokens(t *testing.T) {
	p := writeTemp(t, "blob.bin", []byte("xx ACC_PRODUCT_CONFIG_PATH yy MUTUALLY_EXCLUSIVE_ENV_GROUPS zz"))

	hits, err := ScanTokens(p, mechanismTokens)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	for _, tok := range mechanismTokens {
		if !hits[tok] {
			t.Errorf("未命中 %q", tok)
		}
	}
}

func TestScanTokensReportsMissingTokens(t *testing.T) {
	p := writeTemp(t, "blob.bin", []byte("this bundle only has ACC_PRODUCT_CONFIG_PATH inside"))

	hits, err := ScanTokens(p, mechanismTokens)
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if !hits["ACC_PRODUCT_CONFIG_PATH"] {
		t.Error("应命中 ACC_PRODUCT_CONFIG_PATH")
	}
	if hits["MUTUALLY_EXCLUSIVE_ENV_GROUPS"] {
		t.Error("不该命中不存在的标记")
	}
}

// 标记恰好跨块时必须仍能找到，否则自检会误报"机制失效"。
func TestScanTokensFindsTokenStraddlingChunkBoundary(t *testing.T) {
	const chunkSize = 4 << 20
	token := mechanismTokens[0]

	// 复现 ScanTokens 的分块：首轮窗口长度 = chunkSize + maxLen，
	// 因此把标记起点放在该窗口末尾前 5 字节处，让它横跨两块。
	windowLen := chunkSize + len(token)
	start := windowLen - 5

	data := make([]byte, start+len(token)+16)
	for i := range data {
		data[i] = 'x'
	}
	copy(data[start:], token)

	p := writeTemp(t, "big.bin", data)

	hits, err := ScanTokens(p, []string{token})
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if !hits[token] {
		t.Errorf("跨块标记未被找到（起点 %d，文件 %d 字节）", start, len(data))
	}
}

func TestScanTokensFindsTokenAtEndOfFile(t *testing.T) {
	token := mechanismTokens[1]
	data := append(bytes.Repeat([]byte("y"), 5<<20), []byte(token)...)
	p := writeTemp(t, "tail.bin", data)

	hits, err := ScanTokens(p, []string{token})
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if !hits[token] {
		t.Error("文件末尾的标记未被找到")
	}
}

func TestScanTokensHandlesEmptyInput(t *testing.T) {
	p := writeTemp(t, "empty.bin", nil)
	hits, err := ScanTokens(p, mechanismTokens)
	if err != nil {
		t.Fatalf("空文件不应报错: %v", err)
	}
	for _, tok := range mechanismTokens {
		if hits[tok] {
			t.Errorf("空文件不该命中 %q", tok)
		}
	}
}

func TestScanTokensWithNoTokens(t *testing.T) {
	p := writeTemp(t, "blob.bin", []byte("anything"))
	hits, err := ScanTokens(p, nil)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("无标记时应返回空 map, 实际 %v", hits)
	}
}

func TestScanTokensMissingFile(t *testing.T) {
	if _, err := ScanTokens(filepath.Join(t.TempDir(), "absent.bin"), mechanismTokens); err == nil {
		t.Error("文件不存在时应报错")
	}
}

func TestCheckDescriptors(t *testing.T) {
	cn, _ := variant.Get(variant.CN)
	intl, _ := variant.Get(variant.Intl)

	if c := checkDescriptors(cn, intl); c.Level != OK {
		t.Errorf("国内版 → 国际版 应为 OK, 实际 %v: %s", c.Level, c.Detail)
	}
	if c := checkDescriptors(cn, cn); c.Level != Warn {
		t.Errorf("同档位应为 Warn, 实际 %v", c.Level)
	}
	if c := checkDescriptors(cn, variant.Backend{ID: "x", Endpoint: cn.Endpoint}); c.Level != Fail {
		t.Errorf("endpoint 相同应为 Fail, 实际 %v", c.Level)
	}
	if c := checkDescriptors(cn, variant.Backend{ID: "x", Endpoint: "https://other", DataFolderName: cn.DataFolderName}); c.Level != Fail {
		t.Errorf("数据目录相同应为 Fail, 实际 %v", c.Level)
	}
}

func TestCheckHostProductDetectsVariantMismatch(t *testing.T) {
	cn, _ := variant.Get(variant.CN)
	doc := `{"endpoint":"https://www.workbuddy.ai","dataFolderName":".workbuddy-ai"}`
	p := writeTemp(t, "product.json", []byte(doc))

	c := checkHostProduct(variant.Install{ProductJSON: p}, cn)
	if c.Level != Warn {
		t.Errorf("档位不符应给出警告, 实际 %v: %s", c.Level, c.Detail)
	}
	if c.Hint == "" {
		t.Error("警告应附带建议")
	}
}

func TestCheckHostProductAcceptsMatchingVariant(t *testing.T) {
	cn, _ := variant.Get(variant.CN)
	doc := `{"endpoint":"https://www.workbuddy.cn","dataFolderName":".workbuddy"}`
	p := writeTemp(t, "product.json", []byte(doc))

	if c := checkHostProduct(variant.Install{ProductJSON: p}, cn); c.Level != OK {
		t.Errorf("档位相符应为 OK, 实际 %v: %s", c.Level, c.Detail)
	}
}

func TestCheckHostProductRejectsUnreadableFile(t *testing.T) {
	cn, _ := variant.Get(variant.CN)
	c := checkHostProduct(variant.Install{ProductJSON: filepath.Join(t.TempDir(), "nope.json")}, cn)
	if c.Level != Fail {
		t.Errorf("配置不可读应为 Fail, 实际 %v", c.Level)
	}
}

func TestCheckCacheDirCreatesAndVerifies(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deep", "nested")
	if c := checkCacheDir(dir); c.Level != OK {
		t.Fatalf("应能创建并写入, 实际 %v: %s", c.Level, c.Detail)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("目录未被创建: %v", err)
	}
	if c := checkCacheDir(""); c.Level != Fail {
		t.Errorf("空目录应为 Fail, 实际 %v", c.Level)
	}
}

func TestReportAggregation(t *testing.T) {
	r := Report{Checks: []Check{
		{Name: "a", Level: OK},
		{Name: "b", Level: Warn},
		{Name: "c", Level: Warn},
		{Name: "d", Level: Info},
	}}
	if r.Failed() {
		t.Error("无 Fail 时 Failed() 应为 false")
	}
	if r.Warnings() != 2 {
		t.Errorf("Warnings() = %d, 期望 2", r.Warnings())
	}

	r.Checks = append(r.Checks, Check{Name: "e", Level: Fail})
	if !r.Failed() {
		t.Error("含 Fail 时 Failed() 应为 true")
	}
}

// --fast 模式下机制检查应降级为警告而不是失败，避免用户误以为不可用。
func TestMechanismCheckSkippedInFastMode(t *testing.T) {
	rep := &Report{}
	c := checkMechanism(variant.DefaultProbe(), variant.Install{}, rep, true)
	if c.Level != Warn {
		t.Errorf("跳过时应为 Warn, 实际 %v", c.Level)
	}
}

func TestLevelStrings(t *testing.T) {
	for lvl, want := range map[Level]string{OK: "OK", Warn: "警告", Fail: "失败", Info: "信息"} {
		if got := lvl.String(); got != want {
			t.Errorf("Level(%d).String() = %q, 期望 %q", lvl, got, want)
		}
	}
}
