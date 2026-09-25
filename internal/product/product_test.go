package product

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

const samplePath = "../../testdata/product.sample.json"

func loadSample(t *testing.T) Document {
	t.Helper()
	doc, err := Load(samplePath)
	if err != nil {
		t.Fatalf("加载样例配置失败: %v", err)
	}
	return doc
}

func TestPatchWritesBackendFields(t *testing.T) {
	doc := loadSample(t)
	intl, _ := variant.Get(variant.Intl)

	changes, err := Patch(doc, intl, Options{})
	if err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("应产生变更")
	}

	if doc["endpoint"] != intl.Endpoint {
		t.Errorf("endpoint = %v, 期望 %v", doc["endpoint"], intl.Endpoint)
	}
	if doc["stagingEndpoint"] != intl.StagingEndpoint {
		t.Errorf("stagingEndpoint = %v", doc["stagingEndpoint"])
	}
	if doc["dataFolderName"] != intl.DataFolderName {
		t.Errorf("dataFolderName = %v", doc["dataFolderName"])
	}
	if doc["isOversea"] != true {
		t.Errorf("国际版应写入 isOversea=true, 实际 %v", doc["isOversea"])
	}

	auth := doc["authentication"].(map[string]any)
	if auth["id"] != intl.AuthID {
		t.Errorf("authentication.id = %v", auth["id"])
	}
	attrs := auth["attributes"].(map[string]any)
	if attrs["platform"] != intl.AuthPlatform {
		t.Errorf("attributes.platform = %v", attrs["platform"])
	}
	for name, want := range map[string][]string{
		"internalDomain":    intl.Domains.Internal,
		"externalDomain":    intl.Domains.External,
		"iOADomain":         intl.Domains.IOA,
		"cloudHostedDomain": intl.Domains.CloudHosted,
	} {
		got := toStrings(t, attrs[name])
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("attributes.%s = %v, 期望 %v", name, got, want)
		}
	}
}

// 国内版配置里没有 isOversea 这个键，切过去时必须删除而不是置 false。
func TestPatchRemovesOverseaWhenTargetIsDomestic(t *testing.T) {
	doc := loadSample(t)
	intl, _ := variant.Get(variant.Intl)
	cn, _ := variant.Get(variant.CN)

	if _, err := Patch(doc, intl, Options{}); err != nil {
		t.Fatalf("先切到国际版失败: %v", err)
	}
	if _, ok := doc["isOversea"]; !ok {
		t.Fatal("前置条件不成立: 应存在 isOversea")
	}

	changes, err := Patch(doc, cn, Options{})
	if err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	if _, ok := doc["isOversea"]; ok {
		t.Error("切到国内版后 isOversea 应被移除")
	}
	found := false
	for _, c := range changes {
		if c.Field == "isOversea" && c.To == "(移除)" {
			found = true
		}
	}
	if !found {
		t.Errorf("变更清单应记录 isOversea 被移除, 实际 %v", changes)
	}
}

// 最小补丁的核心承诺：没列出来的字段一个都不许动。
func TestPatchLeavesEverythingElseIntact(t *testing.T) {
	doc := loadSample(t)
	before := snapshot(t, doc)

	intl, _ := variant.Get(variant.Intl)
	if _, err := Patch(doc, intl, Options{}); err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	after := snapshot(t, doc)

	touched := map[string]bool{
		"endpoint":          true,
		"stagingEndpoint":   true,
		"officialEndpoints": true,
		"dataFolderName":    true,
		"isOversea":         true,
		"authentication":    true,
	}
	for key, beforeVal := range before {
		if touched[key] {
			continue
		}
		if beforeVal != after[key] {
			t.Errorf("字段 %q 不应被改动:\n  前: %s\n  后: %s", key, beforeVal, after[key])
		}
	}
	// 唯一允许的字段增删是 isOversea（国内版配置里根本没有这个键）。
	for key := range after {
		if _, ok := before[key]; !ok && key != "isOversea" {
			t.Errorf("不应新增顶层字段 %q", key)
		}
	}
	for key := range before {
		if _, ok := after[key]; !ok && key != "isOversea" {
			t.Errorf("不应删除顶层字段 %q", key)
		}
	}
}

// snapshot 把顶层字段各自序列化成字符串，便于逐字段比较。
func snapshot(t *testing.T, doc Document) map[string]string {
	t.Helper()
	out := make(map[string]string, len(doc))
	for k, v := range doc {
		out[k] = mustJSON(t, v)
	}
	return out
}

