// S17-A: the console's Chinese source text, as the English catalog (console/assets/en.js) must cover it. Every string literal of
// the console's scripts that contains a Han character is a key; a `+` chain or a template literal that joins such text with values
// is one key with numbered placeholders ({0}, {1}, ...) where the values go. The static markup of index.html gives its text nodes
// and its aria-label, title, placeholder and alt attributes. `node scripts/i18n_extract.mjs` lists what the catalog lacks (exit 1
// when anything does); console/i18n.test.mjs holds the catalog to it.
import {readFileSync} from 'node:fs';
import {join, resolve} from 'node:path';
import {pathToFileURL} from 'node:url';
import * as espree from 'espree';
import {JSDOM} from 'jsdom';

export const HAN = /[㐀-鿿]/;
// The scripts whose text is the console's (embed.js has none; en.js and lang.js are the translation itself).
export const SCRIPTS = [
  'app.js',
  'dashboard.js',
  'canvas.js',
  'versions.js',
  'studio.js',
  'library.js',
  'issue.js',
  'documents.js',
  'board.js',
  'companies.js',
  'filters.js',
  'present.js',
];
export const ATTRIBUTES = ['aria-label', 'title', 'placeholder', 'alt'];
// The normal form of a key: what the translator looks up after trimming a text node.
export const normal = s => s.replace(/\s+/g, ' ').trim();

function walk(node, visit) {
  if (!node || typeof node.type !== 'string') return;
  if (visit(node) === false) return;
  for (const key of Object.keys(node)) {
    const value = node[key];
    if (Array.isArray(value)) value.forEach(v => walk(v, visit));
    else if (value && typeof value.type === 'string') walk(value, visit);
  }
}

// The operands of a `+` chain, template literals flattened into text and values.
function pieces(node, out) {
  if (node.type === 'BinaryExpression' && node.operator === '+') {
    pieces(node.left, out);
    // A `+` on the right was in parentheses: one value, unless it joins text too ('a' + (b + 'c')).
    const right = node.right.type === 'BinaryExpression' && node.right.operator === '+' ? pieces(node.right, []) : null;
    if (right && !right.some(p => p.text !== undefined)) out.push({value: node.right});
    else if (right) out.push(...right);
    else pieces(node.right, out);
  } else if (node.type === 'Literal' && typeof node.value === 'string') out.push({text: node.value});
  else if (node.type === 'TemplateLiteral')
    node.quasis.forEach((q, i) => {
      out.push({text: q.value.cooked});
      if (i < node.expressions.length) out.push({value: node.expressions[i]});
    });
  else out.push({value: node});
  return out;
}

function add(keys, key, kind, site) {
  key = normal(key);
  if (!HAN.test(key)) return;
  const entry = keys.get(key) || {kind, sites: []};
  entry.sites.push(site);
  keys.set(key, entry);
}

export function scriptKeys(code, file, keys = new Map()) {
  const ast = espree.parse(code, {ecmaVersion: 2024, sourceType: 'script', loc: true});
  const visit = node => {
    if ((node.type === 'BinaryExpression' && node.operator === '+') || node.type === 'TemplateLiteral') {
      const list = pieces(node, []);
      if (list.some(p => p.text !== undefined && HAN.test(p.text))) {
        let key = '',
          n = 0;
        for (const p of list) key += p.text !== undefined ? p.text : '{' + n++ + '}';
        add(keys, key, n ? 'pattern' : 'exact', file + ':' + node.loc.start.line);
      }
      // A value may hold text of its own (a conditional's branches, a call's arguments).
      for (const p of list) if (p.value) walk(p.value, visit);
      return false;
    }
    if (node.type === 'Literal' && typeof node.value === 'string')
      add(keys, node.value, 'exact', file + ':' + node.loc.start.line);
  };
  walk(ast, visit);
  return keys;
}

export function markupKeys(html, file, keys = new Map()) {
  const {document, NodeFilter} = new JSDOM(html).window;
  const texts = document.createTreeWalker(document.documentElement, NodeFilter.SHOW_TEXT);
  for (let t = texts.nextNode(); t; t = texts.nextNode()) {
    const tag = t.parentNode.tagName;
    if (tag === 'SCRIPT' || tag === 'STYLE' || t.parentNode.closest('[lang^="zh"]:not(html)')) continue;
    add(keys, t.nodeValue, 'exact', file);
  }
  for (const e of document.querySelectorAll('*')) {
    if (e.closest('[lang^="zh"]:not(html)')) continue;
    for (const a of ATTRIBUTES) if (e.hasAttribute(a)) add(keys, e.getAttribute(a), 'exact', file + ' ' + a);
  }
  // The noscript text is markup inside <noscript>, a text node to a parser that runs no script.
  for (const n of document.querySelectorAll('noscript'))
    add(keys, new JSDOM(n.textContent).window.document.body.textContent, 'exact', file + ' noscript');
  return keys;
}

export function consoleKeys(root) {
  const assets = join(root, 'console/assets');
  const keys = new Map();
  for (const name of SCRIPTS) scriptKeys(readFileSync(join(assets, name), 'utf8'), name, keys);
  markupKeys(readFileSync(join(assets, 'index.html'), 'utf8'), 'index.html', keys);
  return keys;
}

// The English catalog as the browser sees it: en.js run with a stand-in window.
export function loadCatalog(root) {
  const window = {};
  new Function('window', readFileSync(join(root, 'console/assets/en.js'), 'utf8'))(window);
  return window.reportingEnglish;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const root = resolve(import.meta.dirname, '..');
  const keys = consoleKeys(root);
  let catalog = {};
  try {
    catalog = loadCatalog(root);
  } catch (_) {}
  const missing = [...keys].filter(([k]) => !Object.hasOwn(catalog, k));
  if (process.argv.includes('--json'))
    console.log(JSON.stringify(Object.fromEntries(missing.map(([k, v]) => [k, v.sites[0]])), null, 1));
  else for (const [k, v] of missing) console.log(v.kind + '\t' + v.sites[0] + '\t' + k);
  const stale = Object.keys(catalog).filter(k => !keys.has(k));
  for (const k of stale) console.log('stale\t\t' + k);
  console.error(keys.size + ' keys, ' + missing.length + ' missing, ' + stale.length + ' stale');
  process.exit(missing.length || stale.length ? 1 : 0);
}
