// Real-browser acceptance for the S06-C console shell and dashboard studio (ADR 0008 §4, docs/S06C_SCOPE.md, docs/STUDIO.md "控制台外壳" and "看板工作室"):
// real mouse and keyboard input over the Chrome DevTools Protocol. The console's pages are shown at 1440 px beside the prototype's two non-studio
// screens (IDP_Reporting_Designer_UIUX_V3.0.html: the 模板中心 view, whose header and left column are the shell's specification, and the 数据中心 view),
// alone at 1280, 768 and 390 px in the light and the dark scheme (no horizontal overflow, the contrast of every visible text measured), and a session
// follows: sign in (the workbench is the home since S07-H), the data drawer with the dataset's fields, the workbench (preview, snapshot), the dashboard studio (three widgets, then S21-A's line and pie
// with a title and a full width, the structure panel, save, the versions tab, the publish confirmation, the help), the tasks page, the system page, sign out.
// Visual sameness is judged by a person from the screenshots (docs/STUDIO.md says so); this script measures what it can and records the rest.
// Not part of `npm test`; CI runs it through scripts/ci_browser_evidence.sh (job "browser"). By hand it needs a running reporting-demo started with a dashboard directory, and a Chrome started with
//   --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server
// It exits 1 when any check fails or the run aborts, and 2 on bad arguments; report.json is written either way.
// Safety: --base must be loopback because the bearer token is typed into the page. The prototype is served from a loopback port of its own (--proto-port) for the
// duration of the run. The script reads and writes only the page it drives and its own files in --out (PNGs and report.json); it removes nothing else.
// Usage: node scripts/browser_console_s06c.mjs --out DIR --token-file FILE [--proto FILE] [--base http://127.0.0.1:PORT] [--cdp PORT] [--proto-port PORT]
import {readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync} from 'node:fs';
import {createServer} from 'node:http';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const usage = why => {
  console.error(
    why +
      '\nusage: --out DIR --token-file FILE [--proto FILE] [--base http://127.0.0.1:PORT] [--cdp PORT] [--proto-port PORT]'
  );
  process.exit(2);
};
let args;
try {
  ({values: args} = parseArgs({
    options: {
      out: {type: 'string'},
      'token-file': {type: 'string'},
      proto: {type: 'string'},
      base: {type: 'string'},
      cdp: {type: 'string'},
      'proto-port': {type: 'string'},
    },
    strict: true,
  }));
} catch (e) {
  usage(e.message);
}
if (!args.out || !args['token-file']) usage('--out and --token-file are required');
const base = args.base || 'http://127.0.0.1:7850',
  cdp = args.cdp || '9347',
  protoPort = Number(args['proto-port'] || 7851),
  outDir = args.out;
let baseUrl;
try {
  baseUrl = new URL(base);
} catch {
  usage('--base is not a valid URL');
}
if (baseUrl.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(baseUrl.hostname))
  usage('--base must be an http loopback address');
const token = readFileSync(args['token-file'], 'utf8').trim(),
  proto = args.proto ? readFileSync(args.proto) : null; // without --proto (CI: the prototype is not in the repository) the prototype's screens and the side-by-side pairs are skipped
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
const waitFor = async (expr, label, limit = 15000) => {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    if (await evalJs(`Boolean(${expr})`).catch(() => false)) return;
    await sleep(80);
  }
  throw new Error('timeout waiting for: ' + label);
};
const OWN_FILE = /^(proto|console|pair|session|zz-failure)-[\w-]+\.png$|^report\.json$/;
for (const f of readdirSync(outDir)) if (OWN_FILE.test(f)) rmSync(join(outDir, f));
const shots = new Map();
const shot = async name => {
  const data = (await send('Page.captureScreenshot', {format: 'png'})).data;
  shots.set(name, data);
  writeFileSync(join(outDir, name + '.png'), Buffer.from(data, 'base64'));
  console.log('shot', name);
};
const viewport = async (w, h, scheme = 'light', mobile = false) => {
  await send('Emulation.setDeviceMetricsOverride', {width: w, height: h, deviceScaleFactor: 1, mobile});
  await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: scheme}]});
  await sleep(500);
};

// ---- real input -------------------------------------------------------------------------------------------------------------------
const mouse = (type, x, y) =>
  send('Input.dispatchMouseEvent', {
    type,
    x,
    y,
    button: type === 'mouseMoved' ? 'none' : 'left',
    buttons: type === 'mouseReleased' || type === 'mouseMoved' ? 0 : 1,
    clickCount: type === 'mouseMoved' ? 0 : 1,
  });
const KEYS = {
  Escape: [27, 'Escape'],
  Enter: [13, 'Enter'],
  Tab: [9, 'Tab'],
  ArrowDown: [40, 'ArrowDown'],
  ArrowRight: [39, 'ArrowRight'],
};
const key = async (name, shift = false) => {
  const [vk, code] = KEYS[name],
    modifiers = shift ? 8 : 0;
  await send('Input.dispatchKeyEvent', {
    type: 'rawKeyDown',
    key: name,
    code,
    windowsVirtualKeyCode: vk,
    nativeVirtualKeyCode: vk,
    modifiers,
  });
  await send('Input.dispatchKeyEvent', {
    type: 'keyUp',
    key: name,
    code,
    windowsVirtualKeyCode: vk,
    nativeVirtualKeyCode: vk,
    modifiers,
  });
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
  await sleep(80);
};
const q = sel => `document.querySelector(${JSON.stringify(sel)})`;
const click = sel => clickExpr(q(sel), sel);
const btnIn = (scope, text) =>
  `[...document.querySelectorAll(${JSON.stringify(scope + ' button')})].find(b=>(b.getAttribute('aria-label')||b.textContent).trim().startsWith(${JSON.stringify(text)}))`;
const clickBtn = (scope, text) => clickExpr(btnIn(scope, text), scope + ' ' + text);
const typeInto = async (sel, text) => {
  await click(sel);
  await evalJs(`${q(sel)}.select()`);
  await send('Input.insertText', {text});
  await sleep(120);
};
const idle = () => waitFor(`!document.getElementById('preview').disabled`, 'the console is idle', 40000);
// S09-D (ADR 0015 decision 4): the company scope is the source's directory as a picker when the product lists the companies (the demo
// lists 演示公司, chosen already as the one company), the text field of ids otherwise: whichever the page shows is driven.
const chooseCompany = async () => {
  const state = await evalJs(
    `(()=>{const p=document.getElementById('query-companies-picker');if(!p||p.hidden)return 'text';const boxes=[...p.querySelectorAll('input[type=checkbox]')];return boxes.some(b=>b.checked)?'chosen':'listed';})()`
  );
  if (state === 'chosen') return;
  if (state === 'listed')
    await clickExpr(
      `[...document.querySelectorAll('#query-companies-picker .company-option label')].find(l=>l.textContent.includes('company_demo'))`,
      '演示公司'
    );
  else await typeInto('#query-companies', 'company_demo');
};
// The picker as the page shows it: the one company by name with its id as secondary text, chosen, no typed line and no filter.
const PICKER = `(()=>{const p=document.getElementById('query-companies-picker');if(!p||p.hidden)return null;const o=p.querySelector('.company-option'),b=p.getBoundingClientRect();return {name:o&&o.querySelector('.company-name').textContent,id:o&&o.querySelector('.company-id').textContent,chosen:!!o&&o.querySelector('input').checked,typedHidden:document.getElementById('query-companies').hidden,filterHidden:document.getElementById('query-companies-filter').hidden,count:p.querySelectorAll('.company-option').length,left:b.left,right:b.right,width:b.width,inner:innerWidth};})()`;
// S20-B (ADR 0023 decision 5): the query panel's filters and sort (filters.js). What 添加筛选 / 添加排序 offer for a field, a word of a row
// (an operator, a value of a closed list, a direction: its label is clicked), a row's 移除, and the blocks as the page shows them.
const offerOf = (line, field) =>
  `[...document.querySelectorAll('#${line} button')].find(b=>b.dataset.field===${JSON.stringify(field)})`;
const rowOf = (list, field) =>
  `[...document.querySelectorAll('#${list} .narrow-row')].find(r=>r.dataset.field===${JSON.stringify(field)})`;
const wordOf = (list, field, value) =>
  `${rowOf(list, field)}?.querySelector('input[value=${JSON.stringify(value)}]')?.closest('label')`;
const removeOf = (list, field) =>
  `[...(${rowOf(list, field)}?.querySelectorAll('.narrow-tools button')||[])].find(b=>b.getAttribute('aria-label').startsWith('移除'))`;
