// cdp-shot.mjs — open the injected wbmux panel and capture a screenshot.
// Run: node cdp-shot.mjs <outPng> [cdpPort]
import { writeFileSync } from 'node:fs';

const out = process.argv[2];
const cdpPort = Number(process.argv[3] || 9222);
if (!out) throw new Error('usage: node cdp-shot.mjs <outPng> [cdpPort]');

const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
const page = targets.find(t => t.type === 'page' && t.url.includes('renderer/index.html'))
  || targets.find(t => t.type === 'page');
if (!page) throw new Error('no page target');

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

const state = await cdp('Runtime.evaluate', {
  expression: `(() => {
    const b = document.getElementById('wbmux-panel-badge');
    const p = document.getElementById('wbmux-panel-pop');
    const f = p && p.querySelector('iframe');
    return { badge: !!b, pop: !!p, display: p ? p.style.display : null, src: f ? f.src : null };
  })()`,
  returnByValue: true,
});
console.log('panel state:', JSON.stringify(state.result.value));

await cdp('Runtime.evaluate', {
  expression: `(() => { const p = document.getElementById('wbmux-panel-pop'); if (p && p.style.display === 'none') document.getElementById('wbmux-panel-badge')?.click(); })()`,
});
await new Promise(r => setTimeout(r, 2500));
const shot = await cdp('Page.captureScreenshot', { format: 'jpeg', quality: 72 });
writeFileSync(out, Buffer.from(shot.data, 'base64'));
console.log('saved:', out);
ws.close();
process.exit(0);
