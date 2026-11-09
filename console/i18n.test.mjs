// S17-A (docs/S17A_SCOPE.md): the English console. The catalog against the source (every Chinese text has its English, no key twice,
// the placeholders agree), the translator on its own, and the real console in jsdom against the real Go server in English: every page,
// shell, drawer and dialog it shows has no Chinese left in a text node, an aria-label, a title, a placeholder or an alt but the data
// the server sends. Software DOM tests, not browser/visual QA (scripts/browser_console_s06c.mjs makes the English screenshots).
import {test, before, after} from 'node:test';
import assert from 'node:assert/strict';
import {readFile, mkdtemp, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join, resolve} from 'node:path';
import {spawn, execFileSync} from 'node:child_process';
import {randomBytes, webcrypto} from 'node:crypto';
import {JSDOM, VirtualConsole} from 'jsdom';
import * as espree from 'espree';
import {consoleKeys, loadCatalog, SCRIPTS, ATTRIBUTES} from '../scripts/i18n_extract.mjs';

const root = resolve(import.meta.dirname, '..');
const assets = join(root, 'console/assets');
const html = await readFile(join(assets, 'index.html'), 'utf8');
const scripts = {};
for (const name of ['en.js', 'lang.js', ...SCRIPTS]) scripts[name] = await readFile(join(assets, name), 'utf8');
const HAN = /[㐀-鿿]/;
const placeholders = s => (s.match(/\{\d+\}/g) || []).sort();

test('S17-A: the catalog has every Chinese text of the console once, with the same placeholders and no Chinese left', () => {
  const catalog = loadCatalog(root),
    keys = consoleKeys(root);
  // No key twice: an object literal keeps the last of two equal keys silently.
  const ast = espree.parse(scripts['en.js'], {ecmaVersion: 2024, sourceType: 'script'});
  const object = ast.body.find(s => s.type === 'ExpressionStatement' && s.expression.type === 'AssignmentExpression')
    .expression.right;
  const seen = new Map();
  for (const p of object.properties) {
    const key = p.key.type === 'Literal' ? p.key.value : p.key.name;
    assert.equal(seen.has(key), false, 'twice in en.js: ' + key);
    seen.set(key, true);
  }
  const missing = [...keys.keys()].filter(k => !Object.hasOwn(catalog, k));
  assert.deepEqual(missing, [], 'Chinese text without English (node scripts/i18n_extract.mjs)');
  const stale = Object.keys(catalog).filter(k => !keys.has(k));
  assert.deepEqual(stale, [], 'English for text the console no longer has');
  for (const [key, english] of Object.entries(catalog)) {
    assert.equal(typeof english, 'string', key);
    assert.ok(english.trim(), 'empty English: ' + key);
    assert.equal(HAN.test(english), false, 'Chinese in the English of ' + key);
    assert.deepEqual(placeholders(english), placeholders(key), 'placeholders of ' + key);
    assert.equal(key, key.replace(/\s+/g, ' ').trim(), 'a key not in normal form: ' + JSON.stringify(key));
  }
  assert.ok(keys.size > 1500, 'the extraction found the console: ' + keys.size);
});

// A page with only the translator: the static markup given, the catalog and lang.js run in it.
function page(markup, search = '', storage = {}) {
  const dom = new JSDOM(
    '<!doctype html><html lang="zh-CN"><head><title>报表工作台</title></head><body>' + markup + '</body></html>',
    {
      url: 'http://127.0.0.1:1/console/' + search,
      runScripts: 'outside-only',
    }
  );
  for (const [k, v] of Object.entries(storage)) dom.window.localStorage.setItem(k, v);
  dom.window.eval(scripts['en.js']);
  dom.window.eval(scripts['lang.js']);
  return dom.window;
}
const tick = () => new Promise(resolve => setTimeout(resolve, 0));
const SWITCH =
  '<div class="lang-switch" role="group" aria-label="界面语言"><button type="button" data-lang="zh" lang="zh-CN" aria-pressed="true">中文</button><button type="button" data-lang="en" lang="en" aria-pressed="false">English</button></div>';

