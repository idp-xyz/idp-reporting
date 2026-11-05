// Real-browser acceptance for the S06-B template centre (ADR 0008 section 3, docs/STUDIO.md "模板中心"): real mouse and keyboard input over the Chrome DevTools
// Protocol. The centre is shown at 1440 px beside the prototype's library (IDP_Reporting_Designer_UIUX_V3.0.html, the 模板中心 view), alone at 1280, 768 and 390 px in
// the light and the dark scheme (no horizontal overflow, the contrast of every visible text measured), and a session follows: search, a filter, a favourite, a
// classification into a collection, a sample's preview, the copy of a sample with a mapping opened in the studio, the archive, and the new-template wizard end to end.
// Visual sameness is judged by a person from the screenshots (docs/STUDIO.md says so); this script measures what it can and records the rest.
// Not part of `npm test` and not CI: it needs a running reporting-demo started with the PDF flags and a template directory, and a Chrome started with
//   --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server
// It exits 1 when any check fails or the run aborts, and 2 on bad arguments; report.json is written either way.
// Safety: --base must be loopback because the bearer token is typed into the page. The prototype is served from a loopback port of its own (--proto-port) for the
// duration of the run. The script reads and writes only the page it drives and its own files in --out (PNGs and report.json); it removes nothing else.
// Usage: node scripts/browser_library_s06b.mjs --out DIR --token-file FILE --proto FILE [--base http://127.0.0.1:PORT] [--cdp PORT] [--proto-port PORT]
import {readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync} from 'node:fs';
import {createServer} from 'node:http';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const usage = why => { console.error(why + '\nusage: --out DIR --token-file FILE --proto FILE [--base http://127.0.0.1:PORT] [--cdp PORT] [--proto-port PORT]'); process.exit(2); };
let args;
try {
  ({values: args} = parseArgs({options: {out: {type: 'string'}, 'token-file': {type: 'string'}, proto: {type: 'string'}, base: {type: 'string'}, cdp: {type: 'string'}, 'proto-port': {type: 'string'}}, strict: true}));
} catch (e) { usage(e.message); }
if (!args.out || !args['token-file'] || !args.proto) usage('--out, --token-file and --proto are required');
const base = args.base || 'http://127.0.0.1:7850', cdp = args.cdp || '9347', protoPort = Number(args['proto-port'] || 7851), outDir = args.out;
let baseUrl;
try { baseUrl = new URL(base); } catch { usage('--base is not a valid URL'); }
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname)) usage('--base must be an http loopback address');
const token = readFileSync(args['token-file'], 'utf8').trim(), proto = readFileSync(args.proto);
const sleep = ms => new Promise(r => setTimeout(r, ms));
mkdirSync(outDir, {recursive: true});

const checks = [], evidence = {};
const check = (name, ok, detail) => { checks.push({name, ok: !!ok, detail: ok ? undefined : detail}); console.log(ok ? 'PASS' : 'FAIL', name, ok ? '' : JSON.stringify(detail)); };

