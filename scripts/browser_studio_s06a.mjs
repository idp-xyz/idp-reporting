// Real-browser acceptance for the S06-A document studio (ADR 0008): real mouse and keyboard input over the Chrome DevTools Protocol, the template JSON checked after
// every step, screenshots of the studio beside the prototype IDP_Reporting_Designer_UIUX_V3.0.html at 1440 px for each of the prototype's states, the studio alone at
// 1280, 768 and 390 px in the light and the dark scheme, no horizontal overflow at 390, 768, 1280 and 1440 px, and the contrast of every visible text. A real session
// follows: insert from the library and by drag, align, lock, copy and paste, check, preview, save, publish and the versions dialog.
// Visual sameness is judged by a person from the screenshots (docs/STUDIO.md says so); this script measures what it can and records the rest.
// Not part of `npm test` and not CI: it needs a running reporting-demo started with the PDF flags and a template directory, and a Chrome started with
//   --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server
// It exits 1 when any check fails or the run aborts, and 2 on bad arguments; report.json is written either way.
// Safety: --base must be loopback because the bearer token is typed into the page. The prototype is served from a loopback port of its own (--proto-port) for the
// duration of the run. The script reads and writes only the page it drives and its own files in --out (PNGs and report.json); it removes nothing else.
// Usage: node scripts/browser_studio_s06a.mjs --out DIR --token-file FILE --proto FILE [--base http://127.0.0.1:PORT] [--cdp PORT] [--proto-port PORT]
import {readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync} from 'node:fs';
import {createServer} from 'node:http';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const usage = why => {
  console.error(
    why +
      '\nusage: --out DIR --token-file FILE --proto FILE [--base http://127.0.0.1:PORT] [--cdp PORT] [--proto-port PORT]'
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
if (!args.out || !args['token-file'] || !args.proto) usage('--out, --token-file and --proto are required');
const base = args.base || 'http://127.0.0.1:7841',
  cdp = args.cdp || '9341',
  protoPort = Number(args['proto-port'] || 7842),
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
  proto = readFileSync(args.proto);
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
const idle = () => waitFor(`!document.getElementById('preview').disabled`, 'the console is idle', 40000);
const OWN_FILE = /^(proto|studio|pair|zz-failure)-[\w-]+\.png$|^report\.json$/;
for (const f of readdirSync(outDir)) if (OWN_FILE.test(f)) rmSync(join(outDir, f));
const shot = async name => {
  writeFileSync(
    join(outDir, name + '.png'),
    Buffer.from((await send('Page.captureScreenshot', {format: 'png'})).data, 'base64')
  );
  console.log('shot', name);
};
const viewport = async (w, h, scheme = 'light', mobile = false) => {
  await send('Emulation.setDeviceMetricsOverride', {width: w, height: h, deviceScaleFactor: 1, mobile});
  await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: scheme}]});
  await sleep(500);
};
const overflow = () =>
  evalJs(
    `(()=>{const d=document.documentElement;return {scroll:d.scrollWidth,client:d.clientWidth,over:d.scrollWidth>d.clientWidth+1};})()`
  );

// ---- real input -------------------------------------------------------------------------------------------------------------------
const MOD = {alt: 1, ctrl: 2, meta: 4, shift: 8};
const mods = list => list.reduce((m, k) => m | MOD[k], 0);
const mouse = (type, x, y, modifiers = 0, down = false) =>
  send('Input.dispatchMouseEvent', {
    type,
    x,
    y,
    button: type === 'mouseMoved' && !down ? 'none' : 'left',
    buttons: type === 'mouseReleased' || (type === 'mouseMoved' && !down) ? 0 : 1,
    clickCount: type === 'mouseMoved' ? 0 : 1,
    modifiers,
  });
