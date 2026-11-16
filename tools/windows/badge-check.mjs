// badge-check.mjs — read the capsule text and tag from WorkBuddy.
const targets = await (await fetch('http://127.0.0.1:9222/json/list')).json();
const page = targets.find(t => t.type === 'page' && t.url.includes('renderer/index.html'))
  || targets.find(t => t.type === 'page');
if (!page) throw new Error('no page target');

const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
const expr = `(() => {
  const b = document.getElementById('wbmux-panel-badge');
  const s = document.getElementById('wbmux-panel-stats');
  return {
    badge: !!b,
    text: b ? b.textContent.trim() : null,
    statsColor: s ? s.style.color : null,
    tag: window.__wbmuxPanelVersion || null,
    pop: (() => { const p = document.getElementById('wbmux-panel-pop'); return p ? p.style.display : null; })()
  };
})()`;
const result = await new Promise((resolve, reject) => {
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id === 1) m.error ? reject(new Error(JSON.stringify(m.error))) : resolve(m.result);
  };
  ws.send(JSON.stringify({ id: 1, method: 'Runtime.evaluate', params: { expression: expr, returnByValue: true } }));
});
console.log(JSON.stringify(result.result.value, null, 2));
ws.close();
process.exit(0);
