// 用最小 DOM 垫片跑「会话中心」页的真实脚本，验证 load() 不抛异常。
//
// 用法（在仓库根目录）：
//     node tools/uicheck/sess-dom-test.js
//
// # 为什么需要它
//
// 2026-10-01 用户报"重新扫描好慢"。实测后端只要 0.22 秒，界面却**永远不返回**
// ——根因是 load() 里这行：
//
//     document.getElementById("empty").textContent = "扫描中…";
//
// `#empty` 不是固定元素：renderProjects / renderDetail 用 innerHTML 重建它
// （"没有匹配的项目" / "选一个项目" / "加载中…" 共用这一个 id）。扫过一次后
// 它就不在了 → 那行抛 TypeError（在 fetch **之前**）→ 请求发不出去，
// 而且 .finally 也没挂上，按钮永久禁用。用户看到的就是"点了一直转"。
//
// # 为什么不做成 go test / 进 CI
//
// 它需要 node，而 CI 的 workflow 里没有 setup-node（加了会给所有构建引入一个
// 只为一条检查存在的新依赖）。也试过用 Go 静态扫描代替——**抓不到**：那个 id
// 在静态 HTML 里也存在（<div id="empty">加载中…</div>），只是运行时会被覆盖掉，
// "两边都出现的交集"这个判据无法区分"会被重建"与"正常存在"。
//
// 所以它是个**开发时手动跑**的检查：改动 index.html / sessions.html 里的
// DOM 操作后跑一遍。

const fs = require('fs');
const path = require('path');

const ROOT = path.resolve(__dirname, '..', '..');
const PAGE = path.join(ROOT, 'internal', 'webui', 'assets', 'sessions.html');

const html = fs.readFileSync(PAGE, 'utf8');
const m = html.match(/<script[^>]*>([\s\S]*?)<\/script>/);
if (!m) {
  console.error('页面里没找到 <script>:', PAGE);
  process.exit(1);
}
const js = m[1];

// ---- 最小 DOM：只实现脚本用到的那几个 API ----
function makeEl(id) {
  return {
    id, textContent: '', innerHTML: '', value: '', checked: false,
    disabled: false, style: {}, onclick: null,
    classList: { add() {}, remove() {} },
    addEventListener() {},
    querySelectorAll() { return []; },
    getAttribute() { return null; },
  };
}

// 页面上真实存在的元素（取自 sessions.html 的静态部分）。
// 注意**故意不放 'empty'**：它正是运行时会被 innerHTML 重建掉的那个，
// 不放进来才能复现"扫过一次之后"的状态——那是出事的那一刻。
const staticIds = ['days', 'keep', 'q', 'vendor', 'onlyCand', 'warn',
                   'gen', 'refresh', 'reload', 'projects', 'detail',
                   'clean', 'cleanbar'];

const els = {};
staticIds.forEach(id => { els[id] = makeEl(id); });

global.document = {
  getElementById(id) { return els[id] || null; },
  querySelectorAll() { return []; },
};
global.location = { search: '?t=testtoken' };

let fetchCalls = 0;
global.fetch = function () {
  fetchCalls++;
  return Promise.resolve({
    ok: true,
    status: 200,
    json: () => Promise.resolve({
      sessions: [], projects: [], warnings: [],
      generatedAt: Date.now(), cached: false, keepDays: 30, keepPerProject: 1,
    }),
  });
};

let api;
try {
  api = new Function(js + '\n;return { load: load };')();
} catch (e) {
  console.error('① 脚本初始化抛异常:', e.message);
  console.error('   （load(false) 在脚本末尾，加载即执行——早期版本这里就会炸）');
  process.exit(1);
}

async function main() {
  await new Promise(r => setTimeout(r, 50));
  if (fetchCalls === 0) {
    console.error('① 脚本初始化: 没有发出任何请求');
    process.exit(1);
  }
  console.log('① 脚本初始化（load(false)）: 未抛异常，fetch %d 次', fetchCalls);

  // 模拟"已经扫过一次"之后点「重新扫描」——此刻 #empty 不在。
  const before = fetchCalls;
  try {
    api.load(true);
  } catch (e) {
    console.error('② 点「重新扫描」: **抛异常** —— %s', e.message);
    console.error('   这一行在 fetch 之前，请求发不出去、按钮也不会恢复。');
    process.exit(1);
  }
  await new Promise(r => setTimeout(r, 50));

  if (fetchCalls <= before) {
    console.error('② 点「重新扫描」: **请求没发出**');
    process.exit(1);
  }
  console.log('② 点「重新扫描」: 请求已发出（fetch %d → %d）', before, fetchCalls);

  if (els.refresh.disabled) {
    console.error('③ 按钮状态: **仍然禁用**（用户看到的就是"一直转"）');
    process.exit(1);
  }
  console.log('③ 按钮状态: 已恢复可用');
  console.log('④ #gen 文案: %s', JSON.stringify(els.gen.textContent));
  console.log('\n全部通过。');
}

main().catch(e => { console.error('抛异常:', e.message); process.exit(1); });