const NARROW = `(()=>{const b=document.getElementById('query-narrow');if(!b||b.hidden)return null;const box=e=>{const r=e.getBoundingClientRect();return {left:Math.round(r.left),right:Math.round(r.right),width:Math.round(r.width)};};const rows=id=>[...document.querySelectorAll('#'+id+' .narrow-row')].map(r=>({field:r.dataset.field,name:r.querySelector('.narrow-name').textContent,id:r.querySelector('.narrow-id').textContent,words:[...r.querySelectorAll('.narrow-ops label')].map(l=>l.textContent+(l.querySelector('input').checked?'*':'')),values:[...r.querySelectorAll('.narrow-value input')].filter(i=>i.type==='text'||i.checked).map(i=>i.value),...box(r)}));const body=b.closest('.dialog-body');return {filters:rows('query-filter-rows'),sorts:rows('query-sort-rows'),offers:[...b.querySelectorAll('.narrow-offers button')].map(x=>x.textContent),hint:document.getElementById('query-hint').hidden?'':document.getElementById('query-hint').textContent,previewDisabled:document.getElementById('preview').disabled,snapshotDisabled:document.getElementById('snapshot').disabled,previewTitle:document.getElementById('preview').title,scrolls:body?body.scrollWidth>body.clientWidth+1:false,inner:innerWidth,...box(b)};})()`;
// A studio or the centre hides the console's navigation: the way out is their own wordmark, as a person would take it.
// S07-A: the application rail is on every shell, so a page is one click away from anywhere.
const nav = async page => {
  if (page === 'tasks' && (await evalJs(`!!document.querySelector('[data-panel="tasks"]')`))) {
    await evalJs(
      `document.querySelectorAll('.sh-overlay').forEach(o=>{if(!o.hidden)o.querySelector('[data-act=close]').click();})`
    );
    await click('.app-rail [data-panel="tasks"]');
    await waitFor(`!document.getElementById('sh-d-tasks').hidden`, 'the tasks panel');
    await sleep(250);
    return;
  }
  await evalJs(
    `document.querySelectorAll('.sh-overlay').forEach(o=>{if(!o.hidden)o.querySelector('[data-act=close]').click();})`
  );
  await click(`.app-rail [data-page="${page}"]`);
  await waitFor(`!document.getElementById('page-${page}').hidden`, 'page ' + page);
  await sleep(250);
};
// The document, and in the dashboard studio each panel's own content (a closed drawer sits off-screen by a transform inside the studio's overflow:hidden box; that is not overflow a person sees).
const overflow = () =>
  evalJs(
    `(()=>{const d=document.documentElement,on=document.body.classList.contains('board-on');const panels=on?['bd-desk','bd-sidebar','bd-inspector','bd-studio'].map(id=>{const e=document.getElementById(id);const inner=id==='bd-studio'?[e.querySelector('.topbar'),e.querySelector('.statusbar')]:[e];return [id,...inner.map(x=>[x.scrollWidth,x.clientWidth])];}):[];const bad=panels.some(p=>p.slice(1).some(([s,c])=>s>c+1));return {doc:[d.scrollWidth,d.clientWidth],panels,over:d.scrollWidth>d.clientWidth+1||bad};})()`
  );
const api = (method, path, body) =>
  evalJs(
    `(async()=>{const r=await fetch('/api/reporting/v1'+${JSON.stringify(path)},{method:${JSON.stringify(method)},headers:{Authorization:'Bearer '+${JSON.stringify(token)},'Content-Type':'application/json'},body:${body === undefined ? 'undefined' : JSON.stringify(JSON.stringify(body))}});const t=await r.text();return {status:r.status,body:t?JSON.parse(t):null};})()`
  );

// The contrast of every visible text of the page against the colour behind it (WCAG ratio; the colour behind is composited from the ancestors' fills).
// Decorative glyphs (aria-hidden), screen-reader-only text, hidden sections, closed dialogs and disabled controls are left out.
const AUDIT = `(()=>{
const parse=c=>{const m=c.match(/rgba?\\(([^)]+)\\)/);if(!m)return null;const p=m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number);return {r:p[0],g:p[1],b:p[2],a:p.length>3?p[3]:1};};
const lum=c=>{const f=v=>{v/=255;return v<=.03928?v/12.92:Math.pow((v+.055)/1.055,2.4);};return .2126*f(c.r)+.7152*f(c.g)+.0722*f(c.b);};
const over=(t,b)=>({r:t.r*t.a+b.r*(1-t.a),g:t.g*t.a+b.g*(1-t.a),b:t.b*t.a+b.b*(1-t.a),a:1});
const bgOf=el=>{const layers=[];for(let e=el;e;e=e.parentElement){const c=parse(getComputedStyle(e).backgroundColor);if(c&&c.a>0){layers.push(c);if(c.a>=1)break;}}let out=layers.length&&layers[layers.length-1].a>=1?layers.pop():parse(getComputedStyle(document.documentElement).backgroundColor)||{r:255,g:255,b:255,a:1};for(let i=layers.length-1;i>=0;i--)out=over(layers[i],out);return out;};
const out=[],root=document.body,walker=document.createTreeWalker(root,NodeFilter.SHOW_TEXT),seen=new Set();let n;
const controls=root.querySelectorAll('input,select,textarea');
const visible=el=>{const cs=getComputedStyle(el);if(cs.visibility==='hidden'||cs.display==='none')return false;const b=el.getBoundingClientRect();return b.width>0&&b.height>0&&b.right>0&&b.bottom>0&&b.left<innerWidth&&b.top<innerHeight*4;};
const audit=(el,t)=>{if(!visible(el))return;const cs=getComputedStyle(el);const fg=parse(cs.color),bg=bgOf(el),f2=over({...fg,a:fg.a*(parseFloat(cs.opacity)||1)},bg),L1=lum(f2),L2=lum(bg),r=(Math.max(L1,L2)+.05)/(Math.min(L1,L2)+.05);out.push({text:String(t).slice(0,28),ratio:Math.round(r*100)/100,size:cs.fontSize,disabled:!!el.closest('button:disabled,[aria-disabled="true"],:disabled'),id:el.id||String(el.className).slice(0,24)});};
while(n=walker.nextNode()){const t=n.nodeValue.trim();if(!t)continue;const el=n.parentElement;if(seen.has(el))continue;seen.add(el);
 if(el.closest('svg')||el.closest('.sr-only')||el.closest('[hidden]')||el.closest('[aria-hidden="true"]')||el.closest('.empty-icon')||el.closest('noscript')||el.closest('.overlay:not(.open)')&&!el.closest('#page-dashboard')||el.closest('.connection-overlay')&&document.getElementById('connection-overlay').hidden)continue;
 if(el.closest('[inert]'))continue;
 audit(el,t);}
for(const c of controls){if(c.closest('[hidden]')||c.closest('[inert]'))continue;if(c.tagName==='SELECT'&&c.selectedOptions[0])audit(c,c.selectedOptions[0].text);else if(c.value)audit(c,c.value);}
const bad=out.filter(o=>o.ratio<4.5&&!o.disabled);return {total:out.length,failing:bad.length,worst:bad.sort((a,b)=>a.ratio-b.ratio).slice(0,12)};})()`;
const contrast = async (name, least = 10) => {
  const r = await evalJs(AUDIT);
  evidence['contrast ' + name] = r;
  check(`contrast of every visible text ≥ 4.5:1 (${name})`, r.failing === 0 && r.total > least, r);
};

// ADR 0014 decision 10, held like the task baseline (these may go down, never up): the smallest visible interface text is 11px
// (captions and help lines), and only letter-spaced uppercase eyebrows, badges, the rail's and tool rails' labels and keyboard hints
// may be 10px; nothing is smaller. The canvas drawing (the paper and its rulers) is not interface text. And the main content of a shell begins within the top quarter of a 900px
// viewport: 225px, measured as the top of the workbench's query panel, the document studio's canvas area and the dashboard's desk.
const SMALL_ALLOWED =
  '.eyebrow,.library-eyebrow,.wordmark small,.pill,.badge,.state-badge,.nav-count,.nav-item small,.nav-item b,.rail-help,.toolrail small,kbd,.ribbon-tip,.canvas-hint,.environment';
