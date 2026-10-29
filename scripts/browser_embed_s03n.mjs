// Real-browser acceptance for the S03-N embed pages (ADR 0006): a product page on one loopback origin frames the platform on another, over the
// Chrome DevTools Protocol (Node 22+ global WebSocket/fetch, no dependencies). It proves, in a real Chrome:
//  - the dashboard, the report viewer and the document designer work in the frame after the token handoff (real mouse and keyboard in the frame);
//  - a host on a third, unlisted origin cannot frame the embed page (nor can a host that claims the listed origin, nor a frame under a listed host
//    that is itself on an unlisted origin, nor the console page, nor an embed address with a wrong resource): the page never loads;
//  - the token appears in no request URL, no cookie, no storage, no DOM text and no message, and in the Authorization header of requests to the
//    platform's origin only (the CDP network log of every frame, with the extra-info headers the wire carried);
//  - tokens from another window or another origin are ignored; the size and the events reach the host, and nothing else does.
// Not part of `npm test` and not CI. It needs, started by the caller:
//   a reporting-demo (PDF flags, template and dashboard directories) with -embed-origin <integration>=<--host> for brant-contract-demo and shipment-contract-demo,
//   two static servers of console/embed-host/ on --host and --stranger (different loopback ports, neither the platform's),
//   and a Chrome:  --headless=new --remote-debugging-port=<cdp> --user-data-dir=<empty temp dir> --no-proxy-server
// It exits 1 when any check fails or the run aborts, and 2 on bad arguments; report.json and the screenshots are written to --out either way.
// Safety: all three origins must be loopback because the bearer token is typed into a page. The script writes only its own files in --out and in
// --download-dir, and leaves the browser and the servers running (the caller stops them).
// Usage: node scripts/browser_embed_s03n.mjs --out DIR --token-file FILE --platform URL --host URL --stranger URL --download-dir WINDOWS_PATH --download-dir-wsl PATH [--cdp PORT]
import {readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync, existsSync, statSync} from 'node:fs';
import {parseArgs} from 'node:util';
import {join} from 'node:path';

const usage = why => { console.error(why + '\nusage: --out DIR --token-file FILE --platform URL --host URL --stranger URL --download-dir WINDOWS_PATH --download-dir-wsl PATH [--cdp PORT]'); process.exit(2); };
let args;
try {
  ({values: args} = parseArgs({options: {out: {type: 'string'}, 'token-file': {type: 'string'}, platform: {type: 'string'}, host: {type: 'string'}, stranger: {type: 'string'}, cdp: {type: 'string'}, 'download-dir': {type: 'string'}, 'download-dir-wsl': {type: 'string'}}, strict: true}));
} catch (e) { usage(e.message); }
for (const k of ['out', 'token-file', 'platform', 'host', 'stranger', 'download-dir', 'download-dir-wsl']) if (!args[k]) usage('--' + k + ' is required');
const loopback = (name, value) => { let u; try { u = new URL(value); } catch { usage(`--${name} is not a valid URL`); } if (u.protocol !== 'http:' || !['127.0.0.1', 'localhost', '[::1]'].includes(u.hostname) || u.pathname !== '/' ) usage(`--${name} must be an http loopback origin`); return u.origin; };
const B = loopback('platform', args.platform), A = loopback('host', args.host), C = loopback('stranger', args.stranger);
if (new Set([A, B, C]).size !== 3) usage('--platform, --host and --stranger must be three different origins');
const cdp = args.cdp || '9334', outDir = args.out, token = readFileSync(args['token-file'], 'utf8').trim();
if (token.length < 32) usage('the token is too short to be searched for');
const sleep = ms => new Promise(r => setTimeout(r, ms));
mkdirSync(outDir, {recursive: true});
for (const f of readdirSync(outDir)) if (/^(\d\d-[\w-]+\.png|report\.json)$/.test(f)) rmSync(join(outDir, f));
mkdirSync(args['download-dir-wsl'], {recursive: true});

const checks = [], notes = {};
const check = (name, ok, detail) => { checks.push({name, ok: !!ok, detail: ok ? undefined : detail}); console.log(ok ? 'PASS' : 'FAIL', name, ok ? '' : JSON.stringify(detail)); };