test('S17-A: Chinese is the default and the translator then changes nothing and stores nothing', async () => {
  const w = page(SWITCH + '<p id="p" title="刷新">报表工作台</p>');
  w.document.getElementById('p').textContent = '已取消';
  await tick();
  assert.equal(w.reportingLanguage.current, 'zh');
  assert.equal(w.document.documentElement.lang, 'zh-CN');
  assert.equal(w.document.getElementById('p').textContent, '已取消');
  assert.equal(w.document.getElementById('p').title, '刷新');
  assert.equal(w.localStorage.length, 0);
  assert.equal(w.sessionStorage.length, 0);
});

test('S17-A: ?lang=en translates the markup and what is written later, keeps names and data, and leaves the address', async () => {
  const w = page(
    SWITCH +
      '<p id="p" title="刷新">报表工作台</p><p id="q"></p><p id="r"></p><p id="s"></p><input id="i" placeholder="搜索字段">' +
      '<p id="t" translate="no">报表工作台</p><p id="u">  已取消\n</p>',
    '?lang=en#workspace'
  );
  const $ = id => w.document.getElementById(id);
  assert.equal(w.reportingLanguage.current, 'en');
  assert.equal(w.document.documentElement.lang, 'en');
  assert.equal(w.document.title, 'Report workbench');
  assert.equal(w.location.search, '');
  assert.equal(w.location.hash, '#workspace');
  assert.equal(w.localStorage.getItem('reporting-console-lang'), 'en');
  assert.equal($('p').textContent, 'Report workbench');
  assert.equal($('p').title, 'Refresh');
  assert.equal($('i').placeholder, 'Search fields');
  assert.equal($('t').textContent, '报表工作台', 'translate="no" is kept');
  assert.equal($('u').textContent, '  Cancelled\n', 'the white space around the text stays');
  assert.equal(w.document.querySelector('[data-lang="zh"]').textContent, '中文');
  assert.equal(w.document.querySelector('[data-lang="en"]').getAttribute('aria-pressed'), 'true');
  // Later writes: a message joined with values, a name between quotes (kept), a "·" line of known pieces, text that is data.
  $('q').textContent = '已保存 3 · 版本 2；再次保存会创建新版本。';
  $('r').textContent = '已取消 · 已完成 · 应收余额';
  $('s').setAttribute('aria-label', '刷新');
  await tick();
  assert.equal($('q').textContent, 'Saved 3 · version 2; saving again creates a new version.');
  assert.equal($('r').textContent, 'Cancelled · Completed · 应收余额');
  assert.equal($('s').getAttribute('aria-label'), 'Refresh');
  assert.equal(w.reportingLanguage.source($('q')), '已保存 3 · 版本 2；再次保存会创建新版本。');
  assert.equal(w.reportingLanguage.sourceAttribute($('s'), 'aria-label'), '刷新');
  // Back to Chinese: everything this script wrote is undone, the key is removed, nothing is translated after.
  w.document.querySelector('[data-lang="zh"]').click();
  assert.equal(w.reportingLanguage.current, 'zh');
  assert.equal(w.document.documentElement.lang, 'zh-CN');
  assert.equal(w.localStorage.length, 0);
  assert.equal($('p').textContent, '报表工作台');
  assert.equal($('p').title, '刷新');
  assert.equal($('q').textContent, '已保存 3 · 版本 2；再次保存会创建新版本。');
  assert.equal(w.document.title, '报表工作台');
  $('r').textContent = '已取消';
  await tick();
  assert.equal($('r').textContent, '已取消');
  // And English again, by the switch.
  w.document.querySelector('[data-lang="en"]').click();
  assert.equal($('r').textContent, 'Cancelled');
  assert.equal(w.localStorage.getItem('reporting-console-lang'), 'en');
});

test('S17-A: the stored choice is used, ?lang=zh overrides and removes it, an unknown value changes nothing', () => {
  assert.equal(page(SWITCH, '', {'reporting-console-lang': 'en'}).reportingLanguage.current, 'en');
  const zh = page(SWITCH, '?lang=zh', {'reporting-console-lang': 'en'});
  assert.equal(zh.reportingLanguage.current, 'zh');
  assert.equal(zh.localStorage.length, 0);
  const odd = page(SWITCH, '?lang=fr&x=1');
  assert.equal(odd.reportingLanguage.current, 'zh');
  assert.equal(odd.location.search, '?x=1');
  assert.equal(odd.localStorage.length, 0);
});