function connect(url) {
  const ws = new WebSocket(url), pending = new Map();
  let nextId = 1;
  ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const {res, rej} = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); } };
  ws.onclose = () => { for (const {rej} of pending.values()) rej(new Error('CDP connection closed')); pending.clear(); };
  const ready = new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('websocket failed: ' + url)); });
  const send = (method, params = {}, timeout = 40000) => new Promise((res, rej) => {
    const id = nextId++, timer = setTimeout(() => { pending.delete(id); rej(new Error('CDP timeout: ' + method)); }, timeout);
    pending.set(id, {res: v => { clearTimeout(timer); res(v); }, rej: e => { clearTimeout(timer); rej(e); }});
    ws.send(JSON.stringify({id, method, params}));
  });
  return {ready, send, close: () => ws.close()};
}
let page;
const send = (...a) => page.send(...a);
const evalJs = async expr => {
  const r = await send('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true});
  if (r.exceptionDetails) throw new Error('eval failed: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
};
const waitFor = async (expr, label, limit = 15000) => {
  const end = Date.now() + limit;
  while (Date.now() < end) { if (await evalJs(`Boolean(${expr})`).catch(() => false)) return; await sleep(80); }
  throw new Error('timeout waiting for: ' + label);
};
const OWN_FILE = /^(proto|library|pair|session|zz-failure)-[\w-]+\.png$|^report\.json$/;
for (const f of readdirSync(outDir)) if (OWN_FILE.test(f)) rmSync(join(outDir, f));
const shots = new Map();
const shot = async name => { const data = (await send('Page.captureScreenshot', {format: 'png'})).data; shots.set(name, data); writeFileSync(join(outDir, name + '.png'), Buffer.from(data, 'base64')); console.log('shot', name); };
const viewport = async (w, h, scheme = 'light', mobile = false) => {
  await send('Emulation.setDeviceMetricsOverride', {width: w, height: h, deviceScaleFactor: 1, mobile});
  await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: scheme}]});
  await sleep(500);
};

// ---- real input -------------------------------------------------------------------------------------------------------------------
const mouse = (type, x, y) => send('Input.dispatchMouseEvent', {type, x, y, button: type === 'mouseMoved' ? 'none' : 'left', buttons: type === 'mouseReleased' || type === 'mouseMoved' ? 0 : 1, clickCount: type === 'mouseMoved' ? 0 : 1});
const KEYS = {Escape: [27, 'Escape'], Enter: [13, 'Enter'], Tab: [9, 'Tab']};
const key = async (name, shift = false) => {
  const [vk, code] = KEYS[name], modifiers = shift ? 8 : 0;
  await send('Input.dispatchKeyEvent', {type: 'rawKeyDown', key: name, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers});
  await send('Input.dispatchKeyEvent', {type: 'keyUp', key: name, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers});
};
const centreOf = expr => evalJs(`(()=>{const e=${expr};if(!e)return null;e.scrollIntoView({block:'center',inline:'nearest'});const b=e.getBoundingClientRect();return [b.left+b.width/2,b.top+b.height/2];})()`);
const clickExpr = async (expr, label) => {
  const at = await centreOf(expr);
  if (!at) throw new Error('nothing to click: ' + (label || expr));
  await mouse('mouseMoved', at[0], at[1]); await mouse('mousePressed', at[0], at[1]); await mouse('mouseReleased', at[0], at[1]); await sleep(80);
};
const q = sel => `document.querySelector(${JSON.stringify(sel)})`;
const click = sel => clickExpr(q(sel), sel);
// A button of the page or of the open dialog by its text or label.
const btnIn = (scope, text) => `[...document.querySelectorAll(${JSON.stringify(scope + ' button')})].find(b=>(b.getAttribute('aria-label')||b.textContent).trim().startsWith(${JSON.stringify(text)}))`;
const clickBtn = (scope, text) => clickExpr(btnIn(scope, text), scope + ' ' + text);
const typeInto = async (sel, text) => { await click(sel); await evalJs(`${q(sel)}.select()`); await send('Input.insertText', {text}); await sleep(120); };
const setSelect = (sel, value) => evalJs(`(()=>{const e=${q(sel)};e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('change',{bubbles:true}));return e.value;})()`);
const idle = () => waitFor(`!document.getElementById('preview').disabled`, 'the console is idle', 40000);
const DIALOG = '#page-library .overlay.open';
const overflow = () => evalJs(`(()=>{const d=document.documentElement,p=document.getElementById('page-library');return {doc:[d.scrollWidth,d.clientWidth],page:[p.scrollWidth,p.clientWidth],over:d.scrollWidth>d.clientWidth+1||p.scrollWidth>p.clientWidth+1};})()`);
const api = (method, path, body) => evalJs(`(async()=>{const r=await fetch('/api/reporting/v1'+${JSON.stringify(path)},{method:${JSON.stringify(method)},headers:{Authorization:'Bearer '+${JSON.stringify(token)},'Content-Type':'application/json'},body:${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))}});const t=await r.text();return {status:r.status,body:t?JSON.parse(t):null};})()`);

