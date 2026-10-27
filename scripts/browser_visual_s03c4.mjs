// Real-browser visual acceptance for the console: S03-C4 bar chart plus the dashboard designer layout.
// Drives an external headless Chrome over CDP with no dependencies (Node 22+ global WebSocket/fetch).
// Not part of `npm test`: it needs a running reporting-demo and a Chrome started with
//   --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir>
// It exits 1 when any check fails or the run aborts, and 2 on bad arguments; report.json is written either way.
// Safety: --base must be loopback because the bearer token is typed into the page and a real snapshot export job
// is created there. Only this script's own PNG/report/download files in --out are removed; nothing is deleted
// from the download directory.
// Usage: node scripts/browser_visual_s03c4.mjs --out DIR --token-file FILE [--base URL] [--cdp PORT]
//        (--download-chrome DIR --download-local DIR | --skip-download)
//        --download-chrome and --download-local name the same directory as Chrome and as this script see it.
import {readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {inflateRawSync} from 'node:zlib';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const usage = why => { console.error(why + '\nusage: --out DIR --token-file FILE [--base http://127.0.0.1:PORT] [--cdp PORT] (--download-chrome DIR --download-local DIR | --skip-download)'); process.exit(2); };
let args;
try {
  ({values: args} = parseArgs({options: {out: {type: 'string'}, 'token-file': {type: 'string'}, base: {type: 'string'}, cdp: {type: 'string'}, 'download-chrome': {type: 'string'}, 'download-local': {type: 'string'}, 'skip-download': {type: 'boolean'}}, strict: true}));
} catch (e) { usage(e.message); }
if (!args.out || !args['token-file']) usage('--out and --token-file are required');
const base = args.base || 'http://127.0.0.1:7810', cdp = args.cdp || '9333', outDir = args.out;
let baseUrl;
try { baseUrl = new URL(base); } catch { usage('--base is not a valid URL'); }
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname)) usage('--base must be an http loopback address');
const downloads = !args['skip-download'];
if (downloads && !(args['download-chrome'] && args['download-local'])) usage('give --download-chrome and --download-local, or --skip-download explicitly');
const token = readFileSync(args['token-file'], 'utf8').trim();
const sleep = ms => new Promise(r => setTimeout(r, ms));
mkdirSync(outDir, {recursive: true});

const problems = [], dialogs = [], checks = [], skipped = [];
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
  while (Date.now() < end) { if (await evalJs(`Boolean(${expr})`)) return; await sleep(80); }
  throw new Error('timeout waiting for: ' + label);
};
// elementExpr is JS that yields the element to capture; the clip spans the full page width so that the
// scrollbar-less relayout of captureBeyondViewport cannot cut the right edge.
const shot = async (name, elementExpr) => {
  const params = {format: 'png'};
  if (elementExpr) {
    const r = await evalJs(`(()=>{const e=${elementExpr};if(!e)return null;const b=e.getBoundingClientRect();return {x:0,y:Math.max(0,b.top+scrollY-12),width:Math.max(document.documentElement.clientWidth,Math.ceil(b.right)+24),height:Math.ceil(b.height)+24};})()`);
    if (!r) throw new Error('no element for shot ' + elementExpr);
    Object.assign(params, {clip: {...r, scale: 1}, captureBeyondViewport: true});
  }
  writeFileSync(join(outDir, name + '.png'), Buffer.from((await send('Page.captureScreenshot', params)).data, 'base64'));
};

// name, width, height, mobile, dpr, full: every scenario and screenshot versus layout checks only.
const VIEWPORTS = [['desktop', 1280, 900, false, 1, true], ['tablet', 768, 1024, false, 1, true], ['mobile', 390, 844, true, 2, true], ['narrow', 320, 640, true, 2, false], ['mid', 900, 800, false, 1, false], ['laptop', 1024, 768, false, 1, false], ['wide', 1920, 1080, false, 1, false]];
const PAGES = ['overview', 'catalog', 'workspace', 'dashboard', 'tasks', 'system'];
// Remove only files this script generates (named after its viewports) so a shared --out keeps unrelated PNGs.
const OWN_FILE = new RegExp(`^(00-login|zz-failure|(${VIEWPORTS.map(v => v[0]).join('|')})-\\d\\d-[\\w-]+)\\.png$|^(report\\.json|download\\.json|download\\.xlsx)$`);
for (const f of readdirSync(outDir)) if (OWN_FILE.test(f)) rmSync(join(outDir, f));
const setViewport = async ([, width, height, mobile, dpr]) => { await send('Emulation.setDeviceMetricsOverride', {width, height, deviceScaleFactor: dpr, mobile}); await sleep(250); };
const openPage = name => evalJs(`document.querySelector('[data-page="${name}"]').click()`);
const pageOverflow = () => evalJs(`(()=>{const d=document.documentElement;return {scroll:d.scrollWidth,client:d.clientWidth,over:d.scrollWidth>d.clientWidth+1};})()`);

