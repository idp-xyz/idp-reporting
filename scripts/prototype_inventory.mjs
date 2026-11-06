// Lists the templates the prototype keeps in localStorage (docs/S06B_SCOPE.md). The prototype is not in the repository.
// Usage: node scripts/prototype_inventory.mjs PROTOTYPE.html OUT.json [CDP_PORT, default 9336]; a Chrome must listen on CDP_PORT.
import {readFileSync, writeFileSync} from 'node:fs';
import {createServer} from 'node:http';
const [protoPath, outPath, cdp = '9336'] = process.argv.slice(2);
if (!protoPath || !outPath) {
  console.error('usage: node scripts/prototype_inventory.mjs PROTOTYPE.html OUT.json [CDP_PORT]');
  process.exit(2);
}
const proto = readFileSync(protoPath);
const srv = createServer((q, r) => {
  r.writeHead(200, {'content-type': 'text/html; charset=utf-8'});
  r.end(proto);
});
await new Promise(r => srv.listen(7899, '127.0.0.1', r));
const sleep = ms => new Promise(r => setTimeout(r, ms));
const t = (await (await fetch(`http://127.0.0.1:${cdp}/json/list`)).json()).find(x => x.type === 'page');
const ws = new WebSocket(t.webSocketDebuggerUrl);
let id = 1;
const pend = new Map();
ws.onmessage = e => {
  const m = JSON.parse(e.data);
  if (m.id && pend.has(m.id)) {
    const p = pend.get(m.id);
    pend.delete(m.id);
    m.error ? p.rej(new Error(m.error.message)) : p.res(m.result);
  }
};
await new Promise((res, rej) => {
  ws.onopen = res;
  ws.onerror = rej;
});
const send = (method, params = {}) =>
  new Promise((res, rej) => {
    const i = id++;
    pend.set(i, {res, rej});
    ws.send(JSON.stringify({id: i, method, params}));
  });
const ev = async expr => {
  const r = await send('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true});
  if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 300));
  return r.result.value;
};
await send('Page.enable');
await send('Runtime.enable');
await send('Page.navigate', {url: 'http://127.0.0.1:7899/'});
await sleep(2500);
const out = await ev(
  `(()=>{const keys=Object.keys(localStorage);const res={keys};for(const k of keys){try{const v=JSON.parse(localStorage.getItem(k));if(Array.isArray(v?.templates)){res.templates=v.templates.map(t=>{const d=t.versions?.at(-1)?.doc||t.doc||{};const types={};for(const n of d.nodes||[])types[n.type]=(types[n.type]||0)+1;return {id:t.id,name:d.name,kind:d.kind,datasetId:d.datasetId,mode:d.page?.mode,bands:d.bands,nodes:(d.nodes||[]).length,types,versions:(t.versions||[]).length};});res.sample=v.templates[0];}}catch(e){}}return res;})()`
);
writeFileSync(outPath, JSON.stringify(out, null, 1));
console.log(JSON.stringify(out.keys));
console.log((out.templates || []).length, 'templates');
for (const t of out.templates || [])
  console.log(t.kind, '|', t.name, '|', t.datasetId, '|', t.mode, '|', t.nodes, JSON.stringify(t.types));
ws.close();
srv.close();
