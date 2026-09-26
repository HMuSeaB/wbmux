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
	"time"

	_ "embed"

	"github.com/HMuSeaB/wbmux/internal/config"
	"github.com/HMuSeaB/wbmux/internal/variant"
)

//go:embed assets/sqlite.js
var sqliteScript []byte

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
}

// sqliteResult 是脚本的输出。
type sqliteResult struct {
	Error    string   `json:"error"`
	Rows     []Row    `json:"rows"`
	Inserted []string `json:"inserted"`
	Skipped  []string `json:"skipped"`
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
	dir, err := config.Dir()
	if err != nil {
		return sqliteResult{}, err
	}
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return sqliteResult{}, fmt.Errorf("创建临时目录失败: %w", err)
	}
	scriptPath := filepath.Join(tmp, "sqlite.js")
	specPath := filepath.Join(tmp, "sqlite.json")
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
	trimmed := strings.TrimSpace(sql)
	if !strings.HasPrefix(strings.ToLower(trimmed), "select") {
		return nil, fmt.Errorf("Query 只接受 SELECT，收到 %q", trimmed)
	}

	// 优先用这一档位自己的安装：与它自己的数据库同一次构建，ABI 一致。
	rt, err := detectRuntime(probe, id)
	if err != nil {
		return nil, err
	}
	dbPath := filepath.Join(probe.DataDir(id), "workbuddy.db")
	if !fileExists(dbPath) {
		return nil, fmt.Errorf("%s 侧还没有数据库：%s", id, dbPath)
	}

	res, err := runSQLite(rt, sqliteSpec{
		Mode:          "query",
		DBPath:        dbPath,
		LibPath:       rt.libPath,
		NativeBinding: rt.nativeBinding,
		SQL:           sql,
		Params:        params,
	})
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return res.Result, nil
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