// Controls that sit outside the viewport (and are not inside a scroller) are clipped or force page scroll.
const clippedControls = scope => evalJs(`(()=>{
  const vw=document.documentElement.clientWidth;
  const scrolls=e=>{for(let p=e.parentElement;p&&p!==document.body;p=p.parentElement)if(getComputedStyle(p).overflowX!=='visible')return true;return false;};
  return [...document.querySelectorAll(${JSON.stringify(scope + ' button,' + scope + ' select,' + scope + ' input,' + scope + ' textarea,' + scope + ' label')})]
    .filter(e=>{const b=e.getBoundingClientRect();return b.width>0&&getComputedStyle(e).display!=='none'&&!scrolls(e)&&(b.right>vw+1||b.left<-1);})
    .map(e=>e.tagName.toLowerCase()+(e.id?'#'+e.id:'')+'@right='+Math.round(e.getBoundingClientRect().right));
})()`);

// Dashboard toolbar: metric, table and bar controls must not share a row.
const toolbarRows = () => evalJs(`(()=>{
  const rect=id=>document.getElementById(id).getBoundingClientRect();
  const overlap=(a,b)=>rect(a).top<rect(b).bottom&&rect(b).top<rect(a).bottom;
  const rows=['board-metric','board-columns','board-category'];
  return rows.flatMap((a,i)=>rows.slice(i+1).filter(b=>overlap(a,b)).map(b=>a+'~'+b));
})()`);
// Every static (non-quiet) button in a scope needs a visible border; the JS-built row buttons are quiet by design.
// An empty scope fails, so the check cannot pass vacuously.
const buttonBoundaries = scope => evalJs(`(()=>{
  const bs=[...document.querySelectorAll(${JSON.stringify(scope + ' button.button:not(.quiet)')})];
  return {count:bs.length,borderless:bs.filter(b=>{const s=getComputedStyle(b),c=s.borderTopColor;return parseFloat(s.borderTopWidth)<1||c==='transparent'||c.endsWith(', 0)');}).map(b=>b.id||b.textContent.trim())};
})()`);
// Rows inside a form panel keep the 12px gutter (panel border 1px plus 12px padding), and there must be rows to check.
const gutterFacts = scope => evalJs(`(()=>{
  const rows=[...document.querySelectorAll(${JSON.stringify(scope + ' .designer-row')})].filter(r=>r.getClientRects().length);
  return {rows:rows.length,tight:rows.map(r=>({row:r.firstElementChild.id||r.firstElementChild.tagName,left:Math.round(r.firstElementChild.getBoundingClientRect().left-r.closest('.panel').getBoundingClientRect().left)})).filter(x=>x.left<12)};
})()`);
// Text and controls inside a form panel must not touch the card edge (text counts its own padding).
const insetFacts = ids => evalJs(`(()=>${JSON.stringify(ids)}.map(id=>{
  const e=document.getElementById(id);if(!e||!e.getClientRects().length)return {id,missing:true};
  const p=e.closest('.panel').getBoundingClientRect(),b=e.getBoundingClientRect(),s=getComputedStyle(e),text=['P','LABEL'].includes(e.tagName);
  return {id,left:Math.round(b.left-p.left+(text?parseFloat(s.paddingLeft):0)),right:Math.round(p.right-b.right+(text?parseFloat(s.paddingRight):0))};
}))()`);
const DASHBOARD_INSET = ['board-server-status', 'board-release-status', 'board-status', 'board-json', 'board-save', 'board-open'];
const DESIGNER_INSET = ['layout-server-status', 'layout-release-info', 'layout-status', 'layout-json', 'layout-save', 'layout-open', 'layout-reset'];
// A checked element that is missing or hidden counts as a failure, so the check cannot pass vacuously.
const tooTight = facts => facts.filter(f => f.missing || f.left < 8 || f.right < 8);
// The exact server total must stay on one line; a Range reports one client rect per rendered line.
const totalLines = () => evalJs(`(()=>{const e=document.querySelector('#board-widgets .board-value');if(!e)return null;const r=document.createRange();r.selectNodeContents(e);return r.getClientRects().length;})()`);

