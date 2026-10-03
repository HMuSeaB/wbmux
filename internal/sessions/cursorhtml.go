package sessions

// 把一个 Cursor composer 还原成与 Codex 同构的转录。
//
// # 数据从哪来
//
// composerData 行里有 fullConversationHeadersOnly——气泡的**有序**清单
// （bubbleId + type）；正文在各自的 bubbleId:<composer>:<bid> 行里。
// 两步读：先拿顺序，再按 id 取正文。实测（2026-10-02）大会话有 355/548
// 个气泡，30738 条 type=2 气泡里 29366 条 text 为空——那是工具/检查点泡，
// 正文只在少数气泡里（116 条超过 500 字符）。
//
// # 为什么要防御性解析
//
// 实测库里存在 value 不是合法 JSON 的 bubble 行（json_each 直接报
// "malformed JSON"）——单条坏数据跳过，不能炸掉整个会话的渲染。
//
// # 窗口
//
// 大会话全渲染是自找卡顿（与 Codex 的 defaultViewTurns 同一取舍）：
// 窗口取最近 maxTurns 个**有正文**的气泡，TotalTurns 报真实总数，
// 页面上给"还有更早的"入口。

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// cursorIDRe 限定 composer id 的形状（UUID 类：十六进制与连字符）。
// 拼进 LIKE 模式前先过这道闸，风格与"不拿 id 拼路径"同一套谨慎。
var cursorIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

// ParseCursorTail 带窗口：只保留最近 maxTurns 个**有正文**的气泡
// （≤0 时按 120 处理）。查看界面用这个；导出走 ParseCursorTailAll。
func ParseCursorTail(probe *variant.Probe, dbPath, composerID string, maxTurns int) (*CodexTranscript, error) {
	if maxTurns <= 0 {
		maxTurns = 120
	}
	tr, err := parseCursorTailAll(probe, dbPath, composerID)
	if err != nil {
		return nil, err
	}
	if len(tr.Turns) > maxTurns {
		tr.Truncated = true
		tr.Turns = tr.Turns[len(tr.Turns)-maxTurns:]
	}
	return tr, nil
}

// parseCursorTailAll 读一个 Cursor composer 的全部有正文气泡，按对话顺序
// 还原成与 Codex 同构的转录，**不设窗口**。Truncated 恒为假。
func parseCursorTailAll(probe *variant.Probe, dbPath, composerID string) (*CodexTranscript, error) {
	if !cursorIDRe.MatchString(composerID) {
		return nil, errors.New("composer id 形状异常")
	}

	// 第一步：composerData——顺序清单与元数据。
	compRows, err := migrateQueryCursor(probe, dbPath,
		`select value from cursorDiskKV where key = 'composerData:' || ?`, composerID)
	if err != nil {
		return nil, err
	}
	if len(compRows) == 0 {
		return nil, errors.New("找不到这个 composer（可能已被 Cursor 删除）")
	}
	var comp struct {
		Name          string  `json:"name"`
		CreatedAt     float64 `json:"createdAt"`
		LastUpdatedAt float64 `json:"lastUpdatedAt"`
		Headers       []struct {
			BubbleID string `json:"bubbleId"`
			Type     int    `json:"type"`
		} `json:"fullConversationHeadersOnly"`
	}
	if err := json.Unmarshal([]byte(mapStr(compRows[0], "value")), &comp); err != nil {
		return nil, fmt.Errorf("composer 元数据损坏：%w", err)
	}

	// 第二步：这个 composer 的全部气泡，按 bubbleId 建索引。
	bubbleRows, err := migrateQueryCursor(probe, dbPath,
		`select value from cursorDiskKV where key like 'bubbleId:' || ? || ':%'`, composerID)
	if err != nil {
		return nil, err
	}
	type bubble struct {
		kind string // user | assistant
		text string
	}
	bubbles := map[string]bubble{}
	for _, r := range bubbleRows {
		var b struct {
			BubbleID string `json:"bubbleId"`
			Type     int    `json:"type"`
			Text     string `json:"text"`
		}
		// 坏 JSON 静默跳过：实测库里就有这种行。
		if json.Unmarshal([]byte(mapStr(r, "value")), &b) != nil {
			continue
		}
		kind := "assistant"
		if b.Type == 1 {
			kind = "user"
		}
		bubbles[b.BubbleID] = bubble{kind: kind, text: strings.TrimSpace(b.Text)}
	}

	// 第三步：按 headers 的顺序还原，跳过空泡（工具/检查点）。
	all := make([]CodexTurn, 0, len(comp.Headers))
	for _, h := range comp.Headers {
		b, ok := bubbles[h.BubbleID]
		if !ok || b.text == "" {
			continue // 不在库里的、空的工具泡：都不进正文
		}
		all = append(all, CodexTurn{Kind: b.kind, Text: b.text})
	}

	tr := &CodexTranscript{
		ID:         composerID,
		Source:     dbPath,
		Turns:      all,
		TotalTurns: len(comp.Headers),
	}
	if comp.CreatedAt > 0 {
		tr.CreatedAt = time.UnixMilli(int64(comp.CreatedAt))
	}
	if comp.LastUpdatedAt > 0 {
		tr.UpdatedAt = time.UnixMilli(int64(comp.LastUpdatedAt))
	}
	return tr, nil
}

// migrateQueryCursor 走统一的桥执行器读 Cursor 库（live/snapshot 兜底
// 与 WB 侧同款，见 adapter_wb 的 queryLive）。
func migrateQueryCursor(probe *variant.Probe, dbPath, sql string, args ...string) ([]map[string]any, error) {
	params := make([]any, len(args))
	for i, a := range args {
		params[i] = a
	}
	rows, err := migrate.QueryDB(probe, variant.CN, dbPath, sql, params...)
	if err != nil {
		rows2, err2 := migrate.QueryDBSnapshot(probe, variant.CN, dbPath, sql, params...)
		if err2 == nil {
			return rows2, nil
		}
		return nil, err
	}
	return rows, nil
}
