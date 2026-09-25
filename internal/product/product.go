// Package product 负责读取宿主安装自带的 product.json，
// 在内存中打上"最小后端补丁"，并落盘到缓存目录。
//
// 设计原则：只改动决定"连哪套后端"的字段，其余一律保持宿主原样。
// 因此更新通道、遥测配置、品牌资源、功能开关、模型清单都不会被跨版本覆盖，
// 避免了把另一套构建的配置硬套到当前构建上所产生的版本错配。
package product

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Document 是 product.json 的通用表示。
//
// 用 map 而不是结构体，是为了让所有未知字段原样透传——
// 官方随时可能新增字段，硬编码结构体必然漏。
type Document map[string]any

// Change 记录一处被改写的字段，用于 doctor 与 --dry-run 展示。
type Change struct {
	Field string
	From  string
	To    string
}

func (c Change) String() string {
	from := c.From
	if from == "" {
		from = "(缺失)"
	}
	return fmt.Sprintf("%s: %s → %s", c.Field, from, c.To)
}

// Options 控制补丁的覆盖范围。
type Options struct {
	// ExtraEndpoints 会被追加进 officialEndpoints（去重后）。
	// 用于企业自建域等场景。
	ExtraEndpoints []string
}

// Load 读取并解析一个 product.json。
//
// 使用 UseNumber 保留数字原始精度：配置里存在较大的整数标识符，
// 走 float64 会在重新序列化时变成科学计数法并可能丢精度。
func Load(path string) (Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取产品配置失败: %w", err)
	}
	return Parse(raw)
}

// Parse 从字节解析产品配置。
func Parse(raw []byte) (Document, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var doc Document
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("解析产品配置失败: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("产品配置为空")
	}
	return doc, nil
}

// Bytes 序列化，禁用 HTML 转义以保持 URL 与占位符原貌。
func (d Document) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		return nil, fmt.Errorf("序列化产品配置失败: %w", err)
	}
	return buf.Bytes(), nil
}

// Patch 把 target 后端的接入信息写入 doc，返回按字段名排序的变更清单。
//
// 被改写的字段是穷举的：任何未在此列出的字段都保证原样保留。
func Patch(doc Document, target variant.Backend, opts Options) ([]Change, error) {
	var changes []Change

	set := func(key string, val any) {
		before := render(doc[key])
		doc[key] = val
		after := render(val)
		if before != after {
			changes = append(changes, Change{Field: key, From: before, To: after})
		}
	}

	set("endpoint", target.Endpoint)
	set("stagingEndpoint", target.StagingEndpoint)
	set("dataFolderName", target.DataFolderName)

	// isOversea 在国内版配置中根本不存在，国际版为 true。
	// 因此切到国内版时必须删除该键，而不是置为 false——保持与官方形态一致。
	if target.Oversea {
		set("isOversea", true)
	} else if _, exists := doc["isOversea"]; exists {
		changes = append(changes, Change{
			Field: "isOversea",
			From:  render(doc["isOversea"]),
			To:    "(移除)",
		})
		delete(doc, "isOversea")
	}

	endpoints := dedupeStrings(append(append([]string{}, target.OfficialEndpoints...), opts.ExtraEndpoints...))
	set("officialEndpoints", toAnySlice(endpoints))

	auth, err := ensureMap(doc, "authentication")
	if err != nil {
		return nil, err
	}
	setIn(auth, &changes, "authentication.id", target.AuthID)
	setIn(auth, &changes, "authentication.label", target.AuthLabel)

	attrs, err := ensureMap(auth, "attributes")
	if err != nil {
		return nil, err
	}
	setIn(attrs, &changes, "authentication.attributes.platform", target.AuthPlatform)
	setIn(attrs, &changes, "authentication.attributes.internalDomain", toAnySlice(target.Domains.Internal))
	setIn(attrs, &changes, "authentication.attributes.externalDomain", toAnySlice(target.Domains.External))
	setIn(attrs, &changes, "authentication.attributes.iOADomain", toAnySlice(target.Domains.IOA))
	setIn(attrs, &changes, "authentication.attributes.cloudHostedDomain", toAnySlice(target.Domains.CloudHosted))

	// 刻意不触碰的字段，在此显式记录以免日后被误加：
	//   productName / applicationName / win32ExecutableName / urlProtocol
	//     —— 属于宿主自身的身份标识，改动可能影响单实例互斥、协议注册与任务栏归组。
	//   updates / updateUrl
	//     —— 保持宿主自己的更新通道，避免国内版去拉国际版安装包（反之亦然）。
	//   smhHost
	//     —— 区域安全上报地址。宿主缺失时保持缺失，不主动把流量导向其它区域。
	//   models / prompts / agents / tools / productFeatures / config
	//     —— 与后端无关，保持宿主原样。

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })
	return changes, nil
}

// Generate 是 Patch 的落盘封装：读宿主配置 → 打补丁 → 写入 outPath。
func Generate(hostProductJSON, outPath string, target variant.Backend, opts Options) ([]Change, error) {
	doc, err := Load(hostProductJSON)
	if err != nil {
		return nil, err
	}
	changes, err := Patch(doc, target, opts)
	if err != nil {
		return nil, err
	}
	out, err := doc.Bytes()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建缓存目录失败: %w", err)
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return nil, fmt.Errorf("写入生成配置失败: %w", err)
	}
	return changes, nil
}

// ensureMap 取出一个子对象，不存在则创建。
func ensureMap(parent map[string]any, key string) (map[string]any, error) {
	if existing, ok := parent[key]; ok {
		if m, ok := existing.(map[string]any); ok {
			return m, nil
		}
		return nil, fmt.Errorf("字段 %s 存在但不是对象，宿主配置可能已变更", key)
	}
	m := map[string]any{}
	parent[key] = m
	return m, nil
}

// setIn 在嵌套对象里设值并记录变更。
func setIn(container map[string]any, changes *[]Change, field string, val any) {
	key := field[strings.LastIndex(field, ".")+1:]
	before := render(container[key])
	container[key] = val
	after := render(val)
	if before != after {
		*changes = append(*changes, Change{Field: field, From: before, To: after})
	}
}

// render 把字段值压成一行便于展示与比较。
func render(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, render(item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case json.Number:
		return t.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(b)
	}
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func dedupeStrings(in []string) []string {
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
