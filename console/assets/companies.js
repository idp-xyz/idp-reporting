'use strict';
// The company picker (S09-D, ADR 0015 decision 4): the 公司范围 field of the query panel, in the console and in the embed page alike
// (both run app.js, which owns the request and hands the answer here). A source product that lists the companies the principal may
// choose (GET /companies?integration_id=…, ADR 0015 decision 3) turns the text field of ids into a list of the companies by name, the
// id as secondary text (ADR 0014 decision 7): a filter when there are more than eight, the selection bounded by the query's sixteen
// companies (the sixteenth chosen disables the rest and a line says why), one listed company chosen already (a forced choice is not
// asked of the person). With `partial` the text field stays under the list as 输入其他公司 ID… and its ids join the selection. Without
// the capability (`supported: false`), or for any answer that is not exactly the documented shape (unknown keys, more than 1000 entries,
// an id or a name outside 1–128 characters or holding a control character, an id twice), or for the route's failure, the field stays the
// text field of ids exactly as before: the picker fails closed to the old behaviour. The list is advisory (decision 2): the query sends
// ids (`company_ids`), PrepareRead authorizes every read as it did, and a company the source refuses is refused as before. Nothing is
// stored: the directory lives in memory for the session, and app.js forgets it at sign-out.
//
// Ports (app.js): changed() → the selection or the typed ids changed (the host clears its result as for any change of the query).
// The host asks: check(answer) → the verified directory ({supported: false}, or {supported: true, partial, companies}) or null for an
// answer that does not verify; show(product, directory) → the field for this product (null: the text field); ids() → the chosen ids,
// in the list's order, then the typed ones; text(ids, product) → the chip's words for these ids (names where the product's directory
// has them, ids otherwise; more than two: the first and the count); focus() → the control the data drawer focuses when it opens;
// disable(flag) → the controls follow the query panel's busy state; clear() → sign-out: the text field, nothing kept.
window.createReportingCompanyPicker = function (ports) {
  const $ = id => document.getElementById(id);
  const input = $('query-companies');
  if (!input) return null;
  const field = input.parentElement;
  const label = field.querySelector('label[for="query-companies"]');
  const hint = field.querySelector('small');
  const limit = 16; // the query's limit of companies (app.js buildQuery, docs/API.md)
  const node = (tag, text, className) => {
    const n = document.createElement(tag);
    if (text !== undefined) n.textContent = text;
    if (className) n.className = className;
    return n;
  };
  const typedPlaceholder = input.placeholder;

  // ---- the picker's nodes, built once and hidden until a directory arrives ----------------------------------------------------------
  const picker = node('div', undefined, 'company-picker');
  picker.id = 'query-companies-picker';
  picker.setAttribute('role', 'group');
  if (label) {
    if (!label.id) label.id = 'query-companies-label';
    picker.setAttribute('aria-labelledby', label.id);
  }
  picker.hidden = true;
  const filter = node('input', undefined, 'company-filter');
  filter.id = 'query-companies-filter';
  filter.type = 'search';
  filter.autocomplete = 'off';
  filter.setAttribute('aria-label', '筛选公司');
  filter.hidden = true;
  const list = node('ul', undefined, 'company-list');
  list.id = 'query-companies-list';
  const note = node('p', '', 'company-note help');
  note.id = 'query-companies-note';
  note.setAttribute('aria-live', 'polite');
  note.hidden = true;
  picker.append(filter, list, note);
  input.before(picker);

  let product = '',
    directory = null, // the verified directory of the product on show, or null (the text field)
    disabled = false;
  const picking = () => !!directory && directory.supported === true;
  const boxes = () => Array.from(list.querySelectorAll('input[type=checkbox]'));
  const checked = () => boxes().filter(b => b.checked);

  // ---- the answer's shape (ADR 0015 decision 3): exactly the documented one, or nothing ------------------------------------------------
  const plain = s =>
    typeof s === 'string' && s.length >= 1 && s.length <= 128 && s.trim().length > 0 && !/[\x00-\x1f\x7f]/.test(s);
  function check(v) {
    if (!v || typeof v !== 'object' || Array.isArray(v)) return null;
    if (Object.keys(v).sort().join() !== 'companies,partial,supported') return null;
    if (typeof v.supported !== 'boolean' || typeof v.partial !== 'boolean' || !Array.isArray(v.companies)) return null;
    if (!v.supported) return v.partial || v.companies.length ? null : {supported: false};
    if (v.companies.length > 1000) return null;
    const seen = new Set(),
      companies = [];
    for (const c of v.companies) {
      if (!c || typeof c !== 'object' || Array.isArray(c) || Object.keys(c).sort().join() !== 'id,name') return null;
      if (!plain(c.id) || !plain(c.name) || seen.has(c.id)) return null;
      seen.add(c.id);
      companies.push({id: c.id, name: c.name});
    }
    return {supported: true, partial: v.partial, companies};
  }

  // ---- rendering --------------------------------------------------------------------------------------------------------------------
  function option(c) {
    const item = node('li', undefined, 'company-option');
    const row = node('label');
    const box = node('input');
    box.type = 'checkbox';
    box.value = c.id;
    box.addEventListener('change', () => {
      bound();
      ports.changed();
    });
    row.append(box, node('span', c.name, 'company-name'), node('small', c.id, 'company-id'));
    item.append(row);
    return item;
  }
  // The bound of sixteen: once sixteen are chosen the rest cannot be, and the line says why; the busy state disables everything.
  function bound() {
    const full = checked().length >= limit;
    for (const b of boxes()) {
      b.disabled = disabled || (!b.checked && full);
      b.closest('.company-option').classList.toggle('is-disabled', b.disabled);
    }
    filter.disabled = disabled;
    input.disabled = disabled;
    if (!picking()) return;
    const none = directory.companies.length === 0;
    let text = '';
    if (none) text = directory.partial ? '来源没有列出公司；可在下方输入公司 ID。' : '当前身份没有可选择的公司。';
    else if (full) text = '已选 ' + limit + ' 家，达到一次查询的上限；取消一家才能再选其他公司。';
    else if (directory.partial) text = '来源的名单不完整：名单之外的公司可在下方输入 ID。';
    note.textContent = text;
    note.hidden = !text;
  }
  function applyFilter() {
    const needle = filter.value.trim().toLowerCase();
    let shown = 0;
    for (const item of list.querySelectorAll('.company-option')) {
      const hit =
        !needle ||
        item.querySelector('.company-name').textContent.toLowerCase().includes(needle) ||
        item.querySelector('.company-id').textContent.toLowerCase().includes(needle);
      item.hidden = !hit;
      if (hit) shown++;
    }
    const empty = list.querySelector('.company-none');
    if (empty) empty.hidden = shown > 0 || !needle;
  }
  filter.addEventListener('input', applyFilter);
  function render() {
    list.replaceChildren();
    filter.value = '';
    if (!picking()) {
      picker.hidden = true;
      input.hidden = false;
      input.placeholder = typedPlaceholder;
      input.removeAttribute('aria-label');
      if (hint) hint.hidden = false;
      note.hidden = true;
      filter.hidden = true;
      return;
    }
    const companies = directory.companies;
    for (const c of companies) list.append(option(c));
    if (companies.length) list.append(node('li', '没有匹配的公司。', 'company-none help'));
    applyFilter();
    filter.hidden = companies.length <= 8;
    filter.placeholder = '在 ' + companies.length + ' 家公司中筛选（名称或 ID）';
    // One listed company is the only choice: it is made for the person, who may still unmake it.
    if (companies.length === 1) boxes()[0].checked = true;
    picker.hidden = false;
    if (hint) hint.hidden = true;
    input.hidden = !directory.partial;
    if (directory.partial) {
      input.placeholder = '输入其他公司 ID，多个以逗号分隔';
      input.setAttribute('aria-label', '输入其他公司 ID');
    } else {
      input.placeholder = typedPlaceholder;
      input.removeAttribute('aria-label');
    }
    bound();
  }

  // ---- the host's ports ---------------------------------------------------------------------------------------------------------------
  function show(id, verified) {
    product = id;
    directory = verified && typeof verified === 'object' ? verified : null;
    input.value = '';
    render();
  }
  const typed = () =>
    input.value
      .split(/[,，]/)
      .map(s => s.trim())
      .filter(Boolean);
  function ids() {
    if (!picking()) return typed();
    const chosen = checked().map(b => b.value);
    return directory.partial ? chosen.concat(typed().filter(id => !chosen.includes(id))) : chosen;
  }
  function name(id, of) {
    if (!picking() || (of !== undefined && of !== product)) return undefined;
    const known = directory.companies.find(c => c.id === id);
    return known ? known.name : undefined;
  }
  function text(values, of) {
    const names = (values || []).map(id => name(id, of) || id);
    if (names.length <= 2) return names.join('、');
    const count = '共 ' + names.length + ' 家';
    return names[0] + ' · ' + count;
  }
  // The data drawer's first focus (S07-A): what the person has to do next — the company to choose, or 查询预览 once one is chosen.
  function focus() {
    const preview = $('preview');
    if (!picking()) return input.value ? preview : input;
    if (checked().length || (directory.partial && input.value.trim())) return preview;
    if (!filter.hidden) return filter;
    const first = boxes().find(b => !b.disabled);
    return first || (!input.hidden ? input : preview);
  }
  function disable(flag) {
    disabled = !!flag;
    bound();
  }
  function clear() {
    product = '';
    directory = null;
    input.value = '';
    render();
  }
  return {limit, check, show, ids, name, text, focus, disable, clear, picking};
};
