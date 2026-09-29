// Package zimport 把 ZCode 的对话**写进** WorkBuddy 的会话库，让它在客户端
// 的会话列表里能看见、点开能读。
//
// # 为什么这是个"重"操作（与只读导出完全不是一个量级）
//
// 一个 WorkBuddy 会话由两处组成：
//
//	workbuddy.db 的 sessions 表          列表条目（标题、项目、时间、状态）
//	projects/<slug>/<会话id>.jsonl       全部对话正文
//
// 只插一行 SQL 的话，列表里会出现一个**点开是空白**的条目——比没有更糟。
// 所以这一包要做两件事：生成一份客户端认得的 JSONL，再写库建索引。
//
// # 安全闸门
//
//   - 目标档位的**客户端必须先关闭**：它在跑的时候会缓存会话列表，边跑边写
//     轻则新条目不出现，重则被它回写覆盖。所以先探活、明确拒绝。
//   - 写入是**只增不改**：库用 INSERT OR IGNORE（已有 id 一律跳过），
//     jsonl 文件不存在才写；两者都不覆盖任何既有数据。
//   - jsonl 的 id 用**新的 UUID**：绝不能拿 ZCode 的 id 去撞客户端的命名空间。
//
// # JSONL 格式（2026-09-29 实测一份 627 行的样本）
//
//	{"id","timestamp","type":"message","role":"user","content":[{"type":"input_text","text"}],
//	 "providerData":{"agent":"cli"},"sessionId","cwd"}
//	{"id","parentId","timestamp","type":"reasoning","content":[],"rawContent":[{"type":"reasoning_text","text"}],...}
//
// 样本里另有 file-history-snapshot / ai-title / function_call / function_call_result
// 四类行。本包**只产出 message 与 reasoning**：
//
//   - ai-title 是客户端自己调模型生成的，我们直接给 sessions.title 更准；
//   - function_call / result 需要补齐 tool_use_id 之类的配对字段，猜错会让
//     客户端渲染崩掉——工具调用降级成普通文本，宁可少一点信息也不要弄坏界面；
//   - file-history-snapshot 是撤销用的快照索引，没有对应真实文件时写它是撒谎。
package zimport

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/sessions"
	"github.com/HMuSeaB/wbmux/internal/variant"
	"github.com/HMuSeaB/wbmux/internal/zcode"
)

// Options 控制这次导入。
type Options struct {
	// Vendor 目标档位（国内 / 国际）。
	Vendor sessions.Vendor
	// WithReasoning 是否把 ZCode 的思考片段也写进去（默认不带）。
	WithReasoning bool
	// ProjectOverride 覆盖项目路径；空则用 ZCode 会话里的 directory。
	ProjectOverride string
	// TitleOverride 覆盖标题；空则用 ZCode 会话标题。
	TitleOverride string
}

// Result 是导入结果。
type Result struct {
	SessionID string `json:"sessionId"` // 新造的 WorkBuddy 会话 id
	JSONLPath string `json:"jsonlPath"`
	DBPath    string `json:"dbPath"`
	Title     string `json:"title"`
	Project   string `json:"project"`
	Msgs      int    `json:"msgs"`
	Lines     int    `json:"lines"`
	// Skipped 是"因为已存在而没动"的说明（正常情况下为空）。
	Skipped string `json:"skipped,omitempty"`
}

