package sessions

import (
	"fmt"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// wbQuery 读两侧 WorkBuddy 的会话索引。
//
//   - custom_title 优先：用户自己起的名比自动标题值钱。
//   - deleted_at 有两种"没删"：本地行为 NULL，云同步行为 -1
//     （客户端自己的云侧索引 `WHERE deleted_at = -1` 是实据）。
//     漏了哪一种都会少一截会话，所以两个条件都要。
//   - is_playground 保留但打标记：试玩会话也是历史，只是界面上可过滤。
const wbQuery = `select id, cwd, coalesce(custom_title, title) as title, ` +
	`created_at, updated_at, is_playground, status ` +
	`from sessions where deleted_at is null or deleted_at = -1 ` +
	`order by updated_at desc`

// wbUsageQuery 读 session_usage 的每会话消耗。used 的刻度与额度面板
// 一致：面板显示的"积分"= used/1000（实测 408.04 积分的会话，used
// ≈ 408040）。查询失败静默——积分是锦上添花，不能让它挡住会话列表。
const wbUsageQuery = `select session_id, used from session_usage`

// wbUsageByID 把每会话消耗读成 session_id → used 映射。
func wbUsageByID(probe *variant.Probe, id variant.ID, dbPath, label string) map[string]float64 {
	rows, _ := queryLive(probe, id, dbPath, wbUsageQuery, label)
	out := map[string]float64{}
	for _, r := range rows {
		if sid := mapStr(r, "session_id"); sid != "" {
			out[sid] = mapF64(r, "used")
		}
	}
	return out
}

// scanWB 扫一侧 WorkBuddy 的会话（两侧 schema 同构，2026-09-28 实测）。
func scanWB(probe *variant.Probe, id variant.ID, vendor Vendor) ([]Session, []string, error) {
	dbPath := probe.DataDir(id) + "\\workbuddy.db"
	rows, warns := queryLive(probe, id, dbPath, wbQuery, vendor.Label())
	if rows == nil {
		return nil, warns, fmt.Errorf("数据库读取失败")
	}
	usedByID := wbUsageByID(probe, id, dbPath, vendor.Label())
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		s := Session{
			Vendor:     vendor,
			ProjectRaw: mapStr(r, "cwd"),
			ID:         mapStr(r, "id"),
			Title:      mapStr(r, "title"),
			CreatedMs:  mapMs(r, "created_at"),
			UpdatedMs:  mapMs(r, "updated_at"),
			Kind:       mapStr(r, "status"),
			Credits:    usedByID[mapStr(r, "id")] / 1000,
		}
		if mapInt(r, "is_playground") == 1 {
			s.Kind = "playground"
		}
		if s.Title == "" {
			s.Title = "(无标题)"
		}
		s.Source = Source{Kind: "sqlite", Path: dbPath}
		out = append(out, s)
	}
	return out, warns, nil
}

// queryLive 优先读实时快照（含 WAL），失败退回 immutable 陈旧快照。
//
// 为什么需要兜底：live 读取要碰 -wal/-shm，个别情况下会被运行中的
// 客户端锁死（wbmux 的既有实测：客户端在跑时宿主档位 live 读会失败）；
// 而 immutable 又看不到 WAL 里没 checkpoint 的最新会话——两种模式
// 各有一个失败面，互为补位。退回时返回一条警告，界面能看到
// "这源的数据是陈旧快照"，不至于对不上账还找不到原因。
func queryLive(probe *variant.Probe, id variant.ID, dbPath, sql, label string) ([]map[string]any, []string) {
	rows, err := migrate.QueryDB(probe, id, dbPath, sql)
	if err == nil {
		return rows, nil
	}
	rows2, err2 := migrate.QueryDBSnapshot(probe, id, dbPath, sql)
	if err2 == nil {
		return rows2, []string{label + "：实时读取失败（" + headStr(err.Error(), 120) + "），已退回陈旧快照，最新会话可能缺失"}
	}
	// 两条路都断：把 live 的错误交上去——它比 immutable 的更有诊断价值。
	return nil, []string{label + "：读取失败（" + headStr(err.Error(), 160) + "）"}
}

func headStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// mapStr 取字符串列。JSON 反序列化后数字是 float64、缺列是 nil，
// 这里统一兜底：缺列/类型不对都按空串处理，绝不让一行脏数据炸掉整源。
func mapStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

// mapMs 取毫秒时间戳列。better-sqlite3 把整数当 JS number 给出，
// 过桥后是 float64；毫秒值远小于 2^53，转 int64 无精度损失。
func mapMs(m map[string]any, key string) int64 {
	return int64(mapF64(m, key))
}

func mapInt(m map[string]any, key string) int64 {
	return int64(mapF64(m, key))
}

func mapF64(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	f, ok := v.(float64)
	if !ok {
		return 0
	}
	return f
}