// The contrast of every visible text of the centre against the colour behind it (WCAG ratio; the colour behind is composited from the ancestors' fills).
const AUDIT = `(()=>{
const parse=c=>{const m=c.match(/rgba?\\(([^)]+)\\)/);if(!m)return null;const p=m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number);return {r:p[0],g:p[1],b:p[2],a:p.length>3?p[3]:1};};
const lum=c=>{const f=v=>{v/=255;return v<=.03928?v/12.92:Math.pow((v+.055)/1.055,2.4);};return .2126*f(c.r)+.7152*f(c.g)+.0722*f(c.b);};
const over=(t,b)=>({r:t.r*t.a+b.r*(1-t.a),g:t.g*t.a+b.g*(1-t.a),b:t.b*t.a+b.b*(1-t.a),a:1});
const bgOf=el=>{const layers=[];for(let e=el;e;e=e.parentElement){const c=parse(getComputedStyle(e).backgroundColor);if(c&&c.a>0){layers.push(c);if(c.a>=1)break;}}let out=layers.length&&layers[layers.length-1].a>=1?layers.pop():{r:255,g:255,b:255,a:1};for(let i=layers.length-1;i>=0;i--)out=over(layers[i],out);return out;};
const out=[],root=document.getElementById('page-library'),walker=document.createTreeWalker(root,NodeFilter.SHOW_TEXT),seen=new Set();let n;
const controls=root.querySelectorAll('input,select,textarea');
const audit=(el,t)=>{const cs=getComputedStyle(el);if(cs.visibility==='hidden'||cs.display==='none'||el.getBoundingClientRect().width===0)return;const fg=parse(cs.color),bg=bgOf(el),f2=over({...fg,a:fg.a*(parseFloat(cs.opacity)||1)},bg),L1=lum(f2),L2=lum(bg),r=(Math.max(L1,L2)+.05)/(Math.min(L1,L2)+.05);out.push({text:String(t).slice(0,28),ratio:Math.round(r*100)/100,size:cs.fontSize,disabled:!!el.closest('button:disabled,[aria-disabled="true"]'),id:el.id||String(el.className).slice(0,24)});};
while(n=walker.nextNode()){const t=n.nodeValue.trim();if(!t)continue;const el=n.parentElement;if(seen.has(el))continue;seen.add(el);
 if(el.closest('svg')||el.closest('.sr-only')||el.closest('[hidden]')||el.closest('.overlay:not(.open)')||el.closest('[inert]')&&el.closest('.library-shell')&&root.querySelector('.overlay.open'))continue;
 audit(el,t);}
for(const c of controls){if(c.closest('[hidden]')||c.closest('.overlay:not(.open)'))continue;if(c.tagName==='SELECT'&&c.selectedOptions[0])audit(c,c.selectedOptions[0].text);else if(c.value)audit(c,c.value);else if(c.placeholder){const cs=getComputedStyle(c,'::placeholder');}}
const bad=out.filter(o=>o.ratio<4.5&&!o.disabled);return {total:out.length,failing:bad.length,worst:bad.sort((a,b)=>a.ratio-b.ratio).slice(0,12)};})()`;
const contrast = async name => { const r = await evalJs(AUDIT); evidence['contrast ' + name] = r; check(`contrast of every visible text ≥ 4.5:1 (${name})`, r.failing === 0 && r.total > 10, r); };

