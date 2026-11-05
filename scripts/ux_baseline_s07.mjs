// UX task baseline for S07 (docs/ADR_0009_SHELL_CONTINUITY.md, docs/UX_BASELINE_S07.md): seven tasks a person does in the console, driven with real
// mouse and keyboard input over the Chrome DevTools Protocol, counting what the person pays for each: clicks, texts typed, page switches (a
// section shown in place of another), shell changes (the chrome around the page replaced: console shell ↔ document studio ↔ template centre ↔
// dashboard studio), dialogs opened and dead ends (the page telling the person to go somewhere else first). The same script runs before and after a
// change; the numbers go side by side into docs/UX_BASELINE_S07.md. It judges nothing else: not looks, not speed.
// Not part of `npm test`; CI runs it through scripts/ci_browser_evidence.sh (job "browser") and holds its totals. By hand it needs a running reporting-demo (dashboard and template directories, the PDF flags are not needed) and a
// Chrome started with --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server.
// Safety: --base must be loopback because the bearer token is typed into the page. The script writes one JSON file (--out) and nothing else.
// Usage: node scripts/ux_baseline_s07.mjs --out FILE.json --token-file FILE --label before|after [--base http://127.0.0.1:PORT] [--cdp PORT]
import {readFileSync, writeFileSync, mkdirSync} from 'node:fs';
import {parseArgs} from 'node:util';
import {dirname} from 'node:path';

const usage = why => { console.error(why + '\nusage: --out FILE.json --token-file FILE --label TEXT [--base http://127.0.0.1:PORT] [--cdp PORT]'); process.exit(2); };
let args;
try { ({values: args} = parseArgs({options: {out: {type: 'string'}, 'token-file': {type: 'string'}, label: {type: 'string'}, base: {type: 'string'}, cdp: {type: 'string'}}, strict: true})); } catch (e) { usage(e.message); }
if (!args.out || !args['token-file'] || !args.label) usage('--out, --token-file and --label are required');
const base = args.base || 'http://127.0.0.1:7811', cdp = args.cdp || '9347';
let baseUrl; try { baseUrl = new URL(base); } catch { usage('--base is not a valid URL'); }
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname)) usage('--base must be an http loopback address');
const token = readFileSync(args['token-file'], 'utf8').trim();
const sleep = ms => new Promise(r => setTimeout(r, ms));

function connect(url) {
  const ws = new WebSocket(url), pending = new Map(); let nextId = 1;
  ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const {res, rej} = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); } };
  const ready = new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('websocket failed: ' + url)); });
  const send = (method, params = {}, timeout = 40000) => new Promise((res, rej) => { const id = nextId++, timer = setTimeout(() => { pending.delete(id); rej(new Error('CDP timeout: ' + method)); }, timeout); pending.set(id, {res: v => { clearTimeout(timer); res(v); }, rej: e => { clearTimeout(timer); rej(e); }}); ws.send(JSON.stringify({id, method, params})); });
  return {ready, send, close: () => ws.close()};
}
let page;
const send = (...a) => page.send(...a);
const evalJs = async expr => { const r = await send('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true}); if (r.exceptionDetails) throw new Error('eval failed: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text)); return r.result.value; };
const waitFor = async (expr, label, limit = 30000) => { const end = Date.now() + limit; while (Date.now() < end) { if (await evalJs(`Boolean(${expr})`).catch(() => false)) return; await sleep(80); } throw new Error('timeout waiting for: ' + label); };
const mouse = (type, x, y) => send('Input.dispatchMouseEvent', {type, x, y, button: type === 'mouseMoved' ? 'none' : 'left', buttons: type === 'mouseReleased' || type === 'mouseMoved' ? 0 : 1, clickCount: type === 'mouseMoved' ? 0 : 1});
const q = sel => `document.querySelector(${JSON.stringify(sel)})`;
const btnIn = (scope, text) => `[...document.querySelectorAll(${JSON.stringify(scope + ' button')})].find(b=>!b.disabled&&b.getAttribute('aria-disabled')!=='true'&&(b.getAttribute('aria-label')||b.textContent).trim().startsWith(${JSON.stringify(text)})&&b.getBoundingClientRect().width>0)`;