const chartFacts = () => evalJs(`(()=>{
  const root=document.querySelector('#board-widgets'),bars=[...root.querySelectorAll('.board-chart-bar')];
  return {rows:root.querySelectorAll('.board-chart-row').length,widths:bars.map(b=>b.style.width),
    values:[...root.querySelectorAll('.board-chart strong')].map(s=>s.textContent),
    injected:root.querySelectorAll('.board-chart img,.board-chart script,.board-chart [onerror]').length,
    unavailable:/图表不可用/.test(root.textContent),empty:/没有可绘制行/.test(root.textContent),
    first50:/前 50 行/.test(root.textContent),subResolution:/小于绘图分辨率/.test(root.textContent)};
})()`);

// The page's own fetch is wrapped so /queries returns chosen rows; this is how the DOM tests inject data too.
const installRowOverride = () => evalJs(`(()=>{if(!window.__origFetch){window.__origFetch=window.fetch.bind(window);
  window.fetch=async(path,opts)=>{const r=await window.__origFetch(path,opts);
    if(window.__rows&&String(path).endsWith('/queries')){const v=await r.clone().json();v.rows=window.__rows;return new Response(JSON.stringify(v),{status:r.status,headers:{'Content-Type':'application/json'}});}
    return r;};}return true;})()`);
const preview = async (rows, then = 'dashboard') => {
  await openPage('workspace');
  await evalJs(`window.__rows=${JSON.stringify(rows)};true`);
  await evalJs(`document.getElementById('preview').click()`);
  await sleep(150);
  await waitFor(`!document.getElementById('preview').disabled`, 'preview settled');
  await openPage(then); await sleep(150);
};

const LONG = '华东大区重点客户 · 上海浦东新区张江高科技园区某某医疗器械股份有限公司第二事业部';
const SCENARIOS = [
  ['signed-duplicate-hostile', [['<img src=x onerror=alert(1)>', 'x', '-20.00'], ['<img src=x onerror=alert(1)>', 'x', '10.00'], ['zero', 'x', '0.00'], [LONG, 'x', '-1234567.89'], ['小额', 'x', '0.01']],
    f => f.rows === 5 && f.injected === 0 && f.values[0].startsWith('-20.00') && f.values[3] === '-1234567.89'],
  ['extreme-500-digits', [['huge', 'x', '9'.repeat(500)], ['tiny negative', 'x', '-0.00000000000000000001'], ['normal', 'x', '123.45']],
    f => f.rows === 3 && f.values[0] === '9'.repeat(500) && f.subResolution],
  ['max-int-precision', [['a', 'x', '9007199254740993.01'], ['b', 'x', '-9007199254740993.01'], ['c', 'x', '9007199254740992.00']],
    f => f.rows === 3 && f.values.join('|') === '9007199254740993.01|-9007199254740993.01|9007199254740992.00' && !f.subResolution],
  ['all-zero', [['a', 'x', '0'], ['b', 'x', '-0.00'], ['c', 'x', '0.000']], f => f.rows === 3 && f.widths.every(w => w === '0%')],
  ['fifty-one-rows', Array.from({length: 51}, (_, i) => ['row ' + (i + 1), 'x', String((i % 7) * 100 - 200) + '.00']), f => f.rows === 50 && f.first50],
  ['invalid-NaN', [['ok', 'x', '1'], ['bad', 'x', 'NaN']], f => f.unavailable && f.rows === 0],
  ['empty', [], f => f.empty && f.rows === 0],
];

// Minimal zip reader (central directory) so the downloaded workbook is parsed, not just recognised by its magic.
function readZip(buf) {
  let eocd = -1;
  for (let i = buf.length - 22; i >= Math.max(0, buf.length - 65557); i--) if (buf.readUInt32LE(i) === 0x06054b50) { eocd = i; break; }
  if (eocd < 0) throw new Error('no end-of-central-directory record');
  const count = buf.readUInt16LE(eocd + 10), entries = {};
  let pos = buf.readUInt32LE(eocd + 16);
  for (let i = 0; i < count; i++) {
    if (buf.readUInt32LE(pos) !== 0x02014b50) throw new Error('bad central directory');
    const method = buf.readUInt16LE(pos + 10), size = buf.readUInt32LE(pos + 20), nameLen = buf.readUInt16LE(pos + 28), extraLen = buf.readUInt16LE(pos + 30), commentLen = buf.readUInt16LE(pos + 32), local = buf.readUInt32LE(pos + 42);
    const start = local + 30 + buf.readUInt16LE(local + 26) + buf.readUInt16LE(local + 28), raw = buf.subarray(start, start + size);
    entries[buf.toString('utf8', pos + 46, pos + 46 + nameLen)] = method === 0 ? raw : inflateRawSync(raw);
    pos += 46 + nameLen + extraLen + commentLen;
  }
  return entries;
}
const xmlStrings = xml => [...xml.matchAll(/<t(?:\s[^>]*)?>([^<]*)<\/t>/g)].map(m => m[1].replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&quot;/g, '"').replace(/&apos;/g, "'").replace(/&amp;/g, '&'));

