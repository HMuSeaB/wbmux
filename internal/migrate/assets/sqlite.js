'use strict';

// wbmux 在搬运时把这个脚本写到临时目录，再交给客户端自带的 Electron 执行。
// 用法：ELECTRON_RUN_AS_NODE=1 <客户端主程序> sqlite.js <spec.json>
//
// ## 为什么要绕这一圈
//
// 本项目 Go 侧零第三方依赖，而 Go 标准库没有 SQLite 驱动。
// 但客户端自己捆了 better-sqlite3——Electron 主程序加上
// ELECTRON_RUN_AS_NODE=1 就是一个普通 Node 运行时。复用它意味着：
//   - 不新增任何依赖；
//   - 与客户端同一次构建，ABI 天然匹配，不存在原生模块版本错配。
//
// ## 为什么必须走 nativeBinding
//
// better-sqlite3 的 JS 封装默认调 require('bindings') 去找原生模块，
// 而 bindings 在打包时留在了 app.asar 内（未解包），直接 require 会报
// "Cannot find module 'bindings'"。所以用 nativeBinding 选项直接给出
// .node 的绝对路径，跳过 bindings 这一层。

const fs = require('fs');

// 与 Go 侧 migrate.Row 的 JSON 标签逐字对应，改一边必须同步另一边。
// user_id 虽然是空串也要显式写入：该列是 NOT NULL 且无默认值，
// 漏掉会直接违反约束。
const COLUMNS = [
  'id', 'cwd', 'user_id', 'title', 'custom_title', 'status',
  'created_at', 'updated_at', 'last_activity_at', 'is_playground',
  'source_mode', 'mode', 'model', 'permission_mode',
  'use_sandbox_cli', 'addon_selection', 'context_window', 'thought_level'
];

function emit(obj) {
  process.stdout.write(JSON.stringify(obj));
}

// 出错时也输出结构化 JSON：调用方只需解析 stdout，不必解析本地化的 stderr。
function fail(msg) {
  emit({ error: String(msg) });
  process.exit(1);
}

function main() {
  const specPath = process.argv[2];
  if (!specPath) return fail('缺少 spec 参数');

  let spec;
  try {
    spec = JSON.parse(fs.readFileSync(specPath, 'utf8'));
  } catch (e) {
    return fail('读取 spec 失败：' + e.message);
  }

  let Database;
  try {
    Database = require(spec.libPath);
  } catch (e) {
    return fail('加载 better-sqlite3 失败：' + e.message);
  }

  const options = { nativeBinding: spec.nativeBinding, timeout: 5000 };
  // 读的时候开只读：搬运的读取阶段绝不该有机会改动来源端。
  if (spec.mode === 'read') options.readonly = true;

  let db;
  try {
    db = new Database(spec.dbPath, options);
  } catch (e) {
    return fail('打开数据库失败：' + e.message);
  }

  try {
    if (spec.mode === 'read') {
      const rows = db.prepare('SELECT ' + COLUMNS.join(', ') + ' FROM sessions').all();
      return emit({ rows: rows });
    }

    if (spec.mode === 'write') {
      const insert = db.prepare(
        'INSERT OR IGNORE INTO sessions (' + COLUMNS.join(', ') + ') VALUES (' +
        COLUMNS.map(() => '?').join(', ') + ')'
      );
      const exists = db.prepare('SELECT 1 FROM sessions WHERE id = ?');

      const inserted = [];
      const skipped = [];

      // 整批放在一个事务里：中途出错整体回滚，不会留下写了一半的索引
      // ——半个索引比没写更糟，列表会显示出一批打不开的会话。
      const run = db.transaction((rows) => {
        for (const row of rows) {
          // 先查再插，是为了把"哪些已存在"如实回报给界面，
          // INSERT OR IGNORE 本身只会给一个 changes=0，分不清原因。
          if (exists.get(row.id)) {
            skipped.push(row.id);
            continue;
          }
          const info = insert.run(COLUMNS.map((c) => (c in row ? row[c] : null)));
          if (info.changes > 0) inserted.push(row.id);
          else skipped.push(row.id);
        }
      });
      run(spec.rows || []);

      return emit({ inserted: inserted, skipped: skipped });
    }

    return fail('未知 mode：' + spec.mode);
  } finally {
    try {
      db.close();
    } catch (e) {
      /* 关不掉不影响已经写好的事务 */
    }
  }
}

try {
  main();
} catch (e) {
  fail(e && e.message ? e.message : e);
}