// Import 把 ZCode 的一个会话导入目标档位。
func Import(probe *variant.Probe, zcodeSessionID string, opts Options) (*Result, error) {
	if opts.Vendor == "" {
		opts.Vendor = sessions.VendorWBCN
	}
	id := strings.TrimSpace(zcodeSessionID)
	if id == "" {
		return nil, fmt.Errorf("缺少 ZCode 会话 id")
	}

	// ① 先确认目标档位没在跑。这条必须在**任何写入之前**检查：
	// 客户端在跑时边写边被它回写，比直接失败更难查。
	//
	// 注意：写库与写 jsonl 都发生在数据目录里，但"客户端在不在跑"要按
	// **档位**判断——两个档位可以同时开着。
	target, err := targetVariant(opts.Vendor)
	if err != nil {
		return nil, err
	}
	if err := requireClientClosed(opts.Vendor, migrate.IsRunning(target)); err != nil {
		return nil, err
	}

	// ② 读源数据（只读 ZCode）。
	tr, err := zcode.Load(probe, id)
	if err != nil {
		return nil, err
	}
	if len(tr.Msgs) == 0 {
		return nil, fmt.Errorf("这个 ZCode 会话没有可导入的正文（%s）", id)
	}

	project := strings.TrimSpace(opts.ProjectOverride)
	if project == "" {
		project = tr.Directory
	}
	if project == "" {
		return nil, fmt.Errorf("不知道该放进哪个项目：ZCode 会话里没有项目路径，请用 --project 指定")
	}
	title := strings.TrimSpace(opts.TitleOverride)
	if title == "" {
		title = tr.Title
	}

	// ③ 造一个新的会话 id 与正文文件。
	newID := newUUID()
	root, err := wbDataDir(probe, opts.Vendor)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(root, "projects", projectSlug(project))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建项目目录失败：%w", err)
	}
	jsonlPath := filepath.Join(dir, newID+".jsonl")
	if _, err := os.Stat(jsonlPath); err == nil {
		// 新 UUID 撞上已有文件几乎不可能；真撞上了说明有别的问题，别覆盖。
		return nil, fmt.Errorf("目标文件已存在，拒绝覆盖：%s", jsonlPath)
	}

	lines, err := buildJSONL(tr, newID, project, opts.WithReasoning)
	if err != nil {
		return nil, err
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(jsonlPath, []byte(body), 0o644); err != nil {
		return nil, fmt.Errorf("写入对话文件失败：%w", err)
	}

	// ④ 写库建索引。失败要把 jsonl 撤掉，不留"有正文没索引"的孤儿文件。
	row := rowFor(tr, newID, project, title)
	inserted, skipped, err := migrate.InsertRows(probe, target, root, []migrate.Row{row})
	if err != nil {
		_ = os.Remove(jsonlPath)
		return nil, fmt.Errorf("写入会话索引失败（已回滚对话文件）：%w", err)
	}

	res := &Result{
		SessionID: newID,
		JSONLPath: jsonlPath,
		DBPath:    filepath.Join(root, "workbuddy.db"),
		Title:     title,
		Project:   project,
		Msgs:      len(tr.Msgs),
		Lines:     len(lines),
	}
	if len(inserted) == 0 {
		res.Skipped = "索引里已存在同 id 的行，没有覆盖：" + strings.Join(skipped, ", ")
	}
	return res, nil
}

// rowFor 把 ZCode 会话映射成 sessions 表的一行。
//
// 只填 18 个"搬运用得到"的列（migrate.Row 已经定好），其余留 NULL：
// expert_* / plugin_context_json 之类描述的是来源端的运行时上下文，
// 照搬过来没有意义，猜错比留空危险。
func rowFor(tr *zcode.Transcript, id, project, title string) migrate.Row {
	created := tr.CreatedMs
	updated := tr.UpdatedMs
	if created == 0 {
		created = updated
	}
	if updated == 0 {
		updated = created
	}
	status := "Completed"
	sourceMode := "cli"
	mode := "agent"
	permission := "default"
	playground := 0
	t := title
	tt := title
	return migrate.Row{
		ID:     id,
		Cwd:    project,
		UserID: "",
		// 标题两处都给：客户端列表读的是 coalesce(custom_title, title)。
		Title:          &t,
		CustomTitle:    &tt,
		Status:         &status,
		CreatedAt:      created,
		UpdatedAt:      updated,
		LastActivityAt: &updated,
		IsPlayground:   playground,
		SourceMode:     &sourceMode,
		Mode:           &mode,
		PermissionMode: &permission,
	}
}

// buildJSONL 生成对话文件的行。
//
// 每条 ZCode 消息产出一行；assistant 的推理段（若开启）插在它自己那行**之前**
// ——客户端按 time 排序渲染，父消息的 id 要已经出现过才挂得上 parentId。
func buildJSONL(tr *zcode.Transcript, sessionID, project string, withReasoning bool) ([]string, error) {
	var lines []string
	cwd := project

	for _, m := range tr.Msgs {
		at := m.At
		if at == 0 {
			at = tr.UpdatedMs
		}
		msgID := newUUID()

		if withReasoning && m.Role == "assistant" && strings.TrimSpace(m.Reasoning) != "" {
			// ZCode 有推理但我们没有父消息可挂时，挂到会话起点上，
			// 而不是留一个指向不存在 id 的 parentId。
			parent := sessionID
			line := map[string]any{
				"id":        newUUID(),
				"parentId":  parent,
				"timestamp": at,
				"type":      "reasoning",
				"providerData": map[string]any{
					"agent": "cli",
				},
				"content":    []any{},
				"rawContent": []any{map[string]any{"type": "reasoning_text", "text": m.Reasoning}},
				"sessionId":  sessionID,
				"cwd":        cwd,
			}
			b, err := json.Marshal(line)
			if err != nil {
				return nil, err
			}
			lines = append(lines, string(b))
		}

		role := m.Role
		if role != "user" && role != "assistant" {
			role = "assistant"
		}
		text := m.Text
		if text == "" {
			text = "（这一轮没有正文）"
		}
		// 工具调用降级成一行引用：不假装成 function_call（那需要配对字段，
		// 猜错会让客户端渲染崩），但要让人知道这轮动过手。
		if len(m.Tools) > 0 {
			text += "\n\n> 本轮用过的工具：" + strings.Join(m.Tools, "、")
		}
		contentType := "output_text"
		if role == "user" {
			contentType = "input_text"
		}
		line := map[string]any{
			"id":        msgID,
			"timestamp": at,
			"type":      "message",
			"role":      role,
			"content": []any{
				map[string]any{"type": contentType, "text": text},
			},
			"providerData": map[string]any{"agent": "cli"},
			"sessionId":    sessionID,
			"cwd":          cwd,
		}
		b, err := json.Marshal(line)
		if err != nil {
			return nil, err
		}
		lines = append(lines, string(b))
	}
	return lines, nil
}

