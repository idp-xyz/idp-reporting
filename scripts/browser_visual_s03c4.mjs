// Real-browser visual acceptance for the console: S03-C4 bar chart plus the dashboard designer layout.
// Drives an external headless Chrome over CDP with no dependencies (Node 22+ global WebSocket/fetch).
// Not part of `npm test`: it needs a running reporting-demo and a Chrome started with
//   --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir>
// and it exits non-zero when any check fails. Screenshots and report.json go to --out.
// Usage: node scripts/browser_visual_s03c4.mjs --out DIR --token-file FILE [--base URL] [--cdp PORT]
//        [--download-chrome DIR --download-local DIR]   (the same directory as seen by Chrome / by this script)
import {readFileSync, writeFileSync, mkdirSync, readdirSync, statSync, rmSync} from 'node:fs';
import {join} from 'node:path';

const args = Object.fromEntries(process.argv.slice(2).reduce((acc, v, i, all) => (v.startsWith('--') ? [...acc, [v.slice(2), all[i + 1]]] : acc), []));
if (!args.out || !args['token-file']) { console.error('usage: --out DIR --token-file FILE [--base URL] [--cdp PORT] [--download-chrome DIR --download-local DIR]'); process.exit(2); }
const base = args.base || 'http://127.0.0.1:7810', cdp = args.cdp || '9333', outDir = args.out;
const token = readFileSync(args['token-file'], 'utf8').trim();
const sleep = ms => new Promise(r => setTimeout(r, ms));
mkdirSync(outDir, {recursive: true});

const problems = [], dialogs = [], checks = [];
function connect(url, onEvent) {
  const ws = new WebSocket(url), pending = new Map();
  let nextId = 1;
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) { const {res, rej} = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); } else onEvent?.(m);
  };
  const ready = new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('websocket failed: ' + url)); });
  const send = (method, params = {}) => new Promise((res, rej) => { const id = nextId++; pending.set(id, {res, rej}); ws.send(JSON.stringify({id, method, params})); });
  return {ready, send, close: () => ws.close()};
}
const check = (name, ok, detail) => { checks.push({name, ok: !!ok, detail: ok ? undefined : detail}); console.log(ok ? 'PASS' : 'FAIL', name, ok ? '' : JSON.stringify(detail)); };

const version = await (await fetch(`http://127.0.0.1:${cdp}/json/version`)).json();
const target = (await (await fetch(`http://127.0.0.1:${cdp}/json/list`)).json()).find(t => t.type === 'page');
const page = connect(target.webSocketDebuggerUrl, m => {
  if (m.method === 'Runtime.exceptionThrown') problems.push('exception: ' + (m.params.exceptionDetails.exception?.description || m.params.exceptionDetails.text));
  if (m.method === 'Runtime.consoleAPICalled' && ['error', 'warning'].includes(m.params.type)) problems.push('console.' + m.params.type + ': ' + m.params.args.map(a => a.value ?? a.description).join(' '));
  if (m.method === 'Network.responseReceived' && m.params.response.status >= 400) problems.push('http ' + m.params.response.status + ' ' + m.params.response.url);
  if (m.method === 'Page.javascriptDialogOpening') { dialogs.push(m.params.message); page.send('Page.handleJavaScriptDialog', {accept: false}).catch(() => {}); }
});
await page.ready;
const send = page.send;
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

const VIEWPORTS = [['desktop', 1280, 900, false, 1], ['tablet', 768, 1024, false, 1], ['mobile', 390, 844, true, 2]];
const PAGES = ['overview', 'catalog', 'workspace', 'dashboard', 'tasks', 'system'];
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

