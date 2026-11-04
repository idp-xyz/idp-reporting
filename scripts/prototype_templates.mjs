// Extraction of the prototype's template library (S06-B part 2, B5). No dependencies (Node 24).
//
//   node scripts/prototype_templates.mjs <prototype.html> <out dir> <cdp port> [serve port]
//
// <cdp port> is a headless Chrome the caller started (not the shared one on 9336), for example
//   chrome.exe --headless=new --remote-debugging-port=<port> --user-data-dir=<fresh dir> --no-proxy-server --no-first-run about:blank
// The prototype file is read from the path given and served to that Chrome from 127.0.0.1:<serve port> (default 7846); it is
// never copied into the repository. The app of the prototype is one closure, so its functions cannot be called from outside: the
// served copy (and only the served copy) gets one line before the closure ends that hands out a few of them (rawRows, meta,
// dataset, the template list and the catalogue's default classification). Nothing else of the file is changed, and nothing of its JavaScript is copied.
//
// The prototype's own template ids are random per start and are not written, so that a second extraction gives the same files.
// Written: <out dir>/<kind>.json for each template and <out dir>/inventory.json. The second template of kind "mawb" (the one named
// "航空运单 · Air Waybill (MAWB)") is written as mawb-air-waybill.json. Each file holds exactly what the prototype held:
//   {"source": {"template_name", "kind", "file", "versions", "published"},
//    "catalog": {"domain", "flow", "domain_label", "flow_label"} (the template centre's default classification of the kind),
//    "doc": <the last version's doc>, "dataset": <the dataset definition it binds>, "rows": [<the prototype's sample rows>],
//    "meta": {<id of each scope-meta field the doc uses>: <the value the prototype shows>}}
import {readFileSync, writeFileSync, mkdirSync} from 'node:fs';
import {createServer} from 'node:http';
import {join} from 'node:path';

const [,, protoFile, outDir, cdpPort, servePort = '7846'] = process.argv;
if (!protoFile || !outDir || !cdpPort) { console.error('usage: node scripts/prototype_templates.mjs <prototype.html> <out dir> <cdp port> [serve port]'); process.exit(2); }
mkdirSync(outDir, {recursive: true});
const raw = readFileSync(protoFile, 'utf8');
const tail = '\n})();\n</script></body></html>';
const at = raw.lastIndexOf(tail);
if (at < 0) throw new Error('the prototype does not end as expected');
const probe = '\nObject.defineProperty(window, "__idpExtract", {value: {rawRows, meta, dataset, templates: () => templates, clone, catalogDefault, CAT_DOMAINS, CAT_FLOWS}});';
const served = Buffer.from(raw.slice(0, at) + probe + raw.slice(at), 'utf8');
const srv = createServer((q, r) => { r.writeHead(200, {'content-type': 'text/html; charset=utf-8'}); r.end(served); });
await new Promise(r => srv.listen(Number(servePort), '127.0.0.1', r));
const sleep = ms => new Promise(r => setTimeout(r, ms));
const t = (await (await fetch(`http://127.0.0.1:${cdpPort}/json/list`)).json()).find(x => x.type === 'page');
const ws = new WebSocket(t.webSocketDebuggerUrl); let id = 1; const pend = new Map();
ws.onmessage = e => { const m = JSON.parse(e.data); if (m.id && pend.has(m.id)) { const p = pend.get(m.id); pend.delete(m.id); m.error ? p.rej(new Error(m.error.message)) : p.res(m.result); } };
await new Promise((res, rej) => { ws.onopen = res; ws.onerror = rej; });
const send = (method, params = {}) => new Promise((res, rej) => { const i = id++; pend.set(i, {res, rej}); ws.send(JSON.stringify({id: i, method, params})); });
const ev = async expr => { const r = await send('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true}); if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 500)); return r.result.value; };
await send('Page.enable'); await send('Runtime.enable');
// A fresh profile has no stored library; clear it anyway so that the app seeds its own.
await send('Page.navigate', {url: `http://127.0.0.1:${servePort}/`}); await sleep(1500);
await ev('localStorage.clear()');
await send('Page.navigate', {url: `http://127.0.0.1:${servePort}/`}); await sleep(3000);
const all = JSON.parse(await ev(`JSON.stringify((() => {
  const X = window.__idpExtract;
  return X.templates().map(t => {
    const last = t.versions.at(-1), doc = X.clone(last.doc), ds = X.dataset(doc);
    const meta = {};
    for (const n of doc.nodes) if (n.type === 'field' && n.scope === 'meta' && !(n.field in meta)) meta[n.field] = X.meta(n.field, doc);
    return {catalog: {domain: X.catalogDefault(t).domain, flow: X.catalogDefault(t).flow, domain_label: X.CAT_DOMAINS[X.catalogDefault(t).domain], flow_label: X.CAT_FLOWS[X.catalogDefault(t).domain][X.catalogDefault(t).flow]}, source: {template_name: t.name, kind: doc.kind, versions: t.versions.length, published: t.published}, doc, dataset: X.clone(ds), rows: X.rawRows(doc), meta};
  });
})())`));
const seen = new Map(), inventory = [];
for (const e of all) {
  let name = e.doc.kind;
  if (seen.has(name)) name += '-' + (e.source.template_name.includes('Air Waybill') ? 'air-waybill' : String(seen.get(name) + 1));
  seen.set(e.doc.kind, (seen.get(e.doc.kind) || 0) + 1);
  e.source.file = name;
  writeFileSync(join(outDir, name + '.json'), JSON.stringify(e, null, 1) + '\n');
  const types = {};
  for (const n of e.doc.nodes) types[n.type] = (types[n.type] || 0) + 1;
  inventory.push({file: name, name: e.doc.name, kind: e.doc.kind, datasetId: e.doc.datasetId, mode: e.doc.page?.mode, bands: e.doc.bands, nodes: e.doc.nodes.length, types, versions: e.source.versions, rows: e.rows.length, fields: e.dataset.fields.length});
}
writeFileSync(join(outDir, 'inventory.json'), JSON.stringify({templates: inventory}, null, 1) + '\n');
console.log(all.length, 'templates written to', outDir);
ws.close(); srv.close(); process.exit(0);
