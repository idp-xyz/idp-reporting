// Real-browser acceptance for the S03-I document canvas: real mouse and keyboard input over the Chrome DevTools Protocol,
// the template JSON checked after every step, screenshots at 390, 768 and 1280 px, no horizontal overflow, colour contrast
// in the light and the dark scheme. Drives an external headless Chrome with no dependencies (Node 22+ global WebSocket/fetch).
// Not part of `npm test` and not CI: it needs a running reporting-demo started with the PDF flags and a template directory
// (the check route is mounted with the PDF writer), and a Chrome started with
//   --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server
// It exits 1 when any check fails or the run aborts, and 2 on bad arguments; report.json is written either way.
// Safety: --base must be loopback because the bearer token is typed into the page. The script reads and writes only the
// page it drives and its own files in --out (PNGs named after the viewport or the step, report.json); it removes nothing else.
// Usage: node scripts/browser_canvas_s03i.mjs --out DIR --token-file FILE [--base URL] [--cdp PORT]
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
const box = async id => (await tpl()).elements.find(e => e.id === id);
const status = () => evalJs(`document.getElementById('doc-canvas-status').textContent`);
const selected = () => evalJs(`[...document.querySelectorAll('#doc-canvas-host [data-key][aria-pressed="true"]')].map(e=>e.getAttribute('data-key'))`);
const undoDisabled = () => evalJs(`document.getElementById('doc-undo').disabled`);
const READY = '模板已更新；配置不含业务数据。';
async function load(template) {
  await evalJs(`(()=>{const j=document.getElementById('doc-json');j.value=${JSON.stringify(JSON.stringify(template))};document.getElementById('doc-json-open').click();})()`);
  const text = await evalJs(`document.getElementById('doc-status').textContent`);
  if (text !== READY) throw new Error('template refused: ' + text);
}
const doc = (binding, list, extra = {}) => ({schema_version: '1.0', kind: 'document', binding, elements: list.map(([id, x, y, width, height, source = 'literal', what]) => ({id, source, field: source === 'literal' ? '' : what, text: source === 'literal' ? (what ?? id + ' text') : '', x, y, width, height, font_size: 12, align: 'left'})), ...extra});
const withTable = (binding, list, columns = [['customer.id', 60], ['aging.bucket', 70], ['ar.open_balance', 60]]) => doc(binding, list, {schema_version: '1.3', print: {mode: 'full', offset_x: 0, offset_y: 0}, table: {x: 10, y: 100, width: columns.reduce((s, c) => s + c[1], 0), height: 100, font_size: 9, columns: columns.map(([field, width]) => ({field, width}))}});
const four = b => doc(b, [['n1', 10, 30, 50, 10], ['n2', 80, 30, 50, 10, 'row', 'customer.id'], ['n3', 150, 30, 40, 10, 'total', 'ar.open_balance'], ['n4', 10, 60, 50, 10]]);
const near = (a, b) => Math.abs(a - b) < 1e-9;
// Ctrl-A must go to the canvas: an item is clicked first so that the focus is on it (a Ctrl-A with the focus on a toolbar button
// would select the page's text instead), and the page's own text selection must stay empty.
const pageSelection = () => evalJs(`window.getSelection().toString()`);
async function selectAllOnCanvas(label) {
  await clickSelector(item('n1'));
  const focused = await evalJs(`(document.activeElement.getAttribute('data-key')||document.activeElement.tagName)`);
  await key('a', ['ctrl']);
  const text = await pageSelection();
  check(`Ctrl-A on the canvas (${label}) selects boxes, not page text`, focused === 'n1' && text === '', {focused, text: text.slice(0, 80)});
}

