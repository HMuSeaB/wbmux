// click-test.mjs — dispatch a real mouse click at the badge center via CDP.
const targets = await (await fetch('http://127.0.0.1:9222/json/list')).json();
const page = targets.find(t => t.type === 'page' && t.url.includes('renderer/index.html'));
if (!page) throw new Error('no page');
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0; const pending = new Map();
ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result); } };
const cdp = (method, params = {}) => new Promise((resolve, reject) => { const id = ++seq; pending.set(id, { resolve, reject }); ws.send(JSON.stringify({ id, method, params })); });

const r = await cdp('Runtime.evaluate', {
  expression: `(() => { const b = document.getElementById('wbmux-panel-badge'); if (!b) return null; const r = b.getBoundingClientRect(); return { x: r.left + r.width / 2, y: r.top + r.height / 2 }; })()`,
  returnByValue: true,
});
const { x, y } = r.result.value;
console.log('click at', x, y);

const before = await cdp('Runtime.evaluate', { expression: `document.getElementById('wbmux-panel-pop').style.display`, returnByValue: true });
await cdp('Input.dispatchMouseEvent', { type: 'mouseMoved', x, y });
await cdp('Input.dispatchMouseEvent', { type: 'mousePressed', x, y, button: 'left', clickCount: 1 });
await cdp('Input.dispatchMouseEvent', { type: 'mouseReleased', x, y, button: 'left', clickCount: 1 });
await new Promise(r2 => setTimeout(r2, 600));
const after = await cdp('Runtime.evaluate', { expression: `document.getElementById('wbmux-panel-pop').style.display`, returnByValue: true });
console.log('display before:', before.result.value, '-> after real click:', after.result.value);
ws.close(); process.exit(0);
