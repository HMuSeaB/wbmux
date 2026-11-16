// inject-wbmux-panel.mjs — inject the real wbmux panel into a running WorkBuddy.
// Titlebar badge + popover with an iframe pointing at wbmux's /embed page.
// Reversible: reload the client window and everything is gone. No client files touched.
// Run: node inject-wbmux-panel.mjs [cdpPort]
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';

const cdpPort = Number(process.argv[2] || 9222);
const base = `http://127.0.0.1:${cdpPort}`;

const inst = JSON.parse(readFileSync(join(homedir(), '.wbmux', 'run.json'), 'utf8'));
const u = new URL(inst.url);
const embedURL = `${u.origin}/embed?t=${encodeURIComponent(u.searchParams.get('t') || '')}`;
console.log('wbmux instance:', inst.addr, '-> embed:', embedURL);

const targets = await (await fetch(`${base}/json/list`)).json();
const page = targets.find(t => t.type === 'page' && t.url.includes('renderer/index.html'))
  || targets.find(t => t.type === 'page');
if (!page) throw new Error('no WorkBuddy page target found (is the client running with CDP?)');

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0;
const pending = new Map();
ws.onmessage = ev => {
  const msg = JSON.parse(ev.data);
  if (msg.id && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id);
    pending.delete(msg.id);
    msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result);
  }
};
function cdp(method, params = {}) {
  const id = ++seq;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve, reject });
    ws.send(JSON.stringify({ id, method, params }));
  });
}

const result = await cdp('Runtime.evaluate', {
  expression: `(() => {
    // clean up: previous probe badge + any earlier panel (idempotent re-inject)
    if (window.__wbmuxPanelCleanup) { try { window.__wbmuxPanelCleanup(); } catch (e) {} }
    ['wbmux-probe', 'wbmux-probe-card', 'wbmux-panel-badge', 'wbmux-panel-pop'].forEach(id => {
      document.getElementById(id)?.remove();
    });

    const badge = document.createElement('div');
    badge.id = 'wbmux-panel-badge';
    badge.innerHTML = '<span style="width:6px;height:6px;border-radius:50%;background:#2dd4bf;display:inline-block"></span> wbmux';
    Object.assign(badge.style, {
      position: 'fixed', top: '7px', right: '160px', zIndex: 99998,
      display: 'flex', alignItems: 'center', gap: '6px',
      background: '#1c212b', color: '#c3c9d4',
      border: '1px solid rgba(45,212,191,0.35)', borderRadius: '999px',
      padding: '3px 12px', font: '600 11.5px/1.5 system-ui, sans-serif',
      cursor: 'pointer', userSelect: 'none',
    });
    badge.title = 'wbmux 本机工作区面板';
    // 顶栏是窗口拖拽区，必须显式 no-drag，否则真实鼠标点击被系统吞掉
    badge.style.setProperty('-webkit-app-region', 'no-drag');
    badge.style.setProperty('app-region', 'no-drag');

    const pop = document.createElement('div');
    pop.id = 'wbmux-panel-pop';
    Object.assign(pop.style, {
      position: 'fixed', top: '42px', right: '16px', zIndex: 99999,
      width: '400px', height: '600px', maxHeight: 'calc(100vh - 60px)',
      borderRadius: '12px', overflow: 'hidden',
      border: '1px solid rgba(45,212,191,0.25)',
      boxShadow: '0 12px 40px rgba(0,0,0,0.55)',
      background: '#0d1017',
      // golden rule: fully gone while closed — no invisible glass pane
      display: 'none', pointerEvents: 'none',
    });

    const frame = document.createElement('iframe');
    frame.src = ${JSON.stringify(embedURL)};
    frame.setAttribute('allow', 'clipboard-write');
    Object.assign(frame.style, { width: '100%', height: '100%', border: '0', background: '#0d1017' });
    pop.appendChild(frame);

    let open = false;
    function setOpen(v) {
      open = v;
      pop.style.display = v ? 'block' : 'none';
      pop.style.pointerEvents = v ? 'auto' : 'none';
      badge.style.borderColor = v ? '#2dd4bf' : 'rgba(45,212,191,0.35)';
    }
    function onDocClick(ev) {
      if (!open) return;
      const t = ev.target;
      if (pop.contains(t) || badge.contains(t)) return;
      setOpen(false);
    }
    function onKey(ev) { if (ev.key === 'Escape' && open) setOpen(false); }

    badge.addEventListener('click', () => setOpen(!open));
    document.addEventListener('click', onDocClick, true);
    document.addEventListener('keydown', onKey, true);

    window.__wbmuxPanelCleanup = () => {
      document.removeEventListener('click', onDocClick, true);
      document.removeEventListener('keydown', onKey, true);
      badge.remove(); pop.remove();
      delete window.__wbmuxPanelCleanup;
    };

    document.body.appendChild(badge);
    document.body.appendChild(pop);
    return { ok: true, badgeMounted: !!document.getElementById('wbmux-panel-badge') };
  })()`,
  returnByValue: true,
});
if (result.exceptionDetails) throw new Error(JSON.stringify(result.exceptionDetails));
console.log('injected:', JSON.stringify(result.result.value));

ws.close();
process.exit(0);
