// Takes the user manual's figures (docs/manual/img/01-signin.png … 38-phone-studio.png; the captions of docs/manual/index.html say what each
// must show) from the running console by real mouse and keyboard input over the Chrome DevTools Protocol, so that the manual follows the
// console (ADR 0014, S09-A/B: the type scale, the 56 px header, the studios' chrome) without anyone taking screenshots by hand.
// The run is one session of an identity with nothing saved yet (a fresh memory-store demo, as the CI scripts assume): the sign-in card,
// the workbench (the data drawer and the dataset's fields, a refused company, the second product connection, a preview, a snapshot, the
// layout designer's versions), the dashboard studio (three widgets, the structure panel, 保存并发布, the versions), the tasks panel and a
// restore by ID, the template centre (a sample's preview, the wizard, the classification, the card view, a copy with a field mapping), the
// document studio (the ribbon tabs, the data panel, the edge-sample check, the versions, the publish confirmation, the PDF preview), the
// official issue of a published template, the system page, the dark scheme and the phone width. What clicking would only make slow is seeded
// through the public API with the same token (the identity's own templates and the second dataset's jobs, as scripts/seed_demo_data.mjs
// makes them); every figure is the real state of the page.
// Every figure is 1440 × 900 at device scale 1, the phone figures 390 × 844: the sizes the manual's figures have had since they were first taken;
// a figure of another size is an error. Not part of `npm test`; scripts/manual_screens.sh builds and starts the demo and a Chrome, runs this and
// stops both. By hand it needs a running reporting-demo (memory store, the private directories, the PDF flags for the PDF figures) and a Chrome
// started with --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server
// It exits 1 when a figure could not be taken or the run aborts, and 2 on bad arguments.
// Safety: --base must be loopback because the bearer token is typed into the page. The script writes only the figures into --out (and, when it
// aborts, one screenshot of the failure state into the OS temp directory, whose path it prints); it removes nothing.
// Usage: node scripts/manual_screens.mjs --token-file FILE --base http://127.0.0.1:PORT --cdp PORT [--out docs/manual/img]
import {readFileSync, writeFileSync, mkdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const usage = why => {
  console.error(why + '\nusage: --token-file FILE --base http://127.0.0.1:PORT --cdp PORT [--out docs/manual/img]');
  process.exit(2);
};
let args;
try {
  ({values: args} = parseArgs({
    options: {
      'token-file': {type: 'string'},
      base: {type: 'string'},
      cdp: {type: 'string'},
      out: {type: 'string'},
    },
    strict: true,
  }));
} catch (e) {
  usage(e.message);
}
if (!args['token-file']) usage('--token-file is required');
const base = args.base || 'http://127.0.0.1:7880',
  cdp = args.cdp || '9370',
  outDir = args.out || 'docs/manual/img';
let baseUrl;
try {
  baseUrl = new URL(base);
} catch {
  usage('--base is not a valid URL');
}
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname))
  usage('--base must be an http loopback address');
if (!/^\d+$/.test(cdp)) usage('--cdp must be a port number');
const token = readFileSync(args['token-file'], 'utf8').trim();
const sleep = ms => new Promise(r => setTimeout(r, ms));
mkdirSync(outDir, {recursive: true});

// The figures and their sizes (docs/manual/index.html; the phone figures are the last three).
const DESKTOP = [1440, 900],
  PHONE = [390, 844];
const FIGURES = {
  '01-signin': DESKTOP,
  '02-home': DESKTOP,
  '03-data-fields': DESKTOP,
  '04-dashboard-start': DESKTOP,
  '05-data-drawer': DESKTOP,
  '06-workbench-empty': DESKTOP,
  '07-workbench-preview': DESKTOP,
  '08-workbench-snapshot': DESKTOP,
  '09-layout-designer': DESKTOP,
  '10-refused-company': DESKTOP,
  '11-shipment-preview': DESKTOP,
  '12-dashboard-widgets': DESKTOP,
  '13-dashboard-structure': DESKTOP,
  '14-dashboard-save-publish': DESKTOP,
  '15-dashboard-versions': DESKTOP,
  '16-tasks-panel': DESKTOP,
  '17-tasks-restored': DESKTOP,
  '18-library': DESKTOP,
  '19-sample-preview': DESKTOP,
  '20-sample-mapping': DESKTOP,
  '21-wizard': DESKTOP,
  '22-classify': DESKTOP,
  '23-library-grid': DESKTOP,
  '24-studio': DESKTOP,
  '25-studio-insert': DESKTOP,
  '26-studio-data-panel': DESKTOP,
  '27-studio-check': DESKTOP,
  '28-studio-versions': DESKTOP,
  '29-studio-publish': DESKTOP,
  '30-studio-preview': DESKTOP,
  '31-studio-issue-dialog': DESKTOP,
  '32-issued': DESKTOP,
  '33-system': DESKTOP,
  '34-dark-workbench': DESKTOP,
  '35-dark-dashboard': DESKTOP,
  '36-phone-workbench': PHONE,
  '37-phone-library': PHONE,
  '38-phone-studio': PHONE,
};
const notes = []; // what a figure shows instead of the intended state, reported at the end
const note = text => {
  notes.push(text);
  console.log('NOTE', text);
};