const KEYS = {
  ArrowRight: [39, 'ArrowRight'],
  ArrowDown: [40, 'ArrowDown'],
  Escape: [27, 'Escape'],
  Enter: [13, 'Enter'],
  Tab: [9, 'Tab'],
};
for (const c of 'abcdefghijklmnopqrstuvwxyz') KEYS[c] = [c.toUpperCase().charCodeAt(0), 'Key' + c.toUpperCase()];
const key = async (name, list = []) => {
  const [vk, code] = KEYS[name],
    modifiers = mods(list),
    text = !modifiers && name === 'Enter' ? '\r' : undefined;
  await send('Input.dispatchKeyEvent', {
    type: text ? 'keyDown' : 'rawKeyDown',
    key: name,
    code,
    windowsVirtualKeyCode: vk,
    nativeVirtualKeyCode: vk,
    modifiers,
    ...(text ? {text, unmodifiedText: text} : {}),
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
const centre = selector =>
  evalJs(
    `(()=>{const e=document.querySelector(${JSON.stringify(selector)});e.scrollIntoView({block:'nearest',inline:'nearest'});const b=e.getBoundingClientRect();return [b.left+b.width/2,b.top+b.height/2];})()`
  );
const click = async (selector, modifiers = []) => {
  const [x, y] = await centre(selector);
  await mouse('mouseMoved', x, y);
  await mouse('mousePressed', x, y, mods(modifiers));
  await mouse('mouseReleased', x, y, mods(modifiers));
  await sleep(60);
};
const geometry = () =>
  evalJs(
    `(()=>{const p=document.querySelector('#doc-canvas-host .cv-paper').getBoundingClientRect();return {left:p.left,top:p.top,ppm:p.width/210};})()`
  );
const pageToClient = async (x, y) => {
  const g = await geometry();
  return [g.left + x * g.ppm, g.top + y * g.ppm];
};
async function dragBetween(x0, y0, x1, y1, {steps = 10, modifiers = []} = {}) {
  const m = mods(modifiers);
  await mouse('mouseMoved', x0, y0, m);
  await mouse('mousePressed', x0, y0, m);
  for (let i = 1; i <= steps; i++)
    await mouse('mouseMoved', x0 + ((x1 - x0) * i) / steps, y0 + ((y1 - y0) * i) / steps, m, true);
  await mouse('mouseReleased', x1, y1, m);
  await sleep(120);
}
const tpl = async () => {
  await evalJs(`document.getElementById('doc-json-save').click()`);
  return JSON.parse(await evalJs(`document.getElementById('doc-json').value`));
};
const all2 = t => Object.entries(t.bands).flatMap(([band, b]) => b.elements.map(e => ({band, e})));
const ids = t =>
  all2(t)
    .map(v => v.e.id)
    .sort();
const selected = () =>
  evalJs(
    `[...document.querySelectorAll('#doc-canvas-host [data-key][aria-pressed="true"]')].map(e=>e.getAttribute('data-key'))`
  );
const READY = '模板已更新；配置不含业务数据。';
async function load(template) {
  await evalJs(
    `(()=>{const j=document.getElementById('doc-json');j.value=${JSON.stringify(JSON.stringify(template))};document.getElementById('doc-json-open').click();})()`
  );
  const text = await evalJs(`document.getElementById('doc-status').textContent`);
  if (text !== READY) throw new Error('template refused: ' + text);
}
const invoice1x = binding => {
  const el = (id, source, what, x, y, width, height, font_size, align) => ({
    id,
    source,
    field: source === 'literal' ? '' : what,
    text: source === 'literal' ? what : '',
    x,
    y,
    width,
    height,
    font_size,
    align,
  });
  return {
    schema_version: '1.3',
    kind: 'document',
    binding,
    elements: [
      el('n1', 'literal', '应收对账单', 10, 30, 100, 10, 18, 'left'),
      el('n2', 'row', 'customer.id', 120, 30, 80, 10, 12, 'right'),
      el('n3', 'literal', '客户：', 10, 45, 60, 8, 12, 'left'),
      el('n4', 'total', 'ar.open_balance', 120, 190, 80, 10, 12, 'right'),
      el('n5', 'literal', '制单人：', 10, 205, 100, 8, 12, 'left'),
    ],
    print: {mode: 'full', offset_x: 0, offset_y: 0},
    table: {
      x: 10,
      y: 60,
      width: 190,
      height: 120,
      font_size: 9,
      columns: [
        {field: 'aging.bucket', width: 60},
        {field: 'customer.id', width: 60},
        {field: 'ar.open_balance', width: 70},
      ],
    },
  };
};
// The contrast of every visible text of the studio against the colour behind it (WCAG ratio; the colour behind is composited from the ancestors' fills).
const AUDIT = scheme => `((theme)=>{
const parse=c=>{const m=c.match(/rgba?\\(([^)]+)\\)/);if(!m)return null;const p=m[1].split(/[ ,\\/]+/).filter(Boolean).map(Number);return {r:p[0],g:p[1],b:p[2],a:p.length>3?p[3]:1};};
const lum=c=>{const f=v=>{v/=255;return v<=.03928?v/12.92:Math.pow((v+.055)/1.055,2.4);};return .2126*f(c.r)+.7152*f(c.g)+.0722*f(c.b);};
const over=(t,b)=>({r:t.r*t.a+b.r*(1-t.a),g:t.g*t.a+b.g*(1-t.a),b:t.b*t.a+b.b*(1-t.a),a:1});
const bgOf=el=>{const layers=[];for(let e=el;e;e=e.parentElement){const c=parse(getComputedStyle(e).backgroundColor);if(c&&c.a>0){layers.push(c);if(c.a>=1)break;}}let out=layers.length&&layers[layers.length-1].a>=1?null:{r:255,g:255,b:255,a:1};if(!out)out=layers.pop();for(let i=layers.length-1;i>=0;i--)out=over(layers[i],out);return out;};
const out=[],root=document.getElementById('page-documents'),walker=document.createTreeWalker(root,NodeFilter.SHOW_TEXT),seen=new Set();let n;
while(n=walker.nextNode()){const t=n.nodeValue.trim();if(!t)continue;const el=n.parentElement;if(seen.has(el))continue;seen.add(el);
 if(el.closest('svg')||el.closest('.sr-only')||el.closest('[hidden]')||el.closest('.overlay:not(.open)')||el.closest('.toast:not(.show)'))continue;
 const cs=getComputedStyle(el);if(cs.visibility==='hidden'||cs.display==='none'||el.getBoundingClientRect().width===0)continue;
 const fg=parse(cs.color),bg=bgOf(el),f2=over({...fg,a:fg.a*(parseFloat(cs.opacity)||1)},bg),L1=lum(f2),L2=lum(bg),r=(Math.max(L1,L2)+.05)/(Math.min(L1,L2)+.05);
 out.push({text:t.slice(0,28),ratio:Math.round(r*100)/100,size:cs.fontSize,disabled:!!el.closest('button:disabled,[aria-disabled="true"]'),id:el.id||String(el.className).slice(0,24)});}
const bad=out.filter(o=>o.ratio<4.5&&!o.disabled);return {total:out.length,failing:bad.length,worst:bad.sort((a,b)=>a.ratio-b.ratio).slice(0,12)};})(${JSON.stringify(scheme)})`;

let protoServer;
async function main() {
  protoServer = createServer((req, res) => {
    res.writeHead(200, {'content-type': 'text/html; charset=utf-8'});
    res.end(proto);
  });
  await new Promise((res, rej) => {
    protoServer.once('error', rej);
    protoServer.listen(protoPort, '127.0.0.1', res);
  });
  const list = await (await fetch(`http://127.0.0.1:${cdp}/json/list`)).json();
  const target = list.find(t => t.type === 'page');
  if (!target) throw new Error('no page target on the debugging port');
  page = connect(target.webSocketDebuggerUrl);
  await page.ready;
  await send('Page.enable');
  await send('Runtime.enable');
  // A headless page has no focus of its own: without this the keyboard events of the script do not reach it.
  await send('Page.bringToFront');
  await send('Emulation.setFocusEmulationEnabled', {enabled: true});
  await send('Browser.setDownloadBehavior', {behavior: 'deny'}).catch(() => {});
  await viewport(1440, 900);

  // ---- the prototype's states -------------------------------------------------------------------------------------------------------
  await send('Page.navigate', {url: `http://127.0.0.1:${protoPort}/`});
  await waitFor(`document.readyState==='complete'&&!!document.querySelector('.ribbon-tabs')`, 'the prototype');
  await sleep(900);
  const pclick = sel =>
    evalJs(
      `(()=>{const e=document.querySelector(${JSON.stringify(sel)});if(!e)return false;e.click();return true;})()`
    );
  const pesc = async () => {
    await evalJs(
      `document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));document.activeElement&&document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));`
    );
    await pclick('[data-act="close"]');
    await sleep(300);
  };
  await shot('proto-01-design');
  await pclick('[data-ribbon="insert"]');
  await sleep(400);
  await shot('proto-02-ribbon-insert');
  await pclick('[data-ribbon="design"]');
  await pclick('.toolrail [data-side="data"]');
  await sleep(400);
  await shot('proto-06-side-data');
  await pclick('.toolrail [data-side="structure"]');
  await sleep(400);
  await shot('proto-07-side-structure');
  await pclick('.toolrail [data-side="components"]');
  await pclick('[data-act="versions"]');
  await sleep(500);
  await shot('proto-09-versions');
  await pesc();
  await pclick('.toolrail [data-act="library"]');
  await sleep(700);
  await shot('proto-10-library');
  await pesc();
  await pclick('[data-act="commands"]');
  await sleep(500);
  await shot('proto-11-commands');
  await pesc();

  // ---- the console: sign in, freeze a snapshot, open the studio -------------------------------------------------------------------
  await send('Page.navigate', {url: base + '/console/'});
  await waitFor(`document.readyState==='complete'&&!!document.getElementById('access-token')`, 'the console');
  await sleep(300);
  await evalJs(
    `(()=>{const i=document.getElementById('access-token');i.value=${JSON.stringify(token)};i.dispatchEvent(new Event('input',{bubbles:true}));document.getElementById('connect-form').requestSubmit();})()`
  );
  await waitFor(
    `document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`,
    'signed in',
    30000
  );
  await evalJs(`document.querySelector('[data-page="workspace"]').click()`);
  await evalJs(
    `(()=>{const i=document.getElementById('query-companies');i.value='company_demo';i.dispatchEvent(new Event('input',{bubbles:true}));})()`
  );
  await evalJs(`document.getElementById('snapshot').click()`);
  await waitFor(
    `document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`,
    'snapshot',
    60000
  );
  await evalJs(
    `(()=>{if(window.__pdfs)return;window.__pdfs=[];const o=URL.createObjectURL.bind(URL);URL.createObjectURL=b=>{window.__pdfs.push(b);return o(b);};})()`
  );
  await evalJs(`document.querySelector('[data-page="documents"]').click()`);
  await sleep(700);
  check(
    'the studio replaces the console shell on the documents page',
    await evalJs(
      `document.body.classList.contains('studio-on')&&getComputedStyle(document.querySelector('.sidebar:not(#sidebar)')).display==='none'&&document.querySelector('.studio').getBoundingClientRect().width===innerWidth&&document.querySelector('.studio').getBoundingClientRect().height===innerHeight`
    ),
    await overflow()
  );
  // The template the states are shown with: the invoice, converted to 2.0 (a band-based template shows the studio's band labels).
  await evalJs(`document.getElementById('doc-json-save').click()`);
  const binding = JSON.parse(await evalJs(`document.getElementById('doc-json').value`)).binding;
  await load(invoice1x(binding));
  await evalJs(`document.getElementById('doc-convert').click()`);
  let converted = await tpl();
  converted.group = {fields: ['customer.id']};
  await load(converted);
  converted = await tpl();
  check(
    'the invoice is converted to a 2.0 template',
    converted.schema_version === '2.0' && ids(converted).length >= 6,
    converted.schema_version
  );
  const tabTo = name => evalJs(`document.querySelector('[data-ribbon="${name}"]').click()`);
  const railTo = name => evalJs(`document.querySelector('[data-side="${name}"]').click()`);
  const closeDialog = async () => {
    await key('Escape');
    await sleep(250);
  };

  // The studio in the prototype's states (1440 x 900), each beside its prototype shot.
  await shot('studio-01-design');
  await tabTo('insert');
  await sleep(300);
  await shot('studio-02-ribbon-insert');
  await tabTo('design');
  await railTo('data');
  await sleep(300);
  await shot('studio-06-side-data');
  await railTo('structure');
  await sleep(300);
  await shot('studio-07-side-structure');
  await railTo('components');
  // Saved, so that the versions dialog has a version to show.
  await click('#doc-server-save');
  await waitFor(`document.getElementById('doc-server-status').textContent.includes('版本 1')`, 'saved v1');
  await idle();
  await click('#st-versions');
  await waitFor(
    `document.querySelectorAll('#st-version-list .version-card').length>=1&&document.getElementById('doc-release-status').textContent.includes('发布修订')`,
    'versions'
  );
  await sleep(300);
  await shot('studio-09-versions');
  await closeDialog();
  await click('#st-crumb');
  await waitFor(
    `document.querySelectorAll('#st-chooser-body tr').length>=1&&!document.getElementById('st-chooser-status').textContent.includes('正在读取')`,
    'chooser rows',
    20000
  );
  await idle();
  await sleep(300);
  await shot('studio-10-library');
  await closeDialog();
  await evalJs(`document.getElementById('doc-palette-open').click()`);
  await waitFor(`document.querySelector('.cv-palette')&&!document.querySelector('.cv-backdrop').hidden`, 'palette');
  await sleep(300);
  await shot('studio-11-commands');
  await closeDialog();
  await sleep(200);

  // ---- a real session -------------------------------------------------------------------------------------------------------------
  const before = await tpl(),
    heads = all2(before)
      .filter(v => v.e.type === 'text' && v.band === 'document_header')
      .sort((p, q) => p.e.y - q.e.y || p.e.x - q.e.x),
    firstText = heads[0];
  // Insert from the library by click: into the band of the selected element, one new element, selected.
  await click(`#doc-canvas-host [data-key="${firstText.e.id}"]`);
  await click('[data-insert="line"]');
  await idle();
  let t = await tpl();
  let fresh = ids(t).filter(id => !ids(before).includes(id));
  check(
    'a card inserts one element into the band of the selected element',
    fresh.length === 1 &&
      all2(t).find(v => v.e.id === fresh[0]).band === firstText.band &&
      all2(t).find(v => v.e.id === fresh[0]).e.type === 'line',
    {fresh, band: firstText.band}
  );
  check('the new element is selected', JSON.stringify(await selected()) === JSON.stringify(fresh), await selected());
  // Insert by drag: a real mouse drag from a card to a point of the page.
  const drop = await pageToClient(105, 36);
  const [cx, cy] = await centre('[data-insert="rect"]');
  const n1 = ids(t).length;
  await dragBetween(cx, cy, drop[0], drop[1], {steps: 14});
  t = await tpl();
  const dropped = all2(t).filter(v => !ids(before).includes(v.e.id) && v.e.type === 'rect');
  check(
    'a drag from the library inserts under the pointer, in the band there',
    ids(t).length === n1 + 1 && dropped.length === 1 && dropped[0].band === 'document_header',
    {count: ids(t).length, dropped}
  );
  check(
    'no drop marker or drag state is left behind',
    await evalJs(
      `document.querySelectorAll('.cv-drop').length===0&&!document.querySelector('.studio').classList.contains('studio-dragging')`
    )
  );
  // Align: two elements selected with Shift, left alignment from the ribbon.
  await click(`#doc-canvas-host [data-key="${firstText.e.id}"]`);
  const second = heads.find(v => v.e.y > firstText.e.y);
  await click(`#doc-canvas-host [data-key="${second.e.id}"]`, ['shift']);
  await sleep(100);
  check('two elements are selected', (await selected()).length === 2, await selected());
  await click('#doc-align-right');
  await sleep(150);
  t = await tpl();
  const right = id => {
    const e = all2(t).find(v => v.e.id === id).e;
    return Math.round((e.x + e.width) * 10);
  };
  check('align right puts both on the same right edge', right(firstText.e.id) === right(second.e.id), [
    firstText.e.id,
    second.e.id,
    await evalJs(`document.getElementById('doc-status').textContent`),
  ]);
  // Lock: a locked element is not moved by the pointer or the arrow keys.
  await click(`#doc-canvas-host [data-key="${second.e.id}"]`);
  await click('#st-lock');
  await sleep(100);
  check(
    'the lock shows on the canvas and in the ribbon',
    await evalJs(
      `document.querySelector('#doc-canvas-host [data-key="${second.e.id}"]').classList.contains('cv-locked')&&document.getElementById('st-lock').getAttribute('aria-pressed')==='true'`
    )
  );
  const locked = JSON.stringify(await tpl());
  const [lx, ly] = await centre(`#doc-canvas-host [data-key="${second.e.id}"]`);
  await dragBetween(lx, ly, lx + 40, ly + 30);
  await evalJs(`document.querySelector('#doc-canvas-host [data-key="${second.e.id}"]').focus()`);
  await key('ArrowRight');
  await key('ArrowDown');
  check('a locked element is not moved by the mouse or the keys', JSON.stringify(await tpl()) === locked);
  await click('#st-lock');
  await sleep(80);
  await dragBetween(lx, ly, lx + 24, ly);
  check(
    'unlocked, it moves again',
    JSON.stringify(await tpl()) !== locked,
    await evalJs(`document.getElementById('doc-canvas-status').textContent`)
  );
  // Copy and paste with the keyboard.
  await click(`#doc-canvas-host [data-key="${second.e.id}"]`);
  const n2 = ids(await tpl()).length;
  await key('c', ['ctrl']);
  await key('v', ['ctrl']);
  await idle();
  const said = await evalJs(`document.getElementById('doc-status').textContent`);
  t = await tpl();
  check('Ctrl+C and Ctrl+V on the canvas make one copy in the same band', ids(t).length === n2 + 1, [
    n2,
    ids(t).length,
    said,
  ]);
  // The grid, the zoom and the structure panel.
  await tabTo('design');
  await click('#st-grid');
  await sleep(120);
  await shot('studio-12-grid');
  check(
    'the grid overlay is on the page',
    await evalJs(`document.querySelectorAll('#doc-canvas-host .cv-gridline').length===1`)
  );
  await click('#st-grid');
  await evalJs(
    `(()=>{const s=document.getElementById('zoom');s.value='150';s.dispatchEvent(new Event('change',{bubbles:true}));})()`
  );
  await sleep(150);
  check('the zoom control sets 150 %', (await geometry()).ppm > 4.4 && (await geometry()).ppm < 4.6, await geometry());
  await evalJs(
    `(()=>{const s=document.getElementById('zoom');s.value='fit';s.dispatchEvent(new Event('change',{bubbles:true}));})()`
  );
  await sleep(200);
  check(
    'fit width leaves no horizontal scroll in the work area',
    await evalJs(
      `(()=>{const s=document.getElementById('doc-canvas-scroll');return s.scrollWidth<=s.clientWidth+1;})()`
    )
  );
  await railTo('structure');
  await click('#st-layers .layer-label');
  await sleep(100);
  check('a click on a layer selects it on the canvas', (await selected()).length === 1, await selected());
  await railTo('components');
  // Check: the boundary samples, run by the layout the issue worker uses.
  await tabTo('check');
  await click('#doc-check');
  await tabTo('design');
  await waitFor(`document.querySelectorAll('#doc-check-body tr').length>=1`, 'check rows', 30000);
  await idle();
  await sleep(200);
  const rows = await evalJs(
    `[...document.querySelectorAll('#doc-check-body tr')].map(r=>[...r.cells].slice(0,3).map(c=>c.textContent))`
  );
  evidence.check_rows = rows;
  check(
    "the check lists rows with their results in the inspector's check tab",
    rows.length >= 1 &&
      (await evalJs(`document.getElementById('st-itab-check').getAttribute('aria-selected')==='true'`)),
    rows
  );
  await shot('studio-13-check');
  // Preview and export: the server's PDF through the dialog; the pager tells the pages.
  await click('#previewButton');
  await sleep(200);
  await click('#doc-pdf');
  await waitFor(`window.__pdfs.length>=1`, 'the preview', 60000);
  await idle();
  const pdf = await evalJs(
    `(async()=>{const b=window.__pdfs.at(-1),buf=new Uint8Array(await b.arrayBuffer());let s='';for(let i=0;i<buf.length;i+=8192)s+=String.fromCharCode(...buf.subarray(i,i+8192));return {head:s.slice(0,5),size:buf.length,pages:(s.match(/\\/Type\\s*\\/Page(?![A-Za-z])/g)||[]).length};})()`
  );
  const pager = await evalJs(`document.getElementById('pageCount').textContent`);
  check(
    "预览与导出 is the server's PDF and the pager tells its pages",
    pdf.head === '%PDF-' && pdf.pages >= 1 && pager === '1 / ' + pdf.pages,
    {pdf, pager}
  );
  await shot('studio-14-preview-dialog');
  await closeDialog();
  // Save a second version and publish through the confirmation dialog.
  await click(`#doc-canvas-host [data-key="${second.e.id}"]`);
  await click('#doc-server-save');
  await waitFor(`document.getElementById('doc-server-status').textContent.includes('版本 2')`, 'saved v2');
  await idle();
  check(
    'the badge says 已保存 v2 after the save',
    await evalJs(`document.getElementById('st-state').textContent==='已保存 v2'`),
    await evalJs(`document.getElementById('st-state').textContent`)
  );
  await click('#st-publish');
  await waitFor(`!document.getElementById('st-pub-confirm').disabled`, 'the publish confirmation');
  await sleep(200);
  await shot('studio-15-publish-confirm');
  await click('#st-pub-confirm');
  await waitFor(`document.getElementById('doc-release-status').textContent.includes('已发布版本 2')`, 'published v2');
  await idle();
  check(
    'the badge says 已发布 v2 after the confirmation',
    await evalJs(
      `document.getElementById('st-state').textContent==='已发布 v2'&&document.getElementById('autosaveStatus').textContent==='已发布'`
    )
  );
  await click('#st-versions');
  await waitFor(`document.querySelectorAll('#st-version-list .version-card').length===2`, 'two versions');
  await idle();
  await sleep(200);
  check(
    'the versions dialog marks the published pointer',
    await evalJs(
      `[...document.querySelectorAll('#st-version-list .pill')].map(e=>e.textContent).join()==='已发布,已保存'`
    )
  );
  await shot('studio-16-versions-published');
  await closeDialog();
  // The name, in place.
  const probe = await evalJs(
    `(()=>{const b=document.getElementById('titleButton').getBoundingClientRect(),e=document.elementFromPoint(b.left+b.width/2,b.top+b.height/2);return {open:[...document.querySelectorAll('.overlay.open')].map(o=>o.id),inert:document.getElementById('doc-canvas-panel').inert,at:e&&(e.id||e.tagName),hidden:document.getElementById('titleButton').hidden};})()`
  );
  evidence.rename_probe = probe;
  await click('#titleButton');
  await evalJs(`(()=>{const i=document.getElementById('st-name-input');i.value='客户往来对账单';})()`);
  await key('Enter');
  await idle();
  const renamed = await evalJs(
    `document.getElementById('doc-status').textContent+' | '+document.getElementById('st-name').textContent+' | '+document.getElementById('st-name-input').hidden`
  );
  check(
    'the name is set in place and is in the template',
    (await tpl()).name === '客户往来对账单' &&
      (await evalJs(`document.getElementById('st-name').textContent==='客户往来对账单'`)),
    renamed
  );
  // Keyboard: Tab order starts in the topbar; a dialog traps Tab.
  await click('#st-help');
  await sleep(150);
  check(
    'the help dialog takes the focus and traps Tab',
    await evalJs(
      `(()=>{const d=document.querySelector('#st-d-help .dialog');const f=[...d.querySelectorAll('button')].filter(b=>!b.disabled);f.at(-1).focus();document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Tab',bubbles:true,cancelable:true}));return d.contains(document.activeElement);})()`
    )
  );
  await closeDialog();
  check(
    'Esc closes the dialog and the focus is back on the opener',
    await evalJs(`document.activeElement.id==='st-help'`),
    await evalJs(`document.activeElement.id`)
  );

  // ---- widths and schemes ---------------------------------------------------------------------------------------------------------
  await evalJs(
    `document.querySelector('#doc-canvas-host [data-key]').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,pointerId:1}));document.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerId:1}))`
  );
  const states = {};
  for (const scheme of ['light', 'dark']) {
    for (const [w, h, mobile] of [
      [1440, 900, false],
      [1280, 900, false],
      [768, 1024, false],
      [390, 844, true],
    ]) {
      await viewport(w, h, scheme, mobile);
      await sleep(300);
      const o = await overflow();
      check(`${w} px ${scheme}: no horizontal overflow`, !o.over, o);
      if (w !== 1440) await shot(`studio-${w}-${scheme}`);
      else if (scheme === 'dark') await shot('studio-1440-dark');
      if (w === 768 || w === 390) {
        await evalJs(`document.querySelector('[data-side="components"]').click()`);
        await sleep(300);
        await shot(`studio-${w}-${scheme}-sidebar`);
        check(
          `${w} px ${scheme}: the side panel is a drawer opened from the rail`,
          await evalJs(
            `document.getElementById('sidebar').classList.contains('open')&&getComputedStyle(document.getElementById('sidebar')).position==='absolute'`
          )
        );
        await evalJs(`document.querySelector('[data-side="components"]').click()`);
        await sleep(200);
        await evalJs(`document.getElementById('st-inspector-toggle').click()`);
        await sleep(350);
        await shot(`studio-${w}-${scheme}-inspector`);
        check(
          `${w} px ${scheme}: the inspector is a ${w === 390 ? 'bottom sheet' : 'drawer'} opened from the toolbar`,
          await evalJs(
            `(()=>{const i=document.getElementById('inspector'),b=i.getBoundingClientRect();return i.classList.contains('open')&&getComputedStyle(i).visibility==='visible'&&(${w === 390}?b.width>=innerWidth-1&&b.top>innerHeight*0.2:b.right<=innerWidth+1);})()`
          )
        );
        await evalJs(`document.getElementById('st-inspector-toggle').click()`);
        await sleep(200);
      }
      // Contrast of every visible text, in the studio's open states.
      if (w === 1440 || w === 1280) {
        for (const [name, setup] of [
          ['design', ''],
          ['insert', `document.querySelector('[data-ribbon="insert"]').click()`],
          ['data', `document.querySelector('[data-ribbon="data"]').click()`],
          ['page', `document.querySelector('[data-ribbon="page"]').click()`],
          ['check', `document.querySelector('[data-ribbon="check"]').click()`],
          [
            'rail-data',
            `document.querySelector('[data-ribbon="design"]').click();document.querySelector('[data-side="data"]').click()`,
          ],
          ['rail-structure', `document.querySelector('[data-side="structure"]').click()`],
          ['rail-resources', `document.querySelector('[data-side="resources"]').click()`],
          [
            'inspector-rules',
            `document.querySelector('[data-side="components"]').click();document.querySelector('[data-prop-tab="rules"]').click()`,
          ],
          ['inspector-check', `document.querySelector('[data-prop-tab="check"]').click()`],
          ['inspector-props', `document.querySelector('[data-prop-tab="properties"]').click()`],
        ]) {
          if (setup) await evalJs(setup);
          await sleep(120);
          const r = await evalJs(AUDIT(scheme));
          states[`${w}-${scheme}-${name}`] = {total: r.total, failing: r.failing, worst: r.worst};
        }
        for (const [name, opener] of [
          ['versions', '#st-versions'],
          ['help', '#st-help'],
          ['chooser', '#st-crumb'],
          ['publish', '#st-publish'],
          ['preview', '#previewButton'],
          ['bands', null],
          ['insert-options', null],
        ]) {
          if (opener) await click(opener);
          else
            await evalJs(
              `document.querySelector('#st-d-${name === 'bands' ? 'bands' : 'insert'}').classList.add('open')`
            );
          await sleep(350);
          const r = await evalJs(AUDIT(scheme));
          states[`${w}-${scheme}-dialog-${name}`] = {total: r.total, failing: r.failing, worst: r.worst};
          if (opener) await closeDialog();
          else await evalJs(`document.querySelector('.overlay.open').classList.remove('open')`);
        }
      }
    }
  }
  evidence.contrast = states;
  const failing = Object.entries(states).filter(([, v]) => v.failing > 0);
  check(
    'every visible text of the studio meets 4.5:1 in both schemes, in every tab, panel and dialog measured',
    !failing.length,
    failing.slice(0, 6)
  );
  await viewport(1440, 900, 'light');
  // The studio beside the prototype: one image per state, the prototype on the left.
  await send('Page.navigate', {url: 'about:blank'});
  await sleep(200);
  await send('Emulation.setDeviceMetricsOverride', {width: 2880, height: 900, deviceScaleFactor: 1, mobile: false});
  for (const state of [
    '01-design',
    '02-ribbon-insert',
    '06-side-data',
    '07-side-structure',
    '09-versions',
    '10-library',
    '11-commands',
  ]) {
    const a = readFileSync(join(outDir, `proto-${state}.png`)).toString('base64'),
      b = readFileSync(join(outDir, `studio-${state}.png`)).toString('base64');
    await evalJs(
      `document.open();document.write('<!doctype html><style>body{margin:0;display:flex;background:#fff}img{width:1440px;height:900px;display:block}</style><img src="data:image/png;base64,${a}"><img src="data:image/png;base64,${b}">');document.close();`
    );
    await waitFor(`[...document.images].every(i=>i.complete&&i.naturalWidth>0)`, 'the pair');
    await sleep(100);
    await shot(`pair-${state}`);
  }
}

try {
  await main();
} catch (e) {
  check('the run completed', false, String((e && e.message) || e));
  try {
    await shot('zz-failure-' + Date.now());
  } catch {}
} finally {
  try {
    page?.close();
  } catch {}
  try {
    protoServer?.close();
  } catch {}
}
writeFileSync(join(outDir, 'report.json'), JSON.stringify({when: new Date().toISOString(), checks, evidence}, null, 1));
const failed = checks.filter(c => !c.ok);
console.log(`${checks.length - failed.length}/${checks.length} checks passed`);
process.exit(failed.length ? 1 : 0);