// ---- the meter: what the page shows changing, and what the person does ------------------------------------------------------------------
const METER = `(()=>{if(window.__ux)return;const u=window.__ux={pages:0,shells:0,dialogs:0,deadEnds:0,log:[]};
// A shell is the chrome around the page: the console header while it is on screen (on every page since S07-F), else the full-screen box that replaced it.
const shellOf=()=>{const h=document.querySelector('.console-header');if(h&&getComputedStyle(h).display!=='none')return 'console';return ['studio-on','library-on','board-on'].find(c=>document.body.classList.contains(c))||'console';};
const pageOf=()=>[...document.querySelectorAll('section.page')].find(s=>!s.hidden)?.id||'';
let page=pageOf(),shell=shellOf();
const dialogsNow=()=>[...document.querySelectorAll('[role=dialog]')].filter(d=>{const r=d.getBoundingClientRect();return r.width>0&&r.height>0&&!d.closest('[hidden]')&&!d.closest('.overlay:not(.open):not(#page-dashboard .overlay)')&&!d.closest('#connection-overlay');}).length;
let dialogs=dialogsNow();
const tick=()=>{const p=pageOf(),s=shellOf();if(p!==page){u.pages++;u.log.push('page '+page+' → '+p);page=p;}if(s!==shell){u.shells++;u.log.push('shell '+shell+' → '+s);shell=s;}const d=dialogsNow();if(d>dialogs){u.dialogs+=d-dialogs;u.log.push('dialog +'+(d-dialogs));}dialogs=d;};
new MutationObserver(tick).observe(document.body,{attributes:true,subtree:true,attributeFilter:['hidden','class'],childList:true});
u.deadEnd=()=>{u.deadEnds++;};})()`;
const meter = async () => { await evalJs(METER); };
const read = () => evalJs(`JSON.stringify(window.__ux)`).then(JSON.parse);

let clicks = 0, texts = 0;
const clickExpr = async (expr, label) => {
  const at = await evalJs(`(()=>{const e=${expr};if(!e)return null;e.scrollIntoView({block:'center',inline:'nearest'});const b=e.getBoundingClientRect();return [b.left+b.width/2,b.top+b.height/2];})()`);
  if (!at) throw new Error('nothing to click: ' + (label || expr));
  clicks++; await mouse('mouseMoved', at[0], at[1]); await mouse('mousePressed', at[0], at[1]); await mouse('mouseReleased', at[0], at[1]); await sleep(120);
};
const click = sel => clickExpr(q(sel), sel);
const clickBtn = (scope, text) => clickExpr(btnIn(scope, text), scope + ' ' + text);
const typeInto = async (sel, text) => { await click(sel); clicks--; texts++; await evalJs(`${q(sel)}.select()`); await send('Input.insertText', {text}); await sleep(120); };
const idle = () => waitFor(`!document.getElementById('preview').disabled`, 'idle', 60000);
// A dead end: the page told the person to go somewhere else first (a start card or a status saying so).
const deadEndIf = async expr => { if (await evalJs(`Boolean(${expr})`)) await evalJs(`window.__ux.deadEnd()`); };

