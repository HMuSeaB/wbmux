package webui

// 诊断：抓一次真实请求体的**结构**（不是内容），回答"体积花在哪"。
//
// # 什么时候用得着
//
// 排查"发消息慢""请求体过大""图片是内联还是引用"这类问题时——
// 光看日志里的总字节数不够，得知道那几 MB 具体是什么。
//
// 2026-10-05 第一次用它得到的结论（值得记）：
//   * 客户端发的请求体只有 ~2 MB，而磁盘上的会话文件有 22 MB ——
//     **客户端已经压缩过了**，比例约 10:1；
//   * 所以"会话涨到 32 MB 就会撞上限"那个担心是**错的**
//     （我把磁盘体积当成了请求体积）；
//   * 真正的体积大头是**图片**（base64 内联，占 65%）。
//
// 换句话说：它当时否掉了一个错误的判断，省下了一次白做的优化。
// **排查工具的价值就在这里——先看清，再动手。**
//
// # 安全约束（刻意这么设计）
//
//   - 只在环境变量 WBMUX_DUMP_ONCE 指向一个路径时启用，**默认为空、完全不生效**；
//   - **只统计结构**（字段路径、长度、是否像 base64），不落盘任何正文内容；
//   - 报告写完后立刻自删开关，进程内一次性，不反复写盘。
//
// 用法：
//   WBMUX_DUMP_ONCE=<输出文件路径> wbmux.exe gui ...

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// dumpOncePath 返回本次要写入的诊断报告路径；空串表示不诊断。
func dumpOncePath() string {
	return strings.TrimSpace(os.Getenv("WBMUX_DUMP_ONCE"))
}

// dumpBodyShape 分析请求体结构，返回一份人类可读的报告。
//
// 只报告"形状"：路径、类型、长度、是否像 base64。**不含任何原文。**
func dumpBodyShape(raw []byte) string {
	var out strings.Builder
	fmt.Fprintf(&out, "请求体总大小 %d 字节\n\n", len(raw))

	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		fmt.Fprintf(&out, "不是 JSON 对象：%v\n", err)
		return out.String()
	}

	// 顶层键的体积
	type kv struct {
		k string
		n int
	}
	var tops []kv
	for k, v := range doc {
		b, _ := json.Marshal(v)
		tops = append(tops, kv{k, len(b)})
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].n > tops[j].n })
	out.WriteString("=== 顶层字段体积 ===\n")
	for _, x := range tops {
		fmt.Fprintf(&out, "  %-16s %10d 字节\n", x.k, x.n)
	}
	out.WriteString("\n")

	msgs, _ := doc["messages"].([]any)
	fmt.Fprintf(&out, "messages 条数: %d\n\n", len(msgs))

	// 逐条消息：报告 role + 各部分体积 + 是否含长 base64
	out.WriteString("=== 各消息体积（前 40 条最大的）===\n")
	type mi struct {
		idx  int
		role string
		n    int
		b64  int
		b64n int
		kind string
	}
	var infos []mi
	for i, m := range msgs {
		obj, _ := m.(map[string]any)
		role, _ := obj["role"].(string)
		b, _ := json.Marshal(m)
		bl, bln := countB64(string(b))
		kind := describeContentKind(obj)
		infos = append(infos, mi{i, role, len(b), bl, bln, kind})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].n > infos[j].n })
	for i, x := range infos {
		if i >= 40 {
			break
		}
		fmt.Fprintf(&out, "  [%3d] %-10s %9d 字节  base64 %8d 字节 (%d 块)  content=%s\n",
			x.idx, x.role, x.n, x.b64, x.b64n, x.kind)
	}

	// 汇总
	var totB64, totAll int
	for _, x := range infos {
		totB64 += x.b64
		totAll += x.n
	}
	out.WriteString("\n=== 汇总 ===\n")
	fmt.Fprintf(&out, "  base64 内联总量: %d 字节（占 messages %.0f%%）\n",
		totB64, 100*float64(totB64)/float64(maxInt(totAll, 1)))
	out.WriteString("  → 若这个数很大，说明图片是**内联**在请求体里的，代理确实扛着它\n")
	out.WriteString("  → 若接近 0，说明图片走的是**引用**，代理手上没有可省的空间\n")

	// 工具定义体积（每轮都带，通常是第二大块）
	if tools, ok := doc["tools"].([]any); ok {
		b, _ := json.Marshal(tools)
		fmt.Fprintf(&out, "\n=== tools ===\n  共 %d 个，占 %d 字节（%.1f%%）\n",
			len(tools), len(b), 100*float64(len(b))/float64(maxInt(len(raw), 1)))
	}
	return out.String()
}

// countB64 数出字符串里"像 base64 的长块"的字节数与块数。
//
// 判据刻意简单：连续 >=1024 个 base64 字符。正常文本不会这么长不断。
func countB64(s string) (bytes int, blocks int) {
	run := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' {
			run++
			continue
		}
		if run >= 1024 {
			bytes += run
			blocks++
		}
		run = 0
	}
	if run >= 1024 {
		bytes += run
		blocks++
	}
	return
}

// describeContentKind 说清一条消息的 content 是什么形状。
//
// 这一步专门回答"图片怎么传"：是 image_url / image / blob 引用 / 纯文本。
func describeContentKind(obj map[string]any) string {
	c, ok := obj["content"]
	if !ok {
		return "(无 content)"
	}
	switch v := c.(type) {
	case string:
		if strings.Contains(v, "base64,") {
			return "字符串(含 data:base64)"
		}
		return fmt.Sprintf("字符串(%d 字符)", len(v))
	case []any:
		kinds := map[string]int{}
		for _, part := range v {
			p, ok := part.(map[string]any)
			if !ok {
				kinds["?"]++
				continue
			}
			t, _ := p["type"].(string)
			kinds[t]++
		}
		var ks []string
		for k, n := range kinds {
			ks = append(ks, fmt.Sprintf("%s×%d", k, n))
		}
		sort.Strings(ks)
		return "[" + strings.Join(ks, ",") + "]"
	default:
		return fmt.Sprintf("(%T)", c)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