// ---- The console in English against the real server ----------------------------------------------------------------------------
const token = randomBytes(32).toString('hex');
let temporary, child, url;
const sleep = ms => new Promise(resolve => setTimeout(resolve, ms));
async function until(fn, label, limit = 8000) {
  const end = Date.now() + limit;
  while (Date.now() < end) {
    if (fn()) return;
    await sleep(15);
  }
  throw new Error('DOM condition not reached: ' + label);
}
before(async () => {
  temporary = await mkdtemp(join(tmpdir(), 'reporting-s17a-dom-'));
  execFileSync(
    process.env.REPORTING_GO || 'go',
    ['build', '-o', join(temporary, 'reporting-demo'), './cmd/reporting-demo'],
    {
      cwd: root,
      env: {...process.env, GOTOOLCHAIN: 'local'},
      stdio: ['ignore', 'pipe', 'pipe'],
      timeout: 120000,
    }
  );
  child = spawn(
    join(temporary, 'reporting-demo'),
    [
      '-profile',
      'contract-simulator',
      '-memory',
      '-listen',
      '127.0.0.1:0',
      '-layout-directory',
      join(temporary, 'layouts'),
      '-dashboard-directory',
      join(temporary, 'dashboards'),
      '-template-directory',
      join(temporary, 'templates'),
      '-background-directory',
      join(temporary, 'backgrounds'),
    ],
    {cwd: root, env: {...process.env, REPORTING_DEMO_TOKEN: token}, stdio: ['ignore', 'ignore', 'pipe']}
  );
  // The demo binds a free port itself and says which (S18): a port picked here and handed over could be taken in between by the demo of
  // another suite running at the same time, which then answers this suite's requests.
  url = await new Promise((resolve, reject) => {
    let seen = '';
    const timer = setTimeout(() => reject(new Error('Go fixture startup timeout')), 60000);
    child.stderr.on('data', chunk => {
      if (seen === null) return;
      seen += chunk;
      const m = /listening at (http:\/\/127\.0\.0\.1:\d+)/.exec(seen);
      if (m) {
        clearTimeout(timer);
        seen = null;
        resolve(m[1]);
      } else if (seen.length > 65536) seen = seen.slice(-4096);
    });
    child.once('exit', () => {
      clearTimeout(timer);
      if (seen !== null) reject(new Error('Go fixture did not start'));
    });
  });
  for (let i = 0; i < 100; i++) {
    try {
      if ((await fetch(url + '/console/')).ok) return;
    } catch (_) {}
    if (child.exitCode !== null) throw new Error('Go fixture did not start');
    await sleep(30);
  }
  throw new Error('Go fixture startup timeout');
});
after(async () => {
  if (child && child.exitCode === null) {
    child.kill('SIGTERM');
    await new Promise(resolve => child.once('exit', resolve));
  }
  if (temporary) await rm(temporary, {recursive: true});
});