const tasks = [];
async function task(name, fn) {
  const before = await read(); const c0 = clicks, t0 = texts; const started = Date.now();
  try { await fn(); } catch (e) { tasks.push({name, error: String(e.message)}); console.log('TASK', name, 'FAILED', e.message); return; }
  const after = await read();
  const row = {name, clicks: clicks - c0, texts: texts - t0, pages: after.pages - before.pages, shells: after.shells - before.shells, dialogs: after.dialogs - before.dialogs, deadEnds: after.deadEnds - before.deadEnds, seconds: Math.round((Date.now() - started) / 100) / 10, log: after.log.slice(before.log.length)};
  tasks.push(row); console.log('TASK', name, JSON.stringify({...row, log: undefined}));
}
const signedOut = () => evalJs(`!document.getElementById('connection-overlay').hidden`);
async function signIn() { await typeInto('#access-token', token); await clickBtn('.connection-card', '连接工作区'); await waitFor(`document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`, 'signed in'); await sleep(300); }
async function openPage(name) {
  // A person does not click their way to the page they are on (S07-H, ADR 0011 decision 7).
  if (await evalJs(`(()=>{const p=document.getElementById('page-${name}');return !!p&&!p.hidden;})()`)) return;
  // The way a person gets to a page from wherever they are: the navigation if it is on screen (the rail of S07-A, or the console's column), else the way out the current shell offers.
  const onScreen = await evalJs(`(()=>{const b=document.querySelector('[data-page="${name}"]');if(!b)return false;const r=b.getBoundingClientRect();if(r.width===0)return false;const at=document.elementFromPoint(r.left+r.width/2,r.top+r.height/2);return !!at&&(at===b||b.contains(at));})()`);
  if (onScreen) { await click(`[data-page="${name}"]`); await waitFor(`!document.getElementById('page-${name}').hidden`, 'page ' + name); await sleep(250); return; }
  // S07-B: the tasks are a panel over the current page when the rail offers one.
  if (name === 'tasks' && await evalJs(`!!document.querySelector('[data-panel="tasks"]')`)) { await click('[data-panel="tasks"]'); await waitFor(`!document.getElementById('sh-d-tasks').hidden`, 'the tasks panel'); await sleep(250); return; }
  const shell = await evalJs(`['studio-on','library-on','board-on'].find(c=>document.body.classList.contains(c))||'console'`);
  if (shell === 'board-on') await click('#bd-wordmark');
  else if (shell === 'studio-on') { await click('#st-wordmark'); await waitFor(`!document.getElementById('page-library').hidden`, 'centre'); await sleep(300); await clickBtn('#page-library .library-header', '数据中心'); }
  else if (shell === 'library-on') await clickBtn('#page-library .library-header', '数据中心');
  if (name !== 'catalog' || shell === 'console') await click(`[data-page="${name}"]`);
  await waitFor(`!document.getElementById('page-${name}').hidden`, 'page ' + name); await sleep(250);
}

