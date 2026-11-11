'use strict';
// The filters and the sort of the query panel (S20-B, ADR 0023 decision 5; docs/S20B_SCOPE.md), in the console and in the embed page
// alike (both run app.js, which builds the query and tells this script which dataset is on show). The catalogue declares, per dataset
// version, which columns a query may narrow by and order by and how (S20-A: `filters: [{field, operators, values?, required?}]`,
// `sorts: [{field, directions}]`), and the two blocks are built from that declaration and from nothing else:
//   · 筛选 — a row per filter: the field by its label with the id as secondary text (ADR 0014 decision 7), the operator by a word
//     (only the operators the declaration gives the field; a single one is plain text, not a choice), the value by the column's type
//     and the operator: a date field, a number as text (the exact string is sent, never a float), a list of the closed values when the
//     declaration has them (one of them for 等于 / 不等于, any of them for 属于), 是 / 否 for a boolean, text otherwise; 介于 takes two
//     values, 属于 without closed values one value a line. A field has at most one row: 添加筛选 offers the fields that have none, a
//     `required` field's row is there from the start and has no 移除. At most 16 rows, the server's bound.
//   · 排序 — up to three keys in the order they apply, each a declared field once, 升序 / 降序 as declared.
// A dataset that declares neither shows no block, one that declares only one of them shows that one (ADR 0014 decision 4: a control
// that can never work is not shown). The rows start over whenever the dataset on show changes.
// The console validates to help, never to authorize: what it can see is wrong (a value missing, a number not in the column's form,
// 介于 out of order, more than 32 values) is said before the query is sent (reason()), by the server's own rules (reporting/filters.go:
// ValidFilterValue, CompareValues), so that nothing the server accepts is held back here; everything else is the server's 422.
// The wire form is the server's (docs/API.md "Filters and sort"): `{field, operator, value}`, the values of 属于 and 介于 as a JSON
// array of strings in `value`; `{field, direction}`.
//
// Ports (app.js): changed() → a filter or a sort key changed (the host clears its result as for any change of the query).
// The host asks: check(dataset) → the dataset's declaration verified by ADR 0023 decision 1's rules ({filters, sorts}; both empty
// without a declaration) or null for one that breaks them; show(dataset) → the blocks for this dataset (null: none); filters() and
// sort() → the query's two lists; reason() → what the console can see is wrong, or ''; invalid() → the control to fill for it;
// words(query, columns) → a query's narrowing in words, for the chip, the result and the schedule; disable(flag) → the controls
// follow the query panel's state; clear() → sign-out.
window.createReportingFilters = function (ports) {
  const $ = id => document.getElementById(id);
  const block = $('query-narrow');
  if (!block) return null;
  const filterGroup = $('query-filters'),
    filterList = $('query-filter-rows'),
    filterAddLine = $('query-filter-add-line'),
    filterAdd = $('query-filter-add'),
    sortGroup = $('query-sorts'),
    sortList = $('query-sort-rows'),
    sortAddLine = $('query-sort-add-line'),
    sortAdd = $('query-sort-add');
  // The bounds of ADR 0023 decisions 1 and 2 (reporting/filters.go).
  const MAX_FILTERS = 16, // declared filters of a dataset version, and filters of one query
    MAX_SORTS = 8, // declared sorts of a dataset version
    MAX_SORT_KEYS = 3, // sort keys of one query
    MAX_IN = 32, // values of one 属于
    MAX_CLOSED = 64, // closed values of one declared filter
    MAX_CHARS = 128; // characters of one value
  const OPERATORS = ['eq', 'ne', 'lt', 'le', 'gt', 'ge', 'in', 'between', 'prefix', 'contains'];
  const operatorWords = {
    eq: '等于',
    ne: '不等于',
    lt: '小于',
    le: '不大于',
    gt: '大于',
    ge: '不小于',
    in: '属于',
    between: '介于',
    prefix: '开头为',
    contains: '包含',
  };
  const DIRECTIONS = ['asc', 'desc'];
  const directionWords = {asc: '升序', desc: '降序'};
  const node = (tag, text, className) => {
    const n = document.createElement(tag);
    if (text !== undefined) n.textContent = text;
    if (className) n.className = className;
    return n;
  };
  // An icon of the shell's sprite (index.html and embed.html hold the same symbols).
  const icon = name => {
    const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    s.setAttribute('class', 'ic');
    s.setAttribute('aria-hidden', 'true');
    s.setAttribute('focusable', 'false');
    const u = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    u.setAttribute('href', '#s-' + name);
    s.append(u);
    return s;
  };

  // ---- the server's rules, as the console can apply them (reporting/filters.go, reporting/validation.go) ----------------------------
  // An operator fits a column's type: 等于 / 不等于 every type the others fit and a boolean; 开头为 / 包含 a string only; the rest a
  // date, an integer, a decimal or a string.
  const ordered = type => ['date', 'integer', 'decimal', 'string'].includes(type);
  const fits = (op, type) =>
    op === 'eq' || op === 'ne'
      ? type === 'boolean' || ordered(type)
      : op === 'prefix' || op === 'contains'
        ? type === 'string'
        : ordered(type);
  const INTEGER = /^-?(0|[1-9][0-9]*)$/;
  function validDate(v) {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v);
    if (!m) return false;
    const year = Number(m[1]),
      month = Number(m[2]),
      day = Number(m[3]);
    if (month < 1 || month > 12 || day < 1) return false;
    const leap = (year % 4 === 0 && year % 100 !== 0) || year % 400 === 0;
    return day <= [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1];
  }
  // What is wrong with one value for a column, by the server's ValidFilterValue: '' when it is well formed; else which rule it
  // breaks. A date is YYYY-MM-DD; an integer and a decimal are the exact strings the column writes (the decimal with exactly its
  // scale), at most 128 characters; a boolean true or false; a string 1..128 characters (code points) without a control character.
  function flaw(v, column) {
    if (typeof v !== 'string') return 'type';
    if (column.type === 'integer' || (column.type === 'decimal' && !column.scale))
      return v.length <= MAX_CHARS && INTEGER.test(v) ? '' : 'integer';
    if (column.type === 'decimal') {
      const parts = v.split('.');
      return v.length <= MAX_CHARS &&
        parts.length === 2 &&
        parts[1].length === column.scale &&
        /^[0-9]+$/.test(parts[1]) &&
        INTEGER.test(parts[0])
        ? ''
        : 'decimal';
    }
    if (column.type === 'date') return validDate(v) ? '' : 'date';
    if (column.type === 'boolean') return v === 'true' || v === 'false' ? '' : 'boolean';
    if (column.type !== 'string') return 'type';
    if (/[\u0000-\u001f\u007f-\u009f\ufffe\uffff]/.test(v)) return 'control';
    const length = Array.from(v).length;
    return length < 1 ? 'empty' : length > MAX_CHARS ? 'long' : '';
  }
  // The order of two well-formed values of a column, as the server's CompareValues: an integer or a decimal by its exact value
  // (the digits without the point, never through a float), anything else as text by code point (the order of the UTF-8 bytes).
  function compare(column, a, b) {
    if (column.type === 'integer' || column.type === 'decimal') {
      try {
        const x = BigInt(a.replace('.', '')),
          y = BigInt(b.replace('.', ''));
        return x < y ? -1 : x > y ? 1 : 0;
      } catch (_) {}
    }
    const x = Array.from(a),
      y = Array.from(b);
    for (let i = 0; i < x.length && i < y.length; i++) {
      const d = x[i].codePointAt(0) - y[i].codePointAt(0);
      if (d) return d < 0 ? -1 : 1;
    }
    return x.length < y.length ? -1 : x.length > y.length ? 1 : 0;
  }
  // The declaration of one dataset version, held to ADR 0023 decision 1 as the server holds it (validDeclaration): at most 16 filters
  // and 8 sorts, each on a column of the dataset and a column at most once in each list; operators a non-empty set of the closed set
  // that fit the column's type; closed values at most 64, distinct and well formed for the column; directions a non-empty set of asc
  // and desc. null for a declaration that breaks a rule: the console builds nothing on it (app.js refuses the catalogue, as it
  // refuses a malformed column).
  function check(d) {
    if (!d || typeof d !== 'object' || !Array.isArray(d.columns)) return null;
    const raw = {filters: d.filters === undefined ? [] : d.filters, sorts: d.sorts === undefined ? [] : d.sorts};
    if (!Array.isArray(raw.filters) || raw.filters.length > MAX_FILTERS) return null;
    if (!Array.isArray(raw.sorts) || raw.sorts.length > MAX_SORTS) return null;
    const columns = new Map();
    for (const c of d.columns)
      if (c && typeof c.id === 'string')
        columns.set(c.id, {
          id: c.id,
          label: typeof c.label === 'string' && c.label ? c.label : c.id,
          type: c.type,
          scale: Number.isSafeInteger(c.scale) && c.scale > 0 ? c.scale : 0,
        });
    const distinct = list => Array.isArray(list) && new Set(list).size === list.length;
    const filters = [],
      sorts = [];
    let seen = new Set();
    for (const f of raw.filters) {
      if (!f || typeof f !== 'object' || Array.isArray(f) || typeof f.field !== 'string') return null;
      const column = columns.get(f.field);
      if (!column || seen.has(f.field)) return null;
      seen.add(f.field);
      if (!distinct(f.operators) || !f.operators.length) return null;
      if (!f.operators.every(op => OPERATORS.includes(op) && fits(op, column.type))) return null;
      const values = f.values === undefined ? [] : f.values;
      if (!distinct(values) || values.length > MAX_CLOSED || !values.every(v => flaw(v, column) === '')) return null;
      if (f.required !== undefined && typeof f.required !== 'boolean') return null;
      filters.push({
        field: f.field,
        label: column.label,
        column,
        operators: f.operators.slice(),
        values: values.slice(),
        required: f.required === true,
      });
    }
    seen = new Set();
    for (const s of raw.sorts) {
      if (!s || typeof s !== 'object' || Array.isArray(s) || typeof s.field !== 'string') return null;
      const column = columns.get(s.field);
      if (!column || seen.has(s.field)) return null;
      seen.add(s.field);
      if (!distinct(s.directions) || !s.directions.length || !s.directions.every(v => DIRECTIONS.includes(v)))
        return null;
      sorts.push({field: s.field, label: column.label, directions: s.directions.slice()});
    }
    return {filters, sorts};
  }

  // ---- the blocks ---------------------------------------------------------------------------------------------------------------------
  let spec = null, // the verified declaration of the dataset on show, or null
    disabled = false,
    filterRows = [], // the 筛选 rows, in the order they were added (the required ones first)
    sortRows = []; // the 排序 keys, in the order they apply
  const controls = () => Array.from(block.querySelectorAll('input,select,textarea,button'));
  // A small button of a row: 移除, 上移.
  function quiet(name, label, act) {
    const b = node('button', undefined, 'quiet');
    b.type = 'button';
    b.append(icon(name));
    b.setAttribute('aria-label', label);
    b.title = label;
    b.disabled = disabled;
    b.addEventListener('click', act);
    return b;
  }
  // The field of a row: its label, the id as secondary text.
  function fieldOf(entry, required) {
    const head = node('div', undefined, 'narrow-field');
    head.append(node('span', entry.label, 'narrow-name'));
    if (required) head.append(node('span', '必填', 'pill neutral'));
    head.append(node('small', entry.field, 'narrow-id'));
    return head;
  }
  // A choice among words (the operators of a filter, the directions of a sort key): radios when there is a choice to make, the word
  // alone when the declaration gives one.
  function choice(name, label, values, wordsOf, chosen, pick) {
    const group = node('div', undefined, 'narrow-ops');
    if (values.length === 1) {
      group.append(node('span', wordsOf[values[0]], 'narrow-word'));
      return group;
    }
    group.setAttribute('role', 'radiogroup');
    group.setAttribute('aria-label', label);
    for (const value of values) {
      const option = node('label', undefined, 'narrow-choice');
      const radio = node('input');
      radio.type = 'radio';
      radio.name = name;
      radio.value = value;
      radio.checked = value === chosen;
      radio.disabled = disabled;
      radio.addEventListener('change', () => {
        if (radio.checked) pick(value);
      });
      option.append(radio, document.createTextNode(wordsOf[value]));
      group.append(option);
    }
    return group;
  }
  // The control a filter's value takes, by the operator and what the declaration says of the field.
  const kindOf = (f, op) =>
    op === 'in'
      ? f.values.length
        ? 'set'
        : 'list'
      : op === 'between'
        ? 'range'
        : (f.values.length && (op === 'eq' || op === 'ne')) || f.column.type === 'boolean'
          ? 'choice'
          : 'scalar';
  const numeric = column => column.type === 'integer' || column.type === 'decimal';
  // One value typed: a date field for a date, text for the rest; a number says its form (0, 0.00) and is sent as it is written.
  function valueInput(f, label) {
    const input = node('input');
    if (f.column.type === 'date') input.type = 'date';
    else {
      input.type = 'text';
      input.autocomplete = 'off';
      input.setAttribute('spellcheck', 'false');
      if (numeric(f.column)) {
        input.setAttribute('inputmode', 'decimal');
        input.placeholder = f.column.scale ? '0.' + '0'.repeat(f.column.scale) : '0';
      }
    }
    input.setAttribute('aria-label', label);
    input.disabled = disabled;
    input.addEventListener('input', () => ports.changed());
    return input;
  }
  function filterRow(f) {
    const row = {spec: f, el: node('div', undefined, 'narrow-row narrow-filter')};
    row.el.dataset.field = f.field;
    let operator = f.operators[0],
      kind = '',
      read = () => [];
    const cell = node('div', undefined, 'narrow-value');
    // The value control of the operator on show. An operator that takes the same control keeps what was typed; another control starts
    // from the one value there was (等于 → 介于 keeps it as the first of the two).
    function valueFor() {
      const next = kindOf(f, operator);
      if (next === kind) return;
      const kept = kind === 'scalar' || kind === 'range' ? read()[0] || '' : '';
      kind = next;
      cell.replaceChildren();
      if (kind === 'set') {
        const set = node('div', undefined, 'narrow-set');
        set.setAttribute('role', 'group');
        set.setAttribute('aria-label', f.label + ' 的可选值');
        for (const value of f.values) {
          const option = node('label', undefined, 'narrow-choice');
          const box = node('input');
          box.type = 'checkbox';
          box.value = value;
          box.disabled = disabled;
          box.addEventListener('change', () => ports.changed());
          option.append(box, document.createTextNode(value));
          set.append(option);
        }
        cell.append(set);
        read = () =>
          Array.from(set.querySelectorAll('input'))
            .filter(b => b.checked)
            .map(b => b.value);
      } else if (kind === 'list') {
        const area = node('textarea');
        area.rows = 2;
        area.setAttribute('spellcheck', 'false');
        area.setAttribute('aria-label', f.label + ' 的筛选值，每行一个');
        area.placeholder = '每行一个值，最多 ' + MAX_IN + ' 个';
        area.disabled = disabled;
        area.addEventListener('input', () => ports.changed());
        cell.append(area);
        // One value a line (a value holds no line break), as written but for the spaces around it; a value given twice counts once.
        read = () =>
          Array.from(
            new Set(
              area.value
                .split(/\r?\n/)
                .map(s => s.trim())
                .filter(Boolean)
            )
          );
      } else if (kind === 'range') {
        const range = node('div', undefined, 'narrow-range');
        const from = valueInput(f, f.label + ' 的起始值'),
          to = valueInput(f, f.label + ' 的结束值');
        from.value = kept;
        range.append(from, node('span', '至', 'narrow-word'), to);
        cell.append(range);
        read = () => [from.value.trim(), to.value.trim()];
      } else if (kind === 'choice') {
        const select = node('select');
        select.setAttribute('aria-label', f.label + ' 的筛选值');
        select.append(node('option', '请选择…'));
        select.options[0].value = '';
        // A boolean is 是 / 否 (true / false on the wire); a closed value is shown as the source gives it.
        const truth = f.column.type === 'boolean';
        for (const value of truth && !f.values.length ? ['true', 'false'] : f.values) {
          const option = node('option', truth ? (value === 'true' ? '是' : '否') : value);
          option.value = value;
          select.append(option);
        }
        select.disabled = disabled;
        select.addEventListener('change', () => ports.changed());
        cell.append(select);
        read = () => [select.value];
      } else {
        const input = valueInput(f, f.label + ' 的筛选值');
        input.value = kept;
        cell.append(input);
        read = () => [input.value.trim()];
      }
    }
    valueFor();
    row.el.append(
      fieldOf(f, f.required),
      choice('query-filter-op:' + f.field, f.label + ' 的运算符', f.operators, operatorWords, operator, op => {
        operator = op;
        valueFor();
        ports.changed();
      }),
      cell
    );
    if (!f.required) {
      row.remove = quiet('close', '移除筛选：' + f.label, () => {
        filterRows = filterRows.filter(r => r !== row);
        row.el.remove();
        offers();
        const back = offered(filterAdd, f.field);
        if (back) back.focus();
        ports.changed();
      });
      const tools = node('div', undefined, 'narrow-tools');
      tools.append(row.remove);
      row.el.append(tools);
    }
    row.operator = () => operator;
    row.values = () => read();
    // The control to fill: the first of the value's, or the operator's when the value has none to focus.
    row.focus = () => cell.querySelector('input,select,textarea') || row.el.querySelector('input');
    // What the console can see is wrong with the row, in the server's own terms; '' when it sees nothing.
    row.why = () => {
      const values = read(),
        name = f.label;
      if (kind === 'range') {
        if (!values[0] || !values[1]) return '筛选“' + name + '”的“介于”需要起止两个值。';
      } else if (!values.length || !values[0])
        return f.required
          ? '必填筛选“' + name + '”还没有值：填写后才能查询。'
          : '筛选“' + name + '”还没有值：填写或移除它才能查询。';
      if (values.length > MAX_IN)
        return '筛选“' + name + '”的“属于”最多 ' + MAX_IN + ' 个值，现在是 ' + values.length + ' 个。';
      for (const v of values) {
        const wrong = flaw(v, f.column);
        if (wrong === 'integer') return '筛选“' + name + '”的值须是整数，例如 100。';
        if (wrong === 'decimal')
          return (
            '筛选“' + name + '”的值须写成 ' + f.column.scale + ' 位小数，例如 100.' + '0'.repeat(f.column.scale) + '。'
          );
        if (wrong === 'date') return '筛选“' + name + '”的值须是日期（年-月-日）。';
        if (wrong === 'control') return '筛选“' + name + '”的值含有控制字符，不能使用。';
        if (wrong === 'long') return '筛选“' + name + '”的值超过 ' + MAX_CHARS + ' 个字符。';
        if (wrong) return '筛选“' + name + '”的值不符合字段的类型。';
      }
      if (kind === 'range' && compare(f.column, values[0], values[1]) > 0)
        return '筛选“' + name + '”的起始值大于结束值：“介于”要求从小到大。';
      return '';
    };
    return row;
  }
  function sortRow(s) {
    const row = {spec: s, el: node('div', undefined, 'narrow-row narrow-sort')};
    row.el.dataset.field = s.field;
    let direction = s.directions[0];
    row.rank = node('span', '', 'narrow-rank');
    row.up = quiet('up', '上移排序：' + s.label, () => {
      const at = sortRows.indexOf(row);
      if (at < 1) return;
      sortRows.splice(at - 1, 2, row, sortRows[at - 1]);
      sortList.insertBefore(row.el, sortRows[at].el);
      ranks();
      (row.up.hidden ? row.remove : row.up).focus();
      ports.changed();
    });
    row.remove = quiet('close', '移除排序：' + s.label, () => {
      sortRows = sortRows.filter(r => r !== row);
      row.el.remove();
      ranks();
      offers();
      const back = offered(sortAdd, s.field);
      if (back) back.focus();
      ports.changed();
    });
    const tools = node('div', undefined, 'narrow-tools');
    tools.append(row.up, row.remove);
    row.el.append(
      row.rank,
      fieldOf(s, false),
      choice('query-sort-dir:' + s.field, s.label + ' 的排序方向', s.directions, directionWords, direction, d => {
        direction = d;
        ports.changed();
      }),
      tools
    );
    row.direction = () => direction;
    return row;
  }
  // The place of each sort key: its number, and 上移 for every key but the first.
  function ranks() {
    sortRows.forEach((row, i) => {
      row.rank.textContent = String(i + 1);
      row.up.hidden = i === 0;
    });
  }
  // What can still be added: a filter for each declared field that has no row (while the query has room for one), a sort key for
  // each declared field that is not one yet (while there are fewer than three). The line goes when nothing can be added.
  // The button shows the field's label; its name says what it adds, its title the field's id too.
  function offer(entry, name, title, add) {
    const b = node('button', undefined, 'btn small');
    b.type = 'button';
    b.append(icon('plus'), document.createTextNode(entry.label));
    b.setAttribute('aria-label', name);
    b.title = title;
    b.dataset.field = entry.field;
    b.disabled = disabled;
    b.addEventListener('click', add);
    return b;
  }
  const offered = (line, field) => Array.from(line.querySelectorAll('button')).find(b => b.dataset.field === field);
  function offers() {
    filterAdd.replaceChildren();
    sortAdd.replaceChildren();
    if (spec && filterRows.length < MAX_FILTERS)
      for (const f of spec.filters)
        if (!filterRows.some(r => r.spec === f))
          filterAdd.append(
            offer(f, '添加筛选：' + f.label, '添加筛选：' + f.label + '（' + f.field + '）', () => {
              const row = filterRow(f);
              filterRows.push(row);
              filterList.append(row.el);
              offers();
              row.focus().focus();
              ports.changed();
            })
          );
    if (spec && sortRows.length < MAX_SORT_KEYS)
      for (const s of spec.sorts)
        if (!sortRows.some(r => r.spec === s))
          sortAdd.append(
            offer(s, '添加排序：' + s.label, '添加排序：' + s.label + '（' + s.field + '）', () => {
              const row = sortRow(s);
              sortRows.push(row);
              sortList.append(row.el);
              ranks();
              offers();
              (row.el.querySelector('input') || row.remove).focus();
              ports.changed();
            })
          );
    filterAddLine.hidden = !filterAdd.children.length;
    sortAddLine.hidden = !sortAdd.children.length;
  }

  // ---- the host's ports ---------------------------------------------------------------------------------------------------------------
  // The blocks for a dataset: nothing kept from the one before; the rows of its required filters from the start.
  function show(dataset) {
    spec = dataset ? check(dataset) : null;
    filterRows = [];
    sortRows = [];
    filterList.replaceChildren();
    sortList.replaceChildren();
    if (spec)
      for (const f of spec.filters)
        if (f.required && filterRows.length < MAX_FILTERS) {
          const row = filterRow(f);
          filterRows.push(row);
          filterList.append(row.el);
        }
    offers();
    filterGroup.hidden = !spec || !spec.filters.length;
    sortGroup.hidden = !spec || !spec.sorts.length;
    block.hidden = filterGroup.hidden && sortGroup.hidden;
  }
  // The query's lists, exactly as the server takes them: the values of 属于 and 介于 as a JSON array of strings in `value`.
  function filters() {
    return filterRows.map(row => {
      const operator = row.operator(),
        values = row.values();
      return {
        field: row.spec.field,
        operator,
        value: operator === 'in' || operator === 'between' ? JSON.stringify(values) : values[0] || '',
      };
    });
  }
  const sort = () => sortRows.map(row => ({field: row.spec.field, direction: row.direction()}));
  const wrong = () => filterRows.find(row => row.why());
  function reason() {
    const row = wrong();
    return row ? row.why() : '';
  }
  function invalid() {
    const row = wrong();
    return row ? row.focus() : null;
  }
  // One filter of a query in words: the field by its label, the operator's word, the values as they were sent.
  function sentence(label, f) {
    let values = [String(f.value)];
    if (f.operator === 'in' || f.operator === 'between')
      try {
        const list = JSON.parse(f.value);
        if (Array.isArray(list) && list.length && list.every(v => typeof v === 'string')) values = list;
      } catch (_) {}
    const value = values.join('、');
    switch (f.operator) {
      case 'eq':
        return label + ' 等于 ' + value;
      case 'ne':
        return label + ' 不等于 ' + value;
      case 'lt':
        return label + ' 小于 ' + value;
      case 'le':
        return label + ' 不大于 ' + value;
      case 'gt':
        return label + ' 大于 ' + value;
      case 'ge':
        return label + ' 不小于 ' + value;
      case 'in':
        return label + ' 属于 ' + value;
      case 'between':
        return values.length === 2 ? label + ' 介于 ' + values[0] + ' 至 ' + values[1] : label + ' 介于 ' + value;
      case 'prefix':
        return label + ' 开头为 ' + value;
      case 'contains':
        return label + ' 包含 ' + value;
    }
    return label + ' ' + String(f.operator) + ' ' + value;
  }
  // A query's narrowing in words (the chip, the result's lines, a schedule's line): the fields by the labels of the given columns,
  // by id where there is none. A query without the two lists (a snapshot restored without its query) is not known: nothing is said
  // of it here, and the host says so.
  function words(query, columns) {
    // A list the server wrote as null is an empty one; a query without the key is one the console never had.
    const list = key => {
      const v = query ? query[key] : undefined;
      return v === null ? [] : Array.isArray(v) ? v.filter(e => e && typeof e === 'object') : null;
    };
    const given = list('filters'),
      keys = list('sort');
    if (!given || !keys) return {known: false, count: 0, order: '', parts: [], filters: []};
    const label = id => {
      const c = (columns || []).find(c => c && c.id === id);
      return c && typeof c.label === 'string' && c.label ? c.label : String(id);
    };
    const order = keys
      .map((s, i) => {
        const name = label(s.field);
        if (s.direction === 'desc') return i ? '再按' + name + '降序' : '按' + name + '降序';
        if (s.direction === 'asc') return i ? '再按' + name + '升序' : '按' + name + '升序';
        return name + ' ' + String(s.direction);
      })
      .join('、');
    const parts = [];
    if (given.length) parts.push(given.length + ' 个筛选');
    if (order) parts.push(order);
    return {known: true, count: given.length, order, parts, filters: given.map(f => sentence(label(f.field), f))};
  }
  function disable(flag) {
    disabled = !!flag;
    for (const e of controls()) e.disabled = disabled;
  }
  function clear() {
    show(null);
  }
  show(null);
  return {
    limits: {filters: MAX_FILTERS, sortKeys: MAX_SORT_KEYS, values: MAX_IN, characters: MAX_CHARS},
    check,
    show,
    filters,
    sort,
    reason,
    invalid,
    words,
    disable,
    clear,
  };
};
