package sessions

// Qoder（阿里系的 agentic IDE）的 Quest 对话在
// SharedClientCache/cache/db/local.db 的四张 chat_* 表里。
//
// # 能拿到什么、拿不到什么（2026-10-02 实测）
//
// chat_session 全是明文：session_title、project_uri/project_name（项目
// 归属直接给，不用猜）、gmt_create/gmt_modified（毫秒）、mode——元数据
// 完整，会话中心照常归组/标记候选。
//
// chat_message.content 是**密文**（base64 载荷，实测非明文非 UTF-8）——
// 所以 Qoder 会话**不支持打开查看**，CanRenderInline 不收这个源。
// 解密需要 Qoder 客户端的密钥管理，逆向不在这个工具的范围内。

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// qoderSessionQuery 读 Qoder 的会话清单。项目归属来自 project_uri
// （workspace 的本地路径），缺失时退回 project_name。
const qoderSessionQuery = `select session_id, session_title, ` +
	`coalesce(project_uri, project_name) as project, ` +
	`gmt_create, gmt_modified, mode from chat_session order by gmt_modified desc`

func defaultQoderDBPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "AppData", "Roaming", "Qoder", "SharedClientCache", "cache", "db", "local.db")
}

// scanQoder 扫 Qoder 的 Quest 会话（元数据；正文加密不可读）。
func scanQoder(probe *variant.Probe) ([]Session, []string, error) {
	dbPath := defaultQoderDBPath()
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil, nil // 没装 Qoder 不算错
	}
	rows, warns := queryLive(probe, variant.CN, dbPath, qoderSessionQuery, "Qoder")
	if rows == nil {
		return nil, warns, fmt.Errorf("数据库读取失败")
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		s := Session{
			Vendor:     VendorQoder,
			ProjectRaw: mapStr(r, "project"),
			ID:         mapStr(r, "session_id"),
			Title:      mapStr(r, "session_title"),
			CreatedMs:  mapMs(r, "gmt_create"),
			UpdatedMs:  mapMs(r, "gmt_modified"),
			Kind:       mapStr(r, "mode"),
			Source:     Source{Kind: "sqlite", Path: dbPath},
		}
		if s.Title == "" {
			s.Title = "(无标题)"
		}
		out = append(out, s)
	}
	return out, warns, nil
}