async function main() {
  const list = await (await fetch(`http://127.0.0.1:${cdp}/json/list`)).json();
  const target = list.find(t => t.type === 'page'); if (!target) throw new Error('no page target on the debugging port');
  page = connect(target.webSocketDebuggerUrl); await page.ready;
  await send('Page.enable'); await send('Runtime.enable'); await send('Emulation.setFocusEmulationEnabled', {enabled: true});
  await send('Browser.setDownloadBehavior', {behavior: 'deny'}).catch(() => {});
  await send('Emulation.setDeviceMetricsOverride', {width: 1440, height: 900, deviceScaleFactor: 1, mobile: false});
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&!!document.getElementById('access-token')`, 'the console'); await sleep(400);
  await meter();

  // T1 · 登录到第一份导出：sign in, reach the workbench, scope the query, freeze a snapshot, export Excel.
  await task('T1 登录 → 冻结快照 → 导出 Excel', async () => {
    await signIn();
    await openPage('workspace');
    await typeInto('#query-companies', 'company_demo');
    await clickBtn('#page-workspace', '创建快照'); await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'snapshot', 60000);
    await clickBtn('#page-workspace', '导出 Excel'); await waitFor(`document.getElementById('notice').textContent.includes('下载已发起')`, 'export', 60000); await idle();
  });

  // T2 · 发布一个看板, starting from a signed-in console with no query made yet (a fresh session): the dashboard first, as a person would.
  await task('T2 从空会话发布一个带指标卡的看板', async () => {
    await clickBtn('.console-header', '退出工作区'); await waitFor(`!document.getElementById('connection-overlay').hidden`, 'signed out');
    await signIn();
    await openPage('dashboard');
    // The start card: before S07-A it sends the person to the workbench (a dead end); after, it opens the data drawer in place.
    const sendsAway = await evalJs(`!!${btnIn('#bd-start', '回工作台查询')}`);
    if (sendsAway) { await evalJs(`window.__ux.deadEnd()`); await clickBtn('#bd-start', '回工作台查询'); await waitFor(`!document.getElementById('page-workspace').hidden`, 'workbench'); }
    else { await clickBtn('#bd-start', '选择数据…'); await waitFor(`document.getElementById('sh-query-slot').contains(document.querySelector('.query-panel'))`, 'the data drawer'); }
    await typeInto('#query-companies', 'company_demo');
    await clickBtn('.query-panel', '查询预览'); await waitFor(`!document.getElementById('result-table-wrap').hidden&&!document.getElementById('preview').disabled`, 'preview', 60000);
    await openPage('dashboard');
    await clickBtn('#bd-sidebar', '添加指标卡');
    // Publishing: a person presses 发布. Before S07-B the studio refused unsaved work (a dead end: save first, then 发布 again); after, the confirmation offers 保存并发布.
    await clickBtn('#bd-studio .topbar', '发布'); await sleep(400);
    if (await evalJs(`document.getElementById('bd-d-publish').hidden`)) {
      await evalJs(`window.__ux.deadEnd()`);
      await clickBtn('#bd-studio .topbar', '保存版本'); await waitFor(`document.getElementById('bd-state').textContent==='已保存 v1'`, 'saved', 30000); await idle();
      await clickBtn('#bd-studio .topbar', '发布'); await waitFor(`!document.getElementById('bd-d-publish').hidden`, 'confirmation', 30000);
      await clickBtn('#bd-d-publish', '确认发布');
    } else await clickBtn('#bd-d-publish', '保存并发布');
    await waitFor(`document.getElementById('bd-state').textContent==='已发布 v1'`, 'published', 30000); await idle();
  });

  // T3 · 把一个样例变成自己的模板并发布, from the template centre, with the session's data already queried (a preview, not a snapshot).
  await task('T3 从模板中心复制样例 → 设计器 → 保存 → 发布', async () => {
    await openPage('library'); await waitFor(`document.querySelectorAll('#page-library .sample-card').length>=37`, 'centre', 30000); await sleep(300);
    const disabled = await evalJs(`(()=>{const b=[...document.querySelectorAll('#page-library .sample-card button')].find(b=>(b.getAttribute('aria-label')||'').startsWith('复制示例'));return !b||b.getAttribute('aria-disabled')==='true';})()`);
    if (disabled) {
      // The centre needs a frozen snapshot: back to the workbench for one, then back here.
      await evalJs(`window.__ux.deadEnd()`);
      await openPage('workspace');
      await clickBtn('#page-workspace', '创建快照'); await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'snapshot', 60000);
      await click('[data-page="library"]'); await waitFor(`document.querySelectorAll('#page-library .sample-card').length>=37`, 'centre again', 30000); await sleep(300);
    }
    await clickExpr(`[...document.querySelectorAll('#page-library .sample-card button')].find(b=>(b.getAttribute('aria-label')||'').startsWith('复制示例')&&b.getAttribute('aria-disabled')!=='true')`, '复制示例');
    await waitFor(`${q('#page-library .overlay.open tbody tr')}`, 'mapping'); await sleep(250);
    await typeInto('#page-library .overlay.open input[type=text]', '基线复制件');
    await clickBtn('#page-library .overlay.open', '复制并进入设计器');
    await waitFor(`!document.getElementById('page-documents').hidden&&document.getElementById('doc-server-status').textContent.includes('正在编辑模板 ')`, 'studio', 60000); await idle(); await sleep(400);
    await clickBtn('#page-documents .topbar', '发布'); await waitFor(`${q('#st-d-publish.open')}`, 'publish confirm', 30000); await sleep(200);
    await clickBtn('#st-d-publish', '确认发布'); await waitFor(`document.getElementById('st-state').textContent.startsWith('已发布')`, 'published', 60000); await idle();
  });

  // T4 · 冻结一个快照，再从任务工作区找到它并查看它的结果.
  await task('T4 冻结快照 → 任务工作区 → 查看结果', async () => {
    await openPage('workspace');
    await clickBtn('#page-workspace', '创建快照'); await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'snapshot', 60000);
    await openPage('tasks');
    await clickBtn('#tasks-body', '查看结果'); await waitFor(`!document.getElementById('page-workspace').hidden&&document.getElementById('result-kind').textContent==='冻结快照'`, 'result', 60000); await idle();
  });

  // T5 · 三个设计面之间来回：文档工作室 → 看板工作室 → 模板中心 → 文档工作室.
  await task('T5 文档工作室 → 看板工作室 → 模板中心 → 文档工作室', async () => {
    await openPage('documents'); await sleep(300);
    await openPage('dashboard'); await sleep(300);
    await openPage('library'); await waitFor(`document.querySelectorAll('#page-library .sample-card').length>=37`, 'centre', 30000); await sleep(300);
    await clickBtn('#page-library .library-header', '返回设计器'); await waitFor(`!document.getElementById('page-documents').hidden`, 'studio'); await sleep(300);
  });

  // T6 · 报表工作台里调整一列、保存并发布布局 (ADR 0011), on the result T4 left on the workbench. Publishing as in T2: a person presses 发布 first.
  // Before S07-G the layout designer refuses twice (nothing saved yet, then the release state not read): each refusal is a dead end, and the person
  // does the step it asks for and presses 发布 again. After S07-G 发布 opens a confirmation offering 保存并发布 (or 确认发布), and the script presses it.
  await task('T6 报表工作台：调整一列 → 保存 → 发布布局', async () => {
    await openPage('workspace');
    if (await evalJs(`document.getElementById('result-table-wrap').hidden`)) throw new Error('no result on the workbench for the layout designer');
    if (!await evalJs(`document.getElementById('report-designer').open`)) await click('#report-designer > summary');
    await waitFor(`document.querySelectorAll('#designer-columns .designer-row').length>=2`, 'the designer columns');
    await clickExpr(`[...document.querySelectorAll('#designer-columns .designer-row')][1]?.querySelector('button')`, '上移');
    await waitFor(`document.getElementById('layout-status').textContent.startsWith('布局已应用')`, 'the column moved');
    const published = `document.getElementById('layout-release-info').textContent.startsWith('已发布版本')`;
    const confirm = `(${btnIn('[role=dialog]', '保存并发布')}||${btnIn('[role=dialog]', '确认发布')})`;
    for (let refusals = 0; ; refusals++) {
      await clickBtn('#report-designer', '发布'); await sleep(400); await idle();
      if (await evalJs(`!!${confirm}`)) { await clickExpr(confirm, '保存并发布 / 确认发布'); await waitFor(published, 'published', 30000); await idle(); break; }
      if (await evalJs(published)) break;
      await evalJs(`window.__ux.deadEnd()`);
      if (refusals >= 2) throw new Error('发布 refused three times: ' + await evalJs(`document.getElementById('layout-status').textContent`));
      if (!await evalJs(`document.getElementById('layout-server-status').textContent.startsWith('已保存 ')`)) {
        await clickBtn('#report-designer', '保存服务器版本'); await waitFor(`document.getElementById('layout-server-status').textContent.startsWith('已保存 ')`, 'saved', 30000); await idle();
      } else {
        await clickBtn('#report-designer', '读取发布状态'); await waitFor(`document.getElementById('layout-release-info').textContent.includes('发布修订')`, 'the release state', 30000); await idle();
      }
    }
  });

  // T7 · 查看一个数据集的字段与币种，再用它查询预览 (ADR 0011 decision 7, before S07-H folds the data centre into the data drawer). The person reads
  // the columns, their types and currencies where the console shows them, then queries that dataset: before S07-H through the data centre's card and its
  // 打开工作台; after it, the data drawer shows the chosen dataset's columns beside the query and the preview runs from there.
  await task('T7 查看数据集的字段与币种 → 用它查询预览', async () => {
    if (!await evalJs(`!!document.getElementById('page-catalog')`)) {
      await click('#sh-data-chip'); await waitFor(`!document.getElementById('sh-d-data').hidden`, 'the data drawer');
      if (await evalJs(`document.getElementById('query-dataset').value!=='receivables.aging@1.0.0'`)) { await click('#query-dataset'); await evalJs(`(()=>{const s=document.getElementById('query-dataset');s.value='receivables.aging@1.0.0';s.dispatchEvent(new Event('change',{bubbles:true}));})()`); }
      await waitFor(`[...document.querySelectorAll('#sh-fields .column-line code')].some(c=>c.textContent.includes('CNY'))`, 'the columns with their currency in the drawer');
      if (!await evalJs(`document.getElementById('query-companies').value`)) await typeInto('#query-companies', 'company_demo');
      await clickBtn('#sh-d-data', '查询预览'); await waitFor(`document.getElementById('result-kind').textContent==='查询预览'&&!document.getElementById('result-table-wrap').hidden&&!document.getElementById('preview').disabled&&document.getElementById('sh-d-data').hidden`, 'preview', 60000); await idle();
      return;
    }
    await openPage('catalog');
    await waitFor(`[...document.querySelectorAll('#catalog-grid .catalog-card .column-line code')].some(c=>c.textContent.includes('CNY'))`, 'the columns with their currency');
    await clickExpr(`[...document.querySelectorAll('#catalog-grid .catalog-card')].find(c=>c.querySelector('.description')?.textContent.startsWith('receivables.aging'))?.querySelector('.card-bottom button')`, '打开工作台');
    await waitFor(`!document.getElementById('page-workspace').hidden&&document.getElementById('query-dataset').value==='receivables.aging@1.0.0'`, 'the workbench with the dataset');
    if (!await evalJs(`document.getElementById('query-companies').value`)) await typeInto('#query-companies', 'company_demo');
    await clickBtn('#page-workspace', '查询预览'); await waitFor(`document.getElementById('result-kind').textContent==='查询预览'&&!document.getElementById('result-table-wrap').hidden&&!document.getElementById('preview').disabled`, 'preview', 60000); await idle();
  });

  const meterState = await read();
  const out = {label: args.label, generated: new Date().toISOString(), base, tasks, totals: tasks.filter(t => !t.error).reduce((a, t) => ({clicks: a.clicks + t.clicks, texts: a.texts + t.texts, pages: a.pages + t.pages, shells: a.shells + t.shells, dialogs: a.dialogs + t.dialogs, deadEnds: a.deadEnds + t.deadEnds}), {clicks: 0, texts: 0, pages: 0, shells: 0, dialogs: 0, deadEnds: 0}), log: meterState.log};
  mkdirSync(dirname(args.out), {recursive: true}); writeFileSync(args.out, JSON.stringify(out, null, 2));
  console.log('| 任务 | 点击 | 输入 | 页面切换 | 壳子切换 | 对话框 | 死胡同 |');
  console.log('|---|---|---|---|---|---|---|');
  for (const t of tasks) console.log(t.error ? `| ${t.name} | 失败：${t.error} |` : `| ${t.name} | ${t.clicks} | ${t.texts} | ${t.pages} | ${t.shells} | ${t.dialogs} | ${t.deadEnds} |`);
  console.log(`| 合计 | ${out.totals.clicks} | ${out.totals.texts} | ${out.totals.pages} | ${out.totals.shells} | ${out.totals.dialogs} | ${out.totals.deadEnds} |`);
  return tasks.some(t => t.error) ? 1 : 0;
}
let code = 1;
try { code = await main(); } catch (e) { console.error('ABORT', e.stack || e); }
page?.close();
process.exit(code);