// ---- the DevTools connection ------------------------------------------------------------------------------------------------------
function connect(url) {
  const ws = new WebSocket(url),
    pending = new Map();
  let nextId = 1;
  ws.onmessage = ev => {
    const m = JSON.parse(ev.data);
    if (m.id && pending.has(m.id)) {
      const {res, rej} = pending.get(m.id);
      pending.delete(m.id);
      m.error ? rej(new Error(m.error.message)) : res(m.result);
    }
  };
  ws.onclose = () => {
    for (const {rej} of pending.values()) rej(new Error('CDP connection closed'));
    pending.clear();
  };
  const ready = new Promise((res, rej) => {
    ws.onopen = res;
    ws.onerror = () => rej(new Error('websocket failed: ' + url));
  });
  const send = (method, params = {}, timeout = 40000) =>
    new Promise((res, rej) => {
      const id = nextId++,
        timer = setTimeout(() => {
          pending.delete(id);
          rej(new Error('CDP timeout: ' + method));
        }, timeout);
      pending.set(id, {
        res: v => {
          clearTimeout(timer);
          res(v);
        },
        rej: e => {
          clearTimeout(timer);
          rej(e);
        },
      });
      ws.send(JSON.stringify({id, method, params}));
    });
  return {ready, send, close: () => ws.close()};
}
let page;
const send = (...a) => page.send(...a);
const evalJs = async expr => {
  const r = await send('Runtime.evaluate', {expression: expr, awaitPromise: true, returnByValue: true});
  if (r.exceptionDetails)
    throw new Error('eval failed: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
};
const waitFor = async (expr, label, limit = 20000) => {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    if (await evalJs(`Boolean(${expr})`).catch(() => false)) return;
    await sleep(80);
  }
  throw new Error('timeout waiting for: ' + label);
};
const pngSize = buf => [buf.readUInt32BE(16), buf.readUInt32BE(20)];
const taken = new Set();
// A figure: the page as it is, at the size the manual has for it; the file name is fixed by the manual.
const shot = async name => {
  if (!FIGURES[name]) throw new Error('not a figure of the manual: ' + name);
  await sleep(450); // transitions and toasts settle
  const png = Buffer.from((await send('Page.captureScreenshot', {format: 'png'})).data, 'base64');
  const [w, h] = pngSize(png),
    [ew, eh] = FIGURES[name];
  if (w !== ew || h !== eh) throw new Error(`${name}: ${w}×${h}, the manual's figure is ${ew}×${eh}`);
  writeFileSync(join(outDir, name + '.png'), png);
  taken.add(name);
  console.log('figure', name, `${w}×${h}`, png.length + ' B');
};
const viewport = async (w, h, scheme = 'light', mobile = false) => {
  await send('Emulation.setDeviceMetricsOverride', {width: w, height: h, deviceScaleFactor: 1, mobile});
  await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: scheme}]});
  await sleep(500);
};

// ---- real input (as scripts/browser_console_s06c.mjs) --------------------------------------------------------------------------------
const mouse = (type, x, y) =>
  send('Input.dispatchMouseEvent', {
    type,
    x,
    y,
    button: type === 'mouseMoved' ? 'none' : 'left',
    buttons: type === 'mouseReleased' || type === 'mouseMoved' ? 0 : 1,
    clickCount: type === 'mouseMoved' ? 0 : 1,
  });
