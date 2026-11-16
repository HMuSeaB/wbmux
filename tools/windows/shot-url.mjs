// shot-url.mjs — screenshot any URL via a headless Chrome started with
// --remote-debugging-port. Usage: node shot-url.mjs <url> <out.jpg> [cdpPort]
import { writeFileSync } from 'node:fs';

const [url, out, portArg] = process.argv.slice(2);
const port = Number(portArg || 9333);
if (!url || !out) throw new Error('usage: node shot-url.mjs <url> <out.jpg> [cdpPort]');

const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
const page = targets.find(t => t.type === 'page');
if (!page) throw new Error('no page target in headless chrome');

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

await cdp('Page.enable');
await cdp('Emulation.setDeviceMetricsOverride', { width: 1380, height: 1000, deviceScaleFactor: 1, mobile: false });
await cdp('Page.navigate', { url });
await new Promise(r => setTimeout(r, 4000));
const shot = await cdp('Page.captureScreenshot', { format: 'jpeg', quality: 75 });
writeFileSync(out, Buffer.from(shot.data, 'base64'));
console.log('saved:', out);
ws.close(); process.exit(0);