// requireClientClosed 确认目标档位的客户端没在运行。
//
// 为什么这里比搬运更严：搬运那边是**警告**（"重启后才出现"），因为用户
// 可能已经在搬运流程里走了一半。而导入是个显式的一次性动作，让它静默地
// "导了但看不见"最糟——用户会以为坏了、甚至反复导几遍。
//
// 代价是探测不到进程信息时（非 Windows）放行：探不到 ≠ 在跑。
func requireClientClosed(v sessions.Vendor, running bool) error {
	if !running {
		return nil
	}
	return fmt.Errorf("目标档位（%s）的客户端正在运行，请先完全退出再导入——"+
		"它握着会话列表的缓存，边跑边写会看不到新条目、甚至被回写覆盖", v.Label())
}

// targetVariant 把界面的档位映射成 variant.ID。
func targetVariant(v sessions.Vendor) (variant.ID, error) {
	switch v {
	case sessions.VendorWBCN:
		return variant.CN, nil
	case sessions.VendorWBIntl:
		return variant.Intl, nil
	}
	return "", fmt.Errorf("只能导入到 WorkBuddy 的两侧档位，不支持 %s", v)
}

// wbDataDir 返回目标档位的数据目录。
func wbDataDir(probe *variant.Probe, v sessions.Vendor) (string, error) {
	id, err := targetVariant(v)
	if err != nil {
		return "", err
	}
	if probe == nil {
		probe = variant.DefaultProbe()
	}
	dir := probe.DataDir(id)
	if dir == "" {
		return "", fmt.Errorf("找不到 %s 的数据目录", v.Label())
	}
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("%s 的数据目录不存在（%s）：先启动一次该档位的客户端", v.Label(), dir)
	}
	return dir, nil
}

// slugPattern 对齐客户端自己的目录命名：把路径里的非字母数字压成一个连字符。
//
// 实测的样本：`C:\Users\36230\Desktop\Competition\竞赛汇总`
//
//	→ `c-Users-36230-Desktop-Competition-竞赛汇总`
//
// 注意**中文字符被保留了**，压掉的只有分隔符。用 slug 规则重建时若把中文
// 也替换掉，就会写到一个客户端不认的目录里。
// Go 的 regexp 不认 `\uXXXX` 转义（那是 JS/PCRE 的写法），得用 `\x{...}`。
// 保留：英数 + CJK 汉字 + CJK 标点 + 全角字符。中文项目名很常见，
// 被压掉就会写进一个客户端不认的目录。
var slugPattern = regexp.MustCompile(`[^A-Za-z0-9\x{4e00}-\x{9fff}\x{3000}-\x{303f}\x{ff00}-\x{ffef}]+`)

func projectSlug(p string) string {
	s := strings.TrimSpace(p)
	s = strings.ReplaceAll(s, "\\", "/")
	s = strings.Trim(s, "/")
	// 盘符 `C:` → `c`（实测客户端就是小写盘符打头）。
	if len(s) > 1 && s[1] == ':' {
		s = strings.ToLower(s[:1]) + s[2:]
	}
	s = slugPattern.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "unknown-project"
	}
	return s
}

// newUUID 造一个 v4 UUID。客户端用的是这种形态（会话/消息 id 都是）。
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源都拿不到时别硬造：用一个时间戳兜底也比 panic 好，但要可辨认。
		return fmt.Sprintf("wbmux-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