// ---- CDP over the browser's socket, flattened sessions: the page and any frame that gets a target of its own ------------------------------------------
const net = [], extra = new Map(), contexts = new Map(), frames = new Map(), logs = [], downloads = [], dialogs = [], exceptions = [];
let ws, nextId = 1, pageSession, pageTargetId, topFrameId;
const pending = new Map(), sessions = new Map();
const send = (method, params = {}, sessionId, timeout = 30000) => new Promise((res, rej) => {
  const id = nextId++, timer = setTimeout(() => { pending.delete(id); rej(new Error('CDP timeout: ' + method)); }, timeout);
  pending.set(id, {res: v => { clearTimeout(timer); res(v); }, rej: e => { clearTimeout(timer); rej(e); }});
  ws.send(JSON.stringify({id, method, params, ...(sessionId ? {sessionId} : {})}));
});
function onEvent(m) {
  const p = m.params, s = m.sessionId;
  switch (m.method) {
    case 'Target.attachedToTarget': sessions.set(p.sessionId, p.targetInfo); enable(p.sessionId).catch(() => {}); break;
    case 'Network.requestWillBeSent': net.push({s, requestId: p.requestId, url: p.request.url, method: p.request.method, headers: p.request.headers, postData: p.request.postData || '', frameId: p.frameId, type: p.type}); break;
    case 'Network.requestWillBeSentExtraInfo': extra.set(s + p.requestId, {headers: p.headers, cookies: (p.associatedCookies || []).map(c => c.cookie?.name)}); break;
    case 'Network.responseReceived': net.push({s, requestId: p.requestId, response: true, url: p.response.url, status: p.response.status, headers: p.response.headers, frameId: p.frameId, type: p.type}); break;
    case 'Network.loadingFailed': net.push({s, requestId: p.requestId, failed: true, errorText: p.errorText, blockedReason: p.blockedReason, type: p.type}); break;
    case 'Network.webSocketCreated': net.push({s, requestId: p.requestId, url: p.url, websocket: true}); break;
    case 'Runtime.executionContextCreated': contexts.set(s + ':' + p.context.id, {session: s, id: p.context.id, origin: p.context.origin, frameId: p.context.auxData?.frameId, isDefault: p.context.auxData?.isDefault}); break;
    case 'Runtime.executionContextDestroyed': contexts.delete(s + ':' + p.executionContextId); break;
    case 'Runtime.executionContextsCleared': for (const k of [...contexts.keys()]) if (k.startsWith(s + ':')) contexts.delete(k); break;
    case 'Page.frameNavigated': frames.set(p.frame.id, {...p.frame, seen: Date.now()}); break;
    case 'Log.entryAdded': logs.push({level: p.entry.level, source: p.entry.source, text: p.entry.text, url: p.entry.url}); break;
    case 'Runtime.exceptionThrown': exceptions.push(p.exceptionDetails.exception?.description || p.exceptionDetails.text); break;
    case 'Page.javascriptDialogOpening': dialogs.push(p.message); send('Page.handleJavaScriptDialog', {accept: false}, s).catch(() => {}); break;
    case 'Browser.downloadProgress': downloads.push({state: p.state, id: p.guid, bytes: p.receivedBytes}); break;
    case 'Browser.downloadWillBegin': downloads.push({state: 'begin', id: p.guid, name: p.suggestedFilename, url: p.url}); break;
  }
}
async function enable(session) {
  for (const d of ['Page', 'Runtime', 'Log', 'Network']) await send(d + '.enable', {}, session);
  await send('Network.setCacheDisabled', {cacheDisabled: true}, session);
  await send('Target.setAutoAttach', {autoAttach: true, waitForDebuggerOnStart: false, flatten: true}, session).catch(() => {});
}
// ---- helpers ------------------------------------------------------------------------------------------------------------------------------------
const evalIn = async (expression, contextId) => {
  const r = await send('Runtime.evaluate', {expression, awaitPromise: true, returnByValue: true, ...(contextId ? {contextId} : {})}, pageSession);
  if (r.exceptionDetails) throw new Error('eval failed: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  return r.result.value;
};
const evalHost = expr => evalIn(expr);
// The default execution context of the platform's page inside the host (not the top frame).
const frameContext = (origin = B) => [...contexts.values()].filter(c => c.isDefault && c.origin === origin && c.frameId !== topFrameId && c.session === pageSession).at(-1);
const evalFrame = async expr => { const c = frameContext(); if (!c) throw new Error('no frame context of ' + B); return evalIn(expr, c.id); };
const waitFor = async (fn, label, limit = 15000) => { const end = Date.now() + limit; let last; while (Date.now() < end) { try { last = await fn(); if (last) return last; } catch (e) { last = e.message; } await sleep(80); } throw new Error('timeout waiting for: ' + label + ' (' + JSON.stringify(last) + ')'); };
const inFrame = (expr, label, limit) => waitFor(() => evalFrame(`Boolean(${expr})`), label, limit);
// The whole page of the host (the frame is as tall as its content), up to 5000 px.
const shot = async name => {
  const size = (await send('Page.getLayoutMetrics', {}, pageSession)).cssContentSize;
  const clip = {x: 0, y: 0, width: Math.ceil(size.width), height: Math.min(5000, Math.ceil(size.height)), scale: 1};
  writeFileSync(join(outDir, name + '.png'), Buffer.from((await send('Page.captureScreenshot', {format: 'png', clip, captureBeyondViewport: true}, pageSession)).data, 'base64'));
};
const setViewport = async (width, height) => { await send('Emulation.setDeviceMetricsOverride', {width, height, deviceScaleFactor: 1, mobile: false}, pageSession); await sleep(250); };
const mouse = (type, x, y) => send('Input.dispatchMouseEvent', {type, x, y, button: type === 'mouseMoved' ? 'none' : 'left', buttons: type === 'mouseReleased' || type === 'mouseMoved' ? 0 : 1, clickCount: type === 'mouseMoved' ? 0 : 1}, pageSession);
// The centre of an element of the framed page in the host's client coordinates: the frame's own rectangle plus the element's within it.
async function centreOf(selector) {
  await evalFrame(`(()=>{const e=document.querySelector(${JSON.stringify(selector)});if(!e)throw new Error('no ${selector.replace(/'/g, '')}');e.scrollIntoView({block:'center',inline:'center'});return true;})()`);
  await sleep(120);
  const inner = await evalFrame(`(()=>{const b=document.querySelector(${JSON.stringify(selector)}).getBoundingClientRect();return [b.left+b.width/2,b.top+b.height/2];})()`);
  const outer = await evalHost(`(()=>{const b=document.getElementById('frame').getBoundingClientRect();return [b.left,b.top];})()`);
  return [outer[0] + inner[0], outer[1] + inner[1]];
}
async function clickFrame(selector) { const [x, y] = await centreOf(selector); await mouse('mouseMoved', x, y); await mouse('mousePressed', x, y); await mouse('mouseReleased', x, y); await sleep(60); }
async function typeFrame(selector, text) { await clickFrame(selector); await evalFrame(`document.querySelector(${JSON.stringify(selector)}).select()`); await send('Input.insertText', {text}, pageSession); await sleep(60); }
const giveToken = (value = token) => evalHost(`(()=>{const t=document.getElementById('token');t.value=${JSON.stringify(value)};t.dispatchEvent(new Event('change',{bubbles:true}));return true;})()`);
const heard = () => evalHost('window.heard'), everything = () => evalHost('window.__all');
const hostQuery = (o) => new URLSearchParams({platform: B, ...o}).toString();
async function openHost(origin, query) {
  net.length = 0; extra.clear(); logs.length = 0; frames.clear();
  await send('Page.navigate', {url: `${origin}/index.html?${query}`}, pageSession);
  await waitFor(() => evalHost(`document.readyState==='complete'&&!!document.getElementById('frame')`), 'host loaded');
  topFrameId = (await send('Page.getFrameTree', {}, pageSession)).frameTree.frame.id;
}
async function connected(page) {
  await waitFor(() => frameContext(), 'the platform page loaded in the frame');
  await giveToken();
  await inFrame(`document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0&&!document.getElementById('preview').disabled`, 'session, catalog and resource', 30000);
}
const query = async () => { await typeFrame('#query-companies', 'company_demo'); await clickFrame('#preview'); await inFrame(`!document.getElementById('result-table-wrap').hidden&&!document.getElementById('preview').disabled`, 'query result', 30000); };
const frameState = () => evalFrame(`(()=>({shown:[...document.querySelectorAll('main > .page, main .page')].filter(p=>!p.hidden).map(p=>p.id),title:document.title}))()`);
// The wire-level headers of a request: the request event's and the extra-info event's together.
const wireHeaders = r => ({...Object.fromEntries(Object.entries(r.headers || {}).map(([k, v]) => [k.toLowerCase(), v])), ...Object.fromEntries(Object.entries(extra.get(r.s + r.requestId)?.headers || {}).map(([k, v]) => [k.toLowerCase(), v]))});
const lc = h => Object.fromEntries(Object.entries(h || {}).map(([k, v]) => [k.toLowerCase(), v]));
const origin = url => { try { return new URL(url).origin; } catch { return url.split('/')[0] + '//' + url.split('/')[2]; } };

// ---- run ------------------------------------------------------------------------------------------------------------------------------------------
let fatal;
const seed = async (path, body) => { const r = await fetch(B + '/api/reporting/v1' + path, {method: 'POST', headers: {Authorization: 'Bearer ' + token, 'Content-Type': 'application/json'}, body: JSON.stringify(body)}); if (r.status !== 201) throw new Error('seed ' + path + ': ' + r.status); return (await r.json()).id; };
const api = async path => (await fetch(B + '/api/reporting/v1' + path, {headers: {Authorization: 'Bearer ' + token}})).json();
try {
  const version = await (await fetch(`http://127.0.0.1:${cdp}/json/version`)).json();
  notes.browser = version.Browser;
  ws = new WebSocket(version.webSocketDebuggerUrl);
  ws.onmessage = ev => { const m = JSON.parse(ev.data); if (m.id && pending.has(m.id)) { const {res, rej} = pending.get(m.id); pending.delete(m.id); m.error ? rej(new Error(m.error.message)) : res(m.result); } else onEvent(m); };
  await new Promise((res, rej) => { ws.onopen = res; ws.onerror = () => rej(new Error('websocket failed')); });
  const {targetId} = await send('Target.createTarget', {url: 'about:blank'});
  pageTargetId = targetId;
  pageSession = (await send('Target.attachToTarget', {targetId, flatten: true})).sessionId;
  await enable(pageSession);
  await send('Browser.setDownloadBehavior', {behavior: 'allow', downloadPath: args['download-dir'], eventsEnabled: true});
  // Every message the host receives, from the first moment, whoever sent it (a test-only listener in the capture phase of the top frame).
  await send('Page.addScriptToEvaluateOnNewDocument', {source: `(()=>{if(window!==window.top)return;window.__all=[];window.addEventListener('message',e=>{try{const f=document.getElementById('frame');window.__all.push({origin:e.origin,fromFrame:!!f&&e.source===f.contentWindow,data:JSON.parse(JSON.stringify(e.data))});}catch(_){}},true);})();`}, pageSession);
  await setViewport(1280, 900);

  // The caller's data: one dashboard and one template of the demo user, read back through the API (never through the browser).
  const catalog = await api('/catalog?integration_id=brant-contract-demo'), dataset = catalog[0].dataset;
  const binding = {integration_id: 'brant-contract-demo', dataset};
  const dashboardId = await seed('/dashboards', {id: '', expected_version: 0, config: {schema_version: '1.0', kind: 'dashboard', binding, widgets: [{type: 'metric', field: 'ar.open_balance'}, {type: 'table', columns: ['customer.id', 'ar.open_balance']}]}});
  const templateId = await seed('/document-templates', {id: '', expected_version: 0, config: {schema_version: '1.0', kind: 'document', binding, elements: [{id: 'n1', source: 'literal', field: '', text: 'Embedded title', x: 10, y: 25, width: 120, height: 12, font_size: 18, align: 'left'}, {id: 'n2', source: 'row', field: 'customer.id', text: '', x: 10, y: 45, width: 80, height: 10, font_size: 12, align: 'left'}]}});
  const caps = await api('/capabilities');
  notes.capabilities = {formats: caps.formats, issuance: caps.issuance, embedding: caps.embedding};
  check('the platform says embedding is configured for the caller\'s integration, and says no origin', caps.embedding === true && !JSON.stringify(caps).includes(A) && !JSON.stringify(caps).includes('127.0.0.1'), caps);
  const datasetParam = dataset.id + '@' + dataset.version;

  // 0. The embed page for a listed origin: headers, as the browser got them.
  {
    const r = await fetch(`${B}/embed/dashboard?` + new URLSearchParams({integration: 'brant-contract-demo', parent: A, dashboard: dashboardId}));
    const csp = r.headers.get('content-security-policy') || '';
    check('the embed page for the listed origin has the console\'s policy with that one origin as frame-ancestors, and no X-Frame-Options', r.status === 200 && csp.includes(`frame-ancestors ${A};`) && !csp.includes("frame-ancestors 'none'") && !csp.includes('unsafe') && r.headers.get('x-frame-options') === null && r.headers.get('referrer-policy') === 'no-referrer' && r.headers.get('cache-control') === 'no-store', {status: r.status, csp});
    const consoleR = await fetch(B + '/console/');
    check('the console page keeps frame-ancestors \'none\' and X-Frame-Options: DENY', (consoleR.headers.get('content-security-policy') || '').includes("frame-ancestors 'none'") && consoleR.headers.get('x-frame-options') === 'DENY', {csp: consoleR.headers.get('content-security-policy')});
    for (const [name, parent] of [['an unlisted origin', C], ['a lookalike of the listed origin', A + '.evil.example'], ['the listed origin with a trailing slash', A + '/']]) {
      const x = await fetch(`${B}/embed/dashboard?` + new URLSearchParams({integration: 'brant-contract-demo', parent, dashboard: dashboardId}));
      check(`${name} gets 403 with an unframeable answer`, x.status === 403 && x.headers.get('x-frame-options') === 'DENY' && (x.headers.get('content-security-policy') || '').includes("frame-ancestors 'none'"), {status: x.status});
    }
  }

  // 1. A token the platform refuses: signed out after the wait, then recovered from the page's own button. (Slow: the page waits 10 s for a fresh token.)
  await openHost(A, hostQuery({integration: 'brant-contract-demo', view: 'report', dataset: datasetParam}));
  await waitFor(() => frameContext(), 'frame');
  await giveToken('not-the-token-' + 'x'.repeat(40));
  await inFrame(`document.getElementById('connect-error').textContent.length>0&&!document.getElementById('connection-overlay').hidden`, 'signed out after a refused token', 40000);
  const h1 = await heard();
  check('a refused token asks the host for another once, and ends signed out with the coded error UNAUTHENTICATED and no token form', h1.some(m => m.type === 'reporting:error' && m.code === 'UNAUTHENTICATED') && await evalFrame(`!document.getElementById('access-token')&&!document.querySelector('input[type=password]')`), h1);
  await shot('01-signed-out');
  await giveToken();
  await clickFrame('#embed-retry');
  await inFrame(`document.getElementById('connection-overlay').hidden&&document.getElementById('query-dataset').options.length>0`, 'recovered through the retry button', 30000);
  const h1b = await heard();
  check('the retry button asks the host again: the session starts with the new token and the signed-out card is gone', await evalFrame(`document.getElementById('connect-error').hidden&&!document.querySelector('.shell').hidden`) && (await everything()).filter(m => m.data.type === 'reporting:token-request').length >= 2, h1b);

  // 2. The report viewer: real typing and clicks in the frame, the file downloaded inside it.
  await openHost(A, hostQuery({integration: 'brant-contract-demo', view: 'report', dataset: datasetParam}));
  await connected();
  check('the report viewer selects the dataset of its address and shows only the workbench', await evalFrame(`document.getElementById('query-dataset').value===${JSON.stringify(datasetParam)}`) && JSON.stringify((await frameState()).shown) === '["page-workspace"]', await frameState());
  check('the frame offers one integration and no navigation, token form or logout', await evalFrame(`[...document.getElementById('query-product').options].map(o=>o.value).join()==='brant-contract-demo'&&!document.querySelector('nav,aside,.sidebar,.topbar,#logout,#access-token,#connect-form')`));
  await query();
  const rows = await evalFrame(`document.getElementById('result-table').tBodies[0].rows.length`);
  check('the real query in the frame shows the exact rows', rows >= 3 && await evalFrame(`document.getElementById('result-summary').textContent.includes('9007199254740993.01')`), rows);
  await clickFrame('#snapshot');
  await inFrame(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('export').disabled`, 'frozen snapshot', 30000);
  const before = downloads.length;
  await clickFrame('#export');
  await waitFor(() => downloads.some(d => d.state === 'completed') && downloads.length > before, 'download completed', 30000);
  const begun = downloads.find(d => d.state === 'begin');
  const file = begun && join(args['download-dir-wsl'], begun.name);
  const magic = file && existsSync(file) ? readFileSync(file).subarray(0, 2).toString() : '';
  check('the workbook downloads inside the frame, after the digest check, and is a real xlsx', magic === 'PK', {begun, magic});
  await sleep(300);
  const resized = await evalHost(`parseFloat(document.getElementById('frame').style.height)`), contentHeight = await evalFrame(`Math.ceil(document.body.getBoundingClientRect().height)`);
  check('the frame is as tall as its content because the host followed reporting:resize', Math.abs(resized - contentHeight) <= 1 && resized !== 320, {resized, contentHeight});
  await shot('02-report-viewer');

  // 3. The dashboard.
  await openHost(A, hostQuery({integration: 'brant-contract-demo', view: 'dashboard', dashboard: dashboardId}));
  await connected();
  check('the dashboard view shows only the dashboard page with the query panel inside it', JSON.stringify((await frameState()).shown) === '["page-dashboard"]' && await evalFrame(`document.querySelector('.query-panel').closest('.page').id==='page-dashboard'`), await frameState());
  check('the dashboard\'s dataset is selected from the saved dashboard', await evalFrame(`document.getElementById('query-dataset').value===${JSON.stringify(datasetParam)}`));
  await query();
  await inFrame(`document.getElementById('board-widgets').children.length===2&&document.getElementById('board-server-status').textContent.includes(${JSON.stringify(dashboardId)})`, 'saved dashboard opened', 20000);
  check('the saved dashboard opens over the authorized result: exact total and table', await evalFrame(`document.getElementById('board-widgets').textContent.includes('9007199254740993.01')`));
  await shot('03-dashboard');

  // 4. The designer: the canvas, a save and a publish told to the host by id, and (with the PDF writer) an official issue.
  await openHost(A, hostQuery({integration: 'brant-contract-demo', view: 'document-designer', template: templateId}));
  await connected();
  check('the designer view shows only the documents page', JSON.stringify((await frameState()).shown) === '["page-documents"]', await frameState());
  await query();
  await inFrame(`document.getElementById('doc-server-status').textContent.includes(${JSON.stringify(templateId)})`, 'template opened', 20000);
  check('the canvas draws the named template in the frame', await evalFrame(`!!document.querySelector('#doc-canvas-host svg .cv-paper')&&document.querySelectorAll('#doc-canvas-host [data-key]').length>=2`));
  await shot('04-designer');
  await clickFrame('#doc-server-save');
  await inFrame(`document.getElementById('doc-server-status').textContent.includes('版本 2')&&!document.getElementById('preview').disabled`, 'saved version 2', 20000);
  await clickFrame('#doc-release-read'); await inFrame(`document.getElementById('doc-release-status').textContent.includes('修订')&&!document.getElementById('preview').disabled`, 'release state');
  await clickFrame('#doc-release-publish'); await inFrame(`document.getElementById('doc-release-status').textContent.includes('已发布版本 2')&&!document.getElementById('preview').disabled`, 'published');
  await sleep(200);
  const h4 = await heard();
  const events4 = h4.filter(m => m.type === 'reporting:event');
  check('the host heard saved and published with the template id and version, and only that', JSON.stringify(events4) === JSON.stringify([{type: 'reporting:event', v: 1, name: 'saved', template_id: templateId, version: 2}, {type: 'reporting:event', v: 1, name: 'published', template_id: templateId, version: 2}]), h4);
  if (caps.formats.includes('pdf') && caps.issuance === true) {
    await clickFrame('#snapshot'); await inFrame(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('preview').disabled`, 'frozen', 30000);
    await clickFrame('#doc-published-open'); await inFrame(`document.getElementById('doc-server-status').textContent.includes('已打开发布模板')&&!document.getElementById('preview').disabled`, 'published open', 20000);
    const d0 = downloads.length;
    await clickFrame('#doc-issue');
    await waitFor(async () => (await heard()).some(m => m.type === 'reporting:event' && m.name === 'issued'), 'issued event', 60000);
    await waitFor(() => downloads.filter(d => d.state === 'completed').length > 1 && downloads.length > d0, 'issued file downloaded', 30000);
    const issued = (await heard()).find(m => m.name === 'issued');
    const names = downloads.filter(d => d.state === 'begin').map(d => d.name);
    const pdf = names.map(n => join(args['download-dir-wsl'], n)).filter(f => /\.pdf$/.test(f) && existsSync(f)).map(f => readFileSync(f).subarray(0, 5).toString());
    check('an official issue is told to the host by job id only, and the stamped PDF downloads inside the frame', Object.keys(issued).sort().join() === 'job_id,name,type,v' && /^[a-f0-9]{32}$/.test(issued.job_id) && pdf.includes('%PDF-'), {issued, names, pdf});
    await shot('05-issued');
  } else notes.issue = 'skipped: the server has no PDF writer';

  // 5. The theme, from the host.
  await evalHost(`document.getElementById('theme-dark').click()`); await sleep(200);
  check('the host\'s dark theme reaches the frame', await evalFrame(`document.documentElement.getAttribute('data-theme')==='dark'&&getComputedStyle(document.body).backgroundColor!=='rgb(246, 248, 251)'`));
  await shot('06-dark');
  await evalHost(`document.getElementById('theme-light').click()`); await sleep(150);
  check('and the light theme comes back', await evalFrame(`document.documentElement.getAttribute('data-theme')==='light'`));
  await setViewport(600, 900); await sleep(400); await shot('07-narrow'); await setViewport(1280, 900);

  // 6. Other windows cannot talk to the frame: a sibling of the host's origin and a sibling of another origin, each trying to hand it a token.
  const STOLEN = 'sibling-token-' + 'a'.repeat(40), STRANGER_TOKEN = 'stranger-token-' + 'b'.repeat(40);
  await openHost(A, hostQuery({integration: 'brant-contract-demo', view: 'report', dataset: datasetParam}));
  await connected();
  const actorBefore = await evalFrame(`document.getElementById('actor-label').textContent`);
  await evalHost(`(()=>{const s=document.createElement('iframe');s.id='sibling';s.src='about:blank';document.body.append(s);return true;})()`);
  await evalHost(`document.getElementById('sibling').contentWindow.eval(${JSON.stringify(`parent.document.getElementById('frame').contentWindow.postMessage({type:'reporting:token',v:1,token:'${STOLEN}'},'*')`)})`);
  await evalHost(`(()=>{const s=document.createElement('iframe');s.id='stranger';s.src=${JSON.stringify(C + '/index.html?' + hostQuery({integration: 'x', view: 'report', dataset: 'x'}))};document.body.append(s);return true;})()`);
  await waitFor(() => [...contexts.values()].some(c => c.isDefault && c.origin === C && c.frameId !== topFrameId), 'the stranger sibling loaded');
  const strangerCtx = [...contexts.values()].filter(c => c.isDefault && c.origin === C && c.frameId !== topFrameId).at(-1);
  await evalIn(`parent.frames[0].postMessage({type:'reporting:token',v:1,token:${JSON.stringify(STRANGER_TOKEN)}},'*')`, strangerCtx.id);
  await evalIn(`parent.frames[0].postMessage({type:'reporting:theme',v:1,theme:'dark'},'*')`, strangerCtx.id);
  await sleep(500);
  const leaked = net.filter(r => r.url && (JSON.stringify(wireHeaders(r)).includes(STOLEN) || JSON.stringify(wireHeaders(r)).includes(STRANGER_TOKEN) || r.url.includes(STOLEN) || r.url.includes(STRANGER_TOKEN)));
  check('tokens from a sibling window and from a sibling of another origin start nothing and change nothing', leaked.length === 0 && await evalFrame(`document.getElementById('actor-label').textContent===${JSON.stringify(actorBefore)}&&document.documentElement.getAttribute('data-theme')===null`), {leaked: leaked.map(r => r.url)});
  await query();
  check('the session of the frame still works after those messages', await evalFrame(`document.getElementById('result-table').tBodies[0].rows.length>=3`));

  // 7. Where it must not load: the log of the frames that were refused, and the contexts that were never created.
  // Adds the frame with create(), waits for the browser to give up on it (a frame whose address is unreachable) and says whether the platform's page ever ran in it.
  async function refusal(name, create) {
    const marker = Date.now();
    await create();
    await waitFor(() => [...frames.values()].some(f => f.unreachableUrl && f.seen >= marker), 'the browser refuses the frame: ' + name, 15000).catch(() => {});
    await sleep(300);
    const f = [...frames.values()].filter(f => f.seen >= marker && f.unreachableUrl).at(-1);
    return {frame: f, loaded: f ? [...contexts.values()].some(c => c.frameId === f.id && c.origin === B) : null};
  }
  const embedUrl = (parent, extra = {}) => `${B}/embed/dashboard?` + new URLSearchParams({integration: 'brant-contract-demo', parent, dashboard: dashboardId, ...extra});
  // 7a. The unlisted host, honest about itself (the platform answers 403).
  await openHost(C, hostQuery({integration: 'brant-contract-demo', view: 'dashboard', dashboard: dashboardId}));
  await sleep(1500);
  const embedNav = [...frames.values()].filter(f => f.unreachableUrl && f.unreachableUrl.startsWith(B + '/embed/'));
  const embedResponses = net.filter(r => r.response && r.url.startsWith(B + '/embed/'));
  check('a host on an unlisted origin cannot frame the embed page: the platform answers 403 and the frame never loads it', embedResponses.length >= 1 && embedResponses.every(r => r.status === 403) && embedNav.length >= 1 && ![...contexts.values()].some(c => c.origin === B && c.frameId !== topFrameId) && !(await evalHost(`!!document.getElementById('frame')&&document.getElementById('frame').contentWindow&&(()=>{try{return document.getElementById('frame').contentWindow.document.body.textContent}catch(e){return ''}})().includes('IDP')`)), {embedResponses: embedResponses.map(r => r.status), embedNav: embedNav.map(f => f.unreachableUrl), contexts: [...contexts.values()].filter(c => c.origin === B).length});
  check('the refused frame says why: X-Frame-Options DENY and frame-ancestors \'none\' on the 403', embedResponses.every(r => lc(r.headers)['x-frame-options'] === 'DENY' && (lc(r.headers)['content-security-policy'] || '').includes("frame-ancestors 'none'")), embedResponses.map(r => r.headers));
  await giveToken();
  await sleep(300);
  check('the token the stranger\'s page was given reached nothing of the platform: no request to it at all but the refused embed page', net.filter(r => r.url && r.url.startsWith(B) && !r.url.startsWith(B + '/embed/')).length === 0, net.filter(r => r.url && r.url.startsWith(B)).map(r => r.url));
  await shot('08-unlisted-host');
  // 7b. The unlisted host claiming the listed origin: the platform serves the page (200, frame-ancestors the listed origin) and the browser refuses to frame it under the unlisted one.
  await openHost(C, hostQuery({integration: 'brant-contract-demo', view: 'dashboard', dashboard: dashboardId}));
  const spoof = await refusal('a host on an unlisted origin claiming the listed origin', () => evalHost(`(()=>{const f=document.createElement('iframe');f.name='spoof';f.src=${JSON.stringify(embedUrl(A))};document.body.append(f);return 'spoof';})()`));
  const spoofResponses = net.filter(r => r.response && r.url === embedUrl(A));
  check('a host that claims the listed origin from an unlisted one is refused by the browser: served with frame-ancestors of the listed origin, never loaded under the unlisted one', spoofResponses.length >= 1 && spoofResponses[0].status === 200 && (lc(spoofResponses[0].headers)['content-security-policy'] || '').includes(`frame-ancestors ${A};`) && spoof.frame && spoof.loaded === false, {spoof: {url: spoof.frame?.unreachableUrl, loaded: spoof.loaded}, status: spoofResponses[0]?.status});
  // 7c. A frame on an unlisted origin under the listed host: every ancestor must be listed, not only the nearest... the nearest here is the unlisted one.
  await openHost(A, hostQuery({integration: 'brant-contract-demo', view: 'dashboard', dashboard: dashboardId}));
  await waitFor(() => frameContext(), 'the legitimate frame under the listed host');
  await evalHost(`(()=>{const s=document.createElement('iframe');s.id='middle';s.src=${JSON.stringify(C + '/index.html?' + hostQuery({integration: 'x', view: 'report', dataset: 'x'}))};document.body.append(s);return true;})()`);
  await waitFor(() => [...contexts.values()].some(c => c.isDefault && c.origin === C && c.frameId !== topFrameId), 'the middle frame loaded');
  const middle = [...contexts.values()].filter(c => c.isDefault && c.origin === C && c.frameId !== topFrameId).at(-1);
  const nested = await refusal('an unlisted frame between the listed host and the embed page', () => evalIn(`(()=>{const f=document.createElement('iframe');f.src=${JSON.stringify(embedUrl(A))};document.body.append(f);return 'nested';})()`, middle.id));
  check('a frame of an unlisted origin under the listed host cannot frame the embed page either (every ancestor is checked)', nested.frame && nested.loaded === false, {nested: nested.frame?.unreachableUrl});
  // 7d. The console page and an embed address with a wrong resource are not frameable by the listed host.
  const consoleFrame = await refusal('the console page', () => evalHost(`(()=>{const f=document.createElement('iframe');f.src=${JSON.stringify(B + '/console/')};document.body.append(f);return 'console';})()`));
  check('the console page cannot be framed, not even by the listed host', consoleFrame.frame && consoleFrame.loaded === false, {consoleFrame: consoleFrame.frame?.unreachableUrl});
  const badResource = await refusal('an embed address with a malformed resource', () => evalHost(`(()=>{const f=document.createElement('iframe');f.src=${JSON.stringify(embedUrl(A, {dashboard: 'not-an-id'}))};document.body.append(f);return 'bad';})()`));
  check('an embed address with a malformed resource is refused with 400 and is not framed', badResource.frame && badResource.loaded === false && net.some(r => r.response && r.url.includes('dashboard=not-an-id') && r.status === 400), {bad: badResource.frame?.unreachableUrl});
  await shot('09-refused-frames');

  // 8. The embed page opened as the top-level page: it is not framed, so it says so, talks to nobody and takes no token.
  net.length = 0;
  await send('Page.navigate', {url: embedUrl(A)}, pageSession);
  await waitFor(() => evalHost(`document.readyState==='complete'&&!!document.getElementById('connect-error')&&!document.getElementById('connect-error').hidden`), 'top-level embed page', 15000);
  topFrameId = (await send('Page.getFrameTree', {}, pageSession)).frameTree.frame.id;
  check('the embed page opened on its own is not usable: it says it must be framed, offers no way in and requests nothing', await evalHost(`document.getElementById('embed-retry').hidden&&document.querySelector('.shell').hidden`) && net.filter(r => r.url && r.url.startsWith(B + '/api/')).length === 0, net.map(r => r.url));
  await shot('10-top-level');

} catch (e) { fatal = e; console.log('FATAL', e.stack || e.message); }

