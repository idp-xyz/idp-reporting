'use strict';
// S17-A (docs/S17A_SCOPE.md): the console in English. The console's code and markup stay in Chinese, its source language; en.js is
// a catalog keyed by that Chinese text, and this script swaps the text for its English as it reaches the page: the static markup
// when the page loads, and everything the console writes afterwards through a MutationObserver (text nodes and the aria-label,
// title, placeholder and alt attributes). Chinese is the default and does nothing here at all: no observer, no change to the page.
// The choice is ?lang=en or ?lang=zh in the address (read once, then taken out of it) or the switch in the console's header and
// sign-in card, kept in the browser's local storage under one key, written only when English is chosen and removed when Chinese
// is. A subtree marked translate="no" or lang="zh…" (the switch's own 中文, the document on the studio's canvas) stays as it is.
// window.reportingLanguage.source(node) gives back the Chinese a node was written with, for the code that compares the page's text.
(function () {
  const KEY = 'reporting-console-lang',
    HAN = /[\u3400-\u9fff]/,
    ATTRIBUTES = ['aria-label', 'title', 'placeholder', 'alt'],
    catalog = window.reportingEnglish || {};
  const exact = new Map(),
    patterns = [];
  // A key with {0}, {1}, ... is a pattern: its text between the placeholders must match, the values are translated in turn
  // (except one between quotes: a name the person gave, kept as written).
  for (const key of Object.keys(catalog)) {
    if (!/\{\d+\}/.test(key)) {
      exact.set(key, catalog[key]);
      continue;
    }
    const parts = key.split(/(\{\d+\})/),
      slots = [];
    let source = '^',
      longest = '';
    parts.forEach((p, i) => {
      const slot = /^\{(\d+)\}$/.exec(p);
      if (slot) {
        slots.push({n: slot[1], quoted: /[「“《‘"]$/.test(source.slice(-1))});
        // A value right before another holds no Chinese sign ("填充 {2}{3}": the fill, then "，x …").
        source += parts[i + 1] === '' && /^\{\d+\}$/.test(parts[i + 2] || '') ? '([^，；：、。]*)' : '([\\s\\S]*?)';
      } else {
        source += p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
        if (p.length > longest.length) longest = p;
      }
    });
    patterns.push({
      regex: new RegExp(source + '$'),
      slots,
      english: catalog[key],
      longest,
      weight: key.replace(/\{\d+\}/g, '').length,
    });
  }
  patterns.sort((a, b) => b.weight - a.weight);
  // A piece the console joins after a separator ("· 尚未保存到服务器") is also known without it, for a line split at that separator.
  for (const [key, english] of [...exact]) {
    const bare = key.replace(/^[·；，、]\s*/, '');
    if (bare !== key && HAN.test(bare) && !exact.has(bare)) exact.set(bare, english.replace(/^\s*[·;,]\s*/, ''));
  }
  // The console's words that may start or end a text joined with something else ("锁定 n1", "固定文字 “…”", "账龄（明细级）"), longest first.
  const affixes = [...exact.keys()].sort((a, b) => b.length - a.length);
  const SEPARATORS = [' · ', '；', '：', '，', '、', ' / '],
    SEPARATOR_ENGLISH = {' · ': ' · ', ' / ': ' / ', '；': '; ', '：': ': ', '，': ', ', '、': ', '};
  const cache = new Map();

  // A pattern's values translated; null when a value (not between quotes) is still Chinese after it, unless loose.
  function fill(p, m, loose) {
    let out = p.english;
    for (let i = 0; i < p.slots.length; i++) {
      const slot = p.slots[i],
        value = slot.quoted ? m[i + 1] : translate(m[i + 1]);
      if (!loose && !slot.quoted && HAN.test(value)) return null;
      out = out.split('{' + slot.n + '}').join(value);
    }
    return out;
  }
  const hans = s => (s.match(/[\u3400-\u9fff]/g) || []).length;
  // Text between quotes is a name or a text the person wrote: hidden from the splitting below and given back as it was.
  const QUOTED = /“[^“”]*”|「[^「」]*」/g;
  function joined(s) {
    for (const sep of SEPARATORS) {
      if (!s.includes(sep)) continue;
      const parts = s.split(sep),
        english = parts.map(p => translate(p));
      // Only when a piece is the console's: a sentence of the data stays whole.
      if (english.every((p, i) => p === parts[i])) return null;
      return english.join(SEPARATOR_ENGLISH[sep]).replace(/\s+$/, '');
    }
    return null;
  }
  // The console's words at the start or the end, taken only where the word ends (a space, a sign, anything but another Han
  // character) and when it is a piece of a message (it starts or ends with a sign), stands apart by a space, or leaves no Chinese:
  // a sentence of the data that merely contains a word of the console stays whole.
  function affixed(s) {
    const piece = (k, rest, spaced) => spaced || !HAN.test(k[0]) || !HAN.test(k[k.length - 1]) || !HAN.test(rest);
    for (const k of affixes) {
      if (k.length >= s.length) continue;
      if (s.startsWith(k) && (!HAN.test(s[k.length]) || !HAN.test(k[k.length - 1]))) {
        const rest = translate(s.slice(k.length).trim()),
          spaced = /^\s/.test(s.slice(k.length));
        if ((k.length > 1 || spaced) && piece(k, rest, spaced)) return exact.get(k).trim() + (spaced ? ' ' : '') + rest;
      }
      if (s.endsWith(k) && (!HAN.test(s[s.length - k.length - 1]) || !HAN.test(k[0]))) {
        const rest = translate(s.slice(0, -k.length)),
          spaced = /\s$/.test(s.slice(0, -k.length));
        if ((k.length > 1 || spaced) && piece(k, rest, spaced)) return rest + exact.get(k);
      }
    }
    return null;
  }
  // One text: the catalog's own entry; else the most specific pattern whose values all translate; else the best of that pattern
  // with a value that is data, the text split at its coarsest separator, and a word of the console at either end.
  function phrase(s) {
    if (exact.has(s)) return exact.get(s);
    let loose = null;
    for (const p of patterns) {
      if (p.longest && !s.includes(p.longest.trim())) continue;
      const m = p.regex.exec(s);
      if (!m) continue;
      const out = fill(p, m, false);
      if (out !== null) return out;
      if (loose === null) loose = fill(p, m, true);
    }
    const quoted = [];
    const masked = s.replace(QUOTED, q => '\u0001' + (quoted.push(q) - 1) + '\u0002'),
      unmask = t => t && t.replace(/\u0001(\d+)\u0002/g, (_, i) => quoted[i]);
    let best = s;
    for (const out of [loose, unmask(joined(masked)), unmask(affixed(masked))])
      if (out !== null && hans(out) < hans(best)) best = out;
    return best;
  }
  // translate(text): the English of a text, its surrounding white space kept; text that is not the console's stays as it is.
  function translate(text) {
    if (!text || !HAN.test(text)) return text;
    let hit = cache.get(text);
    if (hit !== undefined) return hit;
    const m = /^(\s*)([\s\S]*?)(\s*)$/.exec(text);
    hit = m[1] + phrase(m[2].replace(/\s+/g, ' ')) + m[3];
    if (cache.size > 20000) cache.clear();
    cache.set(text, hit);
    return hit;
  }

  // What this script wrote, so that it can be undone and told apart from what the console wrote since.
  const texts = new WeakMap(),
    attributes = new WeakMap();
  let lang = 'zh',
    observer = null;
  const skipped = el =>
    !el ||
    !!el.closest('script,style,textarea,[translate="no"],[contenteditable="true"]') ||
    !!(
      el.closest('[lang]') &&
      el.closest('[lang]') !== document.documentElement &&
      /^zh/.test(el.closest('[lang]').lang)
    );
  function textNode(t) {
    const value = t.nodeValue,
      rec = texts.get(t);
    if (rec && rec.out === value) return;
    if (!HAN.test(value) || skipped(t.parentElement)) return;
    const out = translate(value);
    if (out === value) return;
    texts.set(t, {src: value, out});
    t.nodeValue = out;
  }
  function attribute(el, name) {
    const value = el.getAttribute(name);
    if (value === null || !HAN.test(value) || skipped(el)) return;
    let recs = attributes.get(el);
    if (recs && recs[name] && recs[name].out === value) return;
    const out = translate(value);
    if (out === value) return;
    if (!recs) attributes.set(el, (recs = {}));
    recs[name] = {src: value, out};
    el.setAttribute(name, out);
  }
  function tree(root) {
    if (root.nodeType === 3) return textNode(root);
    if (root.nodeType !== 1 && root.nodeType !== 9 && root.nodeType !== 11) return;
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let t = walker.nextNode(); t; t = walker.nextNode()) textNode(t);
    const elements = root.querySelectorAll ? root.querySelectorAll('[aria-label],[title],[placeholder],[alt]') : [];
    for (const el of [root, ...elements]) if (el.getAttribute) for (const a of ATTRIBUTES) attribute(el, a);
  }
  function restore(root) {
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let t = walker.nextNode(); t; t = walker.nextNode()) {
      const rec = texts.get(t);
      if (rec && rec.out === t.nodeValue) t.nodeValue = rec.src;
      texts.delete(t);
    }
    for (const el of root.querySelectorAll('[aria-label],[title],[placeholder],[alt]')) {
      const recs = attributes.get(el);
      if (!recs) continue;
      for (const a of Object.keys(recs)) if (el.getAttribute(a) === recs[a].out) el.setAttribute(a, recs[a].src);
      attributes.delete(el);
    }
  }
  function observe(records) {
    for (const r of records) {
      if (r.type === 'characterData') textNode(r.target);
      else if (r.type === 'attributes') attribute(r.target, r.attributeName);
      else r.addedNodes.forEach(n => n.isConnected && tree(n));
    }
  }
  // The Chinese a node (or an element's text) was written with, whatever language the page shows.
  function source(n) {
    if (!n) return '';
    if (n.nodeType === 3) {
      const rec = texts.get(n);
      return rec && rec.out === n.nodeValue ? rec.src : n.nodeValue;
    }
    return [...n.childNodes].map(c => (c.nodeType === 1 || c.nodeType === 3 ? source(c) : '')).join('');
  }
  function sourceAttribute(el, name) {
    const value = el.getAttribute(name),
      recs = attributes.get(el);
    return recs && recs[name] && recs[name].out === value ? recs[name].src : value;
  }
  function stored(next) {
    try {
      if (next === 'en') localStorage.setItem(KEY, 'en');
      else if (next === 'zh') localStorage.removeItem(KEY);
      else return localStorage.getItem(KEY) === 'en' ? 'en' : 'zh';
    } catch (_) {}
    return 'zh';
  }
  function switches() {
    for (const b of document.querySelectorAll('.lang-switch [data-lang]'))
      b.setAttribute('aria-pressed', b.getAttribute('data-lang') === lang ? 'true' : 'false');
  }
  function apply(next) {
    if (next === lang) return switches();
    lang = next;
    if (lang === 'en') {
      document.documentElement.lang = 'en';
      tree(document.documentElement);
      observer = new MutationObserver(observe);
      observer.observe(document.documentElement, {
        subtree: true,
        childList: true,
        characterData: true,
        attributes: true,
        attributeFilter: ATTRIBUTES,
      });
    } else {
      if (observer) observer.disconnect();
      observer = null;
      restore(document.documentElement);
      document.documentElement.lang = 'zh-CN';
    }
    switches();
  }
  function choose(next) {
    stored(next);
    apply(next);
  }
  // The address decides once: ?lang=en|zh is kept as the choice and taken out of the address (the sign-in compares the page's
  // address with its registered one, and a bookmark should not carry it).
  let chosen = stored();
  try {
    const params = new URLSearchParams(location.search);
    const asked = params.get('lang');
    if (asked === 'en' || asked === 'zh') {
      chosen = asked;
      stored(asked);
    }
    if (params.has('lang') && window.history && history.replaceState) {
      params.delete('lang');
      const rest = params.toString();
      history.replaceState(history.state, '', location.pathname + (rest ? '?' + rest : '') + location.hash);
    }
  } catch (_) {}
  document.addEventListener('click', e => {
    const b = e.target.closest && e.target.closest('.lang-switch [data-lang]');
    if (b) choose(b.getAttribute('data-lang') === 'en' ? 'en' : 'zh');
  });
  window.reportingLanguage = {
    get current() {
      return lang;
    },
    choose,
    translate: text => (lang === 'en' ? translate(text) : text),
    source,
    sourceAttribute,
  };
  apply(chosen);
})();
