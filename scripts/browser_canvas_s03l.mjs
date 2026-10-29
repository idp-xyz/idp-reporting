// Real-browser acceptance for the S03-L canvas for schema 2.0 bands and the 1.x to 2.0 conversion: real mouse and keyboard input over
// the Chrome DevTools Protocol, the template JSON checked after every step, screenshots at 390, 768 and 1280 px, no horizontal
// overflow, colour contrast in the light and the dark scheme. It builds an invoice-like 2.0 template from a converted 1.x one, edits
// its bands and elements, previews it (the PDF's bytes are read from the page's Blob) and checks samples. Drives an external headless Chrome with no dependencies (Node 22+ global WebSocket/fetch).
// Not part of `npm test` and not CI: it needs a running reporting-demo started with the PDF flags and a template directory
// (the check route is mounted with the PDF writer), and a Chrome started with
//   --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server
// It exits 1 when any check fails or the run aborts, and 2 on bad arguments; report.json is written either way.
// Safety: --base must be loopback because the bearer token is typed into the page. The script reads and writes only the
// page it drives and its own files in --out (PNGs named after the viewport or the step, report.json); it removes nothing else.
// Usage: node scripts/browser_canvas_s03l.mjs --out DIR --token-file FILE [--base URL] [--cdp PORT]
import {readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const usage = why => { console.error(why + '\nusage: --out DIR --token-file FILE [--base http://127.0.0.1:PORT] [--cdp PORT]'); process.exit(2); };
let args;
try {
  ({values: args} = parseArgs({options: {out: {type: 'string'}, 'token-file': {type: 'string'}, base: {type: 'string'}, cdp: {type: 'string'}}, strict: true}));
} catch (e) { usage(e.message); }
if (!args.out || !args['token-file']) usage('--out and --token-file are required');
const base = args.base || 'http://127.0.0.1:7831', cdp = args.cdp || '9334', outDir = args.out;
let baseUrl;
try { baseUrl = new URL(base); } catch { usage('--base is not a valid URL'); }
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname)) usage('--base must be an http loopback address');
const token = readFileSync(args['token-file'], 'utf8').trim();
const sleep = ms => new Promise(r => setTimeout(r, ms));
mkdirSync(outDir, {recursive: true});

const problems = [], dialogs = [], checks = [], evidence = {};
const check = (name, ok, detail) => { checks.push({name, ok: !!ok, detail: ok ? undefined : detail}); console.log(ok ? 'PASS' : 'FAIL', name, ok ? '' : JSON.stringify(detail)); };

function connect(url, onEvent) {
  const ws = new WebSocket(url), pending = new Map();
  let nextId = 1;
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { const {res, rej} = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); } else onEvent?.(m);
  };
  ws.onclose = () => { for (const {rej} of pending.values()) rej(new Error('CDP connection closed')); pending.clear(); };
  const ready = new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('websocket failed: ' + url)); });
  const send = (method, params = {}, timeout = 30000) => new Promise((res, rej) => {
    const id = nextId++, timer = setTimeout(() => { pending.delete(id); rej(new Error('CDP timeout: ' + method)); }, timeout);
    pending.set(id, {res: v => { clearTimeout(timer); res(v); }, rej: e => { clearTimeout(timer); rej(e); }});
    ws.send(JSON.stringify({id, method, params}));
  });
  return {ready, send, close: () => ws.close()};
}

