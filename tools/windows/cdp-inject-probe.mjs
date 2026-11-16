// cdp-inject-probe.mjs — minimal CDP injection probe for WorkBuddy.
// Read-only recon + one reversible on-screen badge (disappears on reload).
// Run: node cdp-inject-probe.mjs [port]
const port = Number(process.argv[2] || 9222);
const base = `http://127.0.0.1:${port}`;

const targets = await (await fetch(`${base}/json/list`)).json();
const page = targets.find(t => t.type === 'page' && t.url.includes('renderer/index.html'))
  || targets.find(t => t.type === 'page');
if (!page) throw new Error('no page target found');
console.log('target:', page.title, '|', page.url.split('?')[0]);

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
async function evaluate(expression) {
  const r = await cdp('Runtime.evaluate', {
    expression, returnByValue: true, awaitPromise: true,
  });
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
  return r.result.value;
}

// 1) recon: DOM structure + CSP, decides how the future panel can be mounted.
const recon = await evaluate(`(() => {
  const meta = document.querySelector('meta[http-equiv="Content-Security-Policy"]');
  const sig = el => el.tagName.toLowerCase()
    + (el.id ? '#' + el.id : '')
    + (typeof el.className === 'string' && el.className.trim()
        ? '.' + el.className.trim().split(/\\s+/).slice(0, 3).join('.') : '');
  return {
    title: document.title,
    readyState: document.readyState,
    csp: meta ? meta.content : null,
    bodyChildren: [...document.body.children].map(sig).slice(0, 25),
  };
})()`);
console.log('recon:', JSON.stringify(recon, null, 2));

// 2) inject a reversible badge. Click toggles a small card; close button removes all.
const injected = await evaluate(`(() => {
  document.getElementById('wbmux-probe')?.remove();
  document.getElementById('wbmux-probe-card')?.remove();

  const badge = document.createElement('div');
  badge.id = 'wbmux-probe';
  badge.textContent = 'wbmux · 注入成功';
  Object.assign(badge.style, {
    position: 'fixed', bottom: '18px', right: '18px', zIndex: 99999,
    background: '#10b981', color: '#052e22', padding: '8px 16px',
    borderRadius: '999px', font: '600 12px/1.4 system-ui, sans-serif',
    cursor: 'pointer', boxShadow: '0 4px 16px rgba(0,0,0,.35)',
    userSelect: 'none',
  });

  const card = document.createElement('div');
  card.id = 'wbmux-probe-card';
  Object.assign(card.style, {
    position: 'fixed', bottom: '62px', right: '18px', zIndex: 99999,
    width: '300px', background: '#16181d', color: '#e6e6e6',
    border: '1px solid #2dd4bf55', borderRadius: '12px',
    padding: '14px 16px', font: '13px/1.7 system-ui, sans-serif',
    boxShadow: '0 8px 30px rgba(0,0,0,.5)', display: 'none',
    pointerEvents: 'auto',
  });
  card.innerHTML = '<div style="font-weight:600;margin-bottom:6px">CDP 链路已打通</div>'
    + '<div style="opacity:.85">这是 wbmux 通过调试端口注入的测试元素，未修改客户端任何文件；刷新页面即消失。下一步：把这个位置换成 wbmux 的真实面板。</div>'
    + '<button id="wbmux-probe-close" style="margin-top:10px;background:#2dd4bf;border:0;border-radius:6px;padding:4px 14px;cursor:pointer;color:#062e22;font-weight:600">关闭</button>';

  badge.onclick = () => {
    card.style.display = card.style.display === 'none' ? 'block' : 'none';
  };
  card.querySelector('#wbmux-probe-close').onclick = () => { badge.remove(); card.remove(); };

  document.body.appendChild(badge);
  document.body.appendChild(card);
  return { badge: !!document.getElementById('wbmux-probe') };
})()`);
console.log('injected:', JSON.stringify(injected));

ws.close();
process.exit(0);
