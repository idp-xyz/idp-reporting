// Real-browser check of the English console (S17-A, docs/S17A_SCOPE.md): the console opened with ?lang=en in a real Chrome over the
// DevTools Protocol, signed in with real input, and the sign-in card, the workbench (preview and snapshot), the dashboard studio with
// three components, the document studio, the template centre, the tasks panel, the audit log and the system page shown at 1440 and
// 390 px. Each is screenshotted and measured: no horizontal overflow of the page or the console header, no visible button or tab
// whose English is wider than the control (cut off), and no visible text of the console's own left in Chinese (the server's data,
// the switch's 中文 and the document on the canvas aside). Last, the 中文 switch brings the Chinese back and removes the stored
// choice, so the browser profile is left as it was found (scripts/ci_browser_evidence.sh runs this after the Chinese checks).
// Not part of `npm test`; CI runs it through scripts/ci_browser_evidence.sh (job "browser"). It exits 1 when a check fails.
// Usage: node scripts/browser_english_s17a.mjs --out DIR --token-file FILE [--base http://127.0.0.1:PORT] [--cdp PORT]
import {readFileSync, writeFileSync, mkdirSync} from 'node:fs';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const {values: args} = parseArgs({
  options: {out: {type: 'string'}, 'token-file': {type: 'string'}, base: {type: 'string'}, cdp: {type: 'string'}},
  strict: true,
});
if (!args.out || !args['token-file']) {
  console.error('usage: --out DIR --token-file FILE [--base http://127.0.0.1:PORT] [--cdp PORT]');
  process.exit(2);
}
const base = args.base || 'http://127.0.0.1:7860',
  cdp = args.cdp || '9347',
  outDir = args.out;
const baseUrl = new URL(base);
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname)) {
  console.error('--base must be an http loopback address');
  process.exit(2);
}
const token = readFileSync(args['token-file'], 'utf8').trim();
const sleep = ms => new Promise(r => setTimeout(r, ms));
mkdirSync(outDir, {recursive: true});
const checks = [],
  evidence = {};