// Contrast of a foreground colour against a background, both as computed 'rgb(r, g, b)' strings, composited on white when translucent.
const contrastJs = `(()=>{const rgb=s=>{const m=/rgba?\\(([^)]+)\\)/.exec(s);if(!m)return null;const p=m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number);const a=p.length>3?p[3]:1;return p.slice(0,3).map(c=>c*a+255*(1-a));};
  const lum=c=>{const f=v=>{v/=255;return v<=0.03928?v/12.92:Math.pow((v+0.055)/1.055,2.4);};return 0.2126*f(c[0])+0.7152*f(c[1])+0.0722*f(c[2]);};
  const ratio=(a,b)=>{const x=lum(a),y=lum(b);return (Math.max(x,y)+0.05)/(Math.min(x,y)+0.05);};
  const q=(sel,prop)=>{const e=document.querySelector(sel);return e?rgb(getComputedStyle(e)[prop]):null;};
  const pairs=[
   ['ruler label on the ruler','#doc-canvas-host .cv-ruler-text','fill','#doc-canvas-host .cv-ruler','fill',4.5],
   ['box text on a literal box','#doc-canvas-host .cv-literal .cv-text','fill','#doc-canvas-host .cv-literal .cv-box','fill',4.5],
   ['box text on a row-field box','#doc-canvas-host .cv-row .cv-text','fill','#doc-canvas-host .cv-row .cv-box','fill',4.5],
   ['box text on a total box','#doc-canvas-host .cv-total .cv-text','fill','#doc-canvas-host .cv-total .cv-box','fill',4.5],
   ['table header text on the header','#doc-canvas-host .cv-table .cv-text','fill','#doc-canvas-host .cv-table .cv-thead','fill',4.5],
   ['item tag on the paper','#doc-canvas-host .cv-tag','fill','#doc-canvas-host .cv-paper','fill',4.5],
   ['strip text on the strip','#doc-canvas-host .cv-strip-text','fill','#doc-canvas-host .cv-strip','fill',4.5],
   ['safe-region label on the paper','#doc-canvas-host .cv-safe-text','fill','#doc-canvas-host .cv-paper','fill',4.5],
   ['selection outline on the paper','#doc-canvas-host .cv-selected .cv-box','stroke','#doc-canvas-host .cv-paper','fill',3],
   ['safe-region outline on the paper','#doc-canvas-host .cv-safe','stroke','#doc-canvas-host .cv-paper','fill',3],
   ['guide on the paper','#doc-canvas-host .cv-guide','stroke','#doc-canvas-host .cv-paper','fill',3],
   ['live line on the panel','#doc-canvas-live','color','#doc-canvas-panel','backgroundColor',4.5],
   ['help text on the panel','#doc-canvas-help','color','#doc-canvas-panel','backgroundColor',4.5],
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
    if (m.method === 'Log.entryAdded' && ['error', 'warning'].includes(m.params.entry.level)) problems.push('log.' + m.params.entry.level + ': ' + m.params.entry.text + ' ' + (m.params.entry.url || ''));
    if (m.method === 'Network.responseReceived' && m.params.response.status >= 400) problems.push('http ' + m.params.response.status + ' ' + m.params.response.url);
    if (m.method === 'Network.loadingFailed' && !m.params.canceled) problems.push('request failed: ' + m.params.errorText + ' ' + (m.params.blockedReason || ''));
    if (m.method === 'Page.javascriptDialogOpening') { dialogs.push(m.params.message); page.send('Page.handleJavaScriptDialog', {accept: false}).catch(() => {}); }
  });
  await page.ready;
  for (const [name, path] of [['index.html', '/console/'], ['styles.css', '/console/styles.css'], ['app.js', '/console/app.js'], ['documents.js', '/console/documents.js'], ['canvas.js', '/console/canvas.js']]) {
    const r = await fetch(base + path);
    served[name] = {sha256: createHash('sha256').update(Buffer.from(await r.arrayBuffer())).digest('hex'), csp: r.headers.get('content-security-policy')};
  }
  await send('Page.enable'); await send('Runtime.enable'); await send('Log.enable'); await send('Network.enable'); await send('Network.setCacheDisabled', {cacheDisabled: true});
  await sleep(300); problems.length = 0; dialogs.length = 0;
  await setViewport(VIEWPORTS[0]);
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&document.getElementById('access-token')`, 'console loaded');
  await sleep(300); await shot('00-login');
  // Log in by typing the token into the field and pressing Enter.
  await clickSelector('#access-token'); await typeText(token); await key('Enter');
  await waitFor(`document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`, 'login + catalog');
  await openPage('workspace');
  await evalJs(`(()=>{const i=document.getElementById('query-companies');i.value='company_demo';i.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  await clickSelector('#snapshot');
  await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'frozen snapshot', 30000);
  await openPage('documents'); await sleep(200);
  const binding = (await tpl()).binding;
  check('the check button is offered (the server has the PDF writer)', await evalJs(`!document.getElementById('doc-check').hidden`));
  await evalJs(`document.getElementById('doc-canvas-panel').scrollIntoView()`);

  // 1. The canvas draws the template.
  await load(four(binding));
  const drawn = await evalJs(`(()=>{const h=document.getElementById('doc-canvas-host');const p=h.querySelector('.cv-paper').getBoundingClientRect();return {svg:!!h.querySelector('svg'),rulerLabels:h.querySelectorAll('.cv-ruler-text').length,safe:!!h.querySelector('.cv-safe'),strip:!!h.querySelector('.cv-strip'),keys:[...h.querySelectorAll('[data-key]')].map(e=>e.getAttribute('data-key')),tags:[...h.querySelectorAll('.cv-tag')].map(e=>e.textContent),ratio:p.height/p.width,widthPx:p.width,labels:[...h.querySelectorAll('[data-key] text')].map(e=>e.textContent),named:[...h.querySelectorAll('[data-key]')].every(e=>e.getAttribute('aria-label')&&e.getAttribute('role')==='button')};})()`);
  check('the A4 canvas draws rulers, the safe region, the strip and every item in reading order with its source and label', drawn.svg && drawn.rulerLabels > 20 && drawn.safe && drawn.strip && JSON.stringify(drawn.keys) === '["n1","n2","n3","n4"]' && JSON.stringify(drawn.tags) === '["n1 · 固定文字","n2 · 行字段","n3 · 合计","n4 · 固定文字"]' && drawn.labels.includes('〔客户〕') && drawn.named && Math.abs(drawn.ratio - 297 / 210) < 0.01 && near(drawn.widthPx, 630), drawn);
  await shot('step-01-drawn', `document.getElementById('doc-canvas-panel')`);

  // 2. Selection with the real mouse.
  await clickSelector(item('n1'));
  check('a click selects the item', JSON.stringify(await selected()) === '["n1"]' && /已选择 n1/.test(await evalJs(`document.getElementById('doc-canvas-live').textContent`)), await selected());
  await clickSelector(item('n2'), ['shift']);
  check('Shift-click adds to the selection', JSON.stringify(await selected()) === '["n1","n2"]', await selected());
  await clickSelector(item('n1'), ['shift']);
  check('Shift-click removes from the selection', JSON.stringify(await selected()) === '["n2"]', await selected());
  await scrollTo(item('n1')); await sleep(60);
  const [ex, ey] = await mmToClient(105, 85), hit = await evalJs(`document.elementFromPoint(${ex},${ey}).getAttribute('class')`);
  check('the point clicked for "empty paper" is the paper', /cv-paper/.test(hit), hit);
  await mouse('mouseMoved', ex, ey); await mouse('mousePressed', ex, ey); await mouse('mouseReleased', ex, ey);
  check('a click on empty paper clears the selection', (await selected()).length === 0, await selected());
  await clickSelector(item('n1')); await key('Escape');
  check('Esc clears the selection', (await selected()).length === 0, await selected());
  await selectAllOnCanvas('every box');
  check('Ctrl-A selects every box', (await selected()).length === 4, await selected());
  await key('Escape');

  // 3. Drag with snapping: 10.4 mm to the right snaps to the 1 mm grid.
  await dragFrom(item('n1'), 10.4, 0);
  let t = await tpl();
  check('a mouse drag moves the box on the 1 mm grid (10.4 mm -> x 20)', near(t.elements[0].x, 20) && t.elements[0].y === 30 && t.schema_version === '1.0', t.elements[0]);
  await shot('step-02-after-drag', `document.getElementById('doc-canvas-panel')`);
  await key('z', ['ctrl']);
  check('Ctrl-Z undoes the whole drag (one step)', (await box('n1')).x === 10 && await undoDisabled(), await box('n1'));
  // Alt: 0.1 mm.
  await dragFrom(item('n1'), 10.4, 0, {modifiers: ['alt']});
  t = await tpl();
  check('Alt-drag snaps to 0.1 mm and a schema 1.0 template becomes 1.1', near(t.elements[0].x, 20.4) && t.schema_version === '1.1', t);
  await key('z', ['ctrl']);
  // Edge snap with a guide, observed while the button is still down.
  let held = await dragFrom(item('n1'), 19.6, 0, {hold: true});
  const guide = await evalJs(`(()=>{const g=[...document.querySelectorAll('#doc-canvas-host .cv-guide')];const p=document.querySelector('#doc-canvas-host .cv-paper').getBoundingClientRect();return {count:g.length,x:g.length?g[0].getBoundingClientRect().left+g[0].getBoundingClientRect().width/2:null,paperLeft:p.left,ppm:p.width/210,readout:(document.querySelector('#doc-canvas-host .cv-readout')||{}).textContent};})()`);
  await shot('step-03-guide-while-dragging', `document.getElementById('doc-canvas-panel')`);
  await held.release();
  check('while dragging near another item\'s edge a guide shows at that edge and the readout names the place', guide.count === 1 && Math.abs((guide.x - guide.paperLeft) / guide.ppm - 80) < 0.3 && /x 30/.test(guide.readout || ''), guide);
  check('the box snapped to the edge of n2 (right edge on x 80)', near((await box('n1')).x, 30) && (await evalJs(`document.querySelectorAll('#doc-canvas-host .cv-guide').length`)) === 0, await box('n1'));
  await key('z', ['ctrl']);
  // Several items move together.
  await clickSelector(item('n1')); await clickSelector(item('n4'), ['shift']); await dragFrom(item('n4'), 10.3, 0);
  t = await tpl();
  check('a drag moves every selected item together', near(t.elements[0].x, 20) && near(t.elements[3].x, 20) && t.elements[1].x === 80, t.elements);
  await key('z', ['ctrl']); await key('Escape');

  // 4. Resize with the corner handle; a refused resize snaps back with the reason.
  await clickSelector(item('n1'));
  await dragFrom('#doc-canvas-host .cv-h-se', 5.1, 4.9);
  t = await tpl();
  check('the south-east handle resizes the box on the grid (5.1 x 4.9 mm -> 55 x 15)', t.elements[0].width === 55 && t.elements[0].height === 15 && t.elements[0].x === 10, t.elements[0]);
  const jsonBefore = JSON.stringify(await tpl());
  held = await dragFrom('#doc-canvas-host .cv-h-n', 0, 9, {hold: true});
  const refusing = await evalJs(`({cls:document.querySelector('#doc-canvas-host [data-key="n1"]').getAttribute('class'),readout:(document.querySelector('#doc-canvas-host .cv-readout')||{}).textContent})`);
  await shot('step-04-refused-while-dragging', `document.getElementById('doc-canvas-panel')`);
  await held.release();
  check('a drag that the designer will refuse is drawn in the refusal colour with a warning while it lasts', /cv-bad/.test(refusing.cls) && /将被拒绝/.test(refusing.readout || ''), refusing);
  check('a refused resize leaves the template unchanged, snaps back and names the reason', JSON.stringify(await tpl()) === jsonBefore && /^未应用：文本框超出A4安全区、重复或尺寸无效/.test(await status()) && !/cv-bad/.test(await evalJs(`document.querySelector('#doc-canvas-host [data-key="n1"]').getAttribute('class')`)), await status());
  await shot('step-05-refusal-status', `document.getElementById('doc-canvas-panel')`);
  await key('z', ['ctrl']);
  check('the refused edit was no undo step (one undo returned to the original box)', (await box('n1')).width === 50 && await undoDisabled(), await box('n1'));
  // Refused move: above the safe region.
  const beforeRefused = JSON.stringify(await tpl());
  await dragFrom(item('n1'), 0, -10);
  check('a drag out of the safe region is refused: unchanged, no step, reason on the status line', JSON.stringify(await tpl()) === beforeRefused && await undoDisabled() && /未应用：文本框超出A4安全区/.test(await status()), await status());

  // 5. Keyboard: nudge, delete, duplicate.
  await clickSelector(item('n1'));
  await key('ArrowRight'); await key('ArrowRight', ['shift']); await key('ArrowDown');
  t = await tpl();
  check('arrow keys nudge by 0.1 mm and Shift-arrows by 1 mm (x 11.1, y 30.1) and the schema becomes 1.1', near(t.elements[0].x, 11.1) && near(t.elements[0].y, 30.1) && t.schema_version === '1.1', t.elements[0]);
  check('the focus stays on the nudged item', await evalJs(`document.activeElement.getAttribute('data-key')==='n1'`));
  await load(four(binding)); await clickSelector(item('n1')); await key('d', ['ctrl']);
  t = await tpl();
  check('Ctrl-D duplicates under the lowest free id at the nearest free place (n5 at y 40) and selects the copy', t.elements.length === 5 && t.elements[4].id === 'n5' && t.elements[4].y === 40 && t.elements[4].x === 10 && JSON.stringify(await selected()) === '["n5"]', t.elements[4]);
  await key('Delete');
  check('Delete removes the selected box', (await tpl()).elements.length === 4 && (await selected()).length === 0);
  await load(doc(binding, [['n1', 10, 30, 50, 10]])); await clickSelector(item('n1')); await key('Delete');
  check('deleting the last box is refused and says why', (await tpl()).elements.length === 1 && /至少保留一个文本框/.test(await status()), await status());

  // 6. Align and distribute through the toolbar.
  await load(doc(binding, [['n1', 10, 30, 60, 10], ['n2', 150, 40, 40, 10], ['n3', 80, 50, 20, 10]]));
  await selectAllOnCanvas('before Align top');
  await clickSelector('#doc-align-top');
  t = await tpl();
  check('Align top puts every selected box on the top edge', t.elements.every(e => e.y === 30), t.elements.map(e => e.y));
  await key('z', ['ctrl']); await selectAllOnCanvas('before Align right');
  await clickSelector('#doc-align-right');
  t = await tpl();
  check('Align right puts every selected box on the right edge', t.elements.every(e => near(e.x + e.width, 190)), t.elements.map(e => e.x + e.width));
  await key('z', ['ctrl']); await selectAllOnCanvas('before Distribute');
  await clickSelector('#doc-distribute-h');
  t = await tpl();
  check('Distribute horizontally equalises the gaps (n3 at x 100)', t.elements.map(e => e.x).join() === '10,150,100', t.elements.map(e => e.x));
  check('distribute is disabled for two items and align for one', await (async () => { await key('Escape'); await clickSelector(item('n1')); const one = await evalJs(`document.getElementById('doc-align-left').disabled`); await clickSelector(item('n2'), ['shift']); const two = await evalJs(`[document.getElementById('doc-align-left').disabled,document.getElementById('doc-distribute-h').disabled]`); return one === true && two[0] === false && two[1] === true; })());

  // 7. The table: region, columns, boundaries.
  await load(withTable(binding, [['n1', 10, 30, 50, 10]]));
  await clickSelector(item('table'));
  check('the table is selected and no page text is highlighted in its screenshot', JSON.stringify(await selected()) === '["table"]' && (await pageSelection()) === '', [await selected(), (await pageSelection()).slice(0, 80)]);
  await shot('step-06-table-selected', `document.getElementById('doc-canvas-panel')`);
  await dragFrom('#doc-canvas-host .cv-h-e', -19, 0);
  t = await tpl();
  check('resizing the table 19 mm narrower scales its columns in proportion ([60,70,60] -> [54,63,54])', t.table.width === 171 && JSON.stringify(t.table.columns.map(c => c.width)) === '[54,63,54]', t.table);
  await key('z', ['ctrl']);
  await dragFrom('#doc-canvas-host [data-col="0"]', 10, 0);
  t = await tpl();
  check('dragging a column boundary 10 mm gives the column to its left the width and takes it from the right ([70,60,60])', JSON.stringify(t.table.columns.map(c => c.width)) === '[70,60,60]' && t.table.width === 190, t.table.columns);
  await key('z', ['ctrl']);
  await dragFrom('#doc-canvas-host [data-col="0"]', -50, 0);
  t = await tpl();
  check('a boundary stops at the first column\'s label minimum (31.4 mm at 9 pt)', JSON.stringify(t.table.columns.map(c => c.width)) === '[31.4,98.6,60]', t.table.columns);
  await key('z', ['ctrl']);
  await dragFrom('#doc-canvas-host [data-col="0"]', 200, 0);
  t = await tpl();
  check('a boundary stops at 8 mm for the column to its right', JSON.stringify(t.table.columns.map(c => c.width)) === '[122,8,60]', t.table.columns);
  await key('z', ['ctrl']);
  await dragFrom('#doc-canvas-host .cv-h-e', -150, 0);
  check('a table too narrow for the first column\'s label is refused with the designer\'s reason', /第一列须容纳承前\/过次标签/.test(await status()) && (await tpl()).table.width === 190, await status());
  // Keyboard on a boundary.
  await clickSelector(item('table'));
  await evalJs(`document.querySelector('#doc-canvas-host [data-col="0"]').focus()`); await key('ArrowRight');
  check('a focused column boundary is a slider: the arrow key moves it by 0.1 mm', JSON.stringify((await tpl()).table.columns.map(c => c.width)) === '[60.1,69.9,60]' && (await evalJs(`document.activeElement.getAttribute('data-col')`)) === '0', (await tpl()).table.columns);
  await key('z', ['ctrl']);

  // 8. Undo and redo: more than 100 steps.
  await load(doc(binding, [['n1', 10, 30, 20, 10]])); await clickSelector(item('n1'));
  for (let i = 0; i < 110; i++) await key('ArrowRight');
  check('110 nudges are 110 steps (x 21)', near((await box('n1')).x, 21), await box('n1'));
  for (let i = 0; i < 110; i++) await key('z', ['ctrl']);
  check('110 undos return to the start and disable undo', near((await box('n1')).x, 10) && await undoDisabled(), await box('n1'));
  for (let i = 0; i < 110; i++) await key('z', ['ctrl', 'shift']);
  check('110 redos (Ctrl-Shift-Z) return to the end', near((await box('n1')).x, 21), await box('n1'));
  await key('z', ['ctrl']); await key('y', ['ctrl']);
  check('Ctrl-Y redoes too', near((await box('n1')).x, 21));

  // 9. Inputs and canvas stay in sync: type into a number input, read the canvas.
  await load(four(binding));
  await clickSelector('#doc-elements [aria-label="n1 x"]'); await evalJs(`document.querySelector('#doc-elements [aria-label="n1 x"]').select()`); await typeText('25'); await key('Tab');
  check('typing in an input moves the item on the canvas', near((await box('n1')).x, 25) && await evalJs(`(()=>{const g=document.querySelector('#doc-canvas-host [data-key="n1"] .cv-box').getBoundingClientRect(),p=document.querySelector('#doc-canvas-host .cv-paper').getBoundingClientRect();return Math.abs((g.left-p.left)/(p.width/210)-25)<0.2;})()`));
  await dragFrom(item('n1'), -5, 0);
  check('a drag on the canvas updates the input', (await evalJs(`document.querySelector('#doc-elements [aria-label="n1 x"]').value`)) === '20');

  // 10. The palette.
  await load(four(binding)); await clickSelector(item('n1'));
  await key('k', ['ctrl']); await sleep(80);
  const pal = await evalJs(`(()=>{const d=document.querySelector('.cv-palette');return {role:d.getAttribute('role'),modal:d.getAttribute('aria-modal'),label:document.getElementById(d.getAttribute('aria-labelledby')).textContent,focus:document.activeElement.id,inert:document.querySelector('.shell').inert,options:[...d.querySelectorAll('[role=option]')].map(o=>o.textContent)};})()`);
  await shot('step-07-palette', 'document.body');
  check('Ctrl-K opens a modal, labelled dialog with focus in its field and the page behind inert', pal.role === 'dialog' && pal.modal === 'true' && pal.label === '命令面板' && pal.focus === 'cv-palette-input' && pal.inert === true && pal.options.includes('添加固定文字') && !pal.options.includes('左对齐'), pal);
  await typeText('检查'); await sleep(60);
  check('typing filters the commands', JSON.stringify(await evalJs(`[...document.querySelectorAll('.cv-palette [role=option]')].map(o=>o.textContent)`)) === '["检查样本"]');
  await key('Tab'); const trap1 = await evalJs(`document.activeElement.id`); await key('Tab'); const trap2 = await evalJs(`document.activeElement.id`);
  check('Tab stays inside the dialog and wraps', trap1 === 'cv-palette-close' && trap2 === 'cv-palette-input', [trap1, trap2]);
  await key('Escape');
  check('Esc closes the palette and returns the focus to the item', (await evalJs(`document.querySelector('.cv-palette').parentElement.hidden&&document.activeElement.getAttribute('data-key')==='n1'&&!document.querySelector('.shell').inert`)) === true);
  await key('k', ['ctrl']); await typeText('检查'); await key('Enter');
  await waitFor(`document.getElementById('doc-check-body').rows.length>0`, 'check result', 20000);

  // 11. Check samples: the rows, with reasons, and a row into the preview row number.
  const rows = await evalJs(`[...document.getElementById('doc-check-body').rows].map(r=>[...r.cells].slice(0,3).map(c=>c.textContent))`);
  check('Enter in the palette runs "check samples" and the result lists each checked row with its result and reasons', rows.length >= 2 && rows[0][0] === '第 1 行' && rows.every(r => r[1].startsWith('通过') || r[1].startsWith('不通过')) && /首行/.test(rows[0][2]) && rows.some(r => /末行/.test(r[2])), rows);
  await shot('step-08-check-samples', `document.getElementById('doc-canvas-panel')`);
  await clickSelector('#doc-check-body tr:last-child button');
  check('"载入预览行号" sets the preview row number', (await evalJs(`document.getElementById('doc-row').value`)) === String(parseInt(rows.at(-1)[0].replace(/\D/g, ''), 10)), rows.at(-1));
  // A narrow balance box: the last row (a 19-character balance) must not fit, as the preview itself says.
  await load(doc(binding, [['n1', 10, 30, 40, 10, 'row', 'customer.id'], ['n2', 60, 30, 25, 10, 'row', 'ar.open_balance']]));
  await clickSelector('#doc-check'); await waitFor(`document.getElementById('doc-check-body').rows.length>0`, 'second check', 20000);
  const rows2 = await evalJs(`[...document.getElementById('doc-check-body').rows].map(r=>[...r.cells].slice(0,3).map(c=>c.textContent))`);
  check('a value that does not fit its box is reported as UNSUPPORTED_COMBINATION for exactly that row', rows2.length === 3 && rows2[0][1].startsWith('通过') && rows2[1][1].startsWith('通过') && rows2[2][1].startsWith('不通过 · UNSUPPORTED_COMBINATION'), rows2);
  await clickSelector(item('n1')); await key('ArrowDown');
  check('editing the template voids the check result', (await evalJs(`document.getElementById('doc-check-body').rows.length`)) === 0 && /已作废/.test(await evalJs(`document.getElementById('doc-check-status').textContent`)));

  // 12. Print offset and variables mode.
  const variables = withTable(binding, [['n1', 10, 30, 50, 10], ['n2', 80, 30, 50, 10, 'row', 'customer.id']], [['customer.id', 50], ['aging.bucket', 60], ['ar.open_balance', 50]]);
  variables.print = {mode: 'variables', offset_x: 1.5, offset_y: -2}; await load(variables);
  const variablesFacts = await evalJs(`({ghosts:document.querySelectorAll('#doc-canvas-host .cv-ghost').length,dim:document.querySelector('#doc-canvas-host [data-key="n1"]').getAttribute('class').includes('cv-dim'),mode:document.getElementById('doc-canvas-mode').textContent})`);
  check('the variables mode greys out the literals and the print offset shows as dashed outlines', variablesFacts.ghosts === 2 && variablesFacts.dim && /变量层.*灰显/.test(variablesFacts.mode) && /1\.5 \/ -2/.test(variablesFacts.mode), variablesFacts);
  await shot('step-09-variables-offset', `document.getElementById('doc-canvas-panel')`);

  // 13. Layout at the three widths, with a selection, a table and the palette.
  await load(withTable(binding, [['n1', 10, 30, 50, 10], ['n2', 80, 30, 50, 10, 'row', 'customer.id'], ['n3', 150, 30, 40, 10, 'total', 'ar.open_balance']]));
  await clickSelector(item('n2'));
  for (const vp of VIEWPORTS) {
    const tag = vp[0];
    await setViewport(vp); await openPage('documents'); await sleep(200);
    const o = await pageOverflow();
    check(`${tag}: the documents page has no horizontal overflow`, !o.over, o);
    const sc = await evalJs(`(()=>{const s=document.getElementById('doc-canvas-scroll'),p=document.querySelector('#doc-canvas-host .cv-paper').getBoundingClientRect(),panel=document.getElementById('doc-canvas-panel').getBoundingClientRect();return {scrollW:s.scrollWidth,clientW:s.clientWidth,overflowX:getComputedStyle(s).overflowX,panelRight:Math.round(panel.right),viewport:document.documentElement.clientWidth,paper:Math.round(p.width)};})()`);
    check(`${tag}: the canvas keeps its fixed scale and scrolls inside its own panel`, sc.paper === 630 && sc.panelRight <= sc.viewport + 1 && (sc.clientW >= sc.scrollW || ['auto', 'scroll'].includes(sc.overflowX)), sc);
    const clipped = await evalJs(`(()=>{const vw=document.documentElement.clientWidth;return [...document.querySelectorAll('#doc-canvas-panel button,#doc-canvas-panel input')].filter(e=>{const b=e.getBoundingClientRect();return b.width>0&&(b.right>vw+1||b.left<-1);}).map(e=>e.id||e.textContent);})()`);
    check(`${tag}: no canvas control is clipped by the viewport`, !clipped.length, clipped);
    await shot(`${tag}-01-canvas`, `document.getElementById('doc-canvas-panel')`);
    await key('k', ['ctrl']); await sleep(100);
    const palBox = await evalJs(`(()=>{const b=document.querySelector('.cv-palette').getBoundingClientRect();return {left:Math.round(b.left),right:Math.round(b.right),vw:document.documentElement.clientWidth};})()`);
    check(`${tag}: the palette fits the viewport`, palBox.left >= 0 && palBox.right <= palBox.vw, palBox);
    await shot(`${tag}-02-palette`);
    await key('Escape');
    const o2 = await pageOverflow();
    check(`${tag}: still no horizontal overflow after the palette`, !o2.over, o2);
  }
  // On the phone, a drag with a real mouse on the scaled page still works from where the canvas sits.
  await setViewport(VIEWPORTS[0]); await openPage('documents'); await sleep(200);

  // 14. Colours in the light and the dark scheme.
  await load(withTable(binding, [['n1', 10, 30, 50, 10], ['n2', 80, 30, 50, 10, 'row', 'customer.id'], ['n3', 150, 30, 40, 10, 'total', 'ar.open_balance']]));
  await clickSelector(item('n2'));
  held = await dragFrom(item('n2'), 19.6, 0, {hold: true});
  for (const scheme of ['light', 'dark']) {
    await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: scheme}]}); await sleep(200);
    const ratios = await evalJs(contrastJs);
    evidence['contrast_' + scheme] = ratios;
    const failing = ratios.filter(r => r.ratio === null || r.ratio < r.min);
    check(`${scheme} scheme: every canvas text and outline meets its contrast (4.5:1 text, 3:1 outlines)`, !failing.length, failing);
    await shot(`desktop-03-${scheme}`, `document.getElementById('doc-canvas-panel')`);
  }
  await held.release(); await key('z', ['ctrl']);
  await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: 'light'}, {name: 'forced-colors', value: 'active'}]}); await sleep(200);
  await shot('desktop-04-forced-colors', `document.getElementById('doc-canvas-panel')`);
  check('forced colours: the canvas items remain drawn', (await evalJs(`document.querySelectorAll('#doc-canvas-host [data-key]').length`)) >= 4);
  await send('Emulation.setEmulatedMedia', {features: []});

  // 15. Served assets: the policy is the console's, byte for byte, on the canvas script too.
  const policies = new Set(Object.values(served).map(v => v.csp));
  check('every served asset, canvas.js included, carries the same Content-Security-Policy', policies.size === 1 && [...policies][0] === "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'", [...policies]);
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
