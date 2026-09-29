package migrate

import (
	"path/filepath"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// InsertRows 往某个档位的 sessions 表里插入若干行（只增不改）。
//
// 与搬运共用同一条写入路径（detectRuntime + writeRows），所以：
//   - 只写迁移用得到的那 18 列，其余留 NULL；
//   - INSERT OR IGNORE + 先查再插，**已存在的 id 一律跳过**，如实回报；
//   - 整批一个事务，失败整体回滚（半个索引比没有索引更糟）。
//
// 调用方负责确保目标客户端没在运行——见 zimport 里的说明。
func InsertRows(probe *variant.Probe, id variant.ID, dataDir string, rows []Row) (inserted, skipped []string, err error) {
	rt, err := detectRuntime(probe, id)
	if err != nil {
		return nil, nil, err
	}
	return writeRows(rt, filepath.Join(dataDir, "workbuddy.db"), rows)
}