const check = (name, ok, detail) => {
  checks.push({name, ok: !!ok, detail: ok ? undefined : detail});
  console.log(ok ? 'PASS' : 'FAIL', name, ok ? '' : JSON.stringify(detail));
};

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
  const ready = new Promise((res, rej) => {
    ws.onopen = res;
    ws.onerror = () => rej(new Error('websocket failed: ' + url));
  });
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const id = nextId++,
        timer = setTimeout(() => {
          pending.delete(id);
          rej(new Error('CDP timeout: ' + method));
        }, 40000);
      pending.set(id, {
        res: v => (clearTimeout(timer), res(v)),
        rej: e => (clearTimeout(timer), rej(e)),
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
const shot = async name => {
  const data = (await send('Page.captureScreenshot', {format: 'png'})).data;
  writeFileSync(join(outDir, name + '.png'), Buffer.from(data, 'base64'));
  console.log('shot', name);
};
const viewport = async (w, h, mobile = false) => {
  await send('Emulation.setDeviceMetricsOverride', {width: w, height: h, deviceScaleFactor: 1, mobile});
  await sleep(500);
};
const mouse = (type, x, y) =>
  send('Input.dispatchMouseEvent', {
    type,
    x,
    y,
    button: type === 'mouseMoved' ? 'none' : 'left',
    buttons: type === 'mousePressed' ? 1 : 0,
    clickCount: type === 'mouseMoved' ? 0 : 1,
  });
const q = sel => `document.querySelector(${JSON.stringify(sel)})`;
const click = async sel => {
  const at = await evalJs(
    `(()=>{const e=${q(sel)};if(!e)return null;e.scrollIntoView({block:'center',inline:'nearest'});const b=e.getBoundingClientRect();return [b.left+b.width/2,b.top+b.height/2];})()`
  );
  if (!at) throw new Error('nothing to click: ' + sel);
  await mouse('mouseMoved', at[0], at[1]);
  await mouse('mousePressed', at[0], at[1]);
  await mouse('mouseReleased', at[0], at[1]);
  await sleep(100);
};
const typeInto = async (sel, text) => {
  await click(sel);
  await evalJs(`${q(sel)}.select()`);
  await send('Input.insertText', {text});
  await sleep(120);
};
const closeDrawers = () =>
  evalJs(
    `document.querySelectorAll('.sh-overlay').forEach(o=>{if(!o.hidden)o.querySelector('[data-act=close]').click();})`
  );
const nav = async p => {
  await closeDrawers();
  await click(`.app-rail [data-page="${p}"]`);
  await waitFor(`!document.getElementById('page-${p}').hidden`, 'page ' + p);
  await sleep(500);
};

// What a person sees that is wrong in English: the page or the header wider than the window, a visible control whose text is cut
// (its content wider than its box, with no ellipsis by design), and visible text with Chinese once the data is taken out.
const MEASURE = data => `(()=>{
const data=${JSON.stringify(data)};const HAN=/[\\u3400-\\u9fff]/;
const vis=e=>{const b=e.getBoundingClientRect();const cs=getComputedStyle(e);return b.width>0&&b.height>0&&cs.visibility!=='hidden'&&b.bottom>0&&b.top<innerHeight&&b.right>0&&b.left<innerWidth&&!e.closest('[hidden],[inert],.sr-only,[aria-hidden="true"]');};
const d=document.documentElement,h=document.querySelector('.console-header');
const cut=[];for(const b of document.querySelectorAll('button,[role=tab],.nav-item,.pill,.badge')){if(!vis(b)||b.closest('svg'))continue;const cs=getComputedStyle(b);if(cs.textOverflow==='ellipsis'||cs.overflow==='visible'&&cs.whiteSpace!=='nowrap')continue;if(b.scrollWidth>b.clientWidth+2&&b.textContent.trim())cut.push((b.id||b.className||b.tagName)+': '+b.textContent.trim().slice(0,40)+' '+b.scrollWidth+'>'+b.clientWidth);}
const chinese=[];const w=document.createTreeWalker(document.body,NodeFilter.SHOW_TEXT);let n;
while(n=w.nextNode()){const t=n.nodeValue;if(!HAN.test(t))continue;const el=n.parentElement;if(!el||!vis(el)||el.closest('script,style,textarea,[translate="no"],[lang^="zh"]'))continue;let rest=t;for(const x of data)rest=rest.split(x).join('');if(HAN.test(rest)&&!data.some(x=>x.replace(/\\s+/g,'').includes(t.replace(/\\s+/g,''))))chinese.push(t.trim().slice(0,60));}
return {doc:[d.scrollWidth,d.clientWidth],header:h?[h.scrollWidth,h.clientWidth]:null,cut:cut.slice(0,12),chinese:[...new Set(chinese)].slice(0,12)};})()`;
let data = [];
const measure = async name => {
  const r = await evalJs(MEASURE(data));
  evidence[name] = r;
  check(
    `no horizontal overflow (${name})`,
    r.doc[0] <= r.doc[1] + 1 && (!r.header || r.header[0] <= r.header[1] + 1),
    r
  );
  check(`no control's English cut off (${name})`, r.cut.length === 0, r.cut);
  check(`no Chinese of the console visible (${name})`, r.chinese.length === 0, r.chinese);
};

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
  await viewport(1440, 900);
  await send('Page.navigate', {url: base + '/console/?lang=en'});
  await waitFor(
    `document.documentElement.lang==='en'&&document.getElementById('connect-title').textContent==='Enter the reporting workspace'`,
    'the English sign-in'
  );
  await sleep(400);
  check(
    '?lang=en leaves the address and is stored',
    await evalJs(`location.search===''&&localStorage.getItem('reporting-console-lang')==='en'`),
    await evalJs('location.href')
  );
  const title = await evalJs('document.title');
  check('the title is English', /^IDP Reporting · /.test(title) && !/[\u3400-\u9fff]/.test(title), title);
  await shot('en-signin-1440');
  await measure('sign-in 1440');
  await typeInto('#access-token', token);
  await click('#connect-button');
  await waitFor(`document.getElementById('connection-overlay').hidden`, 'signed in');
  await waitFor(`document.getElementById('query-dataset').options.length>0`, 'the catalog');
  // The server's data, as the API gives it to this identity: what may stay Chinese.
  data =
    await evalJs(`(async()=>{const ask=async p=>(await fetch('/api/reporting/v1'+p,{headers:{Authorization:'Bearer '+${JSON.stringify(token)}}})).json();
const found=new Set();const c=v=>{if(typeof v==='string'){if(/[\\u3400-\\u9fff]/.test(v))found.add(v);}else if(v&&typeof v==='object')Object.values(v).forEach(c);};
const s=await ask('/session');for(const id of s.integrations)c(await ask('/catalog?integration_id='+encodeURIComponent(id)));
const samples=await ask('/document-templates/samples');c(samples);for(const x of samples)c(await ask('/document-templates/samples/'+encodeURIComponent(x.kind)));
return [...found].sort((a,b)=>b.length-a.length);})()`);
  await nav('workspace');
  await typeInto('#query-companies', 'company_demo');
  await click('#preview');
  await waitFor(`!document.getElementById('result-table-wrap').hidden`, 'the preview');
  await click('#snapshot');
  await waitFor(`!document.getElementById('export').disabled`, 'the snapshot', 40000);
  await sleep(400);
  await shot('en-workspace-1440');
  await measure('workbench 1440');
  await click('#sh-data-chip');
  await sleep(500);
  await shot('en-data-drawer-1440');
  await measure('data drawer 1440');
  await closeDrawers();
  await nav('dashboard');
  for (const id of ['board-add-metric', 'board-add-table', 'board-add-bar']) {
    await evalJs(`document.getElementById(${JSON.stringify(id)}).click()`);
    await sleep(150);
  }
  await sleep(400);
  await shot('en-dashboard-1440');
  await measure('dashboard studio 1440');
  await nav('documents');
  await sleep(800);
  await shot('en-studio-1440');
  await measure('document studio 1440');
  await nav('library');
  await waitFor(`document.querySelectorAll('#page-library .sample-card').length>10`, 'the template centre', 30000);
  await sleep(400);
  await shot('en-library-1440');
  await measure('template centre 1440');
  await closeDrawers();
  await click('.app-rail [data-panel="tasks"]');
  await waitFor(`!document.getElementById('sh-d-tasks').hidden`, 'the tasks panel');
  await sleep(400);
  await shot('en-tasks-1440');
  await measure('tasks panel 1440');
  for (const p of ['audit', 'system']) {
    await nav(p);
    await shot('en-' + p + '-1440');
    await measure(p + ' 1440');
  }
  await viewport(390, 844, true);
  for (const p of ['workspace', 'dashboard', 'documents', 'library', 'system']) {
    await nav(p);
    await shot('en-' + p + '-390');
    await measure(p + ' 390');
  }
  // Back to Chinese by the switch: the page is Chinese again and the stored choice is gone.
  await viewport(1440, 900);
  await click('.console-header .lang-switch [data-lang="zh"]');
  await sleep(300);
  const back = await evalJs(
    `({lang:document.documentElement.lang,title:document.title,stored:localStorage.getItem('reporting-console-lang'),label:document.querySelector('[data-page="workspace"]').getAttribute('aria-label')})`
  );
  check(
    '中文 brings the Chinese back and removes the stored choice',
    back.lang === 'zh-CN' && back.stored === null && back.label === '报表工作台',
    back
  );
  await shot('zh-after-switch-1440');
}

let failed = false;
try {
  await main();
} catch (e) {
  failed = true;
  console.error('ABORT', e.message);
  checks.push({name: 'run', ok: false, detail: e.message});
  // Whatever happened, the profile keeps no English choice for the next run.
  await evalJs(`localStorage.removeItem('reporting-console-lang')`).catch(() => {});
}
writeFileSync(join(outDir, 'report.json'), JSON.stringify({checks, evidence}, null, 2));
const bad = checks.filter(c => !c.ok).length;
console.log(`${checks.length - bad}/${checks.length} checks passed`);
if (page) page.close();
process.exit(failed || bad ? 1 : 0);