const KEYS = {Escape: [27, 'Escape'], Enter: [13, 'Enter'], Tab: [9, 'Tab']};
const key = async name => {
  const [vk, code] = KEYS[name];
  for (const type of ['rawKeyDown', 'keyUp'])
    await send('Input.dispatchKeyEvent', {type, key: name, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk});
};
const centreOf = expr =>
  evalJs(
    `(()=>{const e=${expr};if(!e)return null;e.scrollIntoView({block:'center',inline:'nearest'});const b=e.getBoundingClientRect();return [b.left+b.width/2,b.top+b.height/2];})()`
  );
const clickExpr = async (expr, label) => {
  const at = await centreOf(expr);
  if (!at) throw new Error('nothing to click: ' + (label || expr));
  await mouse('mouseMoved', at[0], at[1]);
  await mouse('mousePressed', at[0], at[1]);
  await mouse('mouseReleased', at[0], at[1]);
  await sleep(100);
};
const q = sel => `document.querySelector(${JSON.stringify(sel)})`;
const click = sel => clickExpr(q(sel), sel);
// A button of a scope by the start of its text or label.
const btnIn = (scope, text) =>
  `[...document.querySelectorAll(${JSON.stringify(scope + ' button')})].find(b=>(b.getAttribute('aria-label')||b.textContent).trim().startsWith(${JSON.stringify(text)}))`;
const clickBtn = (scope, text) => clickExpr(btnIn(scope, text), scope + ' ' + text);
const typeInto = async (sel, text) => {
  await click(sel);
  await evalJs(`${q(sel)}.select()`);
  await send('Input.insertText', {text});
  await sleep(120);
};
const setSelect = (sel, value) =>
  evalJs(
    `(()=>{const e=${q(sel)};e.value=${JSON.stringify(value)};e.dispatchEvent(new Event('change',{bubbles:true}));return e.value;})()`
  );
const idle = () => waitFor(`!document.getElementById('preview').disabled`, 'the console is idle', 60000);
const noticeHas = (text, limit = 30000) =>
  waitFor(`document.getElementById('notice').textContent.includes(${JSON.stringify(text)})`, 'notice ' + text, limit);
const closeOverlays = () =>
  evalJs(
    `document.querySelectorAll('.sh-overlay').forEach(o=>{if(!o.hidden)o.querySelector('[data-act=close]').click();})`
  );
// S07-A: the application rail is on every shell; the tasks panel opens over the current page.
const nav = async target => {
  await closeOverlays();
  if (target === 'tasks') {
    await click('.app-rail [data-panel="tasks"]');
    await waitFor(`!document.getElementById('sh-d-tasks').hidden`, 'the tasks panel');
  } else {
    await click(`.app-rail [data-page="${target}"]`);
    await waitFor(`!document.getElementById('page-${target}').hidden`, 'page ' + target);
  }
  await sleep(300);
};
const top = () => evalJs(`window.scrollTo(0,0)`);
const api = (method, path, body, idempotent) =>
  evalJs(
    `(async()=>{const r=await fetch('/api/reporting/v1'+${JSON.stringify(path)},{method:${JSON.stringify(method)},headers:{Authorization:'Bearer '+${JSON.stringify(token)},'Content-Type':'application/json'${idempotent ? `,'Idempotency-Key':crypto.randomUUID()` : ''}},body:${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))}});const t=await r.text();let j=null;try{j=t?JSON.parse(t):null;}catch{j={raw:t.slice(0,200)};}return {status:r.status,body:j};})()`
  );