const LEGIBILITY = `(()=>{const vis=e=>{const b=e.getBoundingClientRect();const cs=getComputedStyle(e);return b.width>0&&b.height>0&&cs.visibility!=='hidden'&&cs.opacity!=='0'&&!e.closest('[hidden]')&&!e.closest('[inert]')&&!e.closest('svg')&&!e.closest('.cv-panel,.cv-scroll,#st-ruler,.ruler');};
let min=99,minText='',minSmall=99,minSmallText='';const sizes={};
for(const e of document.querySelectorAll('body *')){if(!vis(e))continue;if(![...e.childNodes].some(n=>n.nodeType===3&&n.nodeValue.trim()))continue;const s=parseFloat(getComputedStyle(e).fontSize);if(!s)continue;sizes[s]=(sizes[s]||0)+1;
 if(e.closest(${JSON.stringify(SMALL_ALLOWED)})){if(s<minSmall){minSmall=s;minSmallText=e.textContent.trim().slice(0,30);}}else if(s<min){min=s;minText=e.textContent.trim().slice(0,30)+' <'+(e.id||e.className)+'>';}}
const top=sel=>{const e=document.querySelector(sel);return e?Math.round(e.getBoundingClientRect().top):null;};
return {min,minText,minSmall,minSmallText,sizes,starts:{workbench:top('#page-workspace:not([hidden]) .query-panel'),studio:top('#page-documents:not([hidden]) .ruler'),dashboard:top('#page-dashboard:not([hidden]) #bd-desk:not([hidden]), #page-dashboard:not([hidden]) #bd-start:not([hidden])')}};})()`;
const legibility = async (name, shell) => {
  const r = await evalJs(LEGIBILITY);
  evidence['legibility ' + name] = r;
  check(
    `legibility (${name}): the smallest text ≥ 11px outside eyebrows, badges, rail labels and hints, and ≥ 10px anywhere`,
    r.min >= 11 && r.minSmall >= 10,
    r
  );
  if (shell)
    check(
      `content begins within the top quarter at 1440×900 (${name}): ≤ 225px`,
      r.starts[shell] !== null && r.starts[shell] <= 225,
      r.starts
    );
};

// A composite of two screenshots side by side (the prototype left, the console right), drawn on the page's own canvas.
async function pair(name, left, right) {
  if (!proto) return;
  const png = await evalJs(
    `(async()=>{const load=d=>new Promise((res,rej)=>{const i=new Image();i.onload=()=>res(i);i.onerror=rej;i.src='data:image/png;base64,'+d;});const a=await load(${JSON.stringify(shots.get(left))}),b=await load(${JSON.stringify(shots.get(right))});const c=document.createElement('canvas');c.width=a.width+b.width+20;c.height=Math.max(a.height,b.height);const x=c.getContext('2d');x.fillStyle='#888';x.fillRect(0,0,c.width,c.height);x.drawImage(a,0,0);x.drawImage(b,a.width+20,0);return c.toDataURL('image/png').split(',')[1];})()`
  );
  writeFileSync(join(outDir, name + '.png'), Buffer.from(png, 'base64'));
  console.log('shot', name);
}

