// diag-badge.mjs — why is the badge unclickable?
const cdpPort = 9222;
const targets = await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json();
const page = targets.find(t => t.type === 'page' && t.url.includes('renderer/index.html'));
if (!page) throw new Error('no page');
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0; const pending = new Map();
ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result); } };
const cdp = (method, params = {}) => new Promise((resolve, reject) => { const id = ++seq; pending.set(id, { resolve, reject }); ws.send(JSON.stringify({ id, method, params })); });
const r = await cdp('Runtime.evaluate', {
  expression: `(() => {
    const b = document.getElementById('wbmux-panel-badge');
    if (!b) return { exists: false };
    const rect = b.getBoundingClientRect();
    const cx = rect.left + rect.width / 2, cy = rect.top + rect.height / 2;
    const hit = document.elementFromPoint(cx, cy);
    const cs = getComputedStyle(b);
    return {
      exists: true,
      rect: { x: Math.round(rect.x), y: Math.round(rect.y), w: Math.round(rect.width), h: Math.round(rect.height) },
      hitElement: hit ? (hit.tagName + (hit.id ? '#' + hit.id : '') + (hit.className && typeof hit.className === 'string' ? '.' + hit.className.split(' ')[0] : '')) : null,
      hitIsBadge: hit ? (hit === b || b.contains(hit)) : false,
      selfAppRegion: cs.webkitAppRegion || cs.getPropertyValue('-webkit-app-region'),
      parentChain: (() => {
        const out = []; let n = b.parentElement; let i = 0;
        while (n && i < 4) { const s = getComputedStyle(n); out.push(n.tagName + (n.id ? '#' + n.id : '') + ':' + (s.webkitAppRegion || 'none')); n = n.parentElement; i++; }
        return out;
      })(),
      pop: (() => { const p = document.getElementById('wbmux-panel-pop'); return p ? { display: p.style.display, pointerEvents: p.style.pointerEvents } : null; })()
    };
  })()`,
  returnByValue: true,
});
console.log(JSON.stringify(r.result.value, null, 2));
ws.close(); process.exit(0);
