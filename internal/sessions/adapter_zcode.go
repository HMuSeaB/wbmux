package sessions

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// zcodeQuery 读 ZCode 的会话表（schema 于 2026-09-28 实测）。
//
// - directory 就是项目路径，不用 join 任何表。
// - task_type 区分 interactive（真人对话）与 subagent_child（子代理）：
//   都收进来，界面默认滤掉子代理——它也是历史，但通常是噪音。
// - time_archived 非空即已归档。ZCode 的归档只清 checkpoint、会话本体
//   仍在（可恢复查看），所以归档会话照样展示，只打标记。
const zcodeQuery = `select id, directory, title, time_created, time_updated, ` +
	`time_archived, task_type from session order by time_updated desc`

// scanZCode 用 WorkBuddy 自带的 better-sqlite3 读 ZCode 的库。
//
// ZCode 自己没捆 better-sqlite3（app.asar.unpacked 里只有 node-pty 和
// sharp，2026-09-28 实测），但执行桥只认 ABI 不认库是谁建的——
// 运行时从 WorkBuddy 安装里借，库路径指向 ~/.zcode，两不相干。
func scanZCode(probe *variant.Probe) ([]Session, []string, error) {
	dbPath, err := zcodeDBPath()
	if err != nil {
		return nil, nil, err
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, nil, nil // 没装 ZCode 不算错，就是没这个源
	}
	rows, warns := queryLive(probe, variant.CN, dbPath, zcodeQuery, "ZCode")
	if rows == nil {
		return nil, warns, fmt.Errorf("数据库读取失败")
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		s := Session{
			Vendor:     VendorZCode,
			ProjectRaw: mapStr(r, "directory"),
			ID:         mapStr(r, "id"),
			Title:      mapStr(r, "title"),
			CreatedMs:  mapMs(r, "time_created"),
			UpdatedMs:  mapMs(r, "time_updated"),
			Kind:       mapStr(r, "task_type"),
			Archived:   mapF64(r, "time_archived") > 0,
		}
		if s.Title == "" {
			s.Title = "(无标题)"
		}
		s.Source = Source{Kind: "sqlite", Path: dbPath}
		out = append(out, s)
	}
	return out, warns, nil
}

func zcodeDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), nil
}
