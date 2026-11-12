'use strict';
// The table report's presentation on screen (S23-B, docs/ADR_0024_TABLE_REPORT_FORMATTING.md decision 5): the console's copy of the
// server's presentation step (export/present) and of the layout rules (layouts.Validate and Check), so that the table view shows what the
// exports print and the designer refuses what the server refuses, in step with it. It changes how the frozen values are shown and never
// which values: a format groups the digits, signs a negative or shows a zero as a dash and changes no digit; a subtotal is the exact
// decimal sum of the frozen strings (scaled integers, BigInt: no JavaScript number ever holds a value); a mark colours a cell and hides,
// changes or adds nothing. It writes no DOM, loads nothing and stores nothing: app.js renders what build() returns.
window.reportingPresent = (function () {
  const EXACT = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/;
  const isExact = v => typeof v === 'string' && EXACT.test(v);
  const numericType = t => t === 'decimal' || t === 'integer';
  // The words of a layout (reporting/presentation.go).
  const ALIGNS = ['left', 'center', 'right'],
    NEGATIVES = ['minus', 'parentheses'],
    ZEROS = ['digits', 'dash'],
    ORDERED = ['lt', 'le', 'gt', 'ge'],
    EQUALITY = ['eq', 'ne'],
    MARKS = ['good', 'warn', 'bad', 'note'],
    DASH = '—';
  const DEFAULT_FORMAT = {grouping: true, negative: 'minus', zero: 'digits'},
    PLAIN_FORMAT = {grouping: false, negative: 'minus', zero: 'digits'};
  const LIMITS = {columns: 100, marks: 8, title: 120, footnote: 400, label: 64, value: 128, bytes: 65536};
  // The closed palette of mark colours (export/present.Palette): light tints under which near-black text stays legible, the same on screen
  // (styles.css .mark-* carries them), in the PDF and in the workbook.
  const PALETTE = {good: 'D9EAD3', warn: 'FFF2CC', bad: 'F4CCCC', note: 'CFE2F3'};
  // A printable literal of at most max characters: valid text without control characters (reporting.Literal).
  function literal(s, max) {
    if (typeof s !== 'string' || [...s].length > max) return false;
    for (const ch of s) {
      const c = ch.codePointAt(0);
      if (c < 32 || c === 0x7f || c === 0xfffe || c === 0xffff) return false;
    }
    return true;
  }
  // groupDigits puts thousands separators into the integer part of an exact decimal string; it changes no digit.
  function groupDigits(v) {
    let sign = '',
      rest = v;
    if (rest.startsWith('-')) {
      sign = '-';
      rest = rest.slice(1);
    }
    const dot = rest.indexOf('.'),
      whole = dot < 0 ? rest : rest.slice(0, dot),
      frac = dot < 0 ? '' : rest.slice(dot);
    let out = '';
    for (let i = 0; i < whole.length; i++) {
      if (i > 0 && (whole.length - i) % 3 === 0) out += ',';
      out += whole[i];
    }
    return sign + out + frac;
  }
  // format shows an exact decimal or integer string as the format says (export/present.Format): grouping, a minus sign or parentheses
  // for a negative, the digits or a dash for a zero. A string that is not an exact number is returned as it is.
  function format(v, f) {
    if (!isExact(v)) return v;
    let sign = '',
      rest = v;
    if (rest.startsWith('-')) {
      sign = '-';
      rest = rest.slice(1);
    }
    if (f.zero === 'dash' && /^[0.]*$/.test(rest)) return DASH;
    if (f.grouping) rest = groupDigits(rest);
    if (sign && f.negative === 'parentheses') return '(' + rest + ')';
    return sign + rest;
  }
  // scaled is an exact decimal string as a BigInt with frac fraction digits.
  function scaled(v, frac) {
    const neg = v.startsWith('-'),
      body = neg ? v.slice(1) : v,
      dot = body.indexOf('.'),
      whole = dot < 0 ? body : body.slice(0, dot),
      f = (dot < 0 ? '' : body.slice(dot + 1)).padEnd(frac, '0');
    const n = BigInt(whole + f);
    return neg ? -n : n;
  }
  const fracOf = v => {
    const dot = v.indexOf('.');
    return dot < 0 ? 0 : v.length - dot - 1;
  };
  // sum is the exact decimal sum of exact decimal strings, scaled to the longest fraction seen or the column's declared scale
  // (export/present.Sum: the document tables' rule, one implementation in each place). A string that is not an exact number throws.
  function sum(values, scale) {
    let frac = 0;
    for (const v of values) {
      if (!isExact(v)) throw new Error('not an exact number: ' + v);
      frac = Math.max(frac, fracOf(v));
    }
    let total = 0n;
    for (const v of values) total += scaled(v, frac);
    const out = Math.max(frac, scale || 0);
    if (out > frac) total *= 10n ** BigInt(out - frac);
    const neg = total < 0n;
    let digits = (neg ? -total : total).toString();
    if (out > 0) {
      digits = digits.padStart(out + 1, '0');
      digits = digits.slice(0, -out) + '.' + digits.slice(-out);
    }
    return (neg ? '-' : '') + digits;
  }
  // compare compares two exact decimal strings numerically: -1, 0 or 1; anything else throws.
  function compare(a, b) {
    if (!isExact(a) || !isExact(b)) throw new Error('a comparison of something that is not an exact number');
    const frac = Math.max(fracOf(a), fracOf(b)),
      x = scaled(a, frac),
      y = scaled(b, frac);
    return x < y ? -1 : x > y ? 1 : 0;
  }
  // holds says whether a mark's relation holds for a value (export/present.Holds): an exact decimal comparison on a numeric column, an exact
  // string comparison or emptiness on any other.
  function holds(mark, v, numeric) {
    if (numeric) {
      const c = compare(v, mark.value);
      switch (mark.when) {
        case 'lt':
          return c < 0;
        case 'le':
          return c <= 0;
        case 'gt':
          return c > 0;
        case 'ge':
          return c >= 0;
        case 'eq':
          return c === 0;
        case 'ne':
          return c !== 0;
      }
      return false;
    }
    switch (mark.when) {
      case 'eq':
        return v === mark.value;
      case 'ne':
        return v !== mark.value;
      case 'blank':
        return v === '';
    }
    return false;
  }
  // The subtotal rule, in one place (ADR 0028 will narrow it to the metrics that declare aggregation: sum): a shown column is summed in a
  // subtotal row when it is one of the query's metrics and holds exact numbers.
  const summed = (column, query) =>
    numericType(column.type) && Array.isArray(query.metrics) && query.metrics.some(m => m && m.id === column.id);
  // effectiveFormat is how a numeric column shows its digits: its layout format, else grouped for a metric and plain for a dimension, as
  // the exports always printed them (export/present.Build).
  function effectiveFormat(column, entry, query) {
    if (entry && entry.format) return Object.assign({}, DEFAULT_FORMAT, entry.format);
    return summed(column, query) ? DEFAULT_FORMAT : PLAIN_FORMAT;
  }
  // uses11 says whether a layout uses anything schema 1.1 added: a layout that uses nothing is saved as 1.0, exactly as before S23-B.
  function uses11(layout) {
    return !!(
      layout.title ||
      layout.footnote ||
      layout.subtotals ||
      (layout.marks && layout.marks.length) ||
      layout.columns.some(c => c.label || c.format || c.align === 'center')
    );
  }
  const keysOf = (value, names) =>
    !!value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(k => names.includes(k));
  class Refusal extends Error {}
  const refuse = why => {
    throw new Refusal(why);
  };
  // validate holds a layout to the server's rules (layouts.Validate, then layouts.Check against the result's columns and the query's
  // dimensions): the shape, the 1.0 fence, the literals' bounds, a format on a numeric column only, a subtotal column that is a shown
  // dimension, marks that compare as their column's type allows. It throws a Refusal that names the rule, in the user's words; a shape
  // that is not a layout at all throws a plain Error (the caller's generic refusal). The binding is the caller's to check.
  function validate(layout, result, query) {
    if (!keysOf(layout, ['schema_version', 'binding', 'columns', 'title', 'footnote', 'subtotals', 'marks']))
      throw new Error('shape');
    if (!['1.0', '1.1'].includes(layout.schema_version)) throw new Error('schema');
    if (!Array.isArray(layout.columns) || !layout.columns.length || layout.columns.length > LIMITS.columns)
      throw new Error('columns');
    if (new Set(layout.columns.map(c => c && c.id)).size !== layout.columns.length) throw new Error('duplicate');
    const known = id => result.columns.find(c => c.id === id);
    for (const c of layout.columns) {
      if (!keysOf(c, ['id', 'align', 'label', 'format']) || typeof c.id !== 'string' || !known(c.id))
        throw new Error('column');
      if (!ALIGNS.includes(c.align)) throw new Error('align');
    }
    if (layout.schema_version === '1.0') {
      if (uses11(layout))
        refuse('1.0 版式没有标题、脚注、列标签、格式、居中、小计或标记规则：请把 schema_version 写成 1.1。');
      if ('title' in layout || 'footnote' in layout || 'subtotals' in layout || 'marks' in layout)
        throw new Error('1.0 keys');
      for (const c of layout.columns) if ('label' in c || 'format' in c) throw new Error('1.0 keys');
      return;
    }
    if (layout.title !== undefined && !literal(layout.title, LIMITS.title))
      refuse('标题最多 ' + LIMITS.title + ' 个字符，不能含控制字符。');
    if (layout.footnote !== undefined && !literal(layout.footnote, LIMITS.footnote))
      refuse('脚注最多 ' + LIMITS.footnote + ' 个字符，不能含控制字符。');
    for (const c of layout.columns) {
      const column = known(c.id);
      if (c.label !== undefined && !literal(c.label, LIMITS.label))
        refuse('列标签最多 ' + LIMITS.label + ' 个字符，不能含控制字符。');
      if (c.format !== undefined) {
        if (
          !keysOf(c.format, ['grouping', 'negative', 'zero']) ||
          (c.format.grouping !== undefined && typeof c.format.grouping !== 'boolean') ||
          (c.format.negative !== undefined && !NEGATIVES.includes(c.format.negative)) ||
          (c.format.zero !== undefined && !ZEROS.includes(c.format.zero))
        )
          refuse('格式只有 千分位（是/否）、负数（负号/括号）、零（数字/—）三项。');
        if (!numericType(column.type)) refuse('格式只能用于数值列：' + column.label + ' 不是数值列。');
      }
    }
    if (layout.subtotals !== undefined) {
      if (!keysOf(layout.subtotals, ['by']) || typeof layout.subtotals.by !== 'string') throw new Error('subtotals');
      const by = layout.columns.find(c => c.id === layout.subtotals.by);
      if (!by) refuse('小计列必须是布局里显示的列。');
      if (!Array.isArray(query.dimensions) || !query.dimensions.includes(layout.subtotals.by))
        refuse('小计列必须是维度列：' + known(by.id).label + ' 不是维度。');
    }
    if (layout.marks !== undefined) {
      if (!Array.isArray(layout.marks) || !layout.marks.length) throw new Error('marks');
      if (layout.marks.length > LIMITS.marks) refuse('标记规则最多 ' + LIMITS.marks + ' 条。');
      layout.marks.forEach((m, i) => {
        const n = '标记规则 ' + (i + 1) + '：';
        if (
          !keysOf(m, ['column', 'when', 'value', 'mark']) ||
          typeof m.column !== 'string' ||
          typeof m.when !== 'string'
        )
          throw new Error('mark');
        if (!layout.columns.some(c => c.id === m.column)) refuse(n + '列必须是布局里显示的列。');
        if (!MARKS.includes(m.mark)) refuse(n + '标记只有 好、注意、差、备注 四种。');
        if (m.value !== undefined && !literal(m.value, LIMITS.value))
          refuse(n + '值最多 ' + LIMITS.value + ' 个字符。');
        const column = known(m.column);
        if (numericType(column.type)) {
          if (![...ORDERED, ...EQUALITY].includes(m.when))
            refuse(n + '数值列的关系只有 小于、小于等于、大于、大于等于、等于、不等于。');
          if (!isExact(m.value)) refuse(n + '数值列比较的值必须是一个数（如 0、-12.50）。');
        } else {
          if (![...EQUALITY, 'blank'].includes(m.when)) refuse(n + '文本列的关系只有 等于、不等于、为空。');
          if (m.when === 'blank' ? m.value !== undefined : typeof m.value !== 'string')
            refuse(n + '“为空”不带值，等于与不等于要带值。');
        }
      });
    }
    if (new TextEncoder().encode(JSON.stringify(layout)).length > LIMITS.bytes) refuse('布局超过 64 KiB。');
  }
  // build presents a result through a layout (export/present.Build): the shown columns in the layout's order with their headers, alignments
  // and formats, the rows in print order (each subtotal group, by the exact strings of the subtotal column in the order of the groups' first
  // rows, followed by its subtotal row with the exact sum of each summed column), and the marks (the first rule in the layout's order that
  // holds colours the cell; a subtotal row is never marked). Every cell carries the exact string and the printed text.
  function build(result, query, layout) {
    const index = new Map(result.columns.map((c, i) => [c.id, i]));
    const columns = layout.columns.map(entry => {
      const source = result.columns[index.get(entry.id)];
      const numeric = numericType(source.type);
      return {
        id: entry.id,
        index: index.get(entry.id),
        source,
        label: entry.label || '',
        header:
          entry.label ||
          source.label + (source.currency ? ' (' + source.currency + ')' : source.unit ? ' (' + source.unit + ')' : ''),
        align: entry.align,
        numeric,
        format: numeric ? effectiveFormat(source, entry, query) : null,
        summed: summed(source, query),
      };
    });
    const marks = new Map();
    for (const m of layout.marks || [])
      columns.forEach((c, k) => {
        if (c.id === m.column) marks.set(k, [...(marks.get(k) || []), m]);
      });
    const by = layout.subtotals ? index.get(layout.subtotals.by) : -1;
    const groups = [],
      position = new Map();
    result.rows.forEach((row, i) => {
      if (by < 0) {
        groups.push({key: '', rows: [i]});
        return;
      }
      const key = row[by];
      let g = position.get(key);
      if (!g) {
        g = {key, rows: []};
        position.set(key, g);
        groups.push(g);
      }
      g.rows.push(i);
    });
    const rows = [];
    for (const g of groups) {
      for (const i of g.rows) {
        const values = result.rows[i];
        rows.push({
          index: i,
          subtotal: false,
          group: g.key,
          cells: columns.map((c, k) => {
            const v = values[c.index];
            let mark = '';
            for (const m of marks.get(k) || [])
              if (holds(m, v, c.numeric)) {
                mark = m.mark;
                break;
              }
            return {exact: v, text: c.numeric && isExact(v) ? format(v, c.format) : v, mark};
          }),
        });
      }
      if (by >= 0) {
        let labelled = false;
        rows.push({
          index: -1,
          subtotal: true,
          group: g.key,
          cells: columns.map(c => {
            if (c.summed) {
              const exact = sum(
                g.rows.map(i => result.rows[i][c.index]),
                c.source.scale || 0
              );
              return {exact, text: format(exact, c.format), mark: ''};
            }
            if (!labelled) {
              labelled = true;
              return {exact: '', text: '小计', mark: ''};
            }
            return {exact: '', text: '', mark: ''};
          }),
        });
      }
    }
    return {
      title: layout.title || '',
      footnote: layout.footnote || '',
      columns,
      rows,
      subtotalBy: layout.subtotals ? layout.subtotals.by : '',
    };
  }
  return {
    isExact,
    numericType,
    literal,
    groupDigits,
    format,
    sum,
    compare,
    holds,
    summed,
    effectiveFormat,
    uses11,
    validate,
    build,
    Refusal,
    words: {
      aligns: ALIGNS,
      negatives: NEGATIVES,
      zeros: ZEROS,
      ordered: ORDERED,
      equality: EQUALITY,
      marks: MARKS,
      dash: DASH,
    },
    formats: {default: DEFAULT_FORMAT, plain: PLAIN_FORMAT},
    limits: LIMITS,
    palette: PALETTE,
  };
})();