// The audit: one more full session in a page of the listed host (token handoff, query, template, snapshot), read from the network log, the storage,
// the DOM and every message the host received, with the log cleared at the start so that it covers exactly that session.
try {
  if (!fatal) {
    const templates = await api('/document-templates');
    await send('Network.clearBrowserCookies', {}, pageSession).catch(() => {});
    await openHost(A, hostQuery({integration: 'brant-contract-demo', view: 'document-designer', template: templates[0].id}));
    await connected();
    await query();
    await inFrame(`document.getElementById('doc-server-status').textContent.includes('正在编辑模板')`, 'template opened for the audit', 20000);
    await clickFrame('#snapshot'); await inFrame(`document.getElementById('result-kind').textContent==='冻结快照'&&!document.getElementById('preview').disabled`, 'frozen', 30000);
    const toPlatform = net.filter(r => r.url && !r.response && !r.failed && origin(r.url) === B);
    const toOthers = net.filter(r => r.url && !r.response && !r.failed && origin(r.url) !== B);
    const carriers = net.filter(r => r.url && !r.response && !r.failed && (r.url.includes(token) || JSON.stringify(wireHeaders(r)).includes(token) || (r.postData || '').includes(token)));
    check('the token is in no URL, no request body and no request to any origin but the platform\'s', carriers.every(r => origin(r.url) === B && !r.url.includes(token) && !(r.postData || '').includes(token)) && toOthers.every(r => !JSON.stringify(wireHeaders(r)).includes(token)), carriers.map(r => r.url));
    const apiRequests = toPlatform.filter(r => r.url.startsWith(B + '/api/reporting/v1/'));
    check('every request the frame made to the platform\'s API carried the token only as the Authorization: Bearer header', apiRequests.length >= 8 && apiRequests.every(r => { const h = wireHeaders(r); return h.authorization === 'Bearer ' + token && Object.entries(h).filter(([k, v]) => String(v).includes(token)).map(([k]) => k).join() === 'authorization'; }), apiRequests.map(r => [r.method, r.url, Object.keys(wireHeaders(r))]));
    check('no request of any frame carried a cookie, and the page set none', net.filter(r => r.url && !r.response && !r.failed).every(r => !('cookie' in wireHeaders(r))) && (await send('Network.getAllCookies', {}, pageSession)).cookies.length === 0, (await send('Network.getAllCookies', {}, pageSession)).cookies);
    check('the page opened its own origin only: no request of the frame left the platform\'s origin', net.filter(r => r.url && !r.response && !r.failed && r.frameId && r.frameId !== topFrameId).every(r => origin(r.url) === B || r.url.startsWith('data:') || r.url.startsWith('blob:')), net.filter(r => r.frameId && r.frameId !== topFrameId && origin(r.url) !== B).map(r => r.url));
    const storage = await evalFrame(`(async()=>({local:localStorage.length,session:sessionStorage.length,cookie:document.cookie,idb:(await indexedDB.databases()).length,caches:(await caches.keys()).length,sw:(await navigator.serviceWorker.getRegistrations()).length}))()`);
    check('the frame\'s storage is empty: no localStorage, sessionStorage, cookie, IndexedDB, Cache Storage or service worker', storage.local === 0 && storage.session === 0 && storage.cookie === '' && storage.idb === 0 && storage.caches === 0 && storage.sw === 0, storage);
    const dom = await evalFrame(`(()=>({html:document.documentElement.outerHTML.includes(${JSON.stringify(token)}),text:document.body.innerText.includes(${JSON.stringify(token)}),url:location.href.includes(${JSON.stringify(token)}),values:[...document.querySelectorAll('input,textarea,select')].some(e=>String(e.value).includes(${JSON.stringify(token)})),resources:performance.getEntries().some(e=>e.name.includes(${JSON.stringify(token)})),title:document.title.includes(${JSON.stringify(token)})}))()`);
    check('the token is not in the frame\'s DOM, text, form values, address, title or resource timing', !Object.values(dom).some(Boolean), dom);
    const all = await everything();
    const fromFrame = all.filter(m => m.fromFrame);
    const closed = new Set(['reporting:ready', 'reporting:token-request', 'reporting:resize', 'reporting:event', 'reporting:error']);
    check('every message the host received came from the frame at the platform\'s origin, of the closed list, version 1', fromFrame.length >= 3 && all.every(m => m.fromFrame && m.origin === B) && fromFrame.every(m => m.data.v === 1 && closed.has(m.data.type)), all.map(m => [m.origin, m.fromFrame, m.data?.type]));
    check('no message carried the token or a business value: only ids, codes, a name and a height', !JSON.stringify(all).includes(token) && !/company_demo|9007199254740993|receivables|customer|Embedded title/.test(JSON.stringify(all)) && all.every(m => Object.keys(m.data).every(k => ['type', 'v', 'height', 'name', 'template_id', 'version', 'job_id', 'code'].includes(k))), all.filter(m => !Object.keys(m.data).every(k => ['type', 'v', 'height', 'name', 'template_id', 'version', 'job_id', 'code'].includes(k))));
    check('the frame raised no exception and opened no dialog', exceptions.length === 0 && dialogs.length === 0, {exceptions, dialogs});
    const violations = logs.filter(l => /Content Security Policy|Refused to/i.test(l.text) && !(l.url || '').startsWith(A) && !(l.url || '').startsWith(C));
    check('the framed page broke no Content Security Policy rule (no inline script or style was needed)', violations.length === 0, violations);
    notes.audit = {requests: net.filter(r => r.url && !r.response && !r.failed).length, toPlatform: toPlatform.length, apiRequests: apiRequests.length, messages: all.length, storage};
  }
} catch (e) { fatal = fatal || e; console.log('FATAL (audit)', e.stack || e.message); }

if (fatal) check('the run completed', false, fatal.message);
const failed = checks.filter(c => !c.ok);
writeFileSync(join(outDir, 'report.json'), JSON.stringify({when: new Date().toISOString(), notes, checks}, null, 2));
console.log(`\n${checks.length - failed.length} of ${checks.length} checks passed`);
try { await send('Target.closeTarget', {targetId: pageTargetId}).catch(() => {}); ws?.close(); } catch {}
process.exit(failed.length ? 1 : 0);