const PAGES = ['workspace', 'dashboard', 'issue', 'tasks', 'system'];
let protoServer;
async function main() {
  if (proto) {
    protoServer = createServer((req, res) => {
      res.writeHead(200, {'content-type': 'text/html; charset=utf-8'});
      res.end(proto);
    });
    await new Promise((res, rej) => {
      protoServer.once('error', rej);
      protoServer.listen(protoPort, '127.0.0.1', res);
    });
  }
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
  await viewport(1440, 900);

  // ---- the prototype's two non-studio screens: the library (the shell's specification) and the data centre --------------------------
  let protoGeo = null;
  if (proto) {
    await send('Page.navigate', {url: `http://127.0.0.1:${protoPort}/`});
    await waitFor(`document.readyState==='complete'&&!!document.querySelector('.ribbon-tabs')`, 'the prototype');
    await sleep(900);
    await evalJs(`document.querySelector('.toolrail [data-act="library"]').click()`);
    await sleep(900);
    await shot('proto-library');
    protoGeo = await evalJs(
      `(()=>{const r=s=>{const e=document.querySelector(s);if(!e)return null;const b=e.getBoundingClientRect();return {x:b.left,y:b.top,w:b.width,h:b.height};};return {header:r('.library-header'),side:r('.catalog-sidebar'),top:r('.catalog-top')};})()`
    );
    evidence.prototypeGeometry = protoGeo;
    await evalJs(
      `(()=>{const b=[...document.querySelectorAll('.library-header button')].find(b=>b.textContent.trim().startsWith('数据中心'));if(b)b.click();})()`
    );
    await sleep(900);
    const dcShown = await evalJs(`(()=>{const v=document.getElementById('dataCenterView');return !!v&&!v.hidden;})()`);
    check('the prototype opens its 数据中心 view from the library header', dcShown, dcShown);
    await shot('proto-datacenter');
    evidence.prototypeDataCentre = await evalJs(
      `(()=>{const r=s=>{const e=document.querySelector(s);if(!e)return null;const b=e.getBoundingClientRect();return {x:b.left,y:b.top,w:b.width,h:b.height};};return {sidebar:r('#dataCenterView .dc-sidebar'),main:r('#dataCenterView .dc-main'),head:r('#dataCenterView .dc-page-head')};})()`
    );
  } else evidence.prototype = 'skipped: no --proto';

  // ---- the console: the sign-in card --------------------------------------------------------------------------------------------------
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&!!document.getElementById('access-token')`, 'the console');
  await sleep(400);
  await evalJs(`window.addEventListener('error',e=>{(window.__errors||(window.__errors=[])).push(e.message);})`);
  check(
    'the sign-in card: the wordmark, 进入报表工作区, a password field, 连接工作区; the shell behind is inert',
    await evalJs(
      `(()=>{const c=document.querySelector('.connection-card');return c.querySelector('h1').textContent==='进入报表工作区'&&/IDP REPORTING\\s*DOCUMENT PLATFORM/.test(c.querySelector('.wordmark').textContent.replace(/\\s+/g,' '))&&document.getElementById('access-token').type==='password'&&document.getElementById('connect-button').textContent.trim()==='连接工作区'&&document.querySelector('.shell').inert===true;})()`
    )
  );
  await shot('console-00-signin');
  await contrast('sign-in 1440 light', 3);
  await typeInto('#access-token', token);
  await clickBtn('.connection-card', '连接工作区');
  await waitFor(
    `document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`,
    'signed in',
    30000
  );
  await sleep(400);

  // ---- the shell at 1440, beside the prototype's library and data centre ----------------------------------------------------------------
  const geo = await evalJs(
    `(()=>{const r=s=>{const b=document.querySelector(s).getBoundingClientRect();return {x:b.left,y:b.top,w:b.width,h:b.height};};return {header:r('.console-header'),rail:r('.app-rail'),heading:r('#page-workspace .page-heading'),wordmark:r('.console-header .wordmark'),logout:r('#logout'),chip:r('#sh-data-chip')};})()`
  );
  evidence.geometry = geo;
  check(
    "geometry at 1440: header 56 px (S09-B, ADR 0014 decision 2; it was the prototype's 68), the application rail 56 px, content from x = 90 (the prototype's library header and padding beside the rail; S07-A)",
    geo.header.h === 56 &&
      geo.rail.w === 56 &&
      geo.rail.x === 0 &&
      Math.abs(geo.heading.x - 90) <= 1 &&
      geo.chip.h < 40,
    {geo, protoGeo}
  );
  check(
    "the navigation: the rail's named buttons (S07-H; 出具 since S09-G, an issuer's; S18 adds 定时报表 and the administrator's 管理, hidden without the role), the workbench active after the sign-in, the task count",
    await evalJs(
      `(()=>{const n=document.querySelector('nav.app-rail[aria-label="主导航"]');const rows=[...n.querySelectorAll('.nav-item')].map(b=>b.getAttribute('aria-label'));return JSON.stringify(rows)===JSON.stringify(['报表工作台','看板工作室','模板中心','文档工作室','正式出具','任务工作区','定时报表','操作记录','管理：数据源与绑定','能力与环境'])&&n.querySelector('[aria-current="page"]').dataset.page==='workspace'&&!document.getElementById('page-workspace').hidden&&/^[0-9]+$/.test(document.getElementById('task-count').textContent);})()`
    )
  );
  // S07-C: the task count is the server's listing of the identity's jobs (at most 50), not this session's; it rises by one with the snapshot below.
  const tasksBefore = Number(await evalJs(`document.getElementById('task-count').textContent`));
  check(
    'the header: wordmark label 报表工作台 (S09-C), the environment tag, the identity, 退出工作区; the steps of a first sign-in in the empty workbench',
    await evalJs(
      `document.getElementById('shell-label').textContent==='报表工作台'&&!document.getElementById('start-steps').hidden&&document.querySelector('.console-header .environment').textContent==='合同模拟器'&&document.getElementById('actor-label').textContent.length>0&&!document.getElementById('logout').disabled`
    )
  );
  await shot('console-01-home');
  await legibility('workbench 1440', 'workbench');
  await pair('pair-shell', 'proto-library', 'console-01-home');
  check('no horizontal overflow at 1440 (light)', !(await overflow()).over, await overflow());
  await contrast('home 1440 light');
  // S07-H: the data centre is the data drawer's field list, for the dataset chosen in it.
  await click('#sh-data-chip');
  await waitFor(
    `!document.getElementById('sh-d-data').hidden&&!document.getElementById('sh-fields-list').hidden`,
    'the data drawer with the fields'
  );
  await sleep(250);
  check(
    "the data drawer lists the chosen dataset's columns, types and currencies, and 刷新目录",
    await evalJs(
      `(()=>{const l=document.getElementById('sh-fields-list');return l.querySelectorAll('.column-line').length===4&&/小数 · CNY/.test(l.textContent)&&/receivables\\.aging · v1\\.0\\.0/.test(document.getElementById('sh-fields-meta').textContent)&&document.getElementById('refresh-catalog').closest('#sh-d-data')!==null;})()`
    )
  );
  await shot('console-02-data-fields');
  await pair('pair-datacenter', 'proto-datacenter', 'console-02-data-fields');
  await contrast('data drawer fields 1440 light');
  await key('Escape');
  await sleep(200);
  await nav('dashboard');
  check(
    'the dashboard studio before a result: the start card with 选择数据…, 未查询, the console header above it (S07-F, one shell), the rail still there and the studio beside it and below the header',
    await evalJs(
      `(()=>{const r=document.querySelector('.app-rail').getBoundingClientRect(),s=document.getElementById('bd-studio').getBoundingClientRect();return document.body.classList.contains('board-on')&&!document.getElementById('bd-start').hidden&&document.getElementById('bd-start-open').textContent.trim()==='选择数据…'&&document.getElementById('bd-state').textContent==='未查询'&&getComputedStyle(document.querySelector('.console-header')).display!=='none'&&document.getElementById('shell-label').textContent==='看板工作室'&&getComputedStyle(document.getElementById('bd-wordmark')).display==='none'&&r.width===56&&Math.abs(s.left-56)<=1&&Math.abs(s.top-document.querySelector('.console-header').getBoundingClientRect().bottom)<=1;})()`
    )
  );
  await shot('console-05-dashboard-start');
  await contrast('dashboard start 1440 light');
  await legibility('dashboard start 1440', 'dashboard');
  // The data drawer from the start card (S07-A): the query panel moves in and back, a preview from inside closes it.
  await click('#bd-start-open');
  await waitFor(`!document.getElementById('sh-d-data').hidden`, 'the data drawer');
  await sleep(250);
  // S09-D: the demo lists one company, chosen already, so the drawer's first focus is 查询预览 (the company to choose when none is).
  check(
    "the start card opens the data drawer with the workbench's query panel inside, focus on 查询预览 as the one company is chosen (the company field otherwise)",
    await evalJs(
      `(()=>{const chosen=[...document.querySelectorAll('#query-companies-picker input[type=checkbox]')].some(b=>b.checked);const f=document.activeElement;return document.getElementById('sh-query-slot').contains(document.querySelector('.query-panel'))&&(chosen?f.id==='preview':f.id==='query-companies'||!!f.closest('#query-companies-picker'))&&document.querySelector('.shell').inert;})()`
    ),
    await evalJs(`document.activeElement.id`)
  );
  await shot('console-05b-data-drawer');
  await key('Escape');
  await sleep(200);
  check(
    'Esc gives the panel back to the workbench and the focus to the button',
    await evalJs(
      `document.getElementById('sh-d-data').hidden&&document.getElementById('page-workspace').contains(document.querySelector('.query-panel'))&&document.activeElement.id==='bd-start-open'`
    )
  );
  await nav('workspace');
  await shot('console-03-workbench');
  await contrast('workbench 1440 light');

  // ---- the session: the workbench ----------------------------------------------------------------------------------------------------
  await nav('workspace');
  check(
    'the workbench has the dataset selected',
    await evalJs(
      `document.getElementById('query-dataset').value==='receivables.aging@1.0.0'&&document.querySelector('.app-rail [aria-current="page"]').dataset.page==='workspace'`
    )
  );
  // S09-D (ADR 0015 decision 4): the company scope is the source's directory as a picker, open on the page: 演示公司 by name with company_demo
  // as secondary text, chosen as the one company listed; a complete directory has no typed line, one company no filter.
  const picker1440 = await evalJs(PICKER);
  check(
    'the company picker at 1440: 演示公司 by name, company_demo as secondary text, chosen; no typed line, no filter for one company',
    !!picker1440 &&
      picker1440.name === '演示公司' &&
      picker1440.id === 'company_demo' &&
      picker1440.chosen &&
      picker1440.typedHidden &&
      picker1440.filterHidden &&
      picker1440.count === 1 &&
      picker1440.width > 0,
    picker1440
  );
  await shot('console-03a-company-picker');
  await contrast('company picker 1440 light');
  check('no horizontal overflow with the picker at 1440', !(await overflow()).over, await overflow());
  // S20-B (ADR 0023 decision 5): the filters and the sort, open on the page between the panel's meta line and its actions. What the
  // dataset declares is offered by label; a filter without a value holds the query and says why; then two filters (a closed list, a
  // number typed in the column's form) and a sort key, in the light and the dark scheme; the narrowed preview; and 移除 gives the
  // whole dataset back for the checks that follow.
  await chooseCompany();
  const narrow0 = await evalJs(NARROW);
  check(
    'the query panel offers the filters and the sorts the dataset declares, by label, and nothing is chosen for the person',
    !!narrow0 &&
      JSON.stringify(narrow0.offers) === JSON.stringify(['客户', '账龄区间', '应收余额', '应收余额', '客户']) &&
      narrow0.filters.length === 0 &&
      narrow0.sorts.length === 0 &&
      narrow0.hint === '' &&
      !narrow0.previewDisabled,
    narrow0
  );
  await clickExpr(offerOf('query-filter-add', 'aging.bucket'), '添加筛选：账龄区间');
  const waiting = await evalJs(NARROW);
  check(
    'a filter without a value holds 查询预览 and 创建快照; their title and the line beside them say why',
    waiting.filters.length === 1 &&
      waiting.previewDisabled &&
      waiting.snapshotDisabled &&
      waiting.hint === '筛选“账龄区间”还没有值：填写或移除它才能查询。' &&
      waiting.previewTitle === waiting.hint,
    waiting
  );
  await shot('console-03c-filter-waiting');
  await contrast('a filter waiting for its value 1440 light');
  await clickExpr(wordOf('query-filter-rows', 'aging.bucket', 'in'), '属于');
  await clickExpr(wordOf('query-filter-rows', 'aging.bucket', '1_30'), '1_30');
  await clickExpr(wordOf('query-filter-rows', 'aging.bucket', '31_60'), '31_60');
  await clickExpr(offerOf('query-filter-add', 'ar.open_balance'), '添加筛选：应收余额');
  await clickExpr(wordOf('query-filter-rows', 'ar.open_balance', 'le'), '不大于');
  await typeInto('#query-filter-rows .narrow-row[data-field="ar.open_balance"] .narrow-value input', '100');
  const malformed = await evalJs(NARROW);
  check(
    "a number that is not in the column's form holds the query and says the form",
    malformed.previewDisabled && malformed.hint === '筛选“应收余额”的值须写成 2 位小数，例如 100.00。',
    malformed
  );
  await typeInto(
    '#query-filter-rows .narrow-row[data-field="ar.open_balance"] .narrow-value input',
    '9007199254740893.00'
  );
  await clickExpr(offerOf('query-sort-add', 'ar.open_balance'), '添加排序：应收余额');
  await clickExpr(wordOf('query-sort-rows', 'ar.open_balance', 'desc'), '降序');
  await sleep(150);
  const narrow1 = await evalJs(NARROW);
  check(
    'two filter rows and a sort key at 1440: the field by its label with the id as secondary text, the declared operators as words with the chosen one marked, the values, inside the panel',
    narrow1.filters.length === 2 &&
      narrow1.filters[0].name === '账龄区间' &&
      narrow1.filters[0].id === 'aging.bucket' &&
      JSON.stringify(narrow1.filters[0].words) === JSON.stringify(['等于', '属于*']) &&
      JSON.stringify(narrow1.filters[0].values) === JSON.stringify(['1_30', '31_60']) &&
      narrow1.filters[1].name === '应收余额' &&
      narrow1.filters[1].id === 'ar.open_balance' &&
      JSON.stringify(narrow1.filters[1].words) === JSON.stringify(['小于', '不大于*', '大于', '不小于', '介于']) &&
      JSON.stringify(narrow1.filters[1].values) === JSON.stringify(['9007199254740893.00']) &&
      narrow1.sorts.length === 1 &&
      narrow1.sorts[0].name === '应收余额' &&
      JSON.stringify(narrow1.sorts[0].words) === JSON.stringify(['升序', '降序*']) &&
      JSON.stringify(narrow1.offers) === JSON.stringify(['客户', '客户']) &&
      narrow1.hint === '' &&
      !narrow1.previewDisabled &&
      [...narrow1.filters, ...narrow1.sorts].every(r => r.left >= narrow1.left && r.right <= narrow1.right + 1) &&
      narrow1.right <= narrow1.inner,
    narrow1
  );
  await shot('console-03d-filters-and-sort');
  await contrast('filters and sort 1440 light');
  await legibility('workbench with filters and sort 1440');
  check('no horizontal overflow with the filters and the sort at 1440', !(await overflow()).over, await overflow());
  // The scheme changes while another page is shown, as in the dark pass at the end of this script. The layout designer below the
  // panel is a closed <details>: Chrome restyles what it does not render only when something reads it, so after a change made with
  // the workbench on screen the audit's own reading starts the 0.15 s background transition of the designer's buttons and measures
  // its first frame, the new scheme's text on the old scheme's fill (1.22:1, on something nobody sees). The rows stay in the panel.
  const schemeOf = async scheme => {
    await nav('system');
    await viewport(1440, 900, scheme);
    await nav('workspace');
  };
  await schemeOf('dark');
  const narrowDark1440 = await evalJs(NARROW);
  check(
    'the rows are still in the panel in the dark scheme, inside it',
    !!narrowDark1440 &&
      narrowDark1440.filters.length === 2 &&
      narrowDark1440.sorts.length === 1 &&
      [...narrowDark1440.filters, ...narrowDark1440.sorts].every(
        r => r.left >= narrowDark1440.left && r.right <= narrowDark1440.right + 1
      ),
    narrowDark1440
  );
  await contrast('filters and sort 1440 dark');
  check(
    'no horizontal overflow with the filters and the sort at 1440 dark',
    !(await overflow()).over,
    await overflow()
  );
  await shot('console-1440-dark-filters-and-sort');
  await schemeOf('light');
  await clickBtn('#page-workspace', '查询预览');
  await waitFor(
    `!document.getElementById('result-table-wrap').hidden&&!document.getElementById('preview').disabled`,
    'the narrowed preview',
    30000
  );
  const narrowedResult = await evalJs(
    `({rows:[...document.querySelectorAll('#result-table tbody tr')].map(r=>r.cells[0].textContent),card:document.querySelector('#result-summary small').textContent,total:document.querySelector('#result-summary strong').textContent,chip:document.getElementById('sh-data-text').textContent,subtitle:document.getElementById('result-subtitle').textContent,evidence:[...document.getElementById('result-evidence').children].map(s=>s.textContent).slice(0,4)})`
  );
  check(
    'the narrowed preview: the rows of the two buckets, the largest balance first; the total is theirs and its card says so; the chip, the subtitle and the evidence line say the scope',
    JSON.stringify(narrowedResult.rows) === JSON.stringify(['customer_c', 'customer_b']) &&
      narrowedResult.card === '应收余额 · 筛选后范围合计 / CNY' &&
      narrowedResult.total === '9007199254740892.76' &&
      /^应收余额 · 演示公司 · \d{4}-\d{2}-\d{2} · 2 个筛选 · 按应收余额降序$/.test(narrowedResult.chip) &&
      narrowedResult.subtitle ===
        '当前显示 2 行 · 当前批准组合的完整结果 · 已按 2 个筛选缩小范围，合计是筛选后的范围' &&
      JSON.stringify(narrowedResult.evidence) ===
        JSON.stringify([
          '范围已缩小：行与合计只含符合全部筛选的数据',
          '筛选：账龄区间 属于 1_30、31_60',
          '筛选：应收余额 不大于 9007199254740893.00',
          '排序：按应收余额降序',
        ]),
    narrowedResult
  );
  await shot('console-03e-narrowed-preview');
  await contrast('narrowed preview 1440 light');
  check('no horizontal overflow with the narrowed preview at 1440', !(await overflow()).over, await overflow());
  for (const [list, field] of [
    ['query-filter-rows', 'aging.bucket'],
    ['query-filter-rows', 'ar.open_balance'],
    ['query-sort-rows', 'ar.open_balance'],
  ])
    await clickExpr(removeOf(list, field), '移除 ' + field);
  const narrow2 = await evalJs(NARROW);
  check(
    '移除 takes the rows away, and the narrowed result with them: the whole dataset is the query again',
    narrow2.filters.length === 0 &&
      narrow2.sorts.length === 0 &&
      narrow2.offers.length === 5 &&
      (await evalJs(
        `document.getElementById('result-kind').textContent==='尚未查询'&&document.getElementById('sh-data-text').textContent==='未选择数据'`
      )),
    narrow2
  );
  await clickBtn('#page-workspace', '查询预览');
  await waitFor(
    `!document.getElementById('result-table-wrap').hidden&&!document.getElementById('preview').disabled`,
    'preview',
    30000
  );
  check(
    'the preview: 查询预览 badge, rows in the table, the total card',
    await evalJs(
      `document.getElementById('result-kind').textContent==='查询预览'&&document.querySelectorAll('#result-table tbody tr').length===3&&document.querySelector('#result-summary strong').textContent==='9007199254740993.01'`
    )
  );
  check(
    'the data chip names the company (S09-D), the id in the title only',
    await evalJs(
      `/^应收余额 · 演示公司 · \\d{4}-\\d{2}-\\d{2}$/.test(document.getElementById('sh-data-text').textContent)&&!/company_demo/.test(document.getElementById('sh-data-text').textContent)`
    ),
    await evalJs(`document.getElementById('sh-data-text').textContent`)
  );
  await clickBtn('#page-workspace', '创建快照');
  await waitFor(
    `document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`,
    'snapshot',
    60000
  );
  await sleep(400);
  await shot('console-04-workbench-snapshot');
  await contrast('workbench snapshot 1440 light');
  check(
    'the snapshot: 冻结快照, the pager, the task count rises',
    await evalJs(
      `document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('result-pager').hidden&&document.getElementById('task-count').textContent==='${tasksBefore + 1}'`
    )
  );
  // S07-G (ADR 0011): the layout designer's bar; 发布 with nothing saved opens 发布前确认 offering 保存并发布, in the centred frame of the shell.
  await click('#report-designer > summary');
  await sleep(200);
  check(
    "the layout designer's bar: 保存, 发布, 历史 (S09-F) and the 草稿 badge",
    await evalJs(
      `[...document.querySelectorAll('#report-designer .designer-bar button')].map(b=>b.textContent.trim()).join(',')==='保存,发布,历史'&&document.getElementById('layout-state').textContent==='草稿'`
    )
  );
  await clickBtn('#report-designer .designer-bar', '发布');
  await waitFor(`!document.getElementById('ly-d-publish').hidden`, 'the layout publish confirmation', 20000);
  await sleep(250);
  check(
    '发布 with nothing saved offers 保存并发布 and holds the focus',
    await evalJs(
      `document.getElementById('ly-publish-confirm').textContent==='保存并发布'&&!!document.activeElement.closest('#ly-d-publish')&&document.querySelector('.shell').inert`
    )
  );
  await shot('console-04a-layout-publish');
  await contrast('layout publish confirmation 1440 light');
  await clickBtn('#ly-d-publish', '保存并发布');
  await waitFor(`document.getElementById('layout-state').textContent==='已发布 v1'`, 'the layout published', 30000);
  await idle();
  await sleep(200);
  await clickBtn('#report-designer .designer-bar', '历史');
  await waitFor(`document.getElementById('ly-version-list').children.length===1`, 'the layout version dialog', 20000);
  await idle();
  await sleep(250);
  check(
    "the layout's 历史 lists v1 as 已发布 and 当前打开, and says the current publication (S09-F)",
    await evalJs(
      `[...document.querySelectorAll('#ly-version-list .pill')].map(p=>p.textContent).join(',')==='已发布,当前打开'&&document.getElementById('ly-version-note').textContent==='当前发布：v1'&&document.getElementById('ly-ver-restore').textContent==='打开'&&document.getElementById('layout-server-new').textContent==='复制为新布局'`
    )
  );
  await shot('console-04b-layout-versions');
  await contrast('layout version dialog 1440 light');
  await key('Escape');
  await sleep(150);
  await click('#report-designer > summary');
  await sleep(150);

  // ---- the session: the dashboard studio ----------------------------------------------------------------------------------------------
  await nav('dashboard');
  check(
    'with a result: the desk, 草稿, the binding in the context bar, the panels and the status bar',
    await evalJs(
      `document.getElementById('bd-start').hidden&&!document.getElementById('bd-desk').hidden&&document.getElementById('bd-state').textContent==='草稿'&&/receivables\\.aging @ 1\\.0\\.0 · .* · 冻结快照当前页/.test(document.getElementById('bd-ctx-source').textContent)&&document.getElementById('bd-status-source').textContent===document.getElementById('bd-ctx-source').textContent&&document.getElementById('bd-side-dataset').textContent==='receivables.aging@1.0.0'`
    )
  );
  await clickBtn('#bd-sidebar', '添加指标卡');
  await clickBtn('#bd-sidebar', '添加明细表');
  await clickBtn('#bd-sidebar', '添加当前页柱状图');
  await sleep(300);
  check(
    'three widgets on the desk, counted in the panel and the status bar',
    await evalJs(
      `document.getElementById('board-widgets').children.length===3&&document.getElementById('bd-status-count').textContent==='3 / 12 个组件'&&document.getElementById('board-count').textContent==='3 / 12 个组件'`
    )
  );
  // S21-A: a line and a pie from the two tools after the bar's; drawn from the snapshot's page (three rows, one of them negative), their
  // legends print the cells' literal values; a title typed into the line's card becomes its heading, and the pie takes the full width.
  await clickBtn('#bd-sidebar', '添加折线图');
  await clickBtn('#bd-sidebar', '添加饼图');
  await sleep(300);
  const charts = await evalJs(
    `(()=>{const cards=[...document.getElementById('board-widgets').children],line=cards[3],pie=cards[4];const cells=[...document.querySelectorAll('#result-table tbody tr')].map(r=>r.cells[2].textContent);const values=el=>[...el.querySelectorAll('.board-legend strong')].map(s=>s.textContent);return {count:cards.length,status:document.getElementById('bd-status-count').textContent,cells,points:line?line.querySelectorAll('.board-line circle.board-point').length:0,lineValues:line?values(line):[],slices:pie?pie.querySelectorAll('.board-pie .board-slice').length:0,pieValues:pie?values(pie):[],notDrawn:pie?[...pie.querySelectorAll('.board-legend em')].map(e=>e.textContent):[],note:pie?(pie.querySelector('.board-note')||{}).textContent:null,headings:cards.map(c=>c.querySelector('h3').textContent),styled:document.querySelectorAll('#board-widgets .board-line [style],#board-widgets .board-pie [style]').length,svg:[...document.querySelectorAll('#board-widgets svg[role=img]')].map(s=>s.getAttribute('aria-label'))};})()`
  );
  check(
    'S21-A: a line of one point a row and a pie of one slice a positive row, the negative row listed and not drawn, the legends the literal cells, five widgets counted, no style attribute in the charts (the bar sets its widths through the CSSOM)',
    charts.count === 5 &&
      charts.status === '5 / 12 个组件' &&
      charts.cells.length === 3 &&
      charts.points === 3 &&
      JSON.stringify(charts.lineValues) === JSON.stringify(charts.cells) &&
      charts.slices === 2 &&
      JSON.stringify(charts.pieValues) === JSON.stringify(charts.cells) &&
      JSON.stringify(charts.notDrawn) === JSON.stringify(['未绘制']) &&
      /^零或负值不绘制扇区：第 2 行 · customer_b · -0\.24。$/.test(charts.note || '') &&
      charts.headings[3] === '应收余额 · 当前页折线图' &&
      charts.headings[4] === '应收余额 · 当前页饼图' &&
      charts.styled === 0 &&
      JSON.stringify(charts.svg) ===
        JSON.stringify(['当前页折线图：应收余额，3 个点', '当前页饼图：应收余额，2 个扇区']),
    charts
  );
  await typeInto('#board-widgets > article:nth-child(4) .board-title', '账龄走势');
  await key('Tab');
  await sleep(200);
  await evalJs(`document.querySelector('#board-widgets > article:nth-child(5) .board-width').focus()`);
  await key('ArrowDown');
  await sleep(250);
  const titled = await evalJs(
    `(()=>{const cards=[...document.getElementById('board-widgets').children];return {heading:cards[3].querySelector('h3').textContent,input:cards[3].querySelector('.board-title').value,wide:cards[4].classList.contains('board-wide'),width:cards[4].querySelector('.board-width').value,layers:[...document.querySelectorAll('#bd-layers .layer-row span')].map(e=>e.textContent),state:document.getElementById('bd-state').textContent,pieWidth:Math.round(cards[4].getBoundingClientRect().width),deskWidth:Math.round(document.getElementById('board-widgets').getBoundingClientRect().width)};})()`
  );
  check(
    "S21-A: the typed title is the line's heading and its name in the structure panel; the pie at 全宽 spans the grid",
    titled.heading === '账龄走势' &&
      titled.input === '账龄走势' &&
      titled.wide &&
      titled.width === 'full' &&
      titled.layers[3] === '4 · 账龄走势' &&
      titled.layers[4] === '5 · 应收余额 · 当前页饼图' &&
      titled.state === '草稿' &&
      Math.abs(titled.pieWidth - titled.deskWidth) <= 1,
    titled
  );
  await shot('console-06-dashboard-widgets');
  await contrast('dashboard 1440 light');
  check('no horizontal overflow in the dashboard studio at 1440', !(await overflow()).over, await overflow());
  await click('#bd-rail-structure');
  await sleep(250);
  await shot('console-07-dashboard-structure');
  const before = await evalJs(`[...document.querySelectorAll('#bd-layers .layer-row span')].map(e=>e.textContent)`);
  await clickExpr(
    `document.querySelectorAll('#bd-layers .layer-row')[0].querySelectorAll('button')[1]`,
    'move the first widget down'
  );
  await sleep(200);
  const after = await evalJs(`[...document.querySelectorAll('#bd-layers .layer-row span')].map(e=>e.textContent)`);
  check(
    "the structure panel's 下移 reorders the desk like the card's button",
    before[0].endsWith('应收余额') &&
      after[0].endsWith('明细表 · 当前结果页') &&
      (await evalJs(
        `document.getElementById('board-widgets').children[0].querySelector('h3').textContent==='明细表 · 当前结果页'`
      )),
    {before, after}
  );
  await click('#bd-rail-components');
  // The rail by keyboard: ArrowDown moves the selection and the focus.
  await click('#bd-rail-components');
  await key('ArrowDown');
  await sleep(120);
  check(
    'the rail is a vertical tablist: ArrowDown selects and focuses the next panel',
    await evalJs(
      `document.activeElement.id==='bd-rail-structure'&&document.getElementById('bd-rail-structure').getAttribute('aria-selected')==='true'&&!document.querySelector('#bd-sidebar [data-view="structure"]').hidden`
    )
  );
  await click('#bd-rail-components');
  await clickBtn('#bd-studio .topbar', '保存');
  await waitFor(`document.getElementById('bd-state').textContent==='已保存 v1'`, 'saved', 30000);
  await idle();
  check(
    '保存 in the topbar saves on the server: the badge says 已保存 v1, the inspector names the dashboard',
    await evalJs(`/看板 [a-f0-9]{8} · 版本 1/.test(document.getElementById('bd-insp-meta').textContent)`),
    await evalJs(`document.getElementById('bd-insp-meta').textContent`)
  );
  await clickBtn('#bd-studio .topbar', '历史');
  await waitFor(`document.getElementById('bd-version-list').children.length===1`, 'the version dialog', 20000);
  await idle();
  await sleep(250);
  check(
    '历史 opens the version dialog (S07-G; the word of S09-F): v1 listed and picked, the focus inside, the studio inert',
    await evalJs(
      `!document.getElementById('bd-d-versions').hidden&&document.getElementById('bd-version-list').children[0].getAttribute('aria-current')==='true'&&!!document.activeElement.closest('#bd-d-versions')&&document.getElementById('bd-studio').inert`
    )
  );
  await shot('console-08-dashboard-versions');
  await contrast('dashboard version dialog 1440 light');
  await key('Escape');
  await sleep(150);
  check(
    'Esc closes the version dialog and returns the focus to 历史',
    await evalJs(`document.getElementById('bd-d-versions').hidden&&document.activeElement.id==='bd-versions'`)
  );
  await clickBtn('#bd-sidebar', '添加指标卡');
  await sleep(150);
  check(
    'a change after the save: · 有未保存修改, and 发布 refuses with the reason',
    await evalJs(`document.getElementById('bd-state').textContent==='已保存 v1 · 有未保存修改'`)
  );
  await clickBtn('#bd-studio .topbar', '发布');
  await sleep(300);
  await waitFor(`!document.getElementById('bd-d-publish').hidden`, 'the confirmation offering 保存并发布', 30000);
  await sleep(250);
  check(
    '发布 with unsaved changes offers 保存并发布 (S07-B)',
    await evalJs(
      `document.getElementById('bd-publish-confirm').textContent==='保存并发布'&&/先保存为版本 2/.test(document.getElementById('bd-publish-summary').textContent)`
    )
  );
  await shot('console-09a-dashboard-save-publish');
  await key('Escape');
  await sleep(200);
  await clickBtn('#bd-studio .topbar', '保存');
  await waitFor(`document.getElementById('bd-state').textContent==='已保存 v2'`, 'saved v2', 30000);
  await idle();
  await clickBtn('#bd-studio .topbar', '发布');
  await waitFor(`!document.getElementById('bd-d-publish').hidden`, 'the publish confirmation', 30000);
  await sleep(250);
  const summary = await evalJs(
    `Object.fromEntries([...document.querySelectorAll('#bd-publish-summary dt')].map(dt=>[dt.textContent,dt.nextElementSibling.textContent]))`
  );
  check(
    'the confirmation names the saved version, the dataset, the widgets and the current pointer, and holds the focus',
    summary['将发布的版本'] === '版本 2' &&
      summary['发布修订'] === undefined &&
      summary['数据集'] === 'receivables.aging@1.0.0' &&
      summary['组件'] === '6 个' &&
      summary['当前发布'] === '未发布' &&
      (await evalJs(`!!document.activeElement.closest('#bd-d-publish')&&document.getElementById('bd-studio').inert`)),
    summary
  );
  await shot('console-09-dashboard-publish-confirm');
  await contrast('publish confirmation 1440 light');
  await key('Tab');
  await key('Tab');
  await key('Tab');
  await key('Tab');
  check('Tab stays inside the confirmation', await evalJs(`!!document.activeElement.closest('#bd-d-publish')`));
  await key('Escape');
  await sleep(150);
  check(
    'Esc closes the confirmation, publishes nothing and returns the focus to 发布',
    await evalJs(
      `document.getElementById('bd-d-publish').hidden&&document.activeElement.id==='bd-publish'&&!document.getElementById('bd-studio').inert&&document.getElementById('bd-state').textContent==='已保存 v2'`
    )
  );
  await clickBtn('#bd-studio .topbar', '发布');
  await waitFor(`!document.getElementById('bd-d-publish').hidden`, 'the confirmation again', 30000);
  await clickBtn('#bd-d-publish', '确认发布');
  await waitFor(`document.getElementById('bd-state').textContent==='已发布 v2'`, 'published', 30000);
  await idle();
  await sleep(200);
  check(
    '确认发布 publishes the saved version: 已发布 v2 in the badge and the status bar',
    await evalJs(`document.getElementById('bd-status-state').textContent.trim()==='已发布 v2'`)
  );
  await shot('console-10-dashboard-published');
  const boards = (await api('GET', '/dashboards')).body;
  const release = boards.length === 1 ? (await api('GET', `/dashboards/${boards[0].id}/release`)).body : null;
  check(
    'the server agrees: one dashboard, version 2 published',
    boards.length === 1 && boards[0].version === 2 && release && release.published_version === 2,
    {boards, release}
  );
  await clickBtn('#bd-studio .topbar', '看板说明与操作帮助');
  await waitFor(`!document.getElementById('bd-d-help').hidden`, 'the help');
  await sleep(200);
  await shot('console-11-dashboard-help');
  await key('Escape');
  await sleep(150);
  check(
    'the help dialog closes on Esc and returns the focus',
    await evalJs(`document.getElementById('bd-d-help').hidden&&document.activeElement.id==='bd-help'`)
  );

  // ---- the session: tasks, system ---------------------------------------------------------------------------------------------------------
  await nav('tasks');
  check(
    "the tasks panel (S07-B, S07-C) opens over the current page with the identity's jobs from the server, the session's snapshot job first and finished",
    await evalJs(
      `!document.getElementById('sh-d-tasks').hidden&&document.getElementById('sh-tasks-slot').contains(document.getElementById('tasks-body'))&&document.querySelectorAll('#tasks-body tr').length===${tasksBefore} + 1&&/报表快照/.test(document.querySelector('#tasks-body tr').cells[0].textContent)&&/已完成/.test(document.querySelector('#tasks-body tr').textContent)&&document.getElementById('tasks-note').textContent===(${tasksBefore} + 1)+' 项'`
    )
  );
  await shot('console-12-tasks');
  await contrast('tasks panel 1440 light');
  await key('Escape');
  await sleep(200);
  await nav('system');
  check(
    'the system page: the identity and the capabilities as definition lists',
    await evalJs(
      `document.getElementById('shell-label').textContent==='能力与环境'&&document.querySelectorAll('#identity-details dt').length===5&&document.querySelectorAll('#capability-details dt').length>=5`
    )
  );
  // ADR 0016 (S10-B): the session's products, each with the state its catalog answered; on the demo both sources answer.
  await waitFor(
    `document.querySelectorAll('#connection-list li .badge').length===2&&[...document.querySelectorAll('#connection-list li .badge')].every(b=>b.textContent.startsWith('可用'))`,
    'the product connections checked'
  );
  check(
    'the system page lists the two products of the session, each 可用 with its number of datasets',
    await evalJs(
      `JSON.stringify([...document.querySelectorAll('#connection-list li code')].map(c=>c.textContent))===JSON.stringify(['brant-contract-demo','shipment-contract-demo'])`
    )
  );
  await shot('console-13-system');
  await contrast('system 1440 light');
  // The centre and the studio keep their own shells; the console chrome gives way to them too.
  await nav('library');
  await waitFor(`document.querySelectorAll('#page-library .sample-card').length>=37`, 'the centre', 30000);
  await sleep(300);
  check(
    'the template centre takes the screen beside the rail and below the console header (S07-F), its own wordmark and data chip hidden',
    await evalJs(
      `document.body.classList.contains('library-on')&&getComputedStyle(document.querySelector('.console-header')).display!=='none'&&document.getElementById('shell-label').textContent==='模板中心'&&Math.abs(document.getElementById('page-library').getBoundingClientRect().left-56)<=1&&Math.abs(document.getElementById('page-library').getBoundingClientRect().top-document.querySelector('.console-header').getBoundingClientRect().bottom)<=1&&getComputedStyle(document.getElementById('lib-data')).display==='none'&&getComputedStyle(document.querySelector('#page-library .library-header>.wordmark')).display==='none'`
    )
  );
  await shot('console-14-library');
  await nav('documents');
  await sleep(600);
  check(
    'the document studio takes the screen beside the rail and below the console header (S07-F), its own wordmark hidden',
    await evalJs(
      `document.body.classList.contains('studio-on')&&getComputedStyle(document.querySelector('.console-header')).display!=='none'&&document.getElementById('shell-label').textContent==='文档工作室'&&getComputedStyle(document.getElementById('st-wordmark')).display==='none'&&Math.abs(document.querySelector('#page-documents .studio').getBoundingClientRect().left-56)<=1&&Math.abs(document.querySelector('#page-documents .studio').getBoundingClientRect().top-document.querySelector('.console-header').getBoundingClientRect().bottom)<=1`
    )
  );
  await shot('console-15-studio');
  await legibility('document studio 1440', 'studio');

  // ---- dark at 1440 ----------------------------------------------------------------------------------------------------------------------
  await viewport(1440, 900, 'dark');
  for (const p of PAGES) {
    await nav(p);
    await contrast(`${p} 1440 dark`);
    check(`no horizontal overflow at 1440 dark (${p})`, !(await overflow()).over, await overflow());
    if (['workspace', 'dashboard'].includes(p)) await shot(`console-1440-dark-${p}`);
  }
  await viewport(1440, 900, 'light');

  // ---- alone at 1280, 768, 390, light and dark ---------------------------------------------------------------------------------------------
  for (const [w, h, mobile] of [
    [1280, 800, false],
    [768, 900, false],
    [390, 844, true],
  ]) {
    for (const scheme of ['light', 'dark']) {
      await viewport(w, h, scheme, mobile);
      await sleep(300);
      for (const p of PAGES) {
        await nav(p);
        const o = await overflow();
        check(`no horizontal overflow at ${w} ${scheme} (${p})`, !o.over, o);
        await contrast(`${p} ${w} ${scheme}`);
        if (['workspace', 'dashboard'].includes(p)) await shot(`console-${w}-${scheme}-${p}`);
      }
    }
    await viewport(w, h, 'light', mobile);
    const navGeo = await evalJs(
      `(()=>{const n=document.querySelector('.app-rail').getBoundingClientRect();return {w:n.width,h:n.height,row:getComputedStyle(document.querySelector('.app-rail')).flexDirection};})()`
    );
    check(
      `the rail at ${w}: ${w <= 700 ? 'a horizontal strip at the top, 48 px' : '56 px at the left edge'}`,
      w <= 700 ? navGeo.row === 'row' && navGeo.h === 48 : navGeo.w === 56,
      navGeo
    );
    // The dashboard studio's drawers at the narrow widths.
    await nav('dashboard');
    if (w <= 950) {
      await click('#bd-ctx-inspector');
      await sleep(350);
      check(
        `the inspector opens as a drawer at ${w} and fits`,
        await evalJs(
          `(()=>{const i=document.getElementById('bd-inspector'),b=i.getBoundingClientRect();return i.classList.contains('open')&&b.right<=innerWidth+1&&b.left>=0;})()`
        ),
        await evalJs(`JSON.stringify(document.getElementById('bd-inspector').getBoundingClientRect())`)
      );
      await shot(`console-${w}-dashboard-inspector`);
      await key('Escape');
      await sleep(250);
      check(
        `Esc closes the drawer at ${w}`,
        await evalJs(`!document.getElementById('bd-inspector').classList.contains('open')`)
      );
    }
    if (w <= 660) {
      await click('#bd-rail-structure');
      await sleep(350);
      check(
        `the side panel opens as a drawer at ${w} and fits`,
        await evalJs(
          `(()=>{const s=document.getElementById('bd-sidebar'),b=s.getBoundingClientRect();return s.classList.contains('open')&&b.left>=0&&b.right<=innerWidth+1;})()`
        ),
        await evalJs(`JSON.stringify(document.getElementById('bd-sidebar').getBoundingClientRect())`)
      );
      await shot(`console-${w}-dashboard-sidebar`);
      await key('Escape');
      await sleep(250);
    }
    if (w === 390) {
      // S09-D: the company picker at 390, where it goes with the query panel: the data drawer. It fits the drawer, no overflow, contrast.
      await nav('workspace');
      await click('#sh-data-chip');
      await waitFor(`!document.getElementById('sh-d-data').hidden`, 'the data drawer at 390');
      await sleep(300);
      const picker390 = await evalJs(PICKER);
      check(
        'the company picker at 390 is in the data drawer and fits it: 演示公司 with company_demo, chosen',
        !!picker390 &&
          picker390.name === '演示公司' &&
          picker390.chosen &&
          picker390.width > 0 &&
          picker390.left >= 0 &&
          picker390.right <= picker390.inner + 1 &&
          (await evalJs(
            `document.getElementById('sh-query-slot').contains(document.getElementById('query-companies-picker'))`
          )),
        picker390
      );
      const o390 = await overflow();
      check('no horizontal overflow with the picker open at 390', !o390.over, o390);
      await contrast('company picker 390 light');
      await shot('console-390-company-picker');
      // S20-B: the filters and the sort at 390, in the data drawer with the rest of the panel: a filter row and a sort key stack
      // inside the drawer (the field and its 移除, then the operators, then the value), nothing scrolls sideways, and the text is
      // legible and holds its contrast in both schemes. 移除 leaves the panel as it was found.
      await clickExpr(offerOf('query-filter-add', 'aging.bucket'), '添加筛选：账龄区间');
      await clickExpr(wordOf('query-filter-rows', 'aging.bucket', 'in'), '属于');
      await clickExpr(wordOf('query-filter-rows', 'aging.bucket', '1_30'), '1_30');
      await clickExpr(offerOf('query-filter-add', 'ar.open_balance'), '添加筛选：应收余额');
      await clickExpr(wordOf('query-filter-rows', 'ar.open_balance', 'between'), '介于');
      await clickExpr(offerOf('query-sort-add', 'ar.open_balance'), '添加排序：应收余额');
      await clickExpr(wordOf('query-sort-rows', 'ar.open_balance', 'desc'), '降序');
      await sleep(200);
      const narrow390 = await evalJs(NARROW);
      check(
        'two filter rows and a sort key at 390 are in the data drawer and fit it; the filter without its two values holds the query and says why',
        !!narrow390 &&
          narrow390.filters.length === 2 &&
          narrow390.sorts.length === 1 &&
          [...narrow390.filters, ...narrow390.sorts].every(
            r => r.width > 0 && r.left >= 0 && r.right <= narrow390.inner
          ) &&
          narrow390.right <= narrow390.inner &&
          !narrow390.scrolls &&
          narrow390.previewDisabled &&
          narrow390.hint === '筛选“应收余额”的“介于”需要起止两个值。' &&
          (await evalJs(`document.getElementById('sh-query-slot').contains(document.getElementById('query-narrow'))`)),
        narrow390
      );
      const narrowOver = await overflow();
      check('no horizontal overflow with the filters and the sort open at 390', !narrowOver.over, narrowOver);
      await contrast('filters and sort 390 light');
      await legibility('data drawer with filters and sort 390');
      await shot('console-390-filters-and-sort');
      await viewport(390, 844, 'dark', true);
      await contrast('filters and sort 390 dark');
      const narrowDark = await overflow();
      check('no horizontal overflow with the filters and the sort open at 390 dark', !narrowDark.over, narrowDark);
      await shot('console-390-dark-filters-and-sort');
      await viewport(390, 844, 'light', true);
      for (const [list, field] of [
        ['query-filter-rows', 'aging.bucket'],
        ['query-filter-rows', 'ar.open_balance'],
        ['query-sort-rows', 'ar.open_balance'],
      ])
        await clickExpr(removeOf(list, field), '移除 ' + field);
      check(
        '移除 at 390 leaves no row and nothing to wait for',
        await evalJs(
          `document.querySelectorAll('#query-narrow .narrow-row').length===0&&document.getElementById('query-hint').hidden&&!document.getElementById('preview').disabled`
        ),
        await evalJs(NARROW)
      );
      await key('Escape');
      await sleep(250);
    }
  }
  await viewport(1440, 900, 'light');

  // ---- S09-D: the picker with many companies. The demo lists one; a directory of twenty, partial, is answered by a stub of the page's
  // fetch for the route alone, in a new session: the filter above eight, the typed line of a partial list, the bound of sixteen, the
  // layout and the contrast of it all. The DOM tests hold the behaviour; this is the real browser's look at it.
  await nav('workspace');
  await clickBtn('.console-header', '退出工作区');
  await waitFor(`!document.getElementById('connection-overlay').hidden`, 'signed out before the stubbed directory');
  await evalJs(
    `(()=>{window.__realFetch=window.fetch;window.fetch=(url,o)=>String(url).includes('/companies?')?Promise.resolve(new Response(JSON.stringify({supported:true,partial:true,companies:Array.from({length:20},(_,i)=>({id:'c'+(i+1),name:'公司'+(i+1)}))}),{status:200,headers:{'Content-Type':'application/json'}})):window.__realFetch(url,o);})()`
  );
  await typeInto('#access-token', token);
  await clickBtn('.connection-card', '连接工作区');
  await waitFor(
    `document.getElementById('connection-overlay').hidden&&document.querySelectorAll('#query-companies-picker .company-option').length===20&&!document.getElementById('preview').disabled`,
    'the picker with twenty companies',
    30000
  );
  await sleep(300);
  const many = await evalJs(PICKER);
  check(
    'twenty companies: the filter above eight, the typed line of a partial list, nothing chosen for the person, the list inside its cell',
    !!many && many.count === 20 && !many.filterHidden && !many.typedHidden && !many.chosen && many.width > 0,
    many
  );
  await typeInto('#query-companies-filter', '公司2');
  check(
    'the filter narrows the list by name',
    await evalJs(
      `[...document.querySelectorAll('#query-companies-picker .company-option:not([hidden])')].map(o=>o.querySelector('.company-name').textContent).join()==='公司2,公司20'`
    ),
    await evalJs(
      `[...document.querySelectorAll('#query-companies-picker .company-option:not([hidden])')].map(o=>o.querySelector('.company-name').textContent)`
    )
  );
  await evalJs(
    `(()=>{const f=document.getElementById('query-companies-filter');f.value='';f.dispatchEvent(new Event('input',{bubbles:true}));})()`
  );
  for (let i = 1; i <= 16; i++) await click(`#query-companies-picker input[value="c${i}"]`);
  check(
    'the sixteenth chosen disables the rest, and the line under the list says why',
    await evalJs(
      `[...document.querySelectorAll('#query-companies-picker input[type=checkbox]')].filter(b=>b.disabled).length===4&&document.getElementById('query-companies-note').textContent.startsWith('已选 16 家')&&!document.getElementById('query-companies-note').hidden`
    ),
    await evalJs(`document.getElementById('query-companies-note').textContent`)
  );
  await shot('console-03b-company-picker-many');
  await contrast('company picker with twenty companies 1440 light');
  check('no horizontal overflow with twenty companies at 1440', !(await overflow()).over, await overflow());
  await evalJs(`window.fetch=window.__realFetch`);

  // ---- sign out --------------------------------------------------------------------------------------------------------------------------
  await nav('workspace');
  await clickBtn('.console-header', '退出工作区');
  await sleep(300);
  check(
    "退出工作区: the card is back, the shell inert, the identity and the counts cleared, nothing in the browser's storage",
    await evalJs(
      `!document.getElementById('connection-overlay').hidden&&document.querySelector('.shell').inert===true&&document.getElementById('actor-label').textContent==='未连接'&&document.getElementById('sh-fields-list').hidden&&document.getElementById('access-token').value===''&&localStorage.length===0&&sessionStorage.length===0`
    )
  );
  await shot('session-signout');
  check('no page error', await evalJs(`!window.__errors`), await evalJs(`JSON.stringify(window.__errors||[])`));
}

let failure = null;
try {
  await main();
} catch (e) {
  failure = e;
  console.error('ABORT', e.stack || e);
  try {
    await shot('zz-failure-state');
  } catch {}
}
protoServer?.close();
const failed = checks.filter(c => !c.ok);
writeFileSync(
  join(outDir, 'report.json'),
  JSON.stringify(
    {
      generated: new Date().toISOString(),
      base,
      checks,
      failed: failed.length,
      evidence,
      abort: failure ? String(failure.message) : null,
    },
    null,
    2
  )
);
console.log(
  `${checks.length - failed.length}/${checks.length} checks passed${failure ? '; aborted: ' + failure.message : ''}`
);
page?.close();
process.exit(failure || failed.length ? 1 : 0);
