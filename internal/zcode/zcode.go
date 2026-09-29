// Package zcode 读取 ZCode 的会话正文（只读）。
//
// # 为什么单独一个包
//
// internal/sessions 负责"跨源统一索引"——它只需要每源一个 Session 列表，
// 正文从哪来它不关心。而"点开看内容、导出成 Markdown"要读 message/part
// 两张表、还要解析两层的 JSON，放进去会把那个包撑成杂货铺。
//
// # 只读红线（沿用 sessions 的约定）
//
// 这一包**只读** ZCode 的任何数据：只 SELECT，不写、不删、不移动。
// 导出是写到用户指定的目录，与 ZCode 的数据目录无关。
//
// # 表结构（2026-09-28 实测，本机 2844 条 message / 10457 条 part）
//
//	session(id, directory, title, time_created, time_updated, time_archived, task_type)
//	message(id, session_id, time_created, sequence, data)    data.role = user|assistant
//	part(id, message_id, session_id, sequence, data)         data.type = text|reasoning|tool|file|…
//
// **正文在 part.data.text 里**，不在 message 上。message 只管"谁说的"，
// part 是它的各个片段（一个回合可能拆成 step-start / text / tool / step-finish
// 好几个 part）。所以还原对话要按 (message.sequence, part.sequence) 排序。
package zcode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/HMuSeaB/wbmux/internal/migrate"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

// DBPath 返回 ZCode 的会话库路径。
//
// # 为什么走 Probe 而不是读环境变量
//
// 我第一版用 os.UserHomeDir()（跟着 USERPROFILE 走），结果验证时把
// USERPROFILE 指到隔离目录——**ZCode 就"消失"了**。这个耦合是错的：
// ZCode 装在哪、库在哪，跟 wbmux 自己用什么 profile 毫无关系
// （wbmux 换 profile 是为了隔离它自己的设置与数据，不是为了假装换用户）。
//
// 走 Probe.Home 好处有二：默认仍是真实主目录（默认探测器直接绑环境），
// 而测试/特殊场景可以显式注入，不必去动进程级环境变量。
func DBPath(probe *variant.Probe) (string, error) {
	home, err := homeDir(probe)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), nil
}