const must = (r, what) => {
  if (r.status < 200 || r.status >= 300)
    throw new Error(`${what}: ${r.status} ${JSON.stringify(r.body).slice(0, 200)}`);
  return r.body;
};
const pollJob = async (id, limit = 60000) => {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    const j = must(await api('GET', '/jobs/' + id), 'job ' + id);
    if (['succeeded', 'failed', 'cancelled'].includes(j.state)) return j;
    await sleep(300);
  }
  throw new Error('job did not finish: ' + id);
};
const dateIn = zone => {
  const p = new Intl.DateTimeFormat('en', {
    timeZone: zone,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).formatToParts(new Date());
  const g = t => p.find(x => x.type === t).value;
  return `${g('year')}-${g('month')}-${g('day')}`;
};

// ---- what the API seeds: the second dataset's jobs and the identity's own templates (scripts/seed_demo_data.mjs makes the same) ------
const PREFIX = '演示';
const ISSUED_TEMPLATE = `${PREFIX} · 出库装箱单`; // a published 1.x template: the manual's case C15 issues it
async function seed() {
  const session = must(await api('GET', '/session'), 'session'),
    caps = must(await api('GET', '/capabilities'), 'capabilities');
  const catalogs = {};
  for (const i of session.integrations)
    catalogs[i] = must(await api('GET', '/catalog?integration_id=' + encodeURIComponent(i)), 'catalog ' + i);
  const finance = session.integrations.find(i => catalogs[i].some(d => d.dataset.id === 'receivables.aging'));
  if (!finance) throw new Error('the demo has no receivables.aging dataset');
  const fin = catalogs[finance].find(d => d.dataset.id === 'receivables.aging');
  const binding = {integration_id: finance, dataset: {id: fin.dataset.id, version: fin.dataset.version}};
  // Every dataset's snapshot and exports: tasks the session did not make, as a real task list has them (scripts/seed_demo_data.mjs "jobs").
  for (const integration of session.integrations)
    for (const d of catalogs[integration]) {
      const spec = {
        schema_version: '1.0',
        integration_id: integration,
        dataset: {id: d.dataset.id, version: d.dataset.version},
        company_ids: ['company_demo'],
        metrics: d.metrics,
        dimensions: d.dimensions,
        filters: [],
        sort: [],
        as_of: dateIn(d.timezone),
        timezone: d.timezone,
        page: {limit: 50},
        ...(d.currency_policy ? {currency_policy: d.currency_policy} : {}),
      };
      const job = await pollJob(must(await api('POST', '/snapshot-jobs', spec, true), 'snapshot job').job_id);
      if (job.state !== 'succeeded') {
        note(`the seeded snapshot of ${d.dataset.id} ended ${job.state}`);
        continue;
      }
      await pollJob(must(await api('POST', '/exports', {snapshot_id: job.snapshot_id}, true), 'export').job_id);
      if (caps.formats.includes('pdf'))
        await pollJob(
          must(await api('POST', '/exports', {snapshot_id: job.snapshot_id, format: 'pdf'}, true), 'pdf export').job_id
        );
    }
  // The identity's own 1.x templates with catalogue metadata: four domains, collections, favourites, an archived one, published ones.
  const element = (text, id = 'n1', y = 25, size = 18) => ({
    id,
    source: 'literal',
    field: '',
    text,
    x: 10,
    y,
    width: 190,
    height: 12,
    font_size: size,
    align: 'left',
  });
  const templates = [
    ['客户往来对账单', '通用业务', '财务', {favourite: true, collections: ['客户A']}, true],
    ['销售订单', '通用业务', '销售', {}, false],
    ['出库装箱单', '仓储 WMS', '出库', {collections: ['上海仓']}, true],
    ['波次拣货单', '仓储 WMS', '出库', {}, false],
    ['海运订舱委托 · Ocean Booking', '运输 / 货代', '海运', {}, true],
    ['商业发票 · Commercial Invoice', '国际贸易', '贸易单证', {collections: ['客户A']}, true],
    ['航空运单 · Air Waybill (MAWB)', '运输 / 货代', '航空运输', {favourite: true}, true],
    ['旧版入库单（已归档）', '仓储 WMS', '入库', {archived: true}, false],
  ];
  for (const [name, domain, flow, extra, publish] of templates) {
    const full = `${PREFIX} · ${name}`;
    const config = {schema_version: '1.0', kind: 'document', name: full, binding, elements: [element(name)]};
    const t = must(await api('POST', '/document-templates', {id: '', expected_version: 0, config}), 'template ' + full);
    must(
      await api('PUT', `/document-templates/${t.id}/catalog`, {
        domain,
        flow,
        favourite: !!extra.favourite,
        archived: !!extra.archived,
        collections: extra.collections || [],
        expected_revision: 0,
      }),
      'catalogue ' + full
    );
    if (publish)
      must(
        await api('POST', `/document-templates/${t.id}/release`, {action: 'publish', version: 1, expected_revision: 0}),
        'publish ' + full
      );
    if (name === '客户往来对账单' || name === '销售订单')
      must(
        await api('POST', '/document-templates', {
          id: t.id,
          expected_version: 1,
          config: {...config, elements: [element(name), element('第 2 版新增的一行', 'n2', 45, 12)]},
        }),
        'version 2 of ' + full
      );
  }
  return {pdf: caps.formats.includes('pdf')};
}

const DIALOG = '#page-library .overlay.open';
const studioDialog = id => `document.querySelector('#${id}.open')`;
async function main() {
  const list = await (await fetch(`http://127.0.0.1:${cdp}/json/list`)).json();
  const target = list.find(t => t.type === 'page');
  if (!target) throw new Error('no page target on the debugging port');
  page = connect(target.webSocketDebuggerUrl);
  await page.ready;
  await send('Page.enable');
  await send('Runtime.enable');
  await send('Page.bringToFront');
  await send('Emulation.setFocusEmulationEnabled', {enabled: true});
  await send('Browser.setDownloadBehavior', {behavior: 'deny'}).catch(() => {});
  await viewport(...DESKTOP);

  // ---- 01 the sign-in card; the seeding happens before the sign-in, so the rail's task count and the centre's listing are the server's ----
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&!!document.getElementById('access-token')`, 'the console');
  await sleep(500);
  await shot('01-signin');
  const caps = await seed();
  if (!caps.pdf)
    note('the demo has no PDF renderer: the PDF preview, the check and the issue show the console without one');

  // ---- 02 the workbench after the sign-in; 03 the data drawer with the fields; 04/05 the dashboard's start card and its drawer; 06 ------
  await typeInto('#access-token', token);
  await clickBtn('.connection-card', '连接工作区');
  await waitFor(
    `document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`,
    'signed in',
    30000
  );
  await idle();
  await sleep(400);
  await shot('02-home');
  await click('#sh-data-chip');
  await waitFor(
    `!document.getElementById('sh-d-data').hidden&&!document.getElementById('sh-fields-list').hidden`,
    'the data drawer with the fields'
  );
  await shot('03-data-fields');
  await key('Escape');
  await sleep(250);
  await nav('dashboard');
  await waitFor(`!document.getElementById('bd-start').hidden`, 'the start card');
  await shot('04-dashboard-start');
  await click('#bd-start-open');
  await waitFor(`!document.getElementById('sh-d-data').hidden`, 'the data drawer from the start card');
  await shot('05-data-drawer');
  await key('Escape');
  await sleep(250);
  await nav('workspace');
  await shot('06-workbench-empty');

  // ---- 10 a company the source refuses; 11 the second product connection; 07 the preview; 08 the snapshot ---------------------------------
  await typeInto('#query-companies', 'company_other');
  await click('#preview');
  await noticeHas('无权');
  await idle();
  await top();
  await shot('10-refused-company');
  const finance = await evalJs(`document.getElementById('query-product').value`);
  const shipping = (await evalJs(`[...document.getElementById('query-product').options].map(o=>o.value)`)).find(
    p => p !== finance
  );
  if (shipping) {
    await setSelect('#query-product', shipping);
    await waitFor(
      `document.getElementById('query-dataset').options.length>0&&!document.getElementById('preview').disabled`,
      'the second product connection'
    );
    const shipments = await evalJs(
      `[...document.getElementById('query-dataset').options].map(o=>o.value).find(v=>v.startsWith('shipments.'))`
    );
    if (shipments) await setSelect('#query-dataset', shipments);
    await typeInto('#query-companies', 'company_demo');
    await click('#preview');
    await waitFor(`!document.getElementById('result-table-wrap').hidden`, 'the shipments preview', 30000);
    await idle();
    await top();
    await shot('11-shipment-preview');
    await setSelect('#query-product', finance);
    await waitFor(
      `document.getElementById('query-dataset').options.length>0&&!document.getElementById('preview').disabled`,
      'the first product connection again'
    );
    await setSelect('#query-dataset', 'receivables.aging@1.0.0');
  } else note('the demo has one product connection: 11-shipment-preview shows the first one');
  await typeInto('#query-companies', 'company_demo');
  await click('#preview');
  await waitFor(
    `!document.getElementById('result-table-wrap').hidden&&document.getElementById('result-kind').textContent==='查询预览'`,
    'the preview',
    30000
  );
  await idle();
  await top();
  if (!shipping) await shot('11-shipment-preview');
  await shot('07-workbench-preview');
  await click('#snapshot');
  await waitFor(`document.getElementById('result-kind').textContent==='冻结快照'`, 'the snapshot', 60000);
  await idle();
  // The caption names the pager (每页 2 / 25 / 50 行), which the larger type of S09-A puts just below the fold at 900 px: the page is shown
  // from the pager up; the title with its 冻结快照 badge and the export buttons stay in view.
  await evalJs(`document.getElementById('result-pager').scrollIntoView({block:'end'});window.scrollBy(0,24)`);
  await sleep(250);
  await shot('08-workbench-snapshot');
  await top();

  // ---- 09 the layout designer: 发布 with nothing saved offers 保存并发布 (S07-G); the versions dialog then lists v1 as published ---------------
  await click('#report-designer > summary');
  await sleep(250);
  await clickBtn('#report-designer .designer-bar', '发布');
  await waitFor(`!document.getElementById('ly-d-publish').hidden`, 'the layout publish confirmation');
  await clickBtn('#ly-d-publish', '保存并发布');
  await waitFor(`document.getElementById('layout-state').textContent==='已发布 v1'`, 'the layout published', 30000);
  await idle();
  await clickBtn('#report-designer .designer-bar', '历史');
  await waitFor(`document.getElementById('ly-version-list').children.length>=1`, 'the layout versions');
  await idle();
  await shot('09-layout-designer');
  await key('Escape');
  await sleep(200);
  await click('#report-designer > summary');
  await top();

  // ---- 12–15 the dashboard studio: three widgets, the structure panel, 保存并发布, the versions --------------------------------------------
  await nav('dashboard');
  await waitFor(`!document.getElementById('bd-desk').hidden`, 'the desk');
  await clickBtn('#bd-sidebar', '添加指标卡');
  await clickBtn('#bd-sidebar', '添加明细表');
  await clickBtn('#bd-sidebar', '添加当前页柱状图');
  await waitFor(`document.getElementById('board-widgets').children.length===3`, 'three widgets');
  // The clicks scrolled the component panel down to the last button: back to its top, as a person sees it first.
  await evalJs(
    `(()=>{let e=document.getElementById('board-add-metric');while(e&&e.scrollHeight<=e.clientHeight)e=e.parentElement;if(e)e.scrollTop=0;})()`
  );
  await shot('12-dashboard-widgets');
  await click('#bd-rail-structure');
  await sleep(250);
  await shot('13-dashboard-structure');
  await click('#bd-rail-components');
  await clickBtn('#bd-studio .topbar', '保存');
  await waitFor(`document.getElementById('bd-state').textContent==='已保存 v1'`, 'the dashboard saved', 30000);
  await idle();
  await clickBtn('#bd-sidebar', '添加指标卡');
  await waitFor(`document.getElementById('bd-state').textContent.includes('有未保存修改')`, 'an unsaved change');
  await clickBtn('#bd-studio .topbar', '发布');
  await waitFor(
    `!document.getElementById('bd-d-publish').hidden&&document.getElementById('bd-publish-confirm').textContent==='保存并发布'`,
    'the confirmation offering 保存并发布',
    30000
  );
  await shot('14-dashboard-save-publish');
  await clickBtn('#bd-d-publish', '保存并发布');
  await waitFor(`document.getElementById('bd-state').textContent==='已发布 v2'`, 'the dashboard published', 30000);
  await idle();
  await clickBtn('#bd-studio .topbar', '历史');
  await waitFor(`document.getElementById('bd-version-list').children.length>=2`, 'the dashboard versions');
  await idle();
  await shot('15-dashboard-versions');
  await key('Escape');
  await sleep(200);

  // ---- 16/17 the tasks panel over the dashboard, and a restore by ID ---------------------------------------------------------------------
  await nav('tasks');
  await waitFor(`document.querySelectorAll('#tasks-body tr').length>=1`, 'the task list');
  await idle();
  await shot('16-tasks-panel');
  const jobId = await evalJs(
    `(()=>{const rows=[...document.querySelectorAll('#tasks-body tr')];const r=rows[rows.length-1]||rows[0];return r?r.cells[1].textContent.trim():'';})()`
  );
  await typeInto('#restore-id', jobId);
  await clickBtn('#sh-d-tasks', '打开任务');
  await noticeHas('已恢复');
  await idle();
  await shot('17-tasks-restored');
  await key('Escape');
  await sleep(250);

  // ---- 18–23 the template centre: the listing, a sample's preview, the wizard, the classification, the card view; 20 a copy with its mapping --
  await nav('library');
  await waitFor(
    `document.querySelectorAll('#page-library .sample-card').length>=37&&document.querySelectorAll('#page-library ul[aria-label="我的模板"] .catalog-card').length>=7`,
    'the centre is listed',
    30000
  );
  await shot('18-library');
  await clickBtn('#page-library .sample-card[aria-label="示例 出库装箱单"]', '预览示例');
  await waitFor(q(DIALOG + ' tbody tr'), 'the sample preview');
  await shot('19-sample-preview');
  await key('Escape');
  await waitFor(`!${q(DIALOG)}`, 'the preview closed');
  await clickBtn('#page-library .library-header', '新建模板');
  await waitFor(q(DIALOG), 'the wizard');
  await shot('21-wizard');
  await key('Escape');
  await waitFor(`!${q(DIALOG)}`, 'the wizard closed');
  await clickBtn('#page-library', `分类 ${PREFIX} · 销售订单`);
  await waitFor(q(DIALOG), 'the classification');
  await shot('22-classify');
  await key('Escape');
  await waitFor(`!${q(DIALOG)}`, 'the classification closed');
  await click('#page-library [aria-label="卡片视图"]');
  await sleep(300);
  await top();
  await shot('23-library-grid');
  await click('#page-library [aria-label="列表视图"]');
  await sleep(200);
  const SAMPLE = 'WMS · 波次拣货单',
    COPY = `${PREFIX} · ${SAMPLE}`;
  await clickBtn(`#page-library .sample-card[aria-label="示例 ${SAMPLE}"]`, '复制示例');
  await waitFor(q(DIALOG + ' tbody tr'), 'the mapping dialog');
  await typeInto(`${DIALOG} input[type=text]`, COPY);
  await shot('20-sample-mapping');
  // The document-level slots (the header's party, number, date) become literals, as a person maps them for this dataset: the snapshot's
  // three rows are three customers, and a header field must agree across the rows of one document, or the PDF is refused.
  await evalJs(
    `[...document.querySelectorAll(${JSON.stringify(DIALOG + ' tbody tr')})].forEach(tr=>{const s=tr.querySelector('select');if(tr.cells[1].textContent==='单据'&&s&&s.value){s.value='';s.dispatchEvent(new Event('change',{bubbles:true}));}})`
  );
  await clickBtn(DIALOG, '复制并进入设计器');
  await waitFor(
    `!document.getElementById('page-documents').hidden&&document.getElementById('st-name').textContent===${JSON.stringify(COPY)}`,
    'the copy opens in the studio',
    40000
  );
  await idle();
  await sleep(600);

  // ---- 24–30 the document studio with the copy: the design tab, 插入, the data panel, the edge-sample check, the versions, the publish
  // confirmation, the PDF preview ---------------------------------------------------------------------------------------------------------
  await shot('24-studio');
  await click('#st-tab-insert');
  await sleep(200);
  await shot('25-studio-insert');
  await click('#st-tab-design');
  await click('#st-rail-data');
  await sleep(250);
  await shot('26-studio-data-panel');
  await click('#st-rail-components');
  await click('#st-tab-check');
  await sleep(150);
  if (await evalJs(`!document.getElementById('doc-check').hidden`)) {
    const before = await evalJs(`document.getElementById('doc-check-status').textContent`);
    await click('#doc-check');
    await waitFor(
      `!document.getElementById('doc-check-wrap').hidden||document.getElementById('doc-check-status').textContent!==${JSON.stringify(before)}`,
      'the edge-sample check',
      40000
    );
    await idle();
  } else
    note('27-studio-check: the demo has no PDF renderer, so 边界样本 is not offered; the figure shows the check tab');
  if (!(await evalJs(`document.getElementById('st-itab-check').classList.contains('active')`)))
    await click('#st-itab-check');
  await sleep(250);
  await shot('27-studio-check');
  await click('#st-versions');
  await waitFor(
    `${studioDialog('st-d-versions')}&&document.getElementById('st-version-list').children.length>=1`,
    'the versions'
  );
  await idle();
  await shot('28-studio-versions');
  await key('Escape');
  await sleep(200);
  await click('#st-publish');
  await waitFor(studioDialog('st-d-publish'), 'the publish confirmation');
  await idle();
  await shot('29-studio-publish');
  await key('Escape');
  await sleep(200);
  await click('#previewButton');
  await waitFor(studioDialog('st-d-preview'), 'the preview dialog');
  if (await evalJs(`!document.getElementById('doc-pdf').hidden`)) {
    await typeInto('#doc-row', '1');
    await click('#doc-pdf');
    await waitFor(
      `document.getElementById('st-pv-pages').textContent.endsWith('页')||document.getElementById('notice').textContent.includes('PDF预览：')`,
      'the PDF preview',
      60000
    );
    await idle();
    if (!(await evalJs(`document.getElementById('st-pv-pages').textContent.endsWith('页')`)))
      note(
        '30-studio-preview: the PDF preview was refused: ' +
          (await evalJs(`document.getElementById('notice').textContent`))
      );
  } else note('30-studio-preview: the demo has no PDF renderer, so 生成PDF预览 is not offered');
  await shot('30-studio-preview');
  await key('Escape');
  await sleep(200);

  // ---- 31/32 the official issue: the published 1.x template (its latest version is the published one), issued for the snapshot's first row ----
  await nav('library');
  await waitFor(
    `${q(`#page-library .catalog-card[aria-label="${ISSUED_TEMPLATE}"]`)}`,
    'the published template is listed'
  );
  await clickBtn(`#page-library .catalog-card[aria-label="${ISSUED_TEMPLATE}"]`, '编辑');
  await waitFor(
    `!document.getElementById('page-documents').hidden&&document.getElementById('st-name').textContent===${JSON.stringify(ISSUED_TEMPLATE)}&&document.getElementById('doc-server-status').textContent.includes('正在编辑模板')`,
    'the published template opens in the studio',
    40000
  );
  await idle();
  // S09-F: there is no 打开已发布模板; the editor holds the published version, and the issue reads the release state itself.
  await click('#st-tab-check');
  await click('#st-tool-issue');
  await waitFor(studioDialog('st-d-issue'), 'the issue dialog');
  await shot('31-studio-issue-dialog');
  if (await evalJs(`!document.getElementById('doc-issue').hidden`)) {
    await click('#doc-issue');
    await waitFor(
      `/下载已发起|出具失败|正式出具：/.test(document.getElementById('notice').textContent)`,
      'the issue finished',
      90000
    );
    await idle();
    const said = await evalJs(`document.getElementById('notice').textContent`);
    if (!/No\. [A-Za-z0-9._-]+/.test(said) || !said.includes('下载已发起'))
      note('32-issued: the issue did not finish with a number: ' + said);
    // The figure shows the finished issue: the record with its number in the dialog's list, beside the notice that names it.
    await click('#doc-issues-refresh');
    await waitFor(
      `document.getElementById('doc-issues-body').querySelectorAll('tr').length>0`,
      'the issue record is listed',
      30000
    );
    await idle();
  } else
    note('32-issued: the demo offers no official issue (no PDF renderer or issuance); the figure shows the dialog');
  await shot('32-issued');
  await key('Escape');
  await sleep(200);

  // ---- 33 the system page; 34/35 the dark scheme; 36–38 the phone width ------------------------------------------------------------------
  await nav('system');
  await shot('33-system');
  await viewport(...DESKTOP, 'dark');
  await nav('workspace');
  await top();
  await shot('34-dark-workbench');
  await nav('dashboard');
  await shot('35-dark-dashboard');
  await viewport(...DESKTOP, 'light');
  await viewport(...PHONE, 'light', true);
  await nav('workspace');
  await top();
  await shot('36-phone-workbench');
  await nav('library');
  await waitFor(`document.querySelectorAll('#page-library .sample-card').length>=37`, 'the centre at 390');
  await top();
  await shot('37-phone-library');
  await nav('documents');
  await sleep(400);
  await shot('38-phone-studio');
  await viewport(...DESKTOP, 'light');

  const missing = Object.keys(FIGURES).filter(n => !taken.has(n));
  if (missing.length) throw new Error('figures not taken: ' + missing.join(', '));
}

let failure = null;
try {
  await main();
} catch (e) {
  failure = e;
  console.error('ABORT', e.stack || e);
  try {
    const file = join(tmpdir(), 'manual-screens-failure.png');
    writeFileSync(file, Buffer.from((await send('Page.captureScreenshot', {format: 'png'})).data, 'base64'));
    console.error('the failure state is in', file);
  } catch {}
}
page?.close();
console.log(
  `${taken.size}/${Object.keys(FIGURES).length} figures written to ${outDir}${failure ? '; aborted: ' + failure.message : ''}`
);
if (notes.length) console.log('notes:\n- ' + notes.join('\n- '));
process.exit(failure ? 1 : 0);
