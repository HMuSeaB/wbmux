package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "embed"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

//go:embed assets/sqlite.js
var sqliteScript []byte

// sqliteSeq 是临时脚本文件的唯一序号，见 runSQLite 里的说明。
var sqliteSeq atomic.Uint64

// nodeModeEnv 是让 Electron 主程序退化成普通 Node 运行时的开关。
const nodeModeEnv = "ELECTRON_RUN_AS_NODE"

// Row 是 sessions 表里的一行会话索引，字段与建表语句逐列对应。
//
// 指针字段对应可空列：源库里是 NULL 的，写过去也该是 NULL，
// 用零值代替会把"没设过"变成"设成了空字符串"，语义不同。
//
// 刻意只覆盖搬运需要的列。expert_* / plugin_context_json / session_settings
// 等列保持 NULL——它们描述的是来源端的运行时上下文（专家绑定、插件上下文），
// 照搬到另一个账号下没有意义，而猜错比留空危险得多。
type Row struct {
	ID             string  `json:"id"`
	Cwd            string  `json:"cwd"`
	UserID         string  `json:"user_id"`
	Title          *string `json:"title"`
	CustomTitle    *string `json:"custom_title"`
	Status         *string `json:"status"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
	LastActivityAt *int64  `json:"last_activity_at"`
	IsPlayground   int     `json:"is_playground"`
	SourceMode     *string `json:"source_mode"`
	Mode           *string `json:"mode"`
	Model          *string `json:"model"`
	PermissionMode *string `json:"permission_mode"`
	UseSandboxCLI  *int    `json:"use_sandbox_cli"`
	AddonSelection *string `json:"addon_selection"`
	ContextWindow  *int    `json:"context_window"`
	ThoughtLevel   *string `json:"thought_level"`
	// Transport 决定这行是"本地会话"还是"云端任务"。客户端本地列表只认
	// 'local'（见 assets/sqlite.js 的 COLUMNS 注释）；留空＝侧栏看不见。
	Transport *string `json:"transport"`
}

// runtimePaths 是执行数据库读写所需的一组路径。
//
// 三个路径必须来自**同一处安装**：Electron 主程序与 better_sqlite3.node
// 是同一次构建的产物，ABI 必须匹配。混用两处安装（版本可能不同）会直接崩，
// 而崩溃发生在子进程里，报错信息未必传得回来。
type runtimePaths struct {
	exe           string
	libPath       string
	nativeBinding string
}

func (rt runtimePaths) usable() bool {
	return rt.exe != "" && rt.libPath != "" && rt.nativeBinding != ""
}

// detectRuntime 找一处自带 better-sqlite3 的安装。
//
// 优先用目标档位的安装（与目标数据库同一次构建最稳妥），
// 找不到再退到另一档位——这正是"只有一份安装"场景下的常态。
func detectRuntime(probe *variant.Probe, prefer variant.ID) (runtimePaths, error) {
	order := []variant.ID{prefer}
	if other, err := prefer.Other(); err == nil {
		order = append(order, other)
	}

	var tried []string
	for _, id := range order {
		inst := probe.Detect(id, "")
		if !inst.Found {
			tried = append(tried, string(id)+": 未安装")
			continue
		}
		rt := pathsFor(inst)
		if probe.Exists(rt.exe) && probe.Exists(rt.libPath) && probe.Exists(rt.nativeBinding) {
			return rt, nil
		}
		tried = append(tried, string(id)+": 安装内缺少 better-sqlite3")
	}
	return runtimePaths{}, fmt.Errorf("找不到可读写数据库的客户端安装（%s）", strings.Join(tried, "；"))
}

// pathsFor 由一处安装推导出三个运行时路径。
func pathsFor(inst variant.Install) runtimePaths {
	base := filepath.Join(inst.ResourcesDir, "app.asar.unpacked", "node_modules", "better-sqlite3")
	return runtimePaths{
		exe:           inst.Executable,
		libPath:       filepath.Join(base, "lib", "index.js"),
		nativeBinding: filepath.Join(base, "build", "Release", "better_sqlite3.node"),
	}
}

// sqliteSpec 是传给内嵌脚本的参数。
type sqliteSpec struct {
	Mode          string `json:"mode"`
	DBPath        string `json:"dbPath"`
	LibPath       string `json:"libPath"`
	NativeBinding string `json:"nativeBinding"`
	Rows          []Row  `json:"rows,omitempty"`
	// SQL 与 Params 只给 query 模式用。
	SQL    string `json:"sql,omitempty"`
	Params []any  `json:"params,omitempty"`
	// Live 为 true 时 query 模式跳过 immutable，读"含 WAL 的实时快照"。
	// 默认（缺省/false）维持 immutable——与既有 Query 行为一致。
	Live bool `json:"live,omitempty"`
	// Statements 与 Tables 只给 update 模式用（工作区维护）。
	//
	// Tables 是**白名单**：update 模式只允许改这些表。把关放在脚本里而不是
	// 只靠调用方自觉——这个口子改的是用户的会话索引，写坏了就是"历史会话
	// 全消失"。
	Statements []Statement `json:"statements,omitempty"`
	Tables     []string    `json:"tables,omitempty"`
}

// Statement 是一条参数化的改写语句。值一律走 Params，不接受字面量拼接。
type Statement struct {
	SQL    string `json:"sql"`
	Params []any  `json:"params,omitempty"`
}

// sqliteResult 是脚本的输出。
type sqliteResult struct {
	Error    string   `json:"error"`
	Rows     []Row    `json:"rows"`
	Inserted []string `json:"inserted"`
	Skipped  []string `json:"skipped"`
	Changed  []int    `json:"changed"`
	// Result 是 query 模式查到的行，每行是一个"列名 → 值"的字典。
	// 之所以用字典而不是结构体：调用方要读的表各不相同，
	// 写死结构体会漏字段，漏了还不容易发现。
	Result []map[string]any `json:"result"`
}

// runSQLite 通过客户端自带的 Electron 在 Node 模式下执行一次数据库操作。
func runSQLite(rt runtimePaths, spec sqliteSpec) (sqliteResult, error) {
	if !rt.usable() {
		return sqliteResult{}, errors.New("客户端缺少可用的 better-sqlite3，无法读写数据库")
	}

	raw, err := json.Marshal(spec)
	if err != nil {
		return sqliteResult{}, err
	}

	// 脚本与参数写到 wbmux 自己的设置目录，而不是系统临时目录：
	// 这样出问题时用户能自己去看这两个文件，也便于整目录清理。
	//
	// 文件名必须每次唯一：会话中心会**并行**跑三个 query（cn/intl/zcode
	// 各起一个 Electron），曾经共用一对固定文件名，结果后写的 spec 覆盖
	// 先写的——先起的进程读到别人的 spec，拿着 A 的 SQL 去查 B 的库，
	// 返回的是"看起来成功"的错误数据（2026-09-28 实测：三家都吐了
	// 国际档的 2 条会话）。用完即删，不让 tmp 目录积垃圾。
	seq := sqliteSeq.Add(1)
	dir, err := config.Dir()
	if err != nil {
		return sqliteResult{}, err
	}
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return sqliteResult{}, fmt.Errorf("创建临时目录失败: %w", err)
	}
	scriptPath := filepath.Join(tmp, fmt.Sprintf("sqlite-%d.js", seq))
	specPath := filepath.Join(tmp, fmt.Sprintf("sqlite-%d.json", seq))
	defer func() {
		_ = os.Remove(scriptPath)
		_ = os.Remove(specPath)
	}()
	if err := os.WriteFile(scriptPath, sqliteScript, 0o600); err != nil {
		return sqliteResult{}, fmt.Errorf("写出脚本失败: %w", err)
	}
	if err := os.WriteFile(specPath, raw, 0o600); err != nil {
		return sqliteResult{}, fmt.Errorf("写出参数失败: %w", err)
	}

	// 超时兜底：Electron 在极端情况下可能起不来也不报错，
	// 没有超时就会让界面永远转圈。
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, rt.exe, scriptPath, specPath)
	cmd.Env = append(envWithout(os.Environ(), nodeModeEnv), nodeModeEnv+"=1")
	hideWindow(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// 脚本自身的失败会以 JSON 写进 stdout，能解析就优先用它；
		// 走到这里说明连脚本都没跑起来，此时 stderr 信息量最大。
		if res, perr := parseResult(stdout.Bytes()); perr == nil {
			if res.Error != "" {
				return sqliteResult{}, errors.New(res.Error)
			}
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return sqliteResult{}, fmt.Errorf("数据库脚本执行失败: %s", head(msg, 300))
	}

	res, err := parseResult(stdout.Bytes())
	if err != nil {
		return sqliteResult{}, err
	}
	if res.Error != "" {
		return sqliteResult{}, errors.New(res.Error)
	}
	return res, nil
}

// parseResult 从子进程输出里取出最后一行可解析的 JSON。
//
// 不直接 Unmarshal 整段输出：Electron 在 Node 模式下也可能往 stdout 打
// 自己的告警，那样整段就不是合法 JSON 了。脚本的结论总是最后一行。
func parseResult(raw []byte) (sqliteResult, error) {
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var res sqliteResult
		if json.Unmarshal([]byte(line), &res) == nil {
			return res, nil
		}
	}
	return sqliteResult{}, errors.New("数据库脚本没有输出可解析的结果")
}

// envWithout 复制一份环境变量并去掉指定键。
//
// 不能直接把新值 append 到末尾了事：Windows 的环境变量名不区分大小写，
// 而 exec 对重复键取哪个值并无保证。留着旧值可能让 ELECTRON_RUN_AS_NODE
// 失效，Electron 就当成正常 GUI 启动——表现是脚本永不返回，卡到超时。
func envWithout(env []string, key string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 && strings.EqualFold(kv[:i], key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// head 截断过长文本，避免把整屏 stderr 塞进界面。
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// readRows 读出某档位数据目录里的全部会话索引行。
func readRows(rt runtimePaths, dbPath string) ([]Row, error) {
	res, err := runSQLite(rt, sqliteSpec{
		Mode:          "read",
		DBPath:        dbPath,
		LibPath:       rt.libPath,
		NativeBinding: rt.nativeBinding,
	})
	if err != nil {
		return nil, err
	}
	return res.Rows, nil
}

// HasDB 判断某一侧有没有建好数据库。
//
// 存在的理由：调用方常需要**先问一句"那儿有库吗"，再决定要不要发查询**。
// 库不存在是正常状态（那一侧从没登录过），不该跟"查询失败"混为一谈——
// 混在一起就会让"某侧没登录"变成界面上一条常驻的红色警告。
//
// 只做文件存在性判断，不打开连接：调用方本来也只是想避免那个必然失败的查询。
func HasDB(probe *variant.Probe, id variant.ID) bool {
	return fileExists(filepath.Join(probe.DataDir(id), "workbuddy.db"))
}

// Query 在指定档位的数据库上跑一条**只读**查询。
//
// 存在的理由：搬运只需要 sessions 表，但有些信息在别的表里——
// 比如额度统计要看 session_usage.credit_json。与其为每张表各写一套读取逻辑，
// 不如开一个受约束的口子。
//
// 约束有三层，缺一不可：
//   - 连接以 readonly 打开
//   - 脚本侧只放行以 SELECT 开头的单条语句
//   - 这里同样先做一次检查，不把把关的责任全推给脚本
//
// 调用方不要把它当成通用数据库接口：它是为了读几个已知的表才存在的。
func Query(probe *variant.Probe, id variant.ID, sql string, params ...any) ([]map[string]any, error) {
	// SELECT 把关要走在"库存不存在"前面：非法语句与"库还没建"是两回事，
	// 报错必须指对方向（TestQueryRejectsNonSelect 钉住了这个次序）。
	// 与 QueryDB 里的检查看似重复，实则各守各的入口——QueryDB 的直接
	// 调用方（如会话中心）不该依赖 Query 帮它把关。
	trimmed := strings.TrimSpace(sql)
	if !strings.HasPrefix(strings.ToLower(trimmed), "select") {
		return nil, fmt.Errorf("Query 只接受 SELECT，收到 %q", trimmed)
	}
	dbPath := filepath.Join(probe.DataDir(id), "workbuddy.db")
	if !fileExists(dbPath) {
		return nil, fmt.Errorf("%s 侧还没有数据库：%s", id, dbPath)
	}
	return QueryDB(probe, id, dbPath, sql, params...)
}

// QueryDB 与 Query 的约束完全相同，只是数据库路径由调用方显式给出，
// 且默认读**实时快照**（含 WAL）。
//
// 存在的理由：会话中心要读的不止 WorkBuddy 自己的库——ZCode 的
// db.sqlite 是另一家客户端用另一套 schema 建的，但同样是 sqlite。
// 执行桥（客户端自带的 better-sqlite3）只认 ABI，不认库是谁建的，
// 所以运行时从 WorkBuddy 安装里借，库路径却可以指向别处。
//
// 为什么默认 live 而不是 immutable：ZCode 客户端常驻，最近会话长期
// 躺在 WAL 里没 checkpoint，immutable 快照只看得到零星几条
// （2026-09-28 实测 2/26）。live 需要 -shm 可用——正在运行的客户端
// 自己维护着它，客户端关了也可由可写目录重建；真失败时调用方退回
// QueryDBSnapshot 即可。
//
// 路径放开后，"只读"约束就更重了：这个口子能碰到任意 sqlite 文件，
// 调用方必须自己保证传进来的 SQL 只是查询。会话中心（internal/sessions）
// 是目前唯一的调用方，全部语句写死在代码里，不接受用户输入拼接。
func QueryDB(probe *variant.Probe, id variant.ID, dbPath, sql string, params ...any) ([]map[string]any, error) {
	return queryDB(probe, id, dbPath, true, sql, params...)
}

// QueryDBSnapshot 与 QueryDB 相同，但读 immutable 陈旧快照——
// 跳过 WAL，客户端怎么跑都读得到，代价是最新写入看不见。
// 作为 live 读取失败时的退路存在。
func QueryDBSnapshot(probe *variant.Probe, id variant.ID, dbPath, sql string, params ...any) ([]map[string]any, error) {
	return queryDB(probe, id, dbPath, false, sql, params...)
}

func queryDB(probe *variant.Probe, id variant.ID, dbPath string, live bool, sql string, params ...any) ([]map[string]any, error) {
	trimmed := strings.TrimSpace(sql)
	if !strings.HasPrefix(strings.ToLower(trimmed), "select") {
		return nil, fmt.Errorf("QueryDB 只接受 SELECT，收到 %q", trimmed)
	}
	if !fileExists(dbPath) {
		return nil, fmt.Errorf("数据库不存在：%s", dbPath)
	}

	// 优先用这一档位自己的安装：与它自己的数据库同一次构建，ABI 一致。
	// 目标库不是这一档位的也没关系——better-sqlite3 读的是文件格式，
	// 与"库是谁建的"无关。
	rt, err := detectRuntime(probe, id)
	if err != nil {
		return nil, err
	}

	res, err := runSQLite(rt, sqliteSpec{
		Mode:          "query",
		DBPath:        dbPath,
		LibPath:       rt.libPath,
		NativeBinding: rt.nativeBinding,
		SQL:           sql,
		Params:        params,
		Live:          live,
	})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res.Result, nil
}

// Update 执行一批参数化的改写语句，返回每条影响的行数。
//
// 只允许 UPDATE、只允许动 tables 白名单里的表，值必须走 Params——
// 三条约束都在 scripts/assets/sqlite.js 里强制，不依赖调用方自觉。
// 整批一个事务，中途失败整体回滚。
//
// 调用方必须自己保证：客户端已退出、已备份数据库。这两件事这个函数管不了。
func Update(probe *variant.Probe, id variant.ID, tables []string, stmts []Statement) ([]int, error) {
	rt, err := detectRuntime(probe, id)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(probe.DataDir(id), "workbuddy.db")
	if !fileExists(dbPath) {
		return nil, fmt.Errorf("%s 侧还没有数据库：%s", id, dbPath)
	}
	if len(stmts) == 0 {
		return nil, nil
	}
	res, err := runSQLite(rt, sqliteSpec{
		Mode:          "update",
		DBPath:        dbPath,
		LibPath:       rt.libPath,
		NativeBinding: rt.nativeBinding,
		Statements:    stmts,
		Tables:        tables,
	})
	if err != nil {
		return nil, err
	}
	return res.Changed, nil
}

// DeleteRows 按 id 删某个库里的行。**不可逆的写操作。**
//
// 与 Update 的区别：
//   - 支持**任意** dbPath（Update 写死了 workbuddy.db），因为要删的不只是
//     WB 的库，还有 ZCode 的 db.sqlite；
//   - 跑的是 delete 模式，脚本那边只放行 DELETE FROM + 表白名单 + 等值 WHERE。
//
// 调用方负责先备份。这个函数不做备份——它只做"删"这一件事，
// 备份与确认是调用链上游的职责（分开了才好各自测）。
func DeleteRows(probe *variant.Probe, id variant.ID, dbPath string, tables []string, stmts []Statement) ([]int, error) {
	if len(stmts) == 0 {
		return nil, nil
	}
	rt, err := detectRuntime(probe, id)
	if err != nil {
		return nil, err
	}
	if !fileExists(dbPath) {
		return nil, fmt.Errorf("数据库不存在：%s", dbPath)
	}
	res, err := runSQLite(rt, sqliteSpec{
		Mode:          "delete",
		DBPath:        dbPath,
		LibPath:       rt.libPath,
		NativeBinding: rt.nativeBinding,
		Statements:    stmts,
		Tables:        tables,
	})
	if err != nil {
		return nil, err
	}
	return res.Changed, nil
}

// writeRows 写入会话索引行。已存在的 id 一律跳过，绝不覆盖。
//
// 传进来的 rows 的 UserID 必须已经被调用方改写成空串：客户端的
// getSessions 用 `user_id IS NULL OR user_id = ”` 保证这类记录对当前
// 登录用户可见。改成目标端的 uid 反而不对——那是另一个账号的会话。
func writeRows(rt runtimePaths, dbPath string, rows []Row) (inserted, skipped []string, err error) {
	if len(rows) == 0 {
		return nil, nil, nil
	}
	res, err := runSQLite(rt, sqliteSpec{
		Mode:          "write",
		DBPath:        dbPath,
		LibPath:       rt.libPath,
		NativeBinding: rt.nativeBinding,
		Rows:          rows,
	})
	if err != nil {
		return nil, nil, err
	}
	return res.Inserted, res.Skipped, nil
}
