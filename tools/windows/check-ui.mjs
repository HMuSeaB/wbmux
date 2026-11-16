// check-ui.mjs — verify collapsible nav + hero-more dropdown in headless chrome.
import { writeFileSync } from 'node:fs';

const [url, out, portArg] = process.argv.slice(2);
const port = Number(portArg || 9333);
const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const page = targets.find(t => t.type === 'page');
const ws = new WebSocket(page.webSocketDebuggerUrl);
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
let seq = 0; const pending = new Map();
ws.onmessage = ev => {
  const m = JSON.parse(ev.data);
  if (m.id && pending.has(m.id)) {
    const p = pending.get(m.id); pending.delete(m.id);
    m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result);
  }
};
const cdp = (method, params = {}) => new Promise((resolve, reject) => {
  const id = ++seq; pending.set(id, { resolve, reject });
  ws.send(JSON.stringify({ id, method, params }));
});
const evaluate = async expr => {
  const r = await cdp('Runtime.evaluate', { expression: expr, returnByValue: true, awaitPromise: true });
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails));
  return r.result.value;
};

await cdp('Page.enable');
await cdp('Emulation.setDeviceMetricsOverride', { width: 1380, height: 1000, deviceScaleFactor: 1, mobile: false });
await cdp('Page.navigate', { url });
await new Promise(r => setTimeout(r, 4000));

const before = await evaluate(`(() => {
  const g = document.querySelector('.nav-group.collapsible');
  return { hasGroup: !!g, collapsed: g ? g.classList.contains('collapsed') : null, visibleItems: document.querySelectorAll('.nav-group.collapsible:not(.collapsed) .nav-item').length };
})()`);
console.log('before click:', JSON.stringify(before));

await evaluate(`document.querySelector('.nav-group.collapsible .nav-group-title').click()`);
await new Promise(r => setTimeout(r, 300));
const after = await evaluate(`(() => {
  const g = document.querySelector('.nav-group.collapsible');
  return { collapsed: g.classList.contains('collapsed'), visibleItems: g.querySelectorAll('.nav-item').length, ls: localStorage.getItem('wbmux.nav.toolbox') };
})()`);
console.log('after nav click:', JSON.stringify(after));

await evaluate(`document.querySelector('.hero-more > summary').click()`);
await new Promise(r => setTimeout(r, 300));
const dropdown = await evaluate(`(() => {
  const d = document.querySelector('.hero-more');
  const body = d.querySelector('.hero-more-body');
  const r = body.getBoundingClientRect();
  return { open: d.open, width: Math.round(r.width), height: Math.round(r.height), buttons: body.querySelectorAll('button').length };
})()`);
console.log('dropdown:', JSON.stringify(dropdown));

const shot = await cdp('Page.captureScreenshot', { format: 'jpeg', quality: 75 });
writeFileSync(out, Buffer.from(shot.data, 'base64'));
console.log('saved:', out);
ws.close(); process.exit(0);
