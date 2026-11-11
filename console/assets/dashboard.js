'use strict';
// Dashboard consumes the currently authorized result; its files contain metadata only.
window.createReportingDashboard = function (ports) {
  const $ = id => document.getElementById(id);
  const node = (tag, text) => {
    const n = document.createElement(tag);
    if (text !== undefined) n.textContent = text;
    return n;
  };
  // The charts are SVG built from nodes and attributes (no markup, no style attribute: the page's CSP; colours are CSS classes).
  const SVG = 'http://www.w3.org/2000/svg';
  const shape = (tag, attributes) => {
    const n = document.createElementNS(SVG, tag);
    for (const [k, v] of Object.entries(attributes || {})) n.setAttribute(k, String(v));
    return n;
  };
  // S21-A: the field set of each widget type (the server's, dashboards.Widget); title and width are optional on every one.
  const SHAPES = {
    metric: ['type', 'field'],
    bar: ['type', 'category', 'field'],
    line: ['type', 'category', 'field'],
    pie: ['type', 'category', 'field'],
    table: ['type', 'columns'],
  };
  const CHART_NEEDS = {
    bar: '柱状图需要当前授权的分类字段和数值字段。',
    line: '折线图需要当前授权的分类字段和数值字段。',
    pie: '饼图需要当前授权的分类字段和数值字段。',
  };
  const shaped = (w, required) =>
    !!w &&
    typeof w === 'object' &&
    !Array.isArray(w) &&
    required.every(k => Object.hasOwn(w, k)) &&
    Object.keys(w).every(k => required.includes(k) || k === 'title' || k === 'width');
  // A title is 1 to 64 characters, none of them a control character (the server's ValidTitle).
  const titled = s => typeof s === 'string' && [...s].length >= 1 && [...s].length <= 64 && !/\p{Cc}/u.test(s);
  // The selects of the three chart tools (bar, line, pie): a category is any column, a value a numeric one.
  const CATEGORY_SELECTS = ['board-category', 'board-line-category', 'board-pie-category'],
    VALUE_SELECTS = ['board-value', 'board-line-value', 'board-pie-value'];
  let result = null,
    query = null,
    config = null,
    saved = null,
    release = null,
    frozen = false,
    latest = 0;
  // The studio layer (board.js) is told whenever what state() reports may have changed.
  const tell = () => {
    if (typeof ports.changed === 'function') ports.changed();
  };
  const binding = () => ({
    integration_id: query.integration_id,
    dataset: {id: query.dataset.id, version: query.dataset.version},
  });
  const matches = b =>
    b &&
    b.integration_id === query.integration_id &&
    b.dataset?.id === query.dataset.id &&
    b.dataset?.version === query.dataset.version;
  const keys = (value, names) =>
    value &&
    typeof value === 'object' &&
    !Array.isArray(value) &&
    Object.keys(value).sort().join(',') === names.slice().sort().join(',');
  const field = id => result.columns.find(c => c.id === id);
  const numeric = id => {
    const c = field(id);
    return c && ['decimal', 'integer'].includes(c.type);
  };
  const metric = id => {
    const c = field(id);
    return c && ['decimal', 'integer'].includes(c.type) && Object.hasOwn(result.totals, id);
  };
  const ready = () => {
    if (!ports.active() || !result || !query || typeof query.integration_id !== 'string')
      throw Error('请先选择数据并完成查询，并等待操作结束。');
  };
  function validate(value) {
    if (
      !keys(value, ['schema_version', 'kind', 'binding', 'widgets']) ||
      value.schema_version !== '1.0' ||
      value.kind !== 'dashboard' ||
      !keys(value.binding, ['integration_id', 'dataset']) ||
      !keys(value.binding.dataset, ['id', 'version']) ||
      !matches(value.binding) ||
      !Array.isArray(value.widgets) ||
      value.widgets.length > 12
    )
      throw Error('看板配置或数据集绑定无效。');
    for (const w of value.widgets) {
      if (w?.type === 'metric') {
        if (!shaped(w, SHAPES.metric) || typeof w.field !== 'string' || !metric(w.field))
          throw Error('指标不在当前授权结果的服务端合计中。');
      } else if (w?.type === 'bar' || w?.type === 'line' || w?.type === 'pie') {
        if (
          !shaped(w, SHAPES[w.type]) ||
          typeof w.category !== 'string' ||
          !field(w.category) ||
          typeof w.field !== 'string' ||
          !numeric(w.field)
        )
          throw Error(CHART_NEEDS[w.type]);
      } else if (w?.type === 'table') {
        if (
          !shaped(w, SHAPES.table) ||
          !Array.isArray(w.columns) ||
          !w.columns.length ||
          w.columns.length > 100 ||
          new Set(w.columns).size !== w.columns.length ||
          !w.columns.every(id => typeof id === 'string' && field(id))
        )
          throw Error('表格包含未知或重复字段。');
      } else throw Error('不支持此看板组件。');
      if (Object.hasOwn(w, 'title') && !titled(w.title)) throw Error('组件标题需为 1–64 个字符，且不含控制字符。');
      if (Object.hasOwn(w, 'width') && !['half', 'full'].includes(w.width)) throw Error('组件宽度只能是半宽或全宽。');
    }
    if (new TextEncoder().encode(JSON.stringify(value)).length > 65536) throw Error('配置超过 64 KiB 限额。');
  }
  function canonical(value) {
    return {
      schema_version: value.schema_version,
      kind: value.kind,
      binding: {
        integration_id: value.binding.integration_id,
        dataset: {id: value.binding.dataset.id, version: value.binding.dataset.version},
      },
      widgets: value.widgets.map(w => {
        const out =
          w.type === 'metric'
            ? {type: 'metric', field: w.field}
            : w.type === 'table'
              ? {type: 'table', columns: w.columns.slice()}
              : {type: w.type, category: w.category, field: w.field};
        if (Object.hasOwn(w, 'title')) out.title = w.title;
        if (Object.hasOwn(w, 'width')) out.width = w.width;
        return out;
      }),
    };
  }
  function action(fn) {
    try {
      ready();
      return fn();
    } catch (e) {
      $('board-status').textContent = e.message;
    } finally {
      tell();
    }
  }
  function apply(value) {
    validate(value);
    config = canonical(value);
    render();
    $('board-status').textContent = '看板已更新；数据仍来自当前授权查询。';
  }
  // Business values stay exact strings/BigInts. Only bounded display ratios become numbers.
  // The numeric cells of the rows a chart plots, exact: each cell's literal text and sign, and every magnitude as a BigInt at one
  // common scale; null when a cell is not a plain decimal of at most 512 characters.
  function exact(rows, valueIndex) {
    const parsed = [];
    for (const row of rows) {
      const text = row[valueIndex];
      if (typeof text !== 'string' || text.length > 512 || !/^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$/.test(text))
        return null;
      const parts = text.replace(/^-/, '').split('.');
      parsed.push({text, negative: text.startsWith('-'), digits: BigInt(parts.join('')), scale: parts[1]?.length || 0});
    }
    const scale = Math.max(0, ...parsed.map(p => p.scale));
    return {parsed, magnitudes: parsed.map(p => p.digits * 10n ** BigInt(scale - p.scale))};
  }
  const unitOf = c => (c.currency ? ' 币种：' + c.currency : c.unit ? ' 单位：' + c.unit : '');
  const indexOf = id => result.columns.findIndex(c => c.id === id);
  // A tooltip on a drawn mark: the row, its category and its exact value.
  const tip = (mark, text) => {
    const title = shape('title');
    title.textContent = text;
    mark.append(title);
    return mark;
  };
  function barChart(widget, heading) {
    const chart = node('div');
    chart.className = 'board-chart';
    const c = field(widget.field),
      categoryIndex = indexOf(widget.category),
      valueIndex = indexOf(widget.field);
    chart.append(node('h3', heading), node('p', '当前页/预览前 50 行，按原顺序逐行显示，不分组、不汇总。' + unitOf(c)));
    const rows = result.rows.slice(0, 50);
    if (!rows.length) {
      chart.append(node('p', '当前页没有可绘制行。'));
      return chart;
    }
    const numbers = exact(rows, valueIndex);
    if (!numbers) {
      chart.append(node('p', '图表不可用：当前绘图范围包含无效或超过限额的数值。'));
      return chart;
    }
    const {parsed, magnitudes: values} = numbers;
    const maximum = values.reduce((a, b) => (a > b ? a : b), 0n),
      maxIndex = values.findIndex(v => v === maximum);
    chart.append(
      node(
        'p',
        '统一中央零线；两侧最大幅度 ' +
          parsed[maxIndex].text.replace(/^-/, '') +
          '。条长为近似比例，精确值见每行标签；尺度仅由绘图行确定。'
      )
    );
    rows.forEach((row, index) => {
      const item = node('div');
      item.className = 'board-chart-row';
      const p = parsed[index],
        units = maximum === 0n ? 0 : Number((values[index] * 10000n) / maximum),
        width = units / 200;
      item.append(
        node('p', '第 ' + (index + 1) + ' 行 · ' + row[categoryIndex]),
        node('strong', p.text + (values[index] > 0n && units === 0 ? ' · 小于绘图分辨率' : ''))
      );
      const track = node('div');
      track.className = 'board-chart-track';
      track.setAttribute('aria-hidden', 'true');
      const bar = node('span');
      bar.className = 'board-chart-bar' + (p.negative ? ' negative' : '');
      bar.style.left = (p.negative ? 50 - width : 50) + '%';
      bar.style.width = width + '%';
      track.append(bar);
      item.append(track);
      chart.append(item);
    });
    return chart;
  }
  // The legend of a chart: one line per plotted row, the row and its category then its exact value; for a pie, the slice's colour
  // swatch first (empty for a row that is not drawn, which is said).
  function legend(label, rows, categoryIndex, parsed, drawn) {
    const list = node('ol');
    list.className = 'board-legend';
    list.setAttribute('aria-label', label);
    rows.forEach((row, index) => {
      const item = node('li');
      if (drawn) {
        const swatch = node('span');
        swatch.className = 'board-swatch' + (drawn[index] ? ' board-slice-' + (index + 1) : '');
        swatch.setAttribute('aria-hidden', 'true');
        item.append(swatch);
      }
      const label = node('span', '第 ' + (index + 1) + ' 行 · ' + row[categoryIndex]);
      label.className = 'board-legend-label';
      item.append(label);
      if (drawn && !drawn[index]) item.append(node('em', '未绘制'));
      item.append(node('strong', parsed[index].text));
      list.append(item);
    });
    return list;
  }
  // S21-A: a line over the current page's first 50 rows in row order (x: the row, y: the value on a linear scale that includes zero).
  // The line, the points and the zero line are approximate; the exact values are the legend's literal strings.
  function lineChart(widget, heading) {
    const chart = node('div');
    chart.className = 'board-line';
    const c = field(widget.field),
      categoryIndex = indexOf(widget.category),
      valueIndex = indexOf(widget.field);
    chart.append(
      node('h3', heading),
      node('p', '当前页 · 前 50 行 · 按行顺序。逐行连线，不分组、不汇总。' + unitOf(c))
    );
    const rows = result.rows.slice(0, 50);
    if (!rows.length) {
      chart.append(node('p', '当前页没有可绘制行。'));
      return chart;
    }
    const numbers = exact(rows, valueIndex);
    if (!numbers) {
      chart.append(node('p', '图表不可用：当前绘图范围包含无效或超过限额的数值。'));
      return chart;
    }
    const {parsed, magnitudes} = numbers,
      values = magnitudes.map((m, i) => (parsed[i].negative ? -m : m));
    let lowest = 0,
      highest = 0;
    values.forEach((v, i) => {
      if (v < values[lowest]) lowest = i;
      if (v > values[highest]) highest = i;
    });
    // The scale includes zero: from the lowest value or zero to the highest value or zero.
    const low = values[lowest] < 0n ? values[lowest] : 0n,
      high = values[highest] > 0n ? values[highest] : 0n,
      span = high - low;
    const W = 400,
      H = 150,
      PAD = 10;
    const x = i => (rows.length === 1 ? W / 2 : PAD + (i * (W - 2 * PAD)) / (rows.length - 1)),
      y = v => PAD + ((H - 2 * PAD) * (span === 0n ? 5000 : Number(((high - v) * 10000n) / span))) / 10000;
    const picture = shape('svg', {
      class: 'board-line-svg',
      viewBox: '0 0 ' + W + ' ' + H,
      role: 'img',
      focusable: 'false',
      'aria-label': '当前页折线图：' + c.label + '，' + rows.length + ' 个点',
    });
    picture.append(
      shape('rect', {class: 'board-plot', x: 0, y: 0, width: W, height: H, rx: 4}),
      shape('line', {class: 'board-zero', x1: 0, x2: W, y1: y(0n).toFixed(2), y2: y(0n).toFixed(2)}),
      shape('polyline', {
        class: 'board-line-path',
        points: values.map((v, i) => x(i).toFixed(2) + ',' + y(v).toFixed(2)).join(' '),
      })
    );
    values.forEach((v, i) =>
      picture.append(
        tip(
          shape('circle', {class: 'board-point', cx: x(i).toFixed(2), cy: y(v).toFixed(2), r: 3}),
          '第 ' + (i + 1) + ' 行 · ' + rows[i][categoryIndex] + ' · ' + parsed[i].text
        )
      )
    );
    const figure = node('figure'),
      caption = node('figcaption');
    caption.append(
      node(
        'p',
        '线性标尺含零线；最小值 ' +
          parsed[lowest].text +
          '，最大值 ' +
          parsed[highest].text +
          '。折线与点位为近似比例，精确值见下方列表。'
      ),
      legend('各点精确值', rows, categoryIndex, parsed)
    );
    figure.append(picture, caption);
    chart.append(figure);
    return chart;
  }
  // S21-A: a pie of the current page's first 12 rows, one slice per row (the dashboard sums nothing: a repeated category is said, not
  // merged). A zero or negative value is listed and not drawn; the proportions come from the exact magnitudes, numbers only for the angles.
  function pieChart(widget, heading) {
    const chart = node('div');
    chart.className = 'board-pie';
    const c = field(widget.field),
      categoryIndex = indexOf(widget.category),
      valueIndex = indexOf(widget.field);
    chart.append(node('h3', heading), node('p', '当前页 · 前 12 行 · 每行一个扇区，不分组、不汇总。' + unitOf(c)));
    const rows = result.rows.slice(0, 12);
    if (!rows.length) {
      chart.append(node('p', '当前页没有可绘制行。'));
      return chart;
    }
    if (result.rows.length > 12) chart.append(node('p', '当前页有 ' + result.rows.length + ' 行，只绘制前 12 行。'));
    const numbers = exact(rows, valueIndex);
    if (!numbers) {
      chart.append(node('p', '图表不可用：当前绘图范围包含无效或超过限额的数值。'));
      return chart;
    }
    const {parsed, magnitudes} = numbers,
      categories = rows.map(row => row[categoryIndex]),
      drawn = parsed.map((p, i) => !p.negative && magnitudes[i] > 0n),
      slices = rows.map((row, i) => i).filter(i => drawn[i]),
      total = slices.reduce((sum, i) => sum + magnitudes[i], 0n);
    if (new Set(categories).size !== categories.length) {
      const note = node('p', '分类重复，饼图按行显示');
      note.className = 'board-note';
      chart.append(note);
    }
    const skipped = rows.map((row, i) => i).filter(i => !drawn[i]);
    if (skipped.length) {
      const note = node(
        'p',
        '零或负值不绘制扇区：' +
          skipped.map(i => '第 ' + (i + 1) + ' 行 · ' + categories[i] + ' · ' + parsed[i].text).join('、') +
          '。'
      );
      note.className = 'board-note';
      chart.append(note);
    }
    const figure = node('figure'),
      caption = node('figcaption');
    const picture = shape('svg', {
      class: 'board-pie-svg',
      viewBox: '0 0 200 200',
      role: 'img',
      focusable: 'false',
      'aria-label': '当前页饼图：' + c.label + '，' + slices.length + ' 个扇区',
    });
    // A point of the circle at a share of the turn, from twelve o'clock clockwise.
    const point = turns => {
      const angle = 2 * Math.PI * turns - Math.PI / 2;
      return (100 + 90 * Math.cos(angle)).toFixed(2) + ',' + (100 + 90 * Math.sin(angle)).toFixed(2);
    };
    const arc = to => ' A90,90 0 0,1 ' + point(to);
    const label = i => '第 ' + (i + 1) + ' 行 · ' + categories[i] + ' · ' + parsed[i].text;
    // Each boundary is the exact cumulative share, rounded once to a millionth; the last one is exactly the whole circle. A slice over
    // half the circle is two arcs (one arc between the same two points would be nothing: the one slice of a pie is the whole disc).
    let cumulative = 0n,
      start = 0;
    for (const i of slices) {
      cumulative += magnitudes[i];
      const end = Number((cumulative * 1000000n) / total) / 1000000;
      picture.append(
        tip(
          shape('path', {
            class: 'board-slice board-slice-' + (i + 1),
            d: 'M100,100 L' + point(start) + (end - start > 0.5 ? arc((start + end) / 2) : '') + arc(end) + ' Z',
          }),
          label(i)
        )
      );
      start = end;
    }
    if (slices.length) figure.append(picture);
    else chart.append(node('p', '没有可绘制的扇区：各行数值均为零或负。'));
    caption.append(
      node('p', '扇区为近似比例，只由绘制的正值确定；精确值见图例。'),
      legend('图例与精确值', rows, categoryIndex, parsed, drawn)
    );
    figure.append(caption);
    chart.append(figure);
    return chart;
  }
  function render() {
    const target = $('board-widgets');
    target.replaceChildren();
    if (!result || !config) return;
    config.widgets.forEach((widget, index) => {
      const card = node('article');
      card.className = 'panel board-widget' + (widget.width === 'full' ? ' board-wide' : '');
      const controls = node('div');
      controls.className = 'board-controls';
      // S21-A: the widget's title (1 to 64 characters; emptied, the default heading returns) and its width (half: one column of the
      // grid; full: both), each written into the document and so saved, published and reopened with it.
      const title = node('input');
      title.type = 'text';
      title.maxLength = 64;
      title.className = 'board-title';
      title.value = Object.hasOwn(widget, 'title') ? widget.title : '';
      title.placeholder = '标题（可选）';
      title.setAttribute('aria-label', '组件标题');
      title.addEventListener('change', () =>
        action(() => {
          const next = JSON.parse(JSON.stringify(config)),
            text = title.value.trim();
          if (text) next.widgets[index].title = text;
          else delete next.widgets[index].title;
          apply(next);
        })
      );
      const width = node('select');
      width.className = 'board-width';
      width.setAttribute('aria-label', '组件宽度');
      for (const [value, label] of [
        ['half', '半宽'],
        ['full', '全宽'],
      ]) {
        const o = node('option', label);
        o.value = value;
        width.append(o);
      }
      width.value = widget.width === 'full' ? 'full' : 'half';
      width.addEventListener('change', () =>
        action(() => {
          const next = JSON.parse(JSON.stringify(config));
          if (width.value === 'full') next.widgets[index].width = 'full';
          else delete next.widgets[index].width;
          apply(next);
        })
      );
      controls.append(title, width);
      for (const [label, delta] of [
        ['上移', -1],
        ['下移', 1],
        ['移除', 0],
      ]) {
        const b = node('button', label);
        b.type = 'button';
        b.className = 'button quiet small';
        b.disabled = delta !== 0 && (index + delta < 0 || index + delta >= config.widgets.length);
        b.addEventListener('click', () =>
          action(() => {
            const next = JSON.parse(JSON.stringify(config));
            if (delta === 0) next.widgets.splice(index, 1);
            else
              [next.widgets[index], next.widgets[index + delta]] = [next.widgets[index + delta], next.widgets[index]];
            apply(next);
          })
        );
        controls.append(b);
      }
      card.append(controls);
      // The heading: the widget's title, else the default of its type.
      const heading = Object.hasOwn(widget, 'title') ? widget.title : null;
      if (widget.type === 'metric') {
        const c = field(widget.field);
        card.append(
          node('h3', heading ?? c.label),
          node('p', '服务端原范围合计' + (c.currency ? ' · ' + c.currency : c.unit ? ' · ' + c.unit : ''))
        );
        const value = node('strong', result.totals[widget.field]);
        value.className = 'board-value';
        card.append(value);
      } else if (widget.type === 'bar') {
        card.append(barChart(widget, heading ?? field(widget.field).label + ' · 当前页柱状图'));
      } else if (widget.type === 'line') {
        card.append(lineChart(widget, heading ?? field(widget.field).label + ' · 当前页折线图'));
      } else if (widget.type === 'pie') {
        card.append(pieChart(widget, heading ?? field(widget.field).label + ' · 当前页饼图'));
      } else {
        card.append(node('h3', heading ?? '明细表 · 当前结果页'));
        const wrap = node('div');
        wrap.className = 'table-wrap';
        const table = node('table'),
          head = node('thead'),
          tr = node('tr'),
          body = node('tbody');
        for (const id of widget.columns) {
          const c = field(id);
          tr.append(node('th', c.label + (c.currency ? ' (' + c.currency + ')' : c.unit ? ' (' + c.unit + ')' : '')));
        }
        head.append(tr);
        for (const values of result.rows) {
          const row = node('tr');
          for (const id of widget.columns) {
            const cell = node('td', values[result.columns.findIndex(c => c.id === id)]);
            cell.className = 'precise';
            row.append(cell);
          }
          body.append(row);
        }
        table.append(head, body);
        wrap.append(table);
        card.append(wrap);
      }
      target.append(card);
    });
    $('board-count').textContent = config.widgets.length + ' / 12 个组件';
    tell();
  }
  function add(widget) {
    const next = JSON.parse(JSON.stringify(config));
    next.widgets.push(widget);
    apply(next);
  }
  $('board-add-metric').addEventListener('click', () =>
    action(() => add({type: 'metric', field: $('board-metric').value}))
  );
  $('board-add-table').addEventListener('click', () =>
    action(() => add({type: 'table', columns: Array.from($('board-columns').selectedOptions).map(o => o.value)}))
  );
  $('board-add-bar').addEventListener('click', () =>
    action(() => add({type: 'bar', category: $('board-category').value, field: $('board-value').value}))
  );
  $('board-add-line').addEventListener('click', () =>
    action(() => add({type: 'line', category: $('board-line-category').value, field: $('board-line-value').value}))
  );
  $('board-add-pie').addEventListener('click', () =>
    action(() => add({type: 'pie', category: $('board-pie-category').value, field: $('board-pie-value').value}))
  );
  $('board-reset').addEventListener('click', () =>
    action(() => apply({schema_version: '1.0', kind: 'dashboard', binding: binding(), widgets: []}))
  );
  $('board-open').addEventListener('click', () =>
    action(() => {
      const text = $('board-json').value;
      if (new TextEncoder().encode(text).length > 65536) throw Error('配置超过 64 KiB 限额。');
      let value;
      try {
        value = JSON.parse(text);
      } catch (_) {
        throw Error('请输入有效的看板 JSON。');
      }
      apply(value);
    })
  );
  $('board-save').addEventListener('click', () =>
    action(() => {
      validate(config);
      const text = JSON.stringify(config, null, 2);
      if (new TextEncoder().encode(text).length > 65536) throw Error('配置超过 64 KiB 限额。');
      $('board-json').value = text;
      const url = URL.createObjectURL(new Blob([text], {type: 'application/json'}));
      const link = node('a');
      link.href = url;
      link.download = 'dashboard-layout.json';
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      $('board-status').textContent = '已生成元数据配置文件，不含业务数据或访问权限。';
    })
  );
  const validID = id => typeof id === 'string' && /^[a-f0-9]{32}$/.test(id);
  function entry(value, id, version) {
    if (
      !value ||
      !validID(value.id) ||
      !Number.isSafeInteger(value.version) ||
      value.version < 1 ||
      value.version > 20 ||
      (id && value.id !== id) ||
      (version && value.version !== version)
    )
      throw Error('无法验证服务器看板版本。');
    validate(value.config);
    return value;
  }
  function saveServer(fresh) {
    return action(() => {
      validate(config);
      const prior = !fresh && saved && matches(saved.config.binding) ? saved : null;
      const document = JSON.parse(JSON.stringify(config));
      resetRelease();
      return ports.exchange(
        '/dashboards',
        {method: 'POST', body: {id: prior?.id || '', expected_version: prior?.version || 0, config: document}},
        value => {
          entry(value, prior?.id, (prior?.version || 0) + 1);
          if (JSON.stringify(canonical(value.config)) !== JSON.stringify(canonical(document)))
            throw Error('服务器返回的配置与保存内容不一致。');
          if (!prior) latest = 0;
          saved = value;
          latest = Math.max(latest, value.version);
          resetRelease();
          $('board-server-status').textContent = '已保存看板 ' + value.id + ' · 版本 ' + value.version;
        },
        '看板保存'
      );
    });
  }
  $('board-server-save').addEventListener('click', () => saveServer(false));
  $('board-server-new').addEventListener('click', () => saveServer(true));
  function showList(values) {
    if (
      !Array.isArray(values) ||
      values.length > 16 ||
      !values.every(v => validID(v.id) && Number.isSafeInteger(v.version) && v.version >= 1 && v.version <= 20)
    )
      throw Error('无法验证服务器看板列表。');
    $('board-server-listing').replaceChildren();
    for (const value of values) {
      const option = node('option', value.id + ' · 最新版本 ' + value.version);
      option.value = value.id;
      option.dataset.version = String(value.version);
      $('board-server-listing').append(option);
    }
    $('board-server-version').value = String(values[0]?.version || 1);
    const mine = saved && values.find(v => v.id === saved.id);
    if (mine) latest = Math.max(mine.version, saved.version);
    return values;
  }
  $('board-server-list').addEventListener('click', () =>
    action(() =>
      ports.exchange(
        '/dashboards',
        undefined,
        values => {
          showList(values);
          $('board-server-status').textContent = '当前身份可访问 ' + values.length + ' 个服务器看板。';
        },
        '看板目录'
      )
    )
  );
  // The version dialog (S07-G, ADR 0011): the saved versions of this dashboard, one of them back into the editor as an unsaved change, or published.
  function listVersions() {
    return action(() => {
      savedBoard();
      return ports.exchange('/dashboards', undefined, showList, '看板目录');
    });
  }
  function restoreVersion(version) {
    return action(() => {
      const id = savedBoard();
      if (!Number.isSafeInteger(version) || version < 1 || version > latest) throw Error('请选择有效的版本。');
      return ports.exchange(
        '/dashboards/' + id + '?version=' + version,
        undefined,
        value => {
          entry(value, id, version);
          apply(value.config);
          $('board-status').textContent =
            '已打开 v' + version + '，是未保存的修改；保存会成为下一个版本，当前发布不变。';
        },
        '看板版本'
      );
    });
  }
  $('board-server-listing').addEventListener('change', () => {
    $('board-server-version').value = $('board-server-listing').selectedOptions[0]?.dataset.version || '1';
  });
  // The console's button and the embed page (S03-N) open a server version the same way: through this one function and its checks.
  function openServer(id, version) {
    return action(() => {
      if (!validID(id) || !Number.isSafeInteger(version) || version < 1 || version > 20)
        throw Error('请选择有效看板和版本。');
      resetRelease();
      return ports.exchange(
        '/dashboards/' + id + '?version=' + version,
        undefined,
        value => {
          entry(value, id, version);
          apply(value.config);
          if (!saved || saved.id !== id) latest = 0;
          saved = value;
          latest = Math.max(latest, version);
          resetRelease();
          $('board-server-status').textContent = '正在编辑看板 ' + id + ' · 版本 ' + version + '；旧版本保存可能冲突。';
        },
        '看板打开'
      );
    });
  }
  $('board-server-open').addEventListener('click', () =>
    openServer($('board-server-listing').value, Number($('board-server-version').value))
  );
  function resetRelease() {
    release = null;
    tell();
  }
  function savedBoard() {
    ready();
    if (!saved || !matches(saved.config.binding)) throw Error('请先保存或打开当前数据集的服务器看板。');
    if (latest < saved.version) latest = saved.version;
    return saved.id;
  }
  function releaseState(value) {
    if (
      !keys(value, ['revision', 'published_version', 'history']) ||
      !Number.isSafeInteger(value.revision) ||
      value.revision < 0 ||
      value.revision > 64 ||
      !Number.isSafeInteger(value.published_version) ||
      value.published_version < 0 ||
      value.published_version > 20 ||
      !Array.isArray(value.history) ||
      value.history.length !== value.revision
    )
      throw Error('无法验证看板发布状态。');
    let published = 0;
    value.history.forEach((event, index) => {
      if (
        !keys(event, ['revision', 'action', 'version', 'at']) ||
        event.revision !== index + 1 ||
        !['publish', 'withdraw'].includes(event.action) ||
        !Number.isSafeInteger(event.version) ||
        (event.action === 'publish' ? event.version < 1 || event.version > 20 : event.version !== 0) ||
        typeof event.at !== 'string' ||
        !Number.isFinite(Date.parse(event.at))
      )
        throw Error('无法验证看板发布历史。');
      published = event.action === 'publish' ? event.version : 0;
    });
    if (published !== value.published_version) throw Error('看板的发布状态与发布记录不一致。');
    return value;
  }
  function showRelease(value) {
    release = releaseState(value);
    tell();
  }
  function readRelease() {
    return action(() => {
      const id = savedBoard();
      resetRelease();
      return ports.exchange('/dashboards/' + id + '/release', undefined, showRelease, '看板发布状态');
    });
  }
  // pick: a version picked in 历史, published as it is (an older one is a rollback); without it the editor's saved version, which must match
  // the editor. conflict: told when the server refused because the release state had changed (S09-F: the confirmation offers 重试).
  function changeRelease(actionName, pick, conflict) {
    return action(() => {
      const id = savedBoard();
      const picked = actionName === 'publish' && !!pick;
      if (picked && (!Number.isSafeInteger(pick) || pick < 1 || pick > latest)) throw Error('请选择有效的版本。');
      if (!release) throw Error('尚未读取到看板的发布状态，请重试。');
      if (
        actionName === 'publish' &&
        !picked &&
        JSON.stringify(canonical(config)) !== JSON.stringify(canonical(saved.config))
      )
        throw Error('请先保存当前修改，或用“发布”的保存并发布。');
      if (actionName === 'withdraw' && !release.published_version) throw Error('当前没有已发布版本。');
      const expected = release.revision,
        version = actionName === 'publish' ? (picked ? pick : saved.version) : 0;
      resetRelease();
      return ports.exchange(
        '/dashboards/' + id + '/release',
        {method: 'POST', body: {action: actionName, version, expected_revision: expected}},
        value => {
          releaseState(value);
          if (value.revision !== expected + 1 || value.published_version !== version)
            throw Error('服务器返回了意外的发布状态，请打开“历史”核对。');
          showRelease(value);
        },
        '看板发布变更',
        actionName === 'publish' ? conflict : undefined
      );
    });
  }
  $('board-release-withdraw').addEventListener('click', () => changeRelease('withdraw'));
  // What the studio layer shows: the binding, the counts, the saved version, whether the configuration differs from it, the release pointer when it has been read.
  function state() {
    const bound = !!result && !!query;
    return {
      result: bound,
      binding: bound
        ? {
            integration_id: query.integration_id,
            dataset: {id: query.dataset.id, version: query.dataset.version},
            as_of: query.as_of,
          }
        : null,
      snapshot: bound && frozen,
      rows: bound ? result.rows.length : 0,
      columns: bound ? result.columns.length : 0,
      widgets: config ? config.widgets.length : 0,
      saved: saved ? {id: saved.id, version: saved.version} : null,
      latest: saved ? Math.max(latest, saved.version) : 0,
      dirty: !!(config && saved && JSON.stringify(canonical(config)) !== JSON.stringify(canonical(saved.config))),
      release: release ? {revision: release.revision, published_version: release.published_version} : null,
    };
  }
  return {
    openServer,
    state,
    readRelease,
    publish: (pick, conflict) => changeRelease('publish', pick, conflict),
    save: () => saveServer(false),
    listVersions,
    restoreVersion,
    withdraw: () => changeRelease('withdraw'),
    clear(logout = false) {
      resetRelease();
      result = null;
      query = null;
      if (logout) {
        config = null;
        saved = null;
        latest = 0;
      }
      $('board-server-listing').replaceChildren();
      $('board-server-version').value = '1';
      $('board-server-status').textContent = '查询当前授权结果后可使用服务器看板。';
      $('board-widgets').replaceChildren();
      $('board-metric').replaceChildren();
      $('board-columns').replaceChildren();
      for (const id of [...CATEGORY_SELECTS, ...VALUE_SELECTS]) $(id).replaceChildren();
      $('board-json').value = '';
      $('board-count').textContent = '0 / 12 个组件';
      $('board-source').textContent = '尚无当前授权结果';
      $('board-status').textContent = '请先选择数据：预览或冻结快照。';
      frozen = false;
      tell();
    },
    refresh(value, q, snapshot) {
      resetRelease();
      result = value;
      query = q;
      frozen = !!snapshot;
      if (saved && !matches(saved.config.binding)) saved = null;
      try {
        if (!config) throw Error();
        validate(config);
      } catch (_) {
        config = {schema_version: '1.0', kind: 'dashboard', binding: binding(), widgets: []};
      }
      $('board-metric').replaceChildren();
      $('board-columns').replaceChildren();
      for (const id of [...CATEGORY_SELECTS, ...VALUE_SELECTS]) $(id).replaceChildren();
      for (const c of result.columns) {
        for (const id of CATEGORY_SELECTS) {
          const category = node('option', c.label);
          category.value = c.id;
          $(id).append(category);
        }
        if (numeric(c.id))
          for (const id of VALUE_SELECTS) {
            const value = node('option', c.label);
            value.value = c.id;
            $(id).append(value);
          }
        if (metric(c.id)) {
          const o = node('option', c.label);
          o.value = c.id;
          $('board-metric').append(o);
        }
        const o = node('option', c.label);
        o.value = c.id;
        o.selected = true;
        $('board-columns').append(o);
      }
      $('board-source').textContent =
        query.dataset.id +
        ' @ ' +
        query.dataset.version +
        ' · ' +
        query.as_of +
        ' · ' +
        (snapshot ? '冻结快照当前页' : '当前查询预览');
      $('board-status').textContent = '指标使用服务端原范围合计；表格仅显示当前结果页。';
      $('board-server-status').textContent = saved
        ? '正在编辑看板 ' + saved.id + ' · 版本 ' + saved.version
        : '未关联服务器看板；首次保存创建新看板。';
      render();
    },
  };
};