function english(intercept, search = '?lang=en') {
  const errors = [];
  const virtualConsole = new VirtualConsole();
  virtualConsole.on('jsdomError', error => {
    if (!error.message.includes('navigation')) errors.push(error.message);
  });
  const dom = new JSDOM(html, {
    url: url + '/console/' + search,
    runScripts: 'outside-only',
    pretendToBeVisual: true,
    virtualConsole,
  });
  const w = dom.window;
  Object.defineProperty(w, 'crypto', {value: webcrypto});
  w.TextDecoder = TextDecoder;
  w.TextEncoder = TextEncoder;
  w.Blob = Blob;
  w.AbortController = AbortController;
  w.URL.createObjectURL = () => 'blob:fixture';
  w.URL.revokeObjectURL = () => {};
  w.HTMLAnchorElement.prototype.click = function () {};
  w.fetch = async (path, options) => {
    if (intercept) {
      const response = await intercept(String(path), options);
      if (response) return response;
    }
    return fetch(url + path, options);
  };
  // index.html's order: the catalog and the translator first, then the console.
  for (const name of [
    'en.js',
    'lang.js',
    'dashboard.js',
    'canvas.js',
    'versions.js',
    'studio.js',
    'library.js',
    'documents.js',
    'board.js',
    'app.js',
  ])
    w.eval(scripts[name]);
  const $ = id => w.document.getElementById(id);
  const input = (id, value) => {
    $(id).value = value;
    $(id).dispatchEvent(new w.Event('input', {bubbles: true}));
  };
  async function login() {
    input('access-token', token);
    $('connect-form').dispatchEvent(new w.Event('submit', {bubbles: true, cancelable: true}));
    await until(() => $('connection-overlay').hidden, 'login');
    await until(() => $('query-dataset').options.length > 0, 'catalog loaded');
  }
  const go = page => w.document.querySelector('[data-page="' + page + '"]').click();
  return {w, $, input, login, go, errors, close: () => w.close()};
}

// The Chinese that may stay is the data the server sends, never the console's own words: the names, fields and labels of the
// datasets of the identity's product connections and the sample pack's names and descriptions, as the API answers them. Each is
// taken out of a text before it is looked at.
async function serverData() {
  const ask = async path =>
    (await fetch(url + '/api/reporting/v1' + path, {headers: {Authorization: 'Bearer ' + token}})).json();
  const found = new Set();
  const collect = v => {
    if (typeof v === 'string') HAN.test(v) && found.add(v);
    else if (v && typeof v === 'object') Object.values(v).forEach(collect);
  };
  const session = await ask('/session');
  for (const id of session.integrations) collect(await ask('/catalog?integration_id=' + encodeURIComponent(id)));
  const samples = await ask('/document-templates/samples');
  collect(samples);
  for (const sample of samples) collect(await ask('/document-templates/samples/' + encodeURIComponent(sample.kind)));
  return [...found].sort((a, b) => b.length - a.length);
}
// Every text node and attribute of the page with Chinese in it once the data is taken out, but those inside translate="no" or
// lang="zh…" (the switch's 中文), in a script or a textarea (the template's JSON).
function chinese(w, data) {
  const left = [];
  const own = el =>
    !el.closest('script,style,textarea,[translate="no"]') &&
    !(el.closest('[lang]') !== w.document.documentElement && /^zh/.test(el.closest('[lang]')?.lang || ''));
  const squeeze = t => t.replace(/\s+/g, '');
  const check = (where, text) => {
    // A line of a text the canvas wraps is a piece of the data.
    if (data.some(d => squeeze(d).includes(squeeze(text)))) return;
    let rest = text;
    for (const d of data) rest = rest.split(d).join('');
    if (HAN.test(rest)) left.push(where + ' ' + text.trim());
  };
  const walker = w.document.createTreeWalker(w.document.documentElement, w.NodeFilter.SHOW_TEXT);
  for (let t = walker.nextNode(); t; t = walker.nextNode())
    if (HAN.test(t.nodeValue) && own(t.parentElement)) check(t.parentElement.tagName, t.nodeValue);
  for (const el of w.document.querySelectorAll('*'))
    for (const a of ATTRIBUTES)
      if (HAN.test(el.getAttribute(a) || '') && own(el)) check(el.tagName + ' @' + a, el.getAttribute(a));
  return left;
}
const settle = () => sleep(60);