async function downloadCheck() {
  if (!args['download-chrome'] || !args['download-local']) { console.log('SKIP excel download (no --download-chrome/--download-local)'); return; }
  const local = args['download-local'];
  mkdirSync(local, {recursive: true}); for (const f of readdirSync(local)) rmSync(join(local, f), {force: true});
  const browser = connect(version.webSocketDebuggerUrl); await browser.ready;
  await browser.send('Browser.setDownloadBehavior', {behavior: 'allow', downloadPath: args['download-chrome']});
  await openPage('workspace');
  await evalJs(`window.__rows=null;document.getElementById('preview').click()`); await sleep(150);
  await waitFor(`!document.getElementById('preview').disabled&&!document.getElementById('result-table-wrap').hidden`, 'real preview for snapshot');
  await evalJs(`document.getElementById('snapshot').click()`);
  await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'snapshot complete', 20000);
  await evalJs(`document.getElementById('export').click()`);
  let file;
  for (let i = 0; i < 200 && !file; i++) { await sleep(100); file = readdirSync(local).find(f => f.endsWith('.xlsx')); }
  browser.close();
  const bytes = file ? readFileSync(join(local, file)) : Buffer.alloc(0);
  check('real Chrome downloads a verified .xlsx', file && bytes.length > 1000 && bytes.subarray(0, 2).toString() === 'PK', {file, size: bytes.length});
  if (file) writeFileSync(join(outDir, 'download.json'), JSON.stringify({file, bytes: bytes.length, magic: bytes.subarray(0, 2).toString()}, null, 1));
}

let fatal;
try {
  await send('Page.enable'); await send('Runtime.enable'); await send('Network.enable'); await send('Network.setCacheDisabled', {cacheDisabled: true});
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
    const tag = vp[0];
    await setViewport(vp);
    for (const name of PAGES) { await openPage(name); await sleep(100); const o = await pageOverflow(); check(`${tag}: ${name} page has no horizontal overflow`, !o.over, o); }
    await openPage('workspace'); await evalJs(`document.getElementById('report-designer').open=true`); await sleep(100);
    check(`${tag}: report layout designer controls are not clipped`, !(await clippedControls('#report-designer')).length, await clippedControls('#report-designer'));
    await shot(`${tag}-00-report-designer`, `document.getElementById('report-designer')`);
    await evalJs(`document.getElementById('report-designer').open=false`);

    await preview(null);
    await shot(`${tag}-01-dashboard`);
    await shot(`${tag}-02-toolbar`, `document.querySelector('#page-dashboard .panel')`);
    await shot(`${tag}-03-server-controls`, `document.getElementById('board-server-save').closest('.panel')`);
    await shot(`${tag}-04-board-real-data`, `document.getElementById('board-widgets')`);
    const real = await chartFacts();
    check(`${tag}: real server rows plot 3 bars with exact labels`, real.rows === 3 && real.values.some(v => v === '9007199254740893.00'), real);
    check(`${tag}: dashboard controls are not clipped`, !(await clippedControls('#page-dashboard')).length, await clippedControls('#page-dashboard'));

    for (const [name, rows, expect] of SCENARIOS) {
      await preview(rows);
      await shot(`${tag}-05-${name}`, `document.getElementById('board-widgets')`);
      const facts = await chartFacts(), o = await pageOverflow();
      check(`${tag}: ${name} renders as specified`, expect(facts), {...facts, values: facts.values.map(v => v.slice(0, 40))});
      check(`${tag}: ${name} has no horizontal page overflow`, !o.over, o);
    }
    await evalJs(`window.__rows=null;true`);
  }

  await setViewport(VIEWPORTS[0]);
  await downloadCheck();
} catch (e) {
  fatal = String(e.message || e); console.log('FATAL', fatal);
  try { await shot('zz-failure'); } catch (_) { /* page may be gone */ }
}
check('no console errors, failed requests or exceptions', !problems.length, [...new Set(problems)]);
check('no JavaScript dialogs (hostile text stayed text)', !dialogs.length, dialogs);
if (fatal) check('script ran to completion', false, fatal);
const failed = checks.filter(c => !c.ok);
writeFileSync(join(outDir, 'report.json'), JSON.stringify({base, viewports: VIEWPORTS.map(v => v.slice(0, 3)), passed: checks.length - failed.length, failed: failed.length, checks}, null, 2));
console.log(`\n${checks.length - failed.length}/${checks.length} checks passed`);
page.close();
process.exit(failed.length ? 1 : 0);