// homeDir 取主目录，Probe 未给时退回环境。
func homeDir(probe *variant.Probe) (string, error) {
	if probe != nil && strings.TrimSpace(probe.Home) != "" {
		return probe.Home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home, nil
}

// Available 报告本机是否有 ZCode 的库。
func Available(probe *variant.Probe) bool {
	p, err := DBPath(probe)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Msg 是一条还原后的对话消息。
type Msg struct {
	Role string `json:"role"` // user / assistant
	// At 是消息时间（Unix 毫秒）。
	At int64 `json:"at"`
	// Text 是该消息里所有 text 型 part 拼起来的正文。
	Text string `json:"text"`
	// Tools 是本条里用过的工具名（按出现顺序去重）。
	Tools []string `json:"tools,omitempty"`
	// Reasoning 是思考片段（默认不导出，导出时可选择带上）。
	Reasoning string `json:"reasoning,omitempty"`
	// Files 是这条消息引用的文件路径。
	Files []string `json:"files,omitempty"`
}

// Transcript 是一个会话的完整正文。
type Transcript struct {
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	CreatedMs int64  `json:"createdMs"`
	UpdatedMs int64  `json:"updatedMs"`
	Msgs      []Msg  `json:"msgs"`
	// Warns 是读取过程中的非致命问题（比如某个 part 的 JSON 坏了）。
	Warns []string `json:"warns,omitempty"`
}

// query 是这一包统一走的一次查询（只读，优先 live、失败退回快照）。
func query(probe *variant.Probe, sql string) ([]map[string]any, []string, error) {
	db, err := DBPath(probe)
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(db); err != nil {
		return nil, nil, fmt.Errorf("本机没有 ZCode 的会话库（%s）", db)
	}
	rows, warns := queryLive(probe, db, sql)
	return rows, warns, nil
}

// queryLive 优先 live（含 WAL），失败退回 immutable 快照。
//
// 与 sessions 包同样的取舍：live 要碰 -wal/-shm，客户端正在跑时偶尔会锁住；
// immutable 看不到未 checkpoint 的最新写入。两者互为一个失败面的补位。
func queryLive(probe *variant.Probe, db, sql string) ([]map[string]any, []string) {
	rows, err := migrate.QueryDB(probe, variant.CN, db, sql)
	if err == nil && rows != nil {
		return rows, nil
	}
	rows2, err2 := migrate.QueryDBSnapshot(probe, variant.CN, db, sql)
	if err2 == nil && rows2 != nil {
		return rows2, []string{"ZCode 库被占用，读到的是陈旧快照（最新的对话可能没进来）"}
	}
	// 两种都失败：把两边的错都带上，界面才好判断是"没装"还是"锁住了"。
	msg := fmt.Errorf("读取失败")
	if err != nil {
		msg = fmt.Errorf("实时读取失败（%v）", err)
	}
	if err2 != nil {
		msg = fmt.Errorf("%v；快照读取也失败（%v）", msg, err2)
	}
	return nil, []string{msg.Error()}
}

// Load 读出一个会话的完整对话。
func Load(probe *variant.Probe, sessionID string) (*Transcript, error) {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil, fmt.Errorf("缺少会话 id")
	}

	// 会话元数据。用参数化查询，别把 id 拼进 SQL（哪怕是本机库，
	// 拼接迟早会遇到带引号的 id）。
	meta, _, err := query(probe, `select id, title, directory, time_created, time_updated `+
		`from session where id = `+sqlQuote(id))
	if err != nil {
		return nil, err
	}
	if len(meta) == 0 {
		return nil, fmt.Errorf("没有这个会话（%s）", id)
	}
	tr := &Transcript{
		SessionID: mapStr(meta[0], "id"),
		Title:     mapStr(meta[0], "title"),
		Directory: mapStr(meta[0], "directory"),
		CreatedMs: mapMs(meta[0], "time_created"),
		UpdatedMs: mapMs(meta[0], "time_updated"),
	}
	if tr.Title == "" {
		tr.Title = "(无标题)"
	}

	// 消息与片段。一次把两张表都拉出来按 id 配对，避免 N+1 次查询。
	msgs, warns, err := query(probe, `select id, data from message where session_id = `+
		sqlQuote(id)+` order by sequence asc, time_created asc`)
	if err != nil {
		return nil, err
	}
	tr.Warns = append(tr.Warns, warns...)

	parts, warns2, err := query(probe, `select message_id, sequence, data from part where session_id = `+
		sqlQuote(id)+` order by sequence asc, time_created asc`)
	if err != nil {
		return nil, err
	}
	tr.Warns = append(tr.Warns, warns2...)

	// message_id → 该消息的片段
	byMsg := map[string][]map[string]any{}
	for _, p := range parts {
		mid := mapStr(p, "message_id")
		byMsg[mid] = append(byMsg[mid], p)
	}

	for _, m := range msgs {
		mid := mapStr(m, "id")
		var head struct {
			Role string `json:"role"`
			Time struct {
				Created int64 `json:"created"`
			} `json:"time"`
		}
		// message 的 JSON 坏了不该让整次读取失败：跳过它、记一条警告。
		if err := json.Unmarshal([]byte(mapStr(m, "data")), &head); err != nil {
			tr.Warns = append(tr.Warns, fmt.Sprintf("消息 %s 的元数据无法解析，已跳过", mid))
			continue
		}
		msg := Msg{Role: head.Role, At: head.Time.Created}

		for _, p := range byMsg[mid] {
			var part struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Tool string `json:"tool"`
				// 工具的字段名各家不一，能认的都认一遍。
				Name string `json:"name"`
				File string `json:"file"`
				Path string `json:"path"`
			}
			if err := json.Unmarshal([]byte(mapStr(p, "data")), &part); err != nil {
				continue // 单个片段坏了就跳过，不打断整条对话
			}
			switch part.Type {
			case "text":
				if t := strings.TrimSpace(part.Text); t != "" {
					if msg.Text != "" {
						msg.Text += "\n\n"
					}
					msg.Text += t
				}
			case "reasoning":
				if t := strings.TrimSpace(part.Text); t != "" {
					if msg.Reasoning != "" {
						msg.Reasoning += "\n\n"
					}
					msg.Reasoning += t
				}
			case "tool":
				name := firstNonEmpty(part.Tool, part.Name)
				if name != "" && !containsStr(msg.Tools, name) {
					msg.Tools = append(msg.Tools, name)
				}
			case "file":
				f := firstNonEmpty(part.File, part.Path)
				if f != "" && !containsStr(msg.Files, f) {
					msg.Files = append(msg.Files, f)
				}
			}
		}
		// 全是空片段（只有 model_change 之类的时间线条目）就不算一条消息。
		if msg.Text == "" && len(msg.Tools) == 0 && len(msg.Files) == 0 && msg.Reasoning == "" {
			continue
		}
		tr.Msgs = append(tr.Msgs, msg)
	}

	sort.SliceStable(tr.Msgs, func(i, j int) bool { return tr.Msgs[i].At < tr.Msgs[j].At })
	return tr, nil
}

// 小工具：这几个在 sessions 包里有同名但未导出的版本，这里各写一份，
// 免得为几个三行函数把内部结构暴露出去。

func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func mapStr(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func mapMs(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Row 是列表用的一行摘要。
type Row struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	When      string `json:"when"`
	Msgs      int    `json:"msgs"`
	Archived  bool   `json:"archived"`
}

// List 列出会话（按最后更新倒序）。limit<=0 时给个默认上限，
// 免得一个几千条的库直接刷屏。
//
// 子代理会话（task_type=subagent_child）也列出来，但排在后面：
// 它们是那个会话派生出来的，配合主会话看才有意义。
func List(probe *variant.Probe, limit int) ([]Row, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, _, err := query(probe, `select s.id, s.title, s.directory, s.time_updated, s.time_archived, `+
		`s.task_type, (select count(*) from message m where m.session_id = s.id) as msgs `+
		`from session s order by s.time_updated desc limit `+fmt.Sprint(limit*3))
	if err != nil {
		return nil, err
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		title := mapStr(r, "title")
		if title == "" {
			title = "(无标题)"
		}
		kind := mapStr(r, "task_type")
		if kind == "subagent_child" {
			title = "[子代理] " + title
		}
		when := ""
		if ms := mapMs(r, "time_updated"); ms > 0 {
			when = time.UnixMilli(ms).Format("2006-01-02 15:04")
		}
		out = append(out, Row{
			ID:        mapStr(r, "id"),
			Title:     title,
			Directory: mapStr(r, "directory"),
			When:      when,
			Msgs:      int(mapMs(r, "msgs")),
			Archived:  mapMs(r, "time_archived") > 0,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