test('S17-A: the console in English, every page, shell, drawer and dialog: no Chinese but the data', async () => {
  const a = english();
  const seen = new Map(),
    data = await serverData();
  assert.ok(data.length > 20, 'the server sends Chinese data');
  const look = async where => {
    await settle();
    for (const s of chinese(a.w, data)) if (!seen.has(s)) seen.set(s, where);
  };
  try {
    await until(
      () => a.$('connect-title').textContent === 'Enter the reporting workspace',
      'the sign-in card in English'
    );
    await look('sign-in');
    await a.login();
    await look('workspace');
    a.go('workspace');
    a.input('query-companies', 'company_demo');
    a.$('preview').click();
    await until(() => !a.$('result-table-wrap').hidden, 'preview');
    await look('preview');
    a.$('snapshot').click();
    await until(() => !a.$('export').disabled, 'snapshot', 12000);
    await look('snapshot');
    a.$('sh-data-chip').click();
    await look('data drawer');
    a.w.document.querySelector('[data-panel="tasks"]').click();
    await look('tasks panel');
    for (const p of ['dashboard', 'documents', 'library', 'schedules', 'audit', 'admin', 'system']) {
      a.go(p);
      await sleep(400);
      await look(p);
    }
    // The dashboard studio with its three kinds of component.
    a.go('dashboard');
    for (const id of ['board-add-metric', 'board-add-table', 'board-add-bar']) a.$(id).click();
    await look('dashboard with components');
    // A refusal told in a notice: a company scope the identity may not read.
    a.go('workspace');
    a.input('query-companies', 'company_other');
    a.$('preview').click();
    await until(() => !a.$('notice').hidden, 'the refusal');
    await look('refusal');
    a.input('query-companies', 'company_demo');
    a.$('preview').click();
    await until(() => !a.$('result-table-wrap').hidden, 'preview again');
    // The template centre, its wizard, a sample's mapping dialog, and the copy open in the document studio.
    const lib = a.$('page-library'),
      dialog = () => lib.querySelector('.overlay.open [role="dialog"]');
    a.go('library');
    await until(() => lib.querySelectorAll('.sample-card').length > 10, 'the template centre', 12000);
    await look('library');
    a.$('lib-new').click();
    await until(dialog, 'the wizard');
    await look('wizard');
    dialog().dispatchEvent(new a.w.KeyboardEvent('keydown', {key: 'Escape', bubbles: true}));
    await until(() => !dialog(), 'the wizard closed');
    lib.querySelector('.sample-card .btn.primary').click();
    await until(dialog, 'the mapping dialog');
    await look('mapping dialog');
    const name = dialog().querySelector('input[type=text]');
    name.value = 'S17A english ' + Date.now();
    [...dialog().querySelectorAll('.btn.primary')].pop().click();
    await until(
      () =>
        !a.$('page-documents').hidden &&
        a.w.reportingLanguage.source(a.$('doc-server-status')).includes('正在编辑模板 '),
      'the copy open in the studio',
      12000
    );
    await look('studio with a template');
    const item = a.$('doc-canvas-host').querySelector('[data-key]');
    if (item) {
      item.dispatchEvent(new a.w.MouseEvent('pointerdown', {bubbles: true}));
      item.dispatchEvent(new a.w.MouseEvent('click', {bubbles: true}));
    }
    await look('studio with a selection');
    for (const tab of a.w.document.querySelectorAll('#page-documents [role="tab"]')) {
      tab.click();
      await look('studio tab ' + tab.id);
    }
    // S18-A: a schedule made from the current result, its dialog, the list and its runs.
    a.go('workspace');
    a.$('preview').click();
    await until(() => !a.$('schedule-from-result').disabled, 'a result to schedule');
    a.$('schedule-from-result').click();
    await until(() => !a.$('sc-d-new').hidden, 'the schedule dialog');
    a.$('sc-frequency').value = 'monthly';
    a.$('sc-frequency').dispatchEvent(new a.w.Event('change', {bubbles: true}));
    await look('schedule dialog');
    a.$('sc-create').click();
    await until(() => !a.$('page-schedules').hidden && a.$('schedules-body').rows.length === 1, 'the schedule');
    await look('schedules with one');
    a.$('schedules-body').querySelector('button').click();
    await until(() => !a.$('schedule-runs-panel').hidden, 'its runs');
    await look('schedule runs');
    a.go('audit');
    await sleep(400);
    await look('audit after actions');
    assert.deepEqual(
      [...seen].map(([s, where]) => where + ': ' + s),
      [],
      'Chinese left in the English console'
    );
    assert.equal(a.w.document.documentElement.lang, 'en');
    assert.deepEqual(a.errors, []);
  } finally {
    a.close();
  }
});