// A composite of two screenshots side by side (the prototype left, the centre right), drawn on the page's own canvas.
async function pair(name, left, right) {
  const png = await evalJs(`(async()=>{const load=d=>new Promise((res,rej)=>{const i=new Image();i.onload=()=>res(i);i.onerror=rej;i.src='data:image/png;base64,'+d;});const a=await load(${JSON.stringify(shots.get(left))}),b=await load(${JSON.stringify(shots.get(right))});const c=document.createElement('canvas');c.width=a.width+b.width+20;c.height=Math.max(a.height,b.height);const x=c.getContext('2d');x.fillStyle='#888';x.fillRect(0,0,c.width,c.height);x.drawImage(a,0,0);x.drawImage(b,a.width+20,0);return c.toDataURL('image/png').split(',')[1];})()`);
  writeFileSync(join(outDir, name + '.png'), Buffer.from(png, 'base64')); console.log('shot', name);
}

let protoServer;
async function main() {
  protoServer = createServer((req, res) => { res.writeHead(200, {'content-type': 'text/html; charset=utf-8'}); res.end(proto); });
  await new Promise((res, rej) => { protoServer.once('error', rej); protoServer.listen(protoPort, '127.0.0.1', res); });
  const list = await (await fetch(`http://127.0.0.1:${cdp}/json/list`)).json();
  const target = list.find(t => t.type === 'page');
  if (!target) throw new Error('no page target on the debugging port');
  page = connect(target.webSocketDebuggerUrl); await page.ready;
  await send('Page.enable'); await send('Runtime.enable');
  await send('Page.bringToFront'); await send('Emulation.setFocusEmulationEnabled', {enabled: true});
  await send('Browser.setDownloadBehavior', {behavior: 'deny'}).catch(() => {});
  await viewport(1440, 900);

  // ---- the prototype's library -------------------------------------------------------------------------------------------------
  await send('Page.navigate', {url: `http://127.0.0.1:${protoPort}/`});
  await waitFor(`document.readyState==='complete'&&!!document.querySelector('.ribbon-tabs')`, 'the prototype'); await sleep(900);
  await evalJs(`document.querySelector('.toolrail [data-act="library"]').click()`); await sleep(900);
  await shot('proto-library');

  // ---- the console: sign in, freeze a snapshot, seed the identity's templates ------------------------------------------------------
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&!!document.getElementById('access-token')`, 'the console'); await sleep(300);
  await evalJs(`(()=>{const i=document.getElementById('access-token');i.value=${JSON.stringify(token)};i.dispatchEvent(new Event('input',{bubbles:true}));document.getElementById('connect-form').requestSubmit();})()`);
  await waitFor(`document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`, 'signed in', 30000);
  await evalJs(`document.querySelector('[data-page="workspace"]').click()`);
  await evalJs(`(()=>{const i=document.getElementById('query-companies');i.value='company_demo';i.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  await evalJs(`document.getElementById('snapshot').click()`);
  await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'snapshot', 60000);
  await evalJs(`window.addEventListener('error',e=>{(window.__errors||(window.__errors=[])).push(e.message);})`);
  await evalJs(`(()=>{if(window.__pdfs)return;window.__pdfs=[];const o=URL.createObjectURL.bind(URL);URL.createObjectURL=b=>{window.__pdfs.push(b);return o(b);};})()`);
  const binding = await evalJs(`(()=>{const [id,version]=document.getElementById('query-dataset').value.split('@');return {integration_id:document.getElementById('query-product').value,dataset:{id,version}};})()`);
  const seeds = [
    ['客户往来对账单', '通用业务', '财务', {favourite: true, collections: ['客户A']}, true],
    ['销售订单', '通用业务', '销售', {}, false],
    ['出库装箱单', '仓储 WMS', '出库', {collections: ['上海仓']}, true],
    ['波次拣货单', '仓储 WMS', '出库', {}, false],
    ['海运订舱委托 · Ocean Booking', '运输 / 货代', '海运', {}, true],
    ['商业发票 · Commercial Invoice', '国际贸易', '贸易单证', {collections: ['客户A']}, true],
    ['航空运单 · Air Waybill (MAWB)', '运输 / 货代', '航空运输', {}, true],
    ['货物舱单 · Cargo Manifest', '运输 / 货代', '海运', {}, false],
  ];
  const seeded = [];
  for (const [name, domain, flow, extra, publish] of seeds) {
    const made = await api('POST', '/document-templates', {id: '', expected_version: 0, config: {schema_version: '1.0', kind: 'document', name, binding, elements: [{id: 'n1', source: 'literal', field: '', text: name, x: 10, y: 25, width: 190, height: 12, font_size: 18, align: 'left'}]}});
    if (made.status !== 201) throw new Error('seed refused: ' + made.status);
    const id = made.body.id;
    const put = await api('PUT', `/document-templates/${id}/catalog`, {domain, flow, favourite: !!extra.favourite, archived: false, collections: extra.collections || [], expected_revision: 0});
    if (put.status !== 200) throw new Error('catalogue refused: ' + put.status);
    if (publish) await api('POST', `/document-templates/${id}/release`, {action: 'publish', version: 1, expected_revision: 0});
    seeded.push(id);
  }
  check('the identity\'s templates are seeded (server)', seeded.length === seeds.length, seeded);

  // ---- the centre at 1440, beside the prototype ---------------------------------------------------------------------------------
  const open = async () => { await evalJs(`document.querySelector('[data-page="library"]').click()`); await waitFor(`!document.getElementById('page-library').hidden&&document.querySelectorAll('#page-library .sample-card').length>=37&&document.querySelectorAll('#page-library ul[aria-label="我的模板"] .catalog-card').length>=${seeds.length}`, 'the centre is listed', 30000); await sleep(300); };
  await open();
  // S07-A put the rail beside every page and S07-F (ADR 0010) the console header above it: the centre fills what is left, its own header is its toolbar.
  check('the centre fills the one shell: beside the rail, below the console header, its own wordmark and data chip hidden', await evalJs(`(()=>{const p=document.getElementById('page-library').getBoundingClientRect(),h=document.querySelector('.console-header');return document.body.classList.contains('library-on')&&getComputedStyle(h).display!=='none'&&Math.abs(p.left-document.querySelector('.app-rail').getBoundingClientRect().right)<=1&&Math.abs(p.top-h.getBoundingClientRect().bottom)<=1&&Math.abs(p.right-innerWidth)<=1&&getComputedStyle(document.querySelector('#page-library .library-header>.wordmark')).display==='none'&&getComputedStyle(document.getElementById('lib-data')).display==='none';})()`));
  await shot('library-01-1440');
  await pair('pair-library', 'proto-library', 'library-01-1440');
  // The measured geometry the prototype's screenshot gives (docs/STUDIO.md): the sidebar's width, the header's height, the card's left edge.
  const geo = await evalJs(`(()=>{const r=s=>{const b=document.querySelector(s).getBoundingClientRect();return {x:b.left,y:b.top,w:b.width,h:b.height};};return {header:r('.library-header'),side:r('.catalog-sidebar'),search:r('.catalog-search'),card:r('ul[aria-label="我的模板"] .catalog-card'),h1:r('h1')};})()`);
  evidence.geometry = geo;
  // The prototype's numbers moved by the rail (56 px) and the console header (68 px); the centre's header became a 56 px toolbar (S07-F).
  check('geometry at 1440: toolbar 56 px below the 68 px console header, sidebar 222 px, content from x = 256 + 56 (the prototype\'s numbers beside the rail)', geo.header.h === 56 && Math.abs(geo.header.y - 68) <= 1 && geo.side.w === 222 && Math.abs(geo.search.x - 312) <= 1 && Math.abs(geo.card.x - 312) <= 1, geo);
  check('no horizontal overflow at 1440 (light)', !(await overflow()).over, await overflow());
  await contrast('1440 light');
  await viewport(1440, 900, 'dark'); await shot('library-02-1440-dark'); await contrast('1440 dark'); check('no horizontal overflow at 1440 (dark)', !(await overflow()).over, await overflow()); await viewport(1440, 900, 'light');

  // ---- alone at 1280, 768, 390, light and dark -----------------------------------------------------------------------------------
  for (const [w, h, mobile] of [[1280, 800, false], [768, 900, false], [390, 844, true]]) {
    for (const scheme of ['light', 'dark']) {
      await viewport(w, h, scheme, mobile); await sleep(300);
      const o = await overflow(); check(`no horizontal overflow at ${w} (${scheme})`, !o.over, o);
      await shot(`library-${w}-${scheme}`); await contrast(`${w} ${scheme}`);
    }
    // The grid view and a dialog at each width too (light).
    await viewport(w, h, 'light', mobile);
    await click('[aria-label="卡片视图"]'); await sleep(250); check(`no horizontal overflow at ${w} in the card view`, !(await overflow()).over, await overflow()); await shot(`library-${w}-grid`); await click('[aria-label="列表视图"]');
    await clickBtn('#page-library .library-header', '新建模板'); await waitFor(`${q(DIALOG)}`, 'the wizard'); await sleep(250);
    check(`the wizard fits at ${w}`, await evalJs(`(()=>{const d=document.querySelector('${DIALOG} .dialog').getBoundingClientRect();return d.left>=0&&d.right<=innerWidth&&d.bottom<=innerHeight+1;})()`), await evalJs(`JSON.stringify(document.querySelector('${DIALOG} .dialog').getBoundingClientRect())`));
    await shot(`library-${w}-wizard`); await key('Escape'); await sleep(200);
  }
  await viewport(1440, 900, 'light');

  // ---- a real session --------------------------------------------------------------------------------------------------------------
  const own = () => evalJs(`[...document.querySelectorAll('#page-library ul[aria-label="我的模板"] .catalog-card')].map(c=>c.getAttribute('aria-label'))`);
  // Search by typing.
  await typeInto('#page-library input[type=search]', '对账'); await waitFor(`document.querySelectorAll('#page-library ul[aria-label="我的模板"] .catalog-card').length===1`, 'search narrows'); await shot('session-01-search');
  check('search narrows the list to the matching template', JSON.stringify(await own()) === JSON.stringify(['客户往来对账单']), await own());
  await typeInto('#page-library input[type=search]', ''); await evalJs(`(()=>{const i=document.querySelector('#page-library input[type=search]');i.value='';i.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  // A domain and a flow filter.
  await clickBtn('#page-library .catalog-sidebar', '运输 / 货代'); await sleep(200);
  await setSelect('#page-library select[aria-label="业务流程"]', '海运'); await sleep(200); await shot('session-02-filter');
  check('domain and flow filters give the two sea-freight templates', JSON.stringify((await own()).sort()) === JSON.stringify(['海运订舱委托 · Ocean Booking', '货物舱单 · Cargo Manifest'].sort()), await own());
  await setSelect('#page-library select[aria-label="业务流程"]', ''); await clickBtn('#page-library .catalog-sidebar', '运输 / 货代'); await sleep(200);
  // Favourite: a real click on the star; the server has it.
  await click('#page-library .catalog-star[aria-label^="收藏 销售订单"]'); await waitFor(`document.querySelector('#page-library .catalog-star[aria-label^="取消收藏 销售订单"]')`, 'starred');
  const listing = (await api('GET', '/document-templates')).body, sales = listing.find(t => t.name === '销售订单');
  check('the favourite is the server\'s (catalogue revision 2, favourite true)', sales.catalog.favourite === true && sales.catalog.revision === 2, sales.catalog);
  // Classify into a new collection.
  await clickBtn('#page-library', '分类 销售订单'); await waitFor(q(DIALOG), 'classify'); await sleep(200);
  await typeInto(`${DIALOG} input[type=text]`, '会话集合'); await shot('session-03-classify'); await clickBtn(DIALOG, '保存分类');
  await waitFor(`!document.querySelector('${DIALOG}')&&[...document.querySelectorAll('#page-library .catalog-sidebar .catalog-nav span')].some(s=>s.textContent==='会话集合')`, 'collection in the sidebar');
  check('the new collection is in the sidebar and on the server', (await api('GET', `/document-templates/${sales.id}/catalog`)).body.collections.join() === '会话集合', (await api('GET', `/document-templates/${sales.id}/catalog`)).body);
  check('focus returned to the 分类 button', await evalJs(`document.activeElement&&document.activeElement.getAttribute('aria-label')==='分类 销售订单'`), await evalJs(`document.activeElement&&document.activeElement.outerHTML.slice(0,120)`));
  // A sample's preview.
  await clickBtn('#page-library .sample-card', '预览示例'); await waitFor(`${q(DIALOG + ' tbody tr')}`, 'preview dialog'); await sleep(200); await shot('session-04-sample-preview');
  await clickBtn(DIALOG, '生成 PDF 预览'); await waitFor(`window.__pdfs.length>=1`, 'the PDF preview', 40000);
  const pdfHead = await evalJs(`(async()=>{const b=window.__pdfs.at(-1);return new TextDecoder().decode(new Uint8Array(await b.slice(0,5).arrayBuffer()));})()`);
  check('the sample\'s preview is a PDF (no dataset needed)', pdfHead === '%PDF-', pdfHead);
  await key('Escape'); await waitFor(`!document.querySelector('#page-library .overlay.open')&&!document.querySelector('.library-shell').inert`, 'the preview dialog closes on Esc'); await sleep(150);
  // Copy a sample with a mapping, and open it in the studio.
  const sampleName = await evalJs(`document.querySelector('#page-library .sample-card .catalog-name').textContent`);
  await clickBtn('#page-library .sample-card', '复制示例'); await waitFor(`${q(DIALOG + ' tbody tr')}`, 'mapping dialog'); await sleep(250);
  const mapped = await evalJs(`[...document.querySelectorAll('${DIALOG} tbody select')].map(s=>s.value)`);
  check('the mapping is pre-filled where a column fits', mapped.some(v => v), mapped);
  await shot('session-05-mapping');
  await evalJs(`(()=>{const s=document.querySelector('${DIALOG} tbody select');const lit=document.querySelector('${DIALOG} tbody input');})()`);
  await typeInto(`${DIALOG} input[type=text]`, '浏览器复制件'); await clickBtn(DIALOG, '复制并进入设计器');
  await waitFor(`!document.getElementById('page-documents').hidden&&document.getElementById('doc-server-status').textContent.includes('正在编辑模板 ')`, 'opened in the studio', 40000); await idle(); await sleep(500);
  check('the copy opens in the studio by its name', await evalJs(`document.getElementById('st-name').textContent==='浏览器复制件'&&document.body.classList.contains('studio-on')`), await evalJs(`document.getElementById('st-name').textContent`));
  await shot('session-06-copy-in-studio');
  // Back through the crumb; archive the copy; 归档 lists it.
  await click('#st-crumb'); await waitFor(`!document.getElementById('page-library').hidden&&document.querySelector('#page-library ul[aria-label="我的模板"] .catalog-card[aria-label="浏览器复制件"]')`, 'back in the centre'); await sleep(300);
  check('the studio\'s crumb opens the centre, which lists the copy', true);
  await clickBtn('#page-library .catalog-card[aria-label="浏览器复制件"]', '归档模板'); await waitFor(`!document.querySelector('#page-library .catalog-card[aria-label="浏览器复制件"]')`, 'archived');
  await clickBtn('#page-library .catalog-sidebar', '归档'); await waitFor(`document.querySelector('#page-library .catalog-card[aria-label="浏览器复制件"]')`, 'in the archive'); await shot('session-07-archive');
  await clickBtn('#page-library .catalog-sidebar', '全部模板'); await sleep(200);
  // 返回设计器 and 数据中心.
  await clickBtn('#page-library .library-header', '返回设计器'); await waitFor(`!document.getElementById('page-documents').hidden`, 'back in the studio');
  check('返回设计器 returns to the studio with the template', await evalJs(`document.getElementById('st-name').textContent==='浏览器复制件'`));
  await click('#st-crumb'); await waitFor(`!document.getElementById('page-library').hidden`, 'the crumb opens the centre (the studio wordmark is hidden in the one shell, S07-F)');
  await clickBtn('#page-library .library-header', '数据中心'); await waitFor(`!document.getElementById('page-catalog').hidden`, 'the data catalogue page');
  check('数据中心 is the console\'s data catalogue page', await evalJs(`!document.body.classList.contains('library-on')`));
  await open();
  // The wizard end to end, by keyboard where it matters.
  await clickBtn('#page-library .library-header', '新建模板'); await waitFor(q(DIALOG), 'wizard'); await sleep(250);
  await shot('session-08-wizard-1');
  await click(`${DIALOG} input[type=radio][value="仓储 WMS|出库"]`); await clickBtn(DIALOG, '下一步'); await sleep(250); await shot('session-09-wizard-2');
  await clickBtn(DIALOG, '下一步'); await waitFor(`${q(DIALOG + ' tbody tr')}`, 'wizard mapping'); await sleep(250); await shot('session-10-wizard-3');
  await clickBtn(DIALOG, '下一步'); await sleep(250); await typeInto(`${DIALOG} #lib-wizard-name`, '向导新建件'); await shot('session-11-wizard-4');
  await key('Tab'); await key('Tab', true);
  check('the focus stays inside the wizard while Tab is pressed', await evalJs(`!!document.activeElement.closest('${DIALOG} [role=dialog]')`));
  await clickBtn(DIALOG, '创建并进入设计器');
  await waitFor(`document.getElementById('st-name').textContent==='向导新建件'&&!document.getElementById('page-documents').hidden`, 'the wizard\'s template opens', 40000);
  const made = (await api('GET', '/document-templates')).body.find(t => t.name === '向导新建件');
  check('the wizard saved a template in the chosen flow (server)', !!made && made.catalog.domain === '仓储 WMS' && made.catalog.flow === '出库' && made.version === 1, made);
  // The blank path on a label preset.
  await click('#st-crumb'); await open(); await clickBtn('#page-library .library-header', '新建模板'); await waitFor(q(DIALOG), 'wizard 2');
  await click(`${DIALOG} input[type=radio][value="blank"]`); await clickBtn(DIALOG, '下一步'); await setSelect(`${DIALOG} select[aria-label="页面预设"]`, 'label_100x100'); await clickBtn(DIALOG, '下一步'); await clickBtn(DIALOG, '下一步');
  await typeInto(`${DIALOG} #lib-wizard-name`, '空白标签件'); await clickBtn(DIALOG, '创建并进入设计器');
  await waitFor(`document.getElementById('st-name').textContent==='空白标签件'&&!document.getElementById('page-documents').hidden`, 'blank label opens', 40000); await idle(); await sleep(500);
  const blank = (await api('GET', '/document-templates')).body.find(t => t.name === '空白标签件');
  check('the blank template is on the 100 × 100 label preset (server)', blank && blank.preset === 'label_100x100' && blank.schema_version === '2.0', blank);
  await shot('session-12-blank-in-studio');
  // Everything the session did is on the server and nothing is in the browser's storage.
  check('nothing in the browser\'s storage', await evalJs(`localStorage.length===0&&sessionStorage.length===0`));
  check('no page error', await evalJs(`!window.__errors`));
}

let failure = null;
try { await main(); } catch (e) { failure = e; console.error('ABORT', e.stack || e); try { await shot('zz-failure-state'); } catch {} }
protoServer?.close();
const failed = checks.filter(c => !c.ok);
writeFileSync(join(outDir, 'report.json'), JSON.stringify({generated: new Date().toISOString(), base, checks, failed: failed.length, evidence, abort: failure ? String(failure.message) : null}, null, 2));
console.log(`${checks.length - failed.length}/${checks.length} checks passed${failure ? '; aborted: ' + failure.message : ''}`);
page?.close();
process.exit(failure || failed.length ? 1 : 0);