async function downloadCheck() {
  const local = args['download-local'];
  mkdirSync(local, {recursive: true});
  const before = new Set(readdirSync(local));
  const browser = connect(version.webSocketDebuggerUrl); await browser.ready;
  let file, cells;
  try {
    await browser.send('Browser.setDownloadBehavior', {behavior: 'allow', downloadPath: args['download-chrome']});
    await openPage('workspace');
    await evalJs(`window.__rows=null;document.getElementById('preview').click()`); await sleep(150);
    await waitFor(`!document.getElementById('preview').disabled&&!document.getElementById('result-table-wrap').hidden`, 'real preview for snapshot');
    await evalJs(`document.getElementById('snapshot').click()`);
    await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'snapshot complete', 20000);
    cells = await evalJs(`[...document.querySelectorAll('#result-table tbody td')].map(td=>td.textContent.trim()).filter(Boolean)`);
    await evalJs(`document.getElementById('export').click()`);
    for (let i = 0; i < 200 && !file; i++) { await sleep(100); file = readdirSync(local).find(f => f.endsWith('.xlsx') && !before.has(f)); }
  } finally { browser.close(); }
  const bytes = file ? readFileSync(join(local, file)) : Buffer.alloc(0);
  let entries = {}, parseError;
  try { entries = readZip(bytes); } catch (e) { parseError = String(e.message); }
  const names = Object.keys(entries), sheets = names.filter(n => /^xl\/worksheets\/[^/]+\.xml$/.test(n));
  check('real Chrome downloads a well-formed .xlsx', file && names.includes('xl/workbook.xml') && sheets.length > 0, {file, size: bytes.length, parseError, names});
  const held = new Set([...(entries['xl/sharedStrings.xml'] ? [entries['xl/sharedStrings.xml']] : []), ...sheets.map(n => entries[n])].flatMap(b => xmlStrings(b.toString('utf8'))));
  const missing = cells.filter(c => !held.has(c));
  check('downloaded workbook holds every on-screen cell value exactly', cells.length > 0 && !missing.length, {cells: cells.length, missing});
  if (file) {
    writeFileSync(join(outDir, 'download.xlsx'), bytes);
    writeFileSync(join(outDir, 'download.json'), JSON.stringify({file, bytes: bytes.length, sha256: createHash('sha256').update(bytes).digest('hex'), entries: names, cellsCompared: cells.length}, null, 1));
  }
}

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
  for (const [name, path] of [['index.html', '/console/'], ['styles.css', '/console/styles.css'], ['app.js', '/console/app.js'], ['dashboard.js', '/console/dashboard.js']]) {
    served[name] = createHash('sha256').update(Buffer.from(await (await fetch(base + path)).arrayBuffer())).digest('hex');
  }
  await send('Page.enable'); await send('Runtime.enable'); await send('Log.enable'); await send('Network.enable'); await send('Network.setCacheDisabled', {cacheDisabled: true});
  // Log.enable replays entries from the tab's earlier life, e.g. a previous run against another origin; none belong to this run.
  await sleep(300); problems.length = 0; dialogs.length = 0;
  await setViewport(VIEWPORTS[0]);
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&document.getElementById('access-token')`, 'console loaded');
  await sleep(300); await shot('00-login');
  await evalJs(`(()=>{const i=document.getElementById('access-token');i.value=${JSON.stringify(token)};document.getElementById('connect-button').click();})()`);
  await waitFor(`document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`, 'login + catalog');
  await openPage('workspace');
  await evalJs(`(()=>{const i=document.getElementById('query-companies');i.value='company_demo';i.dispatchEvent(new Event('input',{bubbles:true}));})()`);
  await evalJs(`document.getElementById('preview').click()`);
  await waitFor(`!document.getElementById('result-table-wrap').hidden&&!document.getElementById('preview').disabled`, 'real preview');
  await openPage('dashboard');
  await evalJs(`document.getElementById('board-add-metric').click();document.getElementById('board-add-bar').click();`);
  await installRowOverride();

  for (const vp of VIEWPORTS) {
    const tag = vp[0], full = vp[5];
    await setViewport(vp);
    for (const name of PAGES) { await openPage(name); await sleep(100); const o = await pageOverflow(); check(`${tag}: ${name} page has no horizontal overflow`, !o.over, o); }
    await openPage('workspace'); await evalJs(`document.getElementById('report-designer').open=true`); await sleep(100);
    const designerClipped = await clippedControls('#report-designer'), designerTight = tooTight(await insetFacts(DESIGNER_INSET));
    check(`${tag}: report layout designer controls are not clipped`, !designerClipped.length, designerClipped);
    check(`${tag}: report layout designer keeps text and controls off the card edge`, !designerTight.length, designerTight);
    const designerButtons = await buttonBoundaries('#report-designer'), designerGutter = await gutterFacts('#report-designer');
    check(`${tag}: report layout designer buttons have visible boundaries`, designerButtons.count > 0 && !designerButtons.borderless.length, designerButtons);
    check(`${tag}: report layout designer rows share the 12px gutter`, designerGutter.rows > 0 && !designerGutter.tight.length, designerGutter);
    if (full) await shot(`${tag}-00-report-designer`, `document.getElementById('report-designer')`);
    await evalJs(`document.getElementById('report-designer').open=false`);

    await preview(null);
    if (full) {
      await shot(`${tag}-01-dashboard`);
      await shot(`${tag}-02-toolbar`, `document.querySelector('#page-dashboard .panel')`);
      await shot(`${tag}-03-server-controls`, `document.getElementById('board-server-save').closest('.panel')`);
      await shot(`${tag}-04-board-real-data`, `document.getElementById('board-widgets')`);
    }
    const real = await chartFacts(), clipped = await clippedControls('#page-dashboard'), sharedRows = await toolbarRows(), buttons = await buttonBoundaries('#page-dashboard'), gutter = await gutterFacts('#page-dashboard'), tight = tooTight(await insetFacts(DASHBOARD_INSET)), lines = await totalLines();
    check(`${tag}: real server rows plot 3 bars with exact labels`, real.rows === 3 && real.values.some(v => v === '9007199254740893.00'), real);
    check(`${tag}: dashboard controls are not clipped`, !clipped.length, clipped);
    check(`${tag}: dashboard toolbar groups controls on separate rows`, !sharedRows.length, sharedRows);
    check(`${tag}: dashboard buttons have visible boundaries`, buttons.count > 0 && !buttons.borderless.length, buttons);
    check(`${tag}: dashboard server rows share the 12px gutter`, gutter.rows > 0 && !gutter.tight.length, gutter);
    check(`${tag}: dashboard server panel keeps text and controls off the card edge`, !tight.length, tight);
    check(`${tag}: exact server total stays on one line`, lines === 1, {lines});

    for (const [name, rows, expect] of SCENARIOS.filter(s => full || s[0] === 'extreme-500-digits')) {
      await preview(rows);
      if (full) await shot(`${tag}-05-${name}`, `document.getElementById('board-widgets')`);
      const facts = await chartFacts(), o = await pageOverflow();
      check(`${tag}: ${name} renders as specified`, expect(facts), {...facts, values: facts.values.map(v => v.slice(0, 40))});
      check(`${tag}: ${name} has no horizontal page overflow`, !o.over, o);
    }
    await evalJs(`window.__rows=null;true`);
  }

  await setViewport(VIEWPORTS[0]);
  if (downloads) await downloadCheck(); else { skipped.push('excel download (--skip-download)'); console.log('SKIP excel download'); }
} catch (e) {
  fatal = String(e.message || e); console.log('FATAL', fatal);
  try { await shot('zz-failure'); } catch (_) { /* page may be gone */ }
}
check('no console errors, failed requests or exceptions', !problems.length, [...new Set(problems)]);
check('no JavaScript dialogs (hostile text stayed text)', !dialogs.length, dialogs);
if (fatal) check('script ran to completion', false, fatal);
const failed = checks.filter(c => !c.ok);
writeFileSync(join(outDir, 'report.json'), JSON.stringify({base, browser: browserName, user_agent: version?.['User-Agent'], served_assets_sha256: served, viewports: VIEWPORTS.map(v => v.slice(0, 3)), passed: checks.length - failed.length, failed: failed.length, skipped, checks}, null, 2));
console.log(`\n${checks.length - failed.length}/${checks.length} checks passed` + (skipped.length ? `; skipped: ${skipped.join(', ')}` : ''));
page?.close();
process.exit(failed.length ? 1 : 0);