// 配置里存在超过 2^53 的整数标识符，走 float64 会丢精度。
func TestPatchPreservesLargeIntegerPrecision(t *testing.T) {
	doc := loadSample(t)
	intl, _ := variant.Get(variant.Intl)
	if _, err := Patch(doc, intl, Options{}); err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	raw, err := doc.Bytes()
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Errorf("大整数精度丢失:\n%s", excerpt(string(raw)))
	}
	if strings.Contains(string(raw), "9.007199254740992e+15") {
		t.Error("大整数被转成了科学计数法")
	}
}

func TestPatchExtendsOfficialEndpointsWithoutDuplicates(t *testing.T) {
	doc := loadSample(t)
	cn, _ := variant.Get(variant.CN)

	changes, err := Patch(doc, cn, Options{
		ExtraEndpoints: []string{"https://corp.example.test", cn.Endpoint},
	})
	if err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	got := toStrings(t, doc["officialEndpoints"])

	seen := map[string]int{}
	for _, e := range got {
		seen[e]++
	}
	for e, n := range seen {
		if n > 1 {
			t.Errorf("officialEndpoints 出现重复项 %q", e)
		}
	}
	if seen["https://corp.example.test"] != 1 {
		t.Errorf("追加的企业域未写入: %v", got)
	}
	var recorded bool
	for _, c := range changes {
		if c.Field == "officialEndpoints" {
			recorded = true
		}
	}
	if !recorded {
		t.Error("变更清单应记录 officialEndpoints")
	}
}

func TestPatchChangesAreSortedByField(t *testing.T) {
	doc := loadSample(t)
	intl, _ := variant.Get(variant.Intl)
	changes, err := Patch(doc, intl, Options{})
	if err != nil {
		t.Fatalf("Patch 失败: %v", err)
	}
	for i := 1; i < len(changes); i++ {
		if changes[i-1].Field > changes[i].Field {
			t.Errorf("变更清单未按字段名排序: %q 在 %q 之前",
				changes[i-1].Field, changes[i].Field)
		}
	}
}

func TestPatchRejectsMalformedAuthentication(t *testing.T) {
	doc := Document{"authentication": "this-should-be-an-object"}
	intl, _ := variant.Get(variant.Intl)
	if _, err := Patch(doc, intl, Options{}); err == nil {
		t.Error("authentication 不是对象时应报错")
	}
}

func TestPatchCreatesMissingAuthentication(t *testing.T) {
	doc := Document{"endpoint": "https://old.test"}
	intl, _ := variant.Get(variant.Intl)
	if _, err := Patch(doc, intl, Options{}); err != nil {
		t.Fatalf("缺失 authentication 时应能补齐: %v", err)
	}
	auth, ok := doc["authentication"].(map[string]any)
	if !ok {
		t.Fatal("应创建 authentication 对象")
	}
	if auth["id"] != intl.AuthID {
		t.Errorf("authentication.id = %v", auth["id"])
	}
}

func TestGenerateWritesFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nested", "generated.json")
	intl, _ := variant.Get(variant.Intl)

	changes, err := Generate(samplePath, out, intl, Options{})
	if err != nil {
		t.Fatalf("Generate 失败: %v", err)
	}
	if len(changes) == 0 {
		t.Error("应返回变更清单")
	}

	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("生成文件不可读: %v", err)
	}
	reloaded, err := Parse(raw)
	if err != nil {
		t.Fatalf("生成文件无法解析: %v", err)
	}
	if reloaded["endpoint"] != intl.Endpoint {
		t.Errorf("落盘后 endpoint = %v", reloaded["endpoint"])
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Error("非法 JSON 应报错")
	}
	if _, err := Parse([]byte("null")); err == nil {
		t.Error("null 应报错")
	}
}

func TestRenderForDisplay(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"plain", "plain"},
		{true, "true"},
		{false, "false"},
		{json.Number("42"), "42"},
		{[]any{"a", "b"}, "[a, b]"},
	}
	for _, c := range cases {
		if got := render(c.in); got != c.want {
			t.Errorf("render(%v) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	return string(b)
}

func toStrings(t *testing.T, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	if !ok {
		t.Fatalf("期望数组，实际 %T", v)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			t.Fatalf("数组元素应为字符串，实际 %T", it)
		}
		out = append(out, s)
	}
	return out
}

func excerpt(s string) string {
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