let page, version;
const send = (...a) => page.send(...a);
const evalJs = async expr => {
  const r = await send('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true});
  if (r.exceptionDetails) throw new Error('eval failed: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
};
const waitFor = async (expr, label, limit = 10000) => {
  const end = Date.now() + limit;
  while (Date.now() < end) { if (await evalJs(`Boolean(${expr})`)) return; await sleep(60); }
  throw new Error('timeout waiting for: ' + label);
};
const shot = async (name, elementExpr) => {
  const params = {format: 'png'};
  if (elementExpr) {
    const r = await evalJs(`(()=>{const e=${elementExpr};if(!e)return null;const b=e.getBoundingClientRect();return {x:0,y:Math.max(0,b.top+scrollY-8),width:Math.max(document.documentElement.clientWidth,Math.ceil(b.right)+16),height:Math.ceil(b.height)+16};})()`);
    if (!r) throw new Error('no element for shot ' + elementExpr);
    Object.assign(params, {clip: {...r, scale: 1}, captureBeyondViewport: true});
  }
  writeFileSync(join(outDir, name + '.png'), Buffer.from((await send('Page.captureScreenshot', params)).data, 'base64'));
};
const VIEWPORTS = [['desktop', 1280, 900, false, 1], ['tablet', 768, 1024, false, 1], ['mobile', 390, 844, true, 2]];
const OWN_FILE = new RegExp(`^(00-login|zz-failure|step-\\d\\d-[\\w-]+|(${VIEWPORTS.map(v => v[0]).join('|')})-\\d\\d-[\\w-]+)\\.png$|^report\\.json$`);
for (const f of readdirSync(outDir)) if (OWN_FILE.test(f)) rmSync(join(outDir, f));
const setViewport = async ([, width, height, mobile, dpr]) => { await send('Emulation.setDeviceMetricsOverride', {width, height, deviceScaleFactor: dpr, mobile}); await sleep(300); };
const openPage = name => evalJs(`document.querySelector('[data-page="${name}"]').click()`);
const pageOverflow = () => evalJs(`(()=>{const d=document.documentElement;return {scroll:d.scrollWidth,client:d.clientWidth,over:d.scrollWidth>d.clientWidth+1};})()`);

// ---- real input -------------------------------------------------------------------------------------------------------------------
const MOD = {alt: 1, ctrl: 2, meta: 4, shift: 8};
const mods = list => list.reduce((m, k) => m | MOD[k], 0);
const mouse = (type, x, y, modifiers = 0, extra = {}) => send('Input.dispatchMouseEvent', {type, x, y, button: type === 'mouseMoved' && !extra.down ? 'none' : 'left', buttons: type === 'mouseReleased' || (type === 'mouseMoved' && !extra.down) ? 0 : 1, clickCount: type === 'mouseMoved' ? 0 : 1, modifiers});
const KEYS = {ArrowLeft: [37, 'ArrowLeft'], ArrowUp: [38, 'ArrowUp'], ArrowRight: [39, 'ArrowRight'], ArrowDown: [40, 'ArrowDown'], Delete: [46, 'Delete'], Escape: [27, 'Escape'], Enter: [13, 'Enter'], Tab: [9, 'Tab'], Backspace: [8, 'Backspace'], ' ': [32, 'Space']};
for (const c of 'abcdefghijklmnopqrstuvwxyz') KEYS[c] = [c.toUpperCase().charCodeAt(0), 'Key' + c.toUpperCase()];
const key = async (name, list = []) => {
  const [vk, code] = KEYS[name], modifiers = mods(list);
  // Enter and Space produce a character as well: with text the key is a 'keyDown', which is what makes a form submit or a button click.
  const text = !modifiers && name === 'Enter' ? '\r' : !modifiers && name === ' ' ? ' ' : undefined;
  await send('Input.dispatchKeyEvent', {type: text ? 'keyDown' : 'rawKeyDown', key: name, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers, ...(text ? {text, unmodifiedText: text} : {})});
  await send('Input.dispatchKeyEvent', {type: 'keyUp', key: name, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers});
};
const typeText = text => send('Input.insertText', {text});
// The canvas page in client pixels: real layout, so the scale and the origin are read, not assumed.
const geometry = () => evalJs(`(()=>{const p=document.querySelector('#doc-canvas-host .cv-paper').getBoundingClientRect();return {left:p.left,top:p.top,ppm:p.width/210};})()`);
const mmToClient = async (x, y) => { const g = await geometry(); return [g.left + x * g.ppm, g.top + y * g.ppm]; };
const scrollTo = selector => evalJs(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.scrollIntoView({block:'center',inline:'center'});return true;})()`);
const centre = selector => evalJs(`(()=>{const b=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return [b.left+b.width/2,b.top+b.height/2];})()`);
const clickSelector = async (selector, modifiers = []) => { await scrollTo(selector); await sleep(60); const [x, y] = await centre(selector); await mouse('mouseMoved', x, y); await mouse('mousePressed', x, y, mods(modifiers)); await mouse('mouseReleased', x, y, mods(modifiers)); await sleep(40); };
const item = id => `#doc-canvas-host [data-key="${id}"]`;
// Press at the centre of an element, move in steps by (dx, dy) millimetres, optionally hold, release.
async function dragFrom(selector, dxMm, dyMm, {modifiers = [], steps = 8, hold = false} = {}) {
  await scrollTo(selector); await sleep(60);
  const [x0, y0] = await centre(selector), g = await geometry(), x1 = x0 + dxMm * g.ppm, y1 = y0 + dyMm * g.ppm, m = mods(modifiers);
  await mouse('mouseMoved', x0, y0, m); await mouse('mousePressed', x0, y0, m);
  for (let i = 1; i <= steps; i++) await mouse('mouseMoved', x0 + (x1 - x0) * i / steps, y0 + (y1 - y0) * i / steps, m, {down: true});
  if (hold) return {release: async () => { await mouse('mouseReleased', x1, y1, m); await sleep(60); }};
  await mouse('mouseReleased', x1, y1, m); await sleep(80);
}
const tpl = async () => { await evalJs(`document.getElementById('doc-json-save').click()`); return JSON.parse(await evalJs(`document.getElementById('doc-json').value`)); };
const status = () => evalJs(`document.getElementById('doc-canvas-status').textContent`);
const selected = () => evalJs(`[...document.querySelectorAll('#doc-canvas-host [data-key][aria-pressed="true"]')].map(e=>e.getAttribute('data-key'))`);
const undoDisabled = () => evalJs(`document.getElementById('doc-undo').disabled`);
const READY = '模板已更新；配置不含业务数据。';
async function load(template) {
  await evalJs(`(()=>{const j=document.getElementById('doc-json');j.value=${JSON.stringify(JSON.stringify(template))};document.getElementById('doc-json-open').click();})()`);
  const text = await evalJs(`document.getElementById('doc-status').textContent`);
  if (text !== READY) throw new Error('template refused: ' + text);
}
// ---- the 2.0 geometry, as the canvas draws it (mm; integer tenths inside) --------------------------------------------------------------
const tenths = n => Math.round(n * 10), minDetail = size => Math.floor(((400 + 146 * size) * 4 * 254 + 7199) / 7200);
function geom(t) {
  const h = n => (t.bands[n] ? tenths(t.bands[n].height) : 0), ph = h('page_header'), dh = h('document_header'), df = h('document_footer'), pf = h('page_footer');
  const detail = Math.max(minDetail(t.bands.detail.elements[0].font_size), 2450 - ph - dh - pf - df), at = (top, height) => ({top: top / 10, height: height / 10});
  return {page_header: at(250, ph), document_header: at(250 + ph, dh), detail: at(250 + ph + dh, detail), document_footer: at(250 + ph + dh + detail, df), page_footer: at(2700 - pf, pf)};
}
const find2 = (t, id) => { for (const n of Object.keys(t.bands)) { const e = t.bands[n].elements.find(v => v.id === id); if (e) return {band: n, e}; } return null; };
const pageRect = (t, id) => { // [x, y, width, height] on the page
  const g = geom(t), {band, e} = find2(t, id), top = g[band].top;
  if (e.type === 'line') return [10 + Math.min(e.x1, e.x2), top + Math.min(e.y1, e.y2), Math.abs(e.x2 - e.x1), Math.abs(e.y2 - e.y1)];
  if (e.type === 'table') return [10 + e.x, top, e.width, g[band].height];
  return [10 + e.x, top + e.y, e.width, e.height];
};
const rel = (t, id) => { const {e} = find2(t, id); return e.type === 'line' ? [e.x1, e.y1, e.x2, e.y2] : [e.x, e.y, e.width, e.height]; };
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const text2 = (id, source, text, field, x, y, width, height, font_size, align) => ({id, type: 'text', source, text, field, wrap: false, x, y, width, height, font_size, align, bold: false});
// The 1.x invoice that is converted: a title and a customer above the table, a total and a signature line below it.
const invoice1x = binding => {
  const el = (id, source, what, x, y, width, height, font_size, align) => ({id, source, field: source === 'literal' ? '' : what, text: source === 'literal' ? what : '', x, y, width, height, font_size, align});
  return {schema_version: '1.3', kind: 'document', binding, elements: [el('n1', 'literal', '应收对账单', 10, 30, 100, 10, 18, 'left'), el('n2', 'row', 'customer.id', 120, 30, 80, 10, 12, 'right'), el('n3', 'literal', '客户：', 10, 45, 60, 8, 12, 'left'), el('n4', 'total', 'ar.open_balance', 120, 190, 80, 10, 12, 'right'), el('n5', 'literal', '制单人：', 10, 205, 100, 8, 12, 'left')],
    print: {mode: 'full', offset_x: 0, offset_y: 0}, table: {x: 10, y: 60, width: 190, height: 120, font_size: 9, columns: [{field: 'aging.bucket', width: 60}, {field: 'customer.id', width: 60}, {field: 'ar.open_balance', width: 70}]}};
};

// ---- real input on the 2.0 canvas -----------------------------------------------------------------------------------------------
const pageToClient = async (x, y) => { const g = await geometry(); return [g.left + x * g.ppm, g.top + y * g.ppm]; };
// Press at a page position (mm), move in steps to another, release (or hold). The pointer goes through the real layout, so what is under it decides.
async function dragPage(x0, y0, x1, y1, {modifiers = [], steps = 8, hold = false} = {}) {
  const [cx0, cy0] = await pageToClient(x0, y0), [cx1, cy1] = await pageToClient(x1, y1), m = mods(modifiers);
  await mouse('mouseMoved', cx0, cy0, m); await mouse('mousePressed', cx0, cy0, m);
  for (let i = 1; i <= steps; i++) await mouse('mouseMoved', cx0 + (cx1 - cx0) * i / steps, cy0 + (cy1 - cy0) * i / steps, m, {down: true});
  if (hold) return {release: async () => { await mouse('mouseReleased', cx1, cy1, m); await sleep(60); }};
  await mouse('mouseReleased', cx1, cy1, m); await sleep(80);
}
// Drag an element by its middle by (dx, dy) mm: its own rectangle on the page, from the template.
async function dragElement(id, dx, dy, o) { const t = await tpl(), [x, y, w, h] = pageRect(t, id); await scrollTo(item(id)); return dragPage(x + w / 2, y + h / 2, x + w / 2 + dx, y + h / 2 + dy, o); }
const clickElement = async (id, modifiers = []) => { await scrollTo(item(id)); await sleep(50); const t = await tpl(), [x, y, w, h] = pageRect(t, id), [cx, cy] = await pageToClient(x + w / 2, y + h / 2); await mouse('mouseMoved', cx, cy); await mouse('mousePressed', cx, cy, mods(modifiers)); await mouse('mouseReleased', cx, cy, mods(modifiers)); await sleep(50); };
const bandEdge = name => `#doc-canvas-host .cv-bh[data-band="${name}"]`, bandKey = name => `#doc-canvas-host .cv-bk[data-band="${name}"]`;
const handle = dir => `#doc-canvas-host .cv-h-${dir}`;
const val = id => evalJs(`document.getElementById(${JSON.stringify(id)}).value`);
const setSelect = (id, value) => evalJs(`(()=>{const s=document.getElementById(${JSON.stringify(id)});s.value=${JSON.stringify(value)};s.dispatchEvent(new Event('change',{bubbles:true}));return s.value;})()`);
// Type into an input with the real keyboard: click it, select its text, type, Tab (the change event).
async function typeInto(id, text) { await clickSelector('#' + id); await key('a', ['ctrl']); await typeText(text); await key('Tab'); await sleep(80); }
const docStatus = () => evalJs(`document.getElementById('doc-status').textContent`);
const notice = () => evalJs(`document.getElementById('notice').textContent`);
const outlined = () => evalJs(`({elements:[...document.querySelectorAll('#doc-canvas-host .cv-refused')].map(e=>e.getAttribute('data-key')),bands:[...document.querySelectorAll('#doc-canvas-host .cv-refused-band')].map(e=>e.getAttribute('data-band-box')),columns:[...document.querySelectorAll('#doc-canvas-host .cv-refused-col')].map(e=>e.getAttribute('data-refused'))})`);
const hookBlobs = () => evalJs(`(()=>{if(window.__pdfs)return;window.__pdfs=[];const o=URL.createObjectURL.bind(URL);URL.createObjectURL=b=>{window.__pdfs.push(b);return o(b);};})()`);
// A preview: the click, the PDF's bytes from the Blob the page made for it (so what is checked is what the server sent).
async function preview(label) {
  const n = await evalJs(`window.__pdfs.length`);
  await clickSelector('#doc-pdf');
  await waitFor(`window.__pdfs.length===${n + 1}&&!document.getElementById('preview').disabled`, 'preview ' + label, 30000);
  return evalJs(`(async()=>{const u=new Uint8Array(await window.__pdfs.at(-1).arrayBuffer());let s='';for(const c of u)s+=String.fromCharCode(c);return {size:u.length,head:s.slice(0,5),b64:btoa(s)};})()`);
}
// The template after each applied step, to compare with what undo and redo give back. A step that changed nothing is none.
const history = [];
const snap = async () => { const t = await tpl(), j = JSON.stringify(t); if (history.at(-1) !== j) history.push(j); return t; };

const COLOUR_PAIRS = `(()=>{const rgb=s=>{const m=/rgba?\\(([^)]+)\\)/.exec(s);if(!m)return null;const p=m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number);const a=p.length>3?p[3]:1;return p.slice(0,3).map(c=>c*a+255*(1-a));};
  const lum=c=>{const f=v=>{v/=255;return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};return 0.2126*f(c[0])+0.7152*f(c[1])+0.0722*f(c[2]);};
  const ratio=(a,b)=>{const x=lum(a),y=lum(b);return (Math.max(x,y)+0.05)/(Math.min(x,y)+0.05);};
  const q=(sel,prop)=>{const e=document.querySelector(sel);return e?rgb(getComputedStyle(e)[prop]):null;};
  const H='#doc-canvas-host ';
  const pairs=[
   ['band label on the desk beside the page',H+'.cv-band-tag','fill','#doc-canvas-scroll','backgroundColor',4.5],
   ['band note on the desk beside the page',H+'.cv-band-note','fill','#doc-canvas-scroll','backgroundColor',4.5],
   ['band outline on the paper',H+'.cv-band','stroke',H+'.cv-paper','fill',3],
   ['text on a literal box',H+'.cv-literal .cv-text','fill',H+'.cv-literal .cv-box','fill',4.5],
   ['text on a field box',H+'.cv-row .cv-text','fill',H+'.cv-row .cv-box','fill',4.5],
   ['text on a group-sum box',H+'.cv-total .cv-text','fill',H+'.cv-total .cv-box','fill',4.5],
   ['text on a page-text box',H+'.cv-page .cv-text','fill',H+'.cv-page .cv-box','fill',4.5],
   ['table header text on the header',H+'.cv-table .cv-bold','fill',H+'.cv-table .cv-thead','fill',4.5],
   ['sample row text on the table',H+'.cv-table .cv-sample','fill',H+'.cv-table .cv-box','fill',4.5],
   ['wrap marker on a field box',H+'.cv-wrap','fill',H+'.cv-row .cv-box','fill',4.5],
   ['item tag on the paper',H+'.cv-tag','fill',H+'.cv-paper','fill',4.5],
   ['selection outline on the paper',H+'.cv-selected .cv-box','stroke',H+'.cv-paper','fill',3],
   ['selected line on the paper',H+'.cv-line.cv-selected .cv-stroke','stroke',H+'.cv-paper','fill',3],
   ['line on the paper',H+'.cv-stroke','stroke',H+'.cv-paper','fill',3],
   ['panel label on the panel','#doc2-panel .cv2-grid label','color','#doc2-panel','backgroundColor',4.5],
   ['band name on the panel','#doc2-panel .cv2-name','color','#doc2-panel','backgroundColor',4.5],
   ['conversion note on the panel','#doc-convert-notes li','color','#doc-convert-panel','backgroundColor',4.5],
   ['group status on the panel','#doc2-group-status','color','#doc2-panel','backgroundColor',4.5],
  ];
  return pairs.map(([name,fs,fp,bs,bp,min])=>{const f=q(fs,fp),b=q(bs,bp);return {name,min,ratio:f&&b?Math.round(ratio(f,b)*100)/100:null};});})()`;

let fatal, browserName, served = {};
try {
  version = await (await fetch(`http://127.0.0.1:${cdp}/json/version`)).json();
  browserName = version.Browser;
  const target = (await (await fetch(`http://127.0.0.1:${cdp}/json/list`)).json()).find(t => t.type === 'page');
  page = connect(target.webSocketDebuggerUrl, m => {
    if (m.method === 'Runtime.exceptionThrown') problems.push('exception: ' + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
    if (m.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(m.params.type)) problems.push('console.' + m.params.type + ': ' + m.params.args.map(a => a.value ?? a.description).join(' '));
    if (m.method === 'Log.entryAdded' && ['error', 'warning'].includes(m.params.entry.level) && !(/status of 422/.test(m.params.entry.text) && /document-(previews|checks)/.test(m.params.entry.url || ''))) problems.push('log.' + m.params.entry.level + ': ' + m.params.entry.text + ' ' + (m.params.entry.url || ''));
    // A refused preview or check is an expected 422 of this script, not a failed request.
    if (m.method === 'Network.responseReceived' && m.params.response.status >= 400 && !(m.params.response.status === 422 && /document-(previews|checks)/.test(m.params.response.url))) problems.push('http ' + m.params.response.status + ' ' + m.params.response.url);
    if (m.method === 'Network.loadingFailed' && !m.params.canceled) problems.push('request failed: ' + m.params.errorText + ' ' + (m.params.blockedReason || ''));
    if (m.method === 'Page.javascriptDialogOpening') { dialogs.push(m.params.message); page.send('Page.handleJavaScriptDialog', {accept: false}).catch(() => {}); }
  });
  await page.ready;
  for (const [name, path] of [['index.html', '/console/'], ['styles.css', '/console/styles.css'], ['app.js', '/console/app.js'], ['documents.js', '/console/documents.js'], ['canvas.js', '/console/canvas.js']]) {
    const r = await fetch(base + path);
    served[name] = {sha256: createHash('sha256').update(Buffer.from(await r.arrayBuffer())).digest('hex'), csp: r.headers.get('content-security-policy')};
  }
  await send('Page.enable'); await send('Runtime.enable'); await send('Log.enable'); await send('Network.enable'); await send('Network.setCacheDisabled', {cacheDisabled: true});
  await send('Browser.setDownloadBehavior', {behavior: 'deny'}).catch(() => {});
  await sleep(300); problems.length = 0; dialogs.length = 0;
  await setViewport(VIEWPORTS[0]);
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&document.getElementById('access-token')`, 'console loaded');
  await sleep(300);
  await clickSelector('#access-token'); await typeText(token); await key('Enter');
  await waitFor(`document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`, 'login + catalog');
  await openPage('workspace');
  await evalJs(`(()=>{const i=document.getElementById('query-companies');i.value='company_demo';i.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  await clickSelector('#snapshot');
  await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'frozen snapshot', 30000);
  await openPage('documents'); await sleep(200);
  await hookBlobs();
  const binding = (await tpl()).binding;
  check('the preview, check and convert buttons are offered (the server has the PDF writer)', await evalJs(`!document.getElementById('doc-pdf').hidden&&!document.getElementById('doc-check').hidden`));
  await evalJs(`document.getElementById('doc-canvas-panel').scrollIntoView()`);

  // 1. A 1.x invoice on the 1.x canvas (S03-I, unchanged), then "转换为 2.0" with the real mouse.
  const one = invoice1x(binding);
  await load(one);
  check('the 1.x invoice is drawn by the 1.x canvas: five boxes and the table, no 2.0 panel', same(await evalJs(`[...document.querySelectorAll('#doc-canvas-host [data-key]')].map(e=>e.getAttribute('data-key'))`), ['n1', 'n2', 'n3', 'table', 'n4', 'n5']) && await evalJs(`document.getElementById('doc2-panel').hidden&&!document.getElementById('doc-convert').hidden`));
  await shot('step-01-1x-invoice', `document.getElementById('doc-canvas-panel')`);
  await clickSelector('#doc-convert');
  let t = await tpl();
  const expected = {schema_version: '2.0', kind: 'document', binding, bands: {
    document_header: {height: 35, elements: [text2('e1', 'literal', '应收对账单', '', 0, 5, 100, 10, 18, 'left'), text2('e2', 'field', '', 'customer.id', 110, 5, 80, 10, 12, 'right'), text2('e3', 'literal', '客户：', '', 0, 20, 60, 8, 12, 'left')]},
    detail: {elements: [{id: 'e4', type: 'table', x: 0, width: 190, font_size: 9, columns: [{field: 'aging.bucket', width: 60}, {field: 'customer.id', width: 60}, {field: 'ar.open_balance', width: 70}]}]},
    document_footer: {height: 33, elements: [text2('e5', 'group_sum', '', 'ar.open_balance', 110, 10, 80, 10, 12, 'right'), text2('e6', 'literal', '制单人：', '', 0, 25, 100, 8, 12, 'left')]}}};
  check('the conversion makes the 2.0 invoice: header above the table (35 mm), the table, the footer under it, ids top to bottom', same(t, expected), t);
  await snap();
  const notes = await evalJs(`[...document.querySelectorAll('#doc-convert-notes li')].map(e=>e.textContent)`);
  check('the conversion says what changed meaning: footer follows the table, row fields need a group, totals are group sums, the table height is gone', notes.length === 4 && /单据尾带/.test(notes[0]) && /分组字段/.test(notes[1]) && /分组合计/.test(notes[2]) && /高/.test(notes[3]), notes);
  check('the 2.0 canvas and panels replace the 1.x ones; the converted template is an unsaved change', await evalJs(`!document.getElementById('doc2-panel').hidden&&document.getElementById('doc-panel-elements').hidden&&document.getElementById('doc-convert').hidden&&!document.getElementById('doc-undo').disabled`));
  await shot('step-02-converted', `document.getElementById('doc-canvas-panel')`);
  await shot('step-02-converted-panels', `document.getElementById('doc-convert-panel')`);
  const bands = await evalJs(`[...document.querySelectorAll('#doc-canvas-host .cv-band')].map(b=>({n:b.getAttribute('data-band-box'),y:Number(b.getAttribute('y')),h:Number(b.getAttribute('height'))}))`);
  const g0 = geom(t), paper = await geometry();
  check('every band is drawn where the stacking rule puts it (SVG units from the real layout: the page corner is 24 px in)', same(bands.map(b => b.n), ['document_header', 'detail', 'document_footer']) && bands.every(b => { const g = g0[b.n]; return Math.abs(b.y - (24 + g.top * paper.ppm)) < 0.6 && Math.abs(b.h - g.height * paper.ppm) < 0.6; }), {bands, paper});

  // 2. Without a group the customer differs between the rows: the preview and the check refuse, with the place, and the canvas outlines it.
  await clickSelector('#doc-pdf');
  await waitFor(`document.getElementById('notice').textContent.includes('模板无法排版')&&!document.getElementById('preview').disabled`, 'refused preview', 30000);
  const n1 = await notice(), o1 = await outlined();
  check('a refused preview names the reason, the band and the element, and the canvas outlines both', /位置：同一分组内该字段的取值不一致 · 单据头带 · 元素 e2/.test(n1) && same(o1.elements, ['e2']) && same(o1.bands, ['document_header']), {n1, o1});
  await shot('step-03-refused', `document.getElementById('doc-canvas-panel')`);
  await clickSelector('#doc-check');
  await waitFor(`document.getElementById('doc-check-body').rows.length>0&&!document.getElementById('preview').disabled`, 'check rows', 30000);
  const checkRow = await evalJs(`[...document.getElementById('doc-check-body').rows].map(r=>[r.cells[0].textContent,r.cells[1].textContent,r.cells[3].querySelectorAll('button').length])`);
  check('the sample check refuses the same group for the same reason and offers to locate it on the canvas', checkRow.length >= 1 && checkRow.every(r => /不通过 .*同一分组内该字段的取值不一致 · 单据头带 · 元素 e2/.test(r[1]) && r[2] === 2), checkRow);
  await evalJs(`document.querySelector('#doc-check-body tr:first-child button:nth-of-type(2)').click()`);
  check('"在画布定位" outlines the element and its band again', same((await outlined()).elements, ['e2']) && same((await outlined()).bands, ['document_header']), await outlined());

  // 3. Group by customer (the select): the outline goes, the preview succeeds.
  check('the group select offers the dimensions only', same(await evalJs(`[...document.getElementById('doc2-group-0').options].map(o=>o.value)`), ['', 'customer.id', 'aging.bucket']));
  await setSelect('doc2-group-0', 'customer.id');
  t = await snap(); check('the group is customer.id', same(t.group, {fields: ['customer.id']}), t.group);
  check('an edit takes the outline away', same(await outlined(), {elements: [], bands: [], columns: []}));
  await evalJs(`document.getElementById('doc-row').value='3'`);
  let pdf = await preview('grouped');
  check('the 2.0 preview of the row\'s group is a PDF', pdf.head === '%PDF-' && pdf.size > 5000, {head: pdf.head, size: pdf.size});
  writeFileSync(join(outDir, 'step-04-grouped-preview.pdf'), Buffer.from(pdf.b64, 'base64'));

  // 4. Edit with the mouse: a text across a drag, the band's edge, a text into another band.
  await clickElement('e1');
  check('a click selects the title and the panel shows its properties', same(await selected(), ['e1']) && /e1 · 单据头带 · 文本（固定文字）/.test(await evalJs(`document.getElementById('doc2-p-title').textContent`)));
  await dragElement('e1', 7.4, 0);
  t = await snap(); check('dragging the title 7.4 mm right lands on the 1 mm grid, x 7 in the band', same(rel(t, 'e1'), [7, 5, 100, 10]), rel(t, 'e1'));
  check('the x input follows the canvas', (await val('doc2-p-x')) === '7');
  await dragElement('e1', -7.2, 0, {modifiers: ['alt']});
  t = await snap(); check('the band\'s own left edge within 1 mm wins over the grid, Alt held or not: the title lands on x 0', same(rel(t, 'e1'), [0, 5, 100, 10]), rel(t, 'e1'));
  // The document header's bottom edge, 5.3 mm lower: the table moves down with it.
  const before = await evalJs(`Number(document.querySelector('#doc-canvas-host [data-key="e4"] .cv-box').getAttribute('y'))`);
  await scrollTo(bandEdge('document_header')); const [ex, ey0] = await centre(bandEdge('document_header')), gg = await geometry(), ey = ey0 - 2;
  await mouse('mouseMoved', ex, ey); await mouse('mousePressed', ex, ey);
  for (let i = 1; i <= 6; i++) await mouse('mouseMoved', ex, ey + 5.3 * gg.ppm * i / 6, 0, {down: true});
  check('while the edge is dragged a readout says the height', /单据头带高度 40 毫米/.test(await evalJs(`(document.querySelector('#doc-canvas-host .cv-readout')||{textContent:''}).textContent`)));
  await mouse('mouseReleased', ex, ey + 5.3 * gg.ppm); await sleep(80);
  t = await snap(); check('the band is 40 mm high now (35 + 5.3 on the 1 mm grid), and the detail band starts lower', t.bands.document_header.height === 40 && geom(t).detail.top === 65, {h: t.bands.document_header.height, top: geom(t).detail.top});
  const after = await evalJs(`Number(document.querySelector('#doc-canvas-host [data-key="e4"] .cv-box').getAttribute('y'))`);
  check('the table is drawn 5 mm lower on screen', Math.abs(after - before - 5 * gg.ppm) < 1.5, {before, after});
  await shot('step-05-band-edge', `document.getElementById('doc-canvas-panel')`);

  // The panel: a page footer band and a page text in it; the signature moves into it by a drag across the boundary.
  await clickSelector('#doc2-band-add-page_footer');
  t = await snap(); check('the page footer band is added with its default height', same(t.bands.page_footer, {height: 12, elements: []}), t.bands.page_footer);
  await evalJs(`(()=>{const s=document.getElementById('doc2-add-band');s.value='page_footer';})()`);
  await typeInto('doc2-text', '第 {page} 页 / 共 {pages} 页');
  await clickSelector('#doc2-add-page');
  t = await snap(); const pg = t.bands.page_footer.elements[0];
  check('a page text is added to the page footer and selected', pg && pg.source === 'page' && pg.text === '第 {page} 页 / 共 {pages} 页' && same(await selected(), [pg.id]), t.bands.page_footer);
  await typeInto('doc2-p-x', '130');
  const afterTab = await evalJs(`document.activeElement.id`);
  check('after a value is typed and Tab pressed the focus is on the next control of the panel, not lost to the page', afterTab === 'doc2-p-y', afterTab);
  await snap(); await typeInto('doc2-p-width', '60'); await snap(); await setSelect('doc2-p-align', 'right');
  t = await snap(); check('its properties are typed into the panel: x 130, width 60, right-aligned', same([find2(t, pg.id).e.x, find2(t, pg.id).e.width, find2(t, pg.id).e.align], [130, 60, 'right']), find2(t, pg.id).e);
  check('the canvas shows the placeholders', (await evalJs(`document.querySelector('#doc-canvas-host [data-key="${pg.id}"] text').textContent`)) === '第 {page} 页 / 共 {pages} 页');
  // e6 (the signature, in the document footer) dragged into the page footer: the pointer ends in the page footer band.
  t = await tpl();
  const gp = geom(t), [sx, sy, sw, sh] = pageRect(t, 'e6'), targetY = gp.page_footer.top + 2 + sh / 2;
  await scrollTo(item('e6')); await dragPage(sx + sw / 2, sy + sh / 2, sx + sw / 2, targetY);
  t = await snap(); check('dragging the signature across the boundary moves it into the page footer, relative to that band', find2(t, 'e6').band === 'page_footer' && Math.abs(find2(t, 'e6').e.y - 2) <= 1 && t.bands.document_footer.elements.length === 1, {band: find2(t, 'e6').band, y: find2(t, 'e6').e.y});
  await shot('step-06-moved-into-band', `document.getElementById('doc-canvas-panel')`);
  // A move the template refuses goes back and says why: the customer text dropped on the title.
  const keep = JSON.stringify(await tpl());
  await dragElement('e2', -100, 0);
  check('a move the template refuses (onto the title) goes back, with the reason on the status line', JSON.stringify(await tpl()) === keep && /^未应用：同一带内的文本框不能重叠/.test(await status()), await status());

  // 5. A line, a rectangle, the title in bold, the table's columns.
  await evalJs(`(()=>{const s=document.getElementById('doc2-add-band');s.value='document_header';})()`);
  await clickSelector('#doc2-add-line');
  t = await tpl(); const ln = t.bands.document_header.elements.at(-1);
  await snap(); check('a line is added along the header\'s bottom edge', ln.type === 'line' && same([ln.x1, ln.y1, ln.x2, ln.y2], [0, 40, 190, 40]), ln);
  check('a selected line has its two end handles, not the eight', (await evalJs(`document.querySelectorAll('#doc-canvas-host .cv-end').length`)) === 2 && (await evalJs(`document.querySelectorAll('#doc-canvas-host [data-dir]').length`)) === 0);
  // The right end 30 mm to the left, by its handle.
  await scrollTo('#doc-canvas-host .cv-end[data-end="2"]'); const [lx, ly] = await centre('#doc-canvas-host .cv-end[data-end="2"]'), g2 = await geometry();
  await mouse('mouseMoved', lx, ly); await mouse('mousePressed', lx, ly); for (let i = 1; i <= 6; i++) await mouse('mouseMoved', lx - 30 * g2.ppm * i / 6, ly, 0, {down: true}); await mouse('mouseReleased', lx - 30 * g2.ppm, ly); await sleep(80);
  t = await snap(); check('dragging the line\'s end handle shortens the line by 30 mm', same(rel(t, ln.id), [0, 40, 160, 40]), rel(t, ln.id));
  await clickElement('e1'); await clickSelector('#doc2-p-bold'); t = await snap();
  check('the bold checkbox is clicked with the mouse', find2(t, 'e1').e.bold === true && (await evalJs(`document.querySelector('#doc-canvas-host [data-key="e1"] text').getAttribute('font-weight')`)) === '700');
  await clickElement('e2'); await clickSelector('#doc2-p-wrap'); t = await snap();
  check('the wrap checkbox is clicked with the mouse: the customer text wraps, and the canvas marks it', find2(t, 'e2').e.wrap === true && (await evalJs(`!!document.querySelector('#doc-canvas-host [data-key="e2"] .cv-wrap')`)));
  await evalJs(`(()=>{const s=document.getElementById('doc2-add-band');s.value='document_footer';})()`);
  await clickSelector('#doc2-add-rect'); t = await tpl(); const rc = t.bands.document_footer.elements.at(-1);
  await snap(); await setSelect('doc2-p-fill', 'grey2'); await snap(); await typeInto('doc2-p-x', '100'); await snap(); await typeInto('doc2-p-y', '2'); await snap(); await typeInto('doc2-p-width', '90'); await snap(); await typeInto('doc2-p-height', '10');
  t = await snap(); check('a grey rectangle is added to the footer and placed by its inputs', same(find2(t, rc.id).e, {id: rc.id, type: 'rect', x: 100, y: 2, width: 90, height: 10, stroke: 0.5, fill: 'grey2'}), find2(t, rc.id).e);
  // Columns: a header label typed, an alignment, and a boundary dragged with the mouse.
  await typeInto('doc2-col-label-0', '账龄'); await snap(); await setSelect('doc2-col-align-0', 'center');
  t = await snap(); check('a column gets a literal header label and an alignment', t.bands.detail.elements[0].columns[0].label === '账龄' && t.bands.detail.elements[0].columns[0].align === 'center', t.bands.detail.elements[0].columns[0]);
  check('the canvas header shows the label', same(await evalJs(`[...document.querySelectorAll('#doc-canvas-host [data-key="e4"] .cv-bold')].map(e=>e.textContent)`), ['账龄', '客户', '应收余额']));
  await scrollTo('#doc-canvas-host [data-col="0"]'); const [cx, cy] = await centre('#doc-canvas-host [data-col="0"]'), g3 = await geometry();
  await mouse('mouseMoved', cx, cy); await mouse('mousePressed', cx, cy); for (let i = 1; i <= 6; i++) await mouse('mouseMoved', cx + 10 * g3.ppm * i / 6, cy, 0, {down: true}); await mouse('mouseReleased', cx + 10 * g3.ppm, cy); await sleep(80);
  t = await snap(); check('dragging a column boundary 10 mm right widens the left column and narrows its neighbour; the table keeps its width', same(t.bands.detail.elements[0].columns.map(c => c.width), [70, 50, 70]), t.bands.detail.elements[0].columns);
  await shot('step-07-edited', `document.getElementById('doc-canvas-panel')`);
  await shot('step-07-panels', `document.getElementById('doc2-panel')`);

  // 6. The keyboard: Tab to a band's edge, arrows; Shift-click and align; undo.
  await evalJs(`document.querySelector(${JSON.stringify(bandKey('page_footer'))}).focus()`);
  const hBefore = (await tpl()).bands.page_footer.height; await key('ArrowDown', ['shift']);
  t = await snap(); check('the arrow keys change the band height from its edge: Shift+Down is 1 mm', t.bands.page_footer.height === hBefore + 1, t.bands.page_footer.height);
  check('the edge keeps the focus after the redraw', (await evalJs(`document.activeElement.getAttribute('data-band')`)) === 'page_footer');
  await key('ArrowUp', ['shift']); await snap();
  await clickElement('e5'); await clickElement(rc.id, ['shift']);
  check('Shift-click adds the second element', same((await selected()).sort(), ['e5', rc.id].sort()));
  await clickSelector('#doc-align-left'); t = await snap(); check('Align left puts the sum and the rectangle at the same x (0.1 mm units)', find2(t, 'e5').e.x === find2(t, rc.id).e.x, [find2(t, 'e5').e.x, find2(t, rc.id).e.x]);

  // 7. Preview again, check the samples, save and reopen.
  pdf = await preview('edited'); check('the edited template previews as a PDF', pdf.head === '%PDF-' && pdf.size > 5000, {size: pdf.size});
  writeFileSync(join(outDir, 'step-08-edited-preview.pdf'), Buffer.from(pdf.b64, 'base64'));
  await clickSelector('#doc-check'); await waitFor(`document.getElementById('doc-check-body').rows.length>0&&!document.getElementById('preview').disabled`, 'check rows', 30000);
  const rows = await evalJs(`[...document.getElementById('doc-check-body').rows].map(r=>[r.cells[0].textContent,r.cells[1].textContent])`);
  check('the sample check agrees with the preview: every sampled group passes', rows.length >= 1 && rows.every(r => r[1] === '通过'), rows);
  await clickSelector('#doc-server-save'); await waitFor(`document.getElementById('doc-server-status').textContent.includes('版本 1')&&!document.getElementById('preview').disabled`, 'saved');
  const saved = await tpl();
  check('saving keeps the 2.0 template as it is (the console compared it with the server\'s copy)', await evalJs(`document.getElementById('doc-server-status').textContent.includes('已保存模板')`) && saved.schema_version === '2.0');

  // 8. Undo walks back through every step and redo forward again.
  const total = history.length; let ok = true;
  for (let i = total - 2; i >= 0; i--) { await key('z', ['ctrl']); await sleep(40); if (JSON.stringify(await tpl()) !== history[i]) { ok = false; break; } }
  check(`Ctrl-Z walks back through all ${total} applied steps, each to the template of that step`, ok, 'a step differs');
  check('the first step is the conversion: one more undo is the 1.x template again, exactly', await (async () => { await key('z', ['ctrl']); await sleep(60); return same(await tpl(), one); })());
  for (let i = 0; i < total; i++) { await key('y', ['ctrl']); await sleep(30); }
  check('Ctrl-Y redoes them all', JSON.stringify(await tpl()) === history.at(-1), 'the last step differs');

  // 9. Three widths: no horizontal overflow, the canvas scrolls inside its panel, the 2.0 panels fit.
  for (const vp of VIEWPORTS) {
    await setViewport(vp); await sleep(200);
    const [name] = vp;
    await evalJs(`document.getElementById('doc-canvas-panel').scrollIntoView()`); await sleep(100);
    const o = await pageOverflow();
    check(`${name}: no horizontal overflow of the page`, !o.over, o);
    const inner = await evalJs(`(()=>{const s=document.getElementById('doc-canvas-scroll'),c=document.getElementById('doc-canvas-panel').getBoundingClientRect();return {scrollW:s.scrollWidth,clientW:s.clientWidth,panelRight:Math.round(c.right),vw:document.documentElement.clientWidth};})()`);
    check(`${name}: the canvas scrolls inside its own panel`, inner.panelRight <= inner.vw + 1 && (vp[1] >= 1000 || inner.scrollW > inner.clientW), inner);
    await shot(`${name}-01-canvas`, `document.getElementById('doc-canvas-panel')`);
    await shot(`${name}-02-panels`, `document.getElementById('doc2-panel')`);
    const clipped = await evalJs(`[...document.querySelectorAll('#doc2-panel button,#doc2-panel select,#doc2-panel input,#doc-convert-panel button')].filter(e=>{const r=e.getBoundingClientRect();return r.width>0&&(r.right>document.documentElement.clientWidth+1||r.left<-1);}).map(e=>e.id)`);
    check(`${name}: no 2.0 panel control is clipped by the viewport`, clipped.length === 0, clipped);
    // The band edge and an element can still be operated at this width: select an element with a touch-sized click.
    await clickElement('e1'); check(`${name}: an element is selected by a click at this width`, same(await selected(), ['e1']), await selected());
    await key('Escape');
  }
  await setViewport(VIEWPORTS[0]);

  // 10. Colours in the light and the dark scheme.
  // A box and a line selected, so that both outlines are measured.
  const lineId = (await tpl()).bands.document_header.elements.find(e => e.type === 'line').id;
  await clickElement('e1'); await clickElement(lineId, ['shift']); await evalJs(`document.getElementById('doc-canvas-panel').scrollIntoView()`);
  for (const scheme of ['light', 'dark']) {
    await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: scheme}]}); await sleep(200);
    const ratios = await evalJs(COLOUR_PAIRS); evidence['contrast_' + scheme] = ratios;
    const failing = ratios.filter(r => r.ratio === null || r.ratio < r.min);
    check(`${scheme} scheme: every 2.0 canvas text and outline meets its contrast (4.5:1 text, 3:1 outlines)`, !failing.length, failing);
    await shot(`desktop-03-${scheme}`, `document.getElementById('doc-canvas-panel')`);
  }
  await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: 'light'}, {name: 'forced-colors', value: 'active'}]}); await sleep(200);
  await shot('desktop-04-forced-colors', `document.getElementById('doc-canvas-panel')`);
  check('forced colours: the bands and elements remain drawn', (await evalJs(`document.querySelectorAll('#doc-canvas-host .cv-band').length`)) >= 4 && (await evalJs(`document.querySelectorAll('#doc-canvas-host [data-key]').length`)) >= 8);
  await send('Emulation.setEmulatedMedia', {features: []});

  // 11. The policy is the console's, byte for byte, on every served asset.
  const policies = new Set(Object.values(served).map(v => v.csp));
  check('every served asset carries the same Content-Security-Policy', policies.size === 1 && [...policies][0] === "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'", [...policies]);
  evidence.served_assets_sha256 = Object.fromEntries(Object.entries(served).map(([k, v]) => [k, v.sha256]));
} catch (e) {
  fatal = String(e.message || e); console.log('FATAL', fatal);
  try { await shot('zz-failure'); } catch (_) { /* page may be gone */ }
}
check('no console errors, CSP violations, failed requests or exceptions', !problems.length, [...new Set(problems)]);
check('no JavaScript dialogs', !dialogs.length, dialogs);
if (fatal) check('script ran to completion', false, fatal);
const failed = checks.filter(c => !c.ok);
writeFileSync(join(outDir, 'report.json'), JSON.stringify({base, browser: browserName, user_agent: version?.['User-Agent'], viewports: VIEWPORTS.map(v => v.slice(0, 3)), passed: checks.length - failed.length, failed: failed.length, evidence, checks}, null, 2));
console.log(`\n${checks.length - failed.length}/${checks.length} checks passed`);
page?.close();
process.exit(failed.length ? 1 : 0);
