'use strict';
// The template centre (S06-B part 3, ADR 0008 section 3, docs/STUDIO.md "模板中心"): the console page that lists the identity's server templates
// with their catalogue metadata, the read-only sample pack, the new-template wizard and the field-mapping dialog. It is built once, with the DOM
// API only (text goes in as text), into <section id="page-library">; it keeps nothing in the browser's storage: favourites, recent, archive and
// collections are the server's catalogue (GET and PUT /document-templates/{id}/catalog, with expected_revision).
// ports (app.js): call(path, options, label, messages) -> Promise (a refusal is told to the user and rethrown; a superseded call throws {stale}),
// pdf(path, label, name) -> Promise, notify(text, type), open(page), previews(), studio() -> documents.js's bridge.
window.createReportingLibrary = function (ports) {
  const root = document.getElementById('page-library');
  const NS = 'http://www.w3.org/2000/svg';
  // The four domains of the prototype and the flows of each (the pack's own labels; a template stores them as text).
  const DOMAINS = [
    {id: '仓储 WMS', flows: ['入库', '库存', '出库', '盘点', '移库补货', '退货', '异常处理', '运营监控', '仓储标签']},
    {id: '运输 / 货代', flows: ['航空运输', '海运', '快递面单']},
    {id: '国际贸易', flows: ['贸易单证']},
    {id: '通用业务', flows: ['销售', '财务', '其他']},
  ];
  const PRESETS = {
    a4: ['A4', '210×297mm'],
    a4_landscape: ['A4 横向', '297×210mm'],
    a5: ['A5', '148×210mm'],
    label_4x6in: ['4×6 英寸标签', '101.6×152.4mm'],
    label_100x150: ['100×150 标签', '100×150mm'],
    label_100x100: ['100×100 标签', '100×100mm'],
    label_150x100: ['150×100 标签', '150×100mm'],
  };
  const presetText = id => (PRESETS[id] ? PRESETS[id][0] + ' · ' + PRESETS[id][1] : String(id || 'A4'));
  const ICONS = {
    layers: 'm12 2 10 5-10 5L2 7zM2 12l10 5 10-5M2 17l10 5 10-5',
    left: 'm15 5-7 7 7 7',
    database: 'M20 6c0 2-4 3-8 3S4 8 4 6s4-3 8-3 8 1 8 3zM4 6v12c0 2 4 3 8 3s8-1 8-3V6M4 12c0 2 4 3 8 3s8-1 8-3',
    plus: 'M12 4v16M4 12h16',
    search: 'M17 10a7 7 0 1 0-14 0 7 7 0 0 0 14 0zM15 15l6 6',
    upload: 'M12 16V4m-5 5 5-5 5 5M4 17v4h16v-4',
    table: 'M3 4h18v16H3zM3 10h18M9 10v10M15 10v10M3 15h18',
    grid: 'M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z',
    file: 'M5 2h9l5 5v15H5zM14 2v6h5M8 12h8M8 16h6',
    barcode: 'M3 4v16M6 4v16M10 4v16M11 4v16M15 4v16M18 4v16M21 4v16',
    copy: 'M9 9h12v12H9zM5 15H3V3h12v2',
    archive: 'M3 4h18v4H3zM5 8v12h14V8M10 12h4',
    eye: 'M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12zM15 12a3 3 0 1 0-6 0 3 3 0 0 0 6 0',
    check: 'm5 12 4 4L19 6',
    close: 'm6 6 12 12M6 18 18 6',
    restore: 'm8 5-5 5 5 5M3 10h10c5 0 7 3 7 8',
  };
  const state = {
    own: [],
    samples: [],
    details: new Map(),
    loaded: false,
    loading: false,
    samplesLoaded: false,
    failed: '',
    quick: 'all',
    domain: '',
    flow: '',
    collection: '',
    search: '',
    status: 'all',
    sort: 'name',
    view: 'list',
    page: 0,
    pageSize: 24,
  };
  let queue = Promise.resolve(),
    dialogs = [],
    alive = 0;
  const ui = {};

  // ---- small DOM helpers ----
  function h(tag, props, ...kids) {
    const e = document.createElement(tag);
    if (props)
      for (const [k, v] of Object.entries(props)) {
        if (v === undefined || v === null || v === false) continue;
        if (k === 'class') e.className = v;
        else if (k === 'text') e.textContent = v;
        else if (k.length > 2 && k.startsWith('on')) e.addEventListener(k.slice(2), v);
        else e.setAttribute(k, v === true ? '' : String(v));
      }
    for (const kid of kids.flat()) if (kid !== undefined && kid !== null && kid !== false) e.append(kid);
    return e;
  }
  function icon(name, cls) {
    const s = document.createElementNS(NS, 'svg');
    s.setAttribute('class', 'ic' + (cls ? ' ' + cls : ''));
    s.setAttribute('aria-hidden', 'true');
    s.setAttribute('focusable', 'false');
    const u = document.createElementNS(NS, 'use');
    u.setAttribute('href', '#l-' + name);
    s.append(u);
    return s;
  }
  function sprite() {
    const s = document.createElementNS(NS, 'svg');
    s.setAttribute('class', 'sprite');
    s.setAttribute('aria-hidden', 'true');
    s.setAttribute('focusable', 'false');
    const defs = document.createElementNS(NS, 'defs');
    for (const [name, path] of Object.entries(ICONS)) {
      const sym = document.createElementNS(NS, 'symbol');
      sym.setAttribute('id', 'l-' + name);
      sym.setAttribute('viewBox', '0 0 24 24');
      const p = document.createElementNS(NS, 'path');
      p.setAttribute('d', path);
      sym.append(p);
      defs.append(sym);
    }
    s.append(defs);
    return s;
  }
  const btn = (text, fn, cls, ic, extra) =>
    h(
      'button',
      {type: 'button', class: 'btn' + (cls ? ' ' + cls : ''), onclick: fn, ...(extra || {})},
      ic ? icon(ic) : null,
      text
    );
  const validID = id => typeof id === 'string' && /^[a-f0-9]{32}$/.test(id);
  const fallbackName = t => t.name || '模板 ' + t.id.slice(0, 8);
  const collator = new Intl.Collator('zh-CN');
  const say = (text, type) => ports.notify(text, type);
  const studio = () => ports.studio();

  // ---- data ----
  function normalize(v) {
    if (
      !v ||
      typeof v !== 'object' ||
      !validID(v.id) ||
      !Number.isSafeInteger(v.version) ||
      v.version < 1 ||
      v.version > 20
    )
      return null;
    const c = v.catalog && typeof v.catalog === 'object' ? v.catalog : {};
    const text = x => (typeof x === 'string' ? x : '');
    return {
      id: v.id,
      versions: v.version,
      binding: v.binding && typeof v.binding === 'object' ? v.binding : null,
      name: text(v.name),
      schema: text(v.schema_version),
      preset: text(v.preset) || 'a4',
      published: Number.isSafeInteger(v.published_version) ? v.published_version : 0,
      catalog: {
        domain: text(c.domain),
        flow: text(c.flow),
        favourite: c.favourite === true,
        archived: c.archived === true,
        collections: Array.isArray(c.collections) ? c.collections.filter(x => typeof x === 'string') : [],
        last_opened: text(c.last_opened),
        revision: Number.isSafeInteger(c.revision) ? c.revision : 0,
      },
    };
  }
  // The latest refresh is the one that counts: the centre is shown again while a listing is still in flight (the crumb, then the rail), and the
  // earlier answer, arriving after the later one, must not put back what it listed.
  let refreshes = 0;
  function setSpace(space) {
    window.reportingTemplateSpace.set(space);
    const team = window.reportingTemplateSpace.get() === 'team';
    ui.spaceOwn.setAttribute('aria-pressed', String(!team));
    ui.spaceTeam.setAttribute('aria-pressed', String(team));
    ui.title.textContent = team ? '团队模板' : '模板中心';
    refresh();
  }
  async function refresh() {
    const seq = ++refreshes;
    state.loading = true;
    state.failed = '';
    render();
    try {
      const list = await ports.call(window.reportingTemplateSpace.path('/document-templates'), undefined, '模板列表');
      if (seq !== refreshes) return;
      if (!Array.isArray(list) || list.length > 64) throw Error('无法验证模板列表。');
      state.own = list.map(normalize).filter(Boolean);
      state.loaded = true;
    } catch (e) {
      if (seq !== refreshes) return;
      if (e && e.stale) {
        state.loading = false;
        if (alive) render();
        return;
      }
      state.failed = '模板列表读取失败。';
    }
    state.loading = false;
    render();
  }
  async function loadSamples() {
    try {
      const list = await ports.call('/document-templates/samples', undefined, '示例模板');
      if (!Array.isArray(list) || list.length > 200) throw Error('无法验证示例列表。');
      state.samples = list
        .filter(s => s && typeof s.kind === 'string' && /^[a-z0-9_]{1,48}$/.test(s.kind) && typeof s.name === 'string')
        .map(s => ({
          kind: s.kind,
          name: s.name,
          domain: String(s.domain || ''),
          flow: String(s.flow || ''),
          description: String(s.description || ''),
          preset: String(s.preset || 'a4'),
          elements: Number(s.elements) || 0,
          fields: Number(s.fields) || 0,
        }));
      state.samplesLoaded = true;
    } catch (e) {
      if (e && e.stale) return;
    }
    render();
  }
  async function sampleDetail(kind) {
    if (state.details.has(kind)) return state.details.get(kind);
    const v = await ports.call('/document-templates/samples/' + encodeURIComponent(kind), undefined, '示例模板');
    if (!v || v.kind !== kind || !Array.isArray(v.field_slots)) throw Error('无法验证示例。');
    const slots = v.field_slots
      .filter(f => f && typeof f.id === 'string' && typeof f.label === 'string')
      .map(f => ({
        id: f.id,
        label: f.label,
        type: String(f.type || 'text'),
        scope: f.scope === 'row' ? 'row' : 'document',
        metric: f.metric === true,
      }));
    const detail = {
      kind,
      name: v.name,
      description: v.description || '',
      slots,
      rows: Array.isArray(v.rows) ? v.rows.length : 0,
    };
    state.details.set(kind, detail);
    return detail;
  }
  // Catalogue writes of one template are serialized (each PUT carries the revision the previous one produced).
  function enqueue(fn) {
    const next = queue.then(fn, fn);
    queue = next.catch(() => {});
    return next;
  }
  function putCatalog(t, patch) {
    return enqueue(async () => {
      const c = {...t.catalog, ...patch};
      const body = {
        domain: c.domain,
        flow: c.flow,
        favourite: c.favourite,
        archived: c.archived,
        collections: c.collections,
        expected_revision: t.catalog.revision,
      };
      try {
        const v = await ports.call(
          window.reportingTemplateSpace.path('/document-templates/') + t.id + '/catalog',
          {method: 'PUT', body},
          '模板分类',
          {
            409: '这个模板的分类刚在别处改过，列表已刷新；请再操作一次。',
            400: '分类不符合规则：业务域与流程各至多 40 个字符，集合至多 16 个，每个 1–40 个字符、单行、前后无空白。',
          }
        );
        const n = normalize({id: t.id, version: t.versions, catalog: v});
        if (!n) throw Error('无法验证分类。');
        t.catalog = n.catalog;
        render();
        return true;
      } catch (e) {
        if (e && e.status === 409) refresh();
        else render();
        return false;
      }
    });
  }

  // ---- what the lists show ----
  const active = () => state.own.filter(t => !t.catalog.archived);
  const domainOf = t => t.catalog.domain || '';
  function ownList() {
    const tokens = state.search.toLowerCase().trim().split(/\s+/).filter(Boolean);
    const wantArchived = state.quick === 'archived' || state.status === 'archived';
    let list = state.own.filter(t => t.catalog.archived === wantArchived);
    list = list.filter(t => {
      const c = t.catalog;
      if (state.quick === 'favourites' && !c.favourite) return false;
      if (state.quick === 'recent' && !c.last_opened) return false;
      if (state.domain && c.domain !== state.domain) return false;
      if (state.flow && c.flow !== state.flow) return false;
      if (state.collection && !c.collections.includes(state.collection)) return false;
      if (state.status === 'draft' && t.published > 0) return false;
      if (state.status === 'published' && !(t.published > 0)) return false;
      const hay = [fallbackName(t), c.domain, c.flow, ...c.collections, t.id, presetText(t.preset)]
        .join(' ')
        .toLowerCase();
      return tokens.every(s => hay.includes(s));
    });
    const byName = (a, b) => collator.compare(fallbackName(a), fallbackName(b));
    list.sort((a, b) => {
      if (state.quick === 'recent' || state.sort === 'recent')
        return b.catalog.last_opened.localeCompare(a.catalog.last_opened) || byName(a, b);
      if (state.sort === 'updated') return b.versions - a.versions || byName(a, b);
      return byName(a, b);
    });
    return list;
  }
  function sampleList() {
    if (state.quick !== 'all' || state.status !== 'all' || state.collection) return [];
    const tokens = state.search.toLowerCase().trim().split(/\s+/).filter(Boolean);
    return state.samples.filter(
      s =>
        (!state.domain || s.domain === state.domain) &&
        (!state.flow || s.flow === state.flow) &&
        tokens.every(k =>
          [s.name, s.domain, s.flow, s.kind, s.description, presetText(s.preset)].join(' ').toLowerCase().includes(k)
        )
    );
  }
  const filtered = () => !!(state.search || state.domain || state.flow || state.collection || state.status !== 'all');
  function reason(t) {
    const s = studio();
    if (!s || !s.ready()) return '先选择数据集并预览或冻结快照：设计器需要当前授权的数据集。';
    const b = s.binding();
    if (
      t &&
      (!t.binding ||
        !t.binding.dataset ||
        t.binding.integration_id !== b.integration_id ||
        t.binding.dataset.id !== b.dataset.id ||
        t.binding.dataset.version !== b.dataset.version)
    )
      return '这个模板绑定的数据集与当前快照的数据集不同；请先查询它绑定的数据集。';
    return '';
  }

  // ---- render ----
  function render() {
    if (!alive) return;
    const focus = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.focus : '';
    renderSide();
    renderTop();
    renderResults();
    if (focus && root.contains(document.activeElement) === false) {
      const e = root.querySelector('[data-focus="' + CSS.escape(focus) + '"]');
      if (e) e.focus();
    }
  }
  function nav(label, count, pressed, fn, key) {
    return h(
      'button',
      {
        type: 'button',
        class: 'catalog-nav' + (pressed ? ' selected' : ''),
        'aria-pressed': pressed ? 'true' : 'false',
        'data-focus': key,
        onclick: fn,
      },
      h('span', {text: label}),
      h('b', {text: String(count)})
    );
  }
  function renderSide() {
    const act = active(),
      side = ui.side;
    const quick = [
      ['all', '全部模板', act.length],
      ['favourites', '我的收藏', act.filter(t => t.catalog.favourite).length],
      ['recent', '最近使用', act.filter(t => t.catalog.last_opened).length],
      ['archived', '归档', state.own.filter(t => t.catalog.archived).length],
    ];
    const collections = [...new Set(act.flatMap(t => t.catalog.collections))].sort((a, b) => collator.compare(a, b));
    side.replaceChildren(
      h('div', {class: 'catalog-caption', id: 'lib-cap-quick', text: '快速访问'}),
      h(
        'div',
        {role: 'group', 'aria-labelledby': 'lib-cap-quick'},
        quick.map(([id, label, n]) =>
          nav(
            label,
            n,
            state.quick === id,
            () => {
              state.quick = id;
              state.page = 0;
              render();
            },
            'q' + id
          )
        )
      ),
      h(
        'div',
        {class: 'catalog-caption'},
        h('span', {id: 'lib-cap-domain', text: '业务域'}),
        h('button', {
          type: 'button',
          onclick: () => {
            state.domain = '';
            state.flow = '';
            state.page = 0;
            render();
          },
          'aria-label': '清除业务域筛选',
          text: '全部',
        })
      ),
      h(
        'div',
        {role: 'group', 'aria-labelledby': 'lib-cap-domain'},
        DOMAINS.map(d =>
          nav(
            d.id,
            act.filter(t => domainOf(t) === d.id).length + state.samples.filter(s => s.domain === d.id).length,
            state.domain === d.id,
            () => {
              state.domain = state.domain === d.id ? '' : d.id;
              state.flow = '';
              state.page = 0;
              render();
            },
            'd' + d.id
          )
        )
      ),
      h('div', {class: 'catalog-caption', id: 'lib-cap-collections', text: '自定义集合'}),
      collections.length
        ? h(
            'div',
            {role: 'group', 'aria-labelledby': 'lib-cap-collections'},
            collections.map(name =>
              nav(
                name,
                act.filter(t => t.catalog.collections.includes(name)).length,
                state.collection === name,
                () => {
                  state.collection = state.collection === name ? '' : name;
                  state.page = 0;
                  render();
                },
                'c' + name
              )
            )
          )
        : h('p', {class: 'catalog-hint', text: '在模板的「分类」中创建集合；模板不需要复制。'}),
      h(
        'div',
        {class: 'catalog-footnote'},
        '业务域 → 流程用于稳定分类。',
        h('br'),
        '集合用于项目、客户或用途。',
        h('br'),
        '分类、收藏与最近使用保存在服务器上，只属于当前身份。'
      )
    );
  }
  function renderTop() {
    const titles = {favourites: '我的收藏', recent: '最近使用', archived: '归档模板'};
    ui.title.textContent = titles[state.quick] || state.domain || '模板中心';
    ui.stats.available.textContent = String(active().length);
    ui.stats.domains.textContent = String(DOMAINS.length);
    // The flow filter: the flows of the chosen domain, else every flow of the four domains.
    const flows = state.domain
      ? DOMAINS.find(d => d.id === state.domain).flows
      : DOMAINS.flatMap(d => d.flows.map(f => f));
    const seen = [...new Set(flows)];
    ui.flow.replaceChildren(
      h('option', {value: '', text: '全部流程'}),
      ...seen.map(f => h('option', {value: f, text: f}))
    );
    ui.flow.value = seen.includes(state.flow) ? state.flow : '';
    if (ui.flow.value !== state.flow) state.flow = ui.flow.value;
  }
  function cardIcon(t) {
    return t.preset.startsWith('label') ? 'barcode' : t.schema.startsWith('2') ? 'table' : 'file';
  }
  function pill(text, cls) {
    return h('span', {class: 'pill ' + cls, text});
  }
  function ownCard(t) {
    const c = t.catalog,
      name = fallbackName(t),
      why = reason(t),
      flow = [c.domain || '未分类', c.flow].filter(Boolean);
    const sub = h('div', {class: 'catalog-subtitle'});
    flow.forEach((x, i) => {
      if (i) sub.append(h('span', {text: '/'}));
      sub.append(x);
    });
    sub.append(h('span', {text: '·'}), presetText(t.preset));
    const label = c.archived ? '已归档' : t.published > 0 ? '已发布 v' + t.published : '草稿';
    const when =
      c.last_opened && !isNaN(Date.parse(c.last_opened))
        ? ' · 最近 ' + new Date(c.last_opened).toLocaleDateString('zh-CN')
        : '';
    const edit = () => openTemplate(t);
    const actions = h(
      'div',
      {class: 'catalog-card-actions'},
      h('button', {
        type: 'button',
        class: 'quiet catalog-star' + (c.favourite ? ' is-favorite' : ''),
        'aria-pressed': c.favourite ? 'true' : 'false',
        'aria-label': (c.favourite ? '取消收藏 ' : '收藏 ') + name,
        'data-focus': 'star' + t.id,
        onclick: () => putCatalog(t, {favourite: !c.favourite}),
        text: c.favourite ? '★' : '☆',
      }),
      btn('分类', () => classifyDialog(t), 'small', null, {'aria-label': '分类 ' + name, 'data-focus': 'cls' + t.id}),
      ...(c.archived
        ? [
            btn(
              '恢复',
              () =>
                putCatalog(t, {archived: false}).then(ok => {
                  if (ok) say('已恢复「' + name + '」。', 'success');
                }),
              'small',
              null,
              {'aria-label': '恢复 ' + name, 'data-focus': 'res' + t.id}
            ),
          ]
        : [
            h(
              'button',
              {
                type: 'button',
                class: 'quiet',
                'aria-label': '复制模板 ' + name,
                title: '复制模板',
                'data-focus': 'dup' + t.id,
                onclick: () => duplicate(t),
              },
              icon('copy')
            ),
            h(
              'button',
              {
                type: 'button',
                class: 'quiet',
                'aria-label': '归档模板 ' + name,
                title: '归档模板',
                'data-focus': 'arc' + t.id,
                onclick: () =>
                  putCatalog(t, {archived: true}).then(ok => {
                    if (ok) say('已归档「' + name + '」；可在“归档”里恢复。', 'success');
                  }),
              },
              icon('archive')
            ),
            btn('编辑', edit, 'small primary', null, {
              'aria-label': '编辑 ' + name,
              'aria-disabled': why ? 'true' : null,
              title: why || '在设计器中打开最新保存版本',
              'data-focus': 'edit' + t.id,
            }),
          ])
    );
    const nameButton = c.archived
      ? h('button', {type: 'button', class: 'catalog-name', disabled: true, text: name})
      : h('button', {
          type: 'button',
          class: 'catalog-name',
          title: why || '在设计器中打开最新保存版本',
          'aria-disabled': why ? 'true' : null,
          onclick: edit,
          text: name,
        });
    return h(
      'article',
      {class: 'catalog-card', 'aria-label': name},
      h('div', {class: 'catalog-doc-icon', 'aria-hidden': 'true'}, icon(cardIcon(t))),
      h(
        'div',
        {class: 'catalog-card-main'},
        nameButton,
        sub,
        c.collections.length
          ? h(
              'div',
              {class: 'catalog-tags'},
              c.collections.map(x => pill('集合：' + x, 'blue'))
            )
          : null
      ),
      h(
        'div',
        {class: 'catalog-state'},
        pill(label, t.published > 0 && !c.archived ? 'green' : 'blue'),
        h('small', {text: t.versions + ' 个保存版本' + when})
      ),
      actions
    );
  }
  function sampleCard(s) {
    const name = s.name;
    const sub = h(
      'div',
      {class: 'catalog-subtitle'},
      s.domain,
      h('span', {text: '/'}),
      s.flow,
      h('span', {text: '·'}),
      presetText(s.preset)
    );
    return h(
      'article',
      {class: 'catalog-card sample-card', 'aria-label': '示例 ' + name},
      h(
        'div',
        {class: 'catalog-doc-icon', 'aria-hidden': 'true'},
        icon(s.preset.startsWith('label') ? 'barcode' : 'table')
      ),
      h(
        'div',
        {class: 'catalog-card-main'},
        h('div', {class: 'catalog-name static', text: name}),
        sub,
        s.description ? h('div', {class: 'catalog-desc', text: s.description}) : null
      ),
      h(
        'div',
        {class: 'catalog-state'},
        pill('示例 · 只读', 'amber'),
        h('small', {text: s.elements + ' 个元素 · ' + s.fields + ' 个字段'})
      ),
      h(
        'div',
        {class: 'catalog-card-actions'},
        btn('预览', () => previewDialog(s), 'small', null, {
          'aria-label': '预览示例 ' + name,
          'data-focus': 'pv' + s.kind,
        }),
        btn('复制到我的模板', () => startCopy(s), 'small primary needs-designer', null, {
          'aria-label': '复制示例 ' + name + ' 到我的模板',
          'data-focus': 'cp' + s.kind,
        })
      )
    );
  }
  function renderResults() {
    const list = ownList(),
      pages = Math.max(1, Math.ceil(list.length / state.pageSize));
    state.page = Math.min(state.page, pages - 1);
    const shown = list.slice(state.page * state.pageSize, (state.page + 1) * state.pageSize);
    ui.view.list.setAttribute('aria-pressed', state.view === 'list' ? 'true' : 'false');
    ui.view.grid.setAttribute('aria-pressed', state.view === 'grid' ? 'true' : 'false');
    ui.count.textContent =
      state.loading && !state.loaded
        ? '正在读取…'
        : list.length +
          ' 个结果' +
          (list.length
            ? ' · 显示 ' +
              (state.page * state.pageSize + 1) +
              '–' +
              Math.min(list.length, (state.page + 1) * state.pageSize)
            : '');
    ui.reset.hidden = !filtered();
    ui.chips.replaceChildren(...(state.collection ? [pill('集合：' + state.collection, 'blue')] : []));
    const empty = h(
      'div',
      {class: 'empty-state'},
      icon('search'),
      h('h3', {
        text: state.failed
          ? state.failed
          : filtered()
            ? '没有匹配的模板'
            : state.quick === 'all'
              ? '还没有自己的模板'
              : '这里还没有模板',
      }),
      h('p', {
        text: state.failed
          ? '请稍后刷新。'
          : filtered()
            ? '清空筛选或调整关键字。'
            : state.quick === 'all'
              ? '从下方的示例模板复制一份，或新建模板。'
              : '收藏、使用或归档模板后会出现在这里。',
      }),
      filtered() ? btn('清空筛选', clearFilters, 'primary') : null
    );
    ui.items.className = 'catalog-items ' + (state.view === 'grid' ? 'is-grid' : 'is-list');
    ui.items.replaceChildren(
      ...(shown.length ? shown.map(t => h('li', null, ownCard(t))) : [h('li', {class: 'span'}, empty)])
    );
    ui.pageSize.value = String(state.pageSize);
    ui.pagination.hidden = !list.length;
    ui.prev.disabled = state.page === 0;
    ui.next.disabled = state.page >= pages - 1;
    ui.pageInfo.textContent = '第 ' + (state.page + 1) + ' / ' + pages + ' 页';
    // The samples: four domains, read-only; not shown under 我的收藏 / 最近使用 / 归档, a status or a collection (a sample has none of them).
    const samples = sampleList();
    ui.samples.hidden = state.quick !== 'all' || state.status !== 'all' || !!state.collection;
    ui.samplesCount.textContent = state.samplesLoaded
      ? samples.length +
        ' 个示例' +
        (filtered() && samples.length !== state.samples.length ? '（共 ' + state.samples.length + ' 个）' : '')
      : '正在读取…';
    const groups = DOMAINS.map(d => [d, samples.filter(s => s.domain === d.id)]).filter(g => g[1].length);
    ui.sampleBody.replaceChildren(
      ...(groups.length
        ? groups.map(([d, items]) =>
            h(
              'section',
              {class: 'sample-group', 'aria-label': d.id},
              h('h3', {text: d.id + ' · ' + items.length}),
              h(
                'ul',
                {class: 'catalog-items ' + (state.view === 'grid' ? 'is-grid' : 'is-list'), role: 'list'},
                items.map(s => h('li', null, sampleCard(s)))
              )
            )
          )
        : [h('p', {class: 'catalog-hint', text: state.samplesLoaded ? '没有匹配的示例。' : '正在读取示例模板…'})])
    );
    const why = state.loaded ? reason(null) : '';
    ui.notice.replaceChildren(
      ...(why
        ? [
            document.createTextNode(why + ' '),
            ...(ports.data
              ? [btn('选择数据…', () => ports.data(ui.notice.querySelector('button')), 'small', 'database')]
              : []),
          ]
        : [])
    );
    ui.notice.hidden = !why;
  }
  function clearFilters() {
    state.search = '';
    state.domain = '';
    state.flow = '';
    state.collection = '';
    state.status = 'all';
    state.page = 0;
    ui.search.value = '';
    ui.status.value = 'all';
    render();
  }

  // ---- dialogs: role=dialog, modal, focus trapped, Esc and the backdrop close, focus returns to the opener ----
  const FOCUSABLE =
    'a[href],button:not([disabled]),input:not([disabled]):not([type=hidden]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

  // Esc and Tab belong to the top dialog wherever the focus is (a control that disables itself while it works drops the focus to the page).
  function trap(event) {
    const top = dialogs[dialogs.length - 1];
    if (!top) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      top.close();
      return;
    }
    if (event.key !== 'Tab') return;
    const items = [...top.box.querySelectorAll(FOCUSABLE)].filter(e => !e.closest('[hidden]'));
    if (!items.length) return;
    const first = items[0],
      last = items[items.length - 1],
      inside = top.box.contains(document.activeElement);
    if (event.shiftKey && (!inside || document.activeElement === first)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (!inside || document.activeElement === last)) {
      event.preventDefault();
      first.focus();
    }
  }
  let trigger = null;
  function dialog(id, title, sub, size) {
    const opener = trigger && trigger.isConnected ? trigger : document.activeElement;
    const titleId = 'lib-d-' + id + '-title';
    const body = h('div', {class: 'dialog-body'}),
      foot = h('div', {class: 'dialog-footer'});
    const head = h(
      'div',
      {class: 'dialog-head'},
      h(
        'div',
        {class: 'head-title'},
        h('div', null, h('h2', {id: titleId, text: title}), sub ? h('p', {text: sub}) : null)
      ),
      h('button', {type: 'button', class: 'quiet', 'aria-label': '关闭', onclick: () => api.close()}, icon('close'))
    );
    const box = h(
      'div',
      {class: 'dialog ' + (size || ''), role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': titleId},
      head,
      body,
      foot
    );
    const overlay = h('div', {class: 'overlay open', 'data-dialog': id}, box);
    const api = {
      overlay,
      box,
      body,
      foot,
      title: h('span'),
      close() {
        if (!overlay.isConnected) return;
        overlay.remove();
        dialogs = dialogs.filter(d => d !== api);
        if (!dialogs.length) {
          ui.shell.inert = false;
          document.removeEventListener('keydown', trap, true);
        }
        const again =
          opener && opener.isConnected
            ? opener
            : opener && opener.dataset && opener.dataset.focus
              ? root.querySelector('[data-focus="' + CSS.escape(opener.dataset.focus) + '"]')
              : null;
        if (again) again.focus();
        else if (ui.title) ui.title.focus();
      },
      focusFirst() {
        const first = body.querySelector(FOCUSABLE) || foot.querySelector(FOCUSABLE) || head.querySelector(FOCUSABLE);
        if (first) first.focus();
      },
    };
    overlay.addEventListener('mousedown', event => {
      if (event.target === overlay) api.close();
    });
    root.append(overlay);
    dialogs.push(api);
    ui.shell.inert = true;
    if (dialogs.length === 1) document.addEventListener('keydown', trap, true);
    return api;
  }
  const field = (label, control, cls) =>
    h('label', {class: 'field-full ' + (cls || '')}, h('span', {text: label}), control);
  function errorLine() {
    return h('div', {class: 'note-error', role: 'alert', hidden: true});
  }
  const showError = (e, text) => {
    e.textContent = text || '';
    e.hidden = !text;
  };

  // The classification: domain, flow and collections of one template (the catalogue's metadata; it changes neither the layout nor the versions).
  function classifyDialog(t) {
    const d = dialog(
      'classify',
      '分类 · ' + fallbackName(t),
      '修改检索信息，不改变模板版式、数据绑定或保存版本',
      'small'
    );
    const domain = h(
      'select',
      {'aria-label': '业务域'},
      h('option', {value: '', text: '未分类'}),
      DOMAINS.map(x => h('option', {value: x.id, text: x.id}))
    );
    const flow = h('select', {'aria-label': '业务流程'});
    const fill = () => {
      const found = DOMAINS.find(x => x.id === domain.value);
      flow.replaceChildren(
        h('option', {value: '', text: found ? '未指定' : '—'}),
        ...(found ? found.flows : []).map(f => h('option', {value: f, text: f}))
      );
      flow.disabled = !found;
    };
    domain.value = DOMAINS.some(x => x.id === t.catalog.domain) ? t.catalog.domain : '';
    fill();
    const known = [...new Set([...state.own.flatMap(x => x.catalog.collections)])].sort((a, b) =>
      collator.compare(a, b)
    );
    const chosen = new Set(t.catalog.collections);
    const boxes = h('div', {class: 'check-list', role: 'group', 'aria-label': '自定义集合'});
    const addBox = name => {
      const id = 'lib-col-' + boxes.children.length;
      boxes.append(
        h(
          'label',
          {class: 'check-row'},
          h('input', {type: 'checkbox', id, value: name, checked: chosen.has(name) ? true : null}),
          h('span', {text: name})
        )
      );
    };
    [...new Set([...known, ...t.catalog.collections])].forEach(addBox);
    const fresh = h('input', {
      type: 'text',
      maxlength: '40',
      'aria-label': '新集合名称（1–40 个字符）',
      placeholder: '如：客户A / 常用出库 / 上海仓',
      autocomplete: 'off',
    });
    const err = errorLine();
    const addNew = () => {
      const v = fresh.value;
      if (!v) return;
      if ([...v].length > 40 || v.trim() !== v || /[\x00-\x1f\x7f]/.test(v)) {
        showError(err, '集合名称须为 1–40 个字符，单行，前后不留空白。');
        return;
      }
      if ([...boxes.querySelectorAll('input')].some(i => i.value === v)) {
        const i = [...boxes.querySelectorAll('input')].find(i => i.value === v);
        i.checked = true;
      } else {
        chosen.add(v);
        addBox(v);
        boxes.lastChild.querySelector('input').checked = true;
      }
      fresh.value = '';
      showError(err, '');
    };
    domain.addEventListener('change', fill);
    fresh.addEventListener('keydown', e => {
      if (e.key === 'Enter') {
        e.preventDefault();
        addNew();
      }
    });
    d.body.append(
      h('div', {class: 'form-grid'}, field('业务域', domain), field('业务流程', flow)),
      h(
        'div',
        {class: 'field-full'},
        h('span', {class: 'field-label', text: '自定义集合（至多 16 个）'}),
        boxes,
        h('div', {class: 'row'}, fresh, btn('添加', addNew, 'small'))
      ),
      h('div', {
        class: 'callout',
        text: '一个模板属于一个业务流程，可同时放入多个集合。新集合会出现在左侧导航，无需复制模板。',
      }),
      err
    );
    const save = btn(
      '保存分类',
      async () => {
        addNew();
        if (!err.hidden) return;
        const cols = [...boxes.querySelectorAll('input:checked')].map(i => i.value);
        if (cols.length > 16) {
          showError(err, '最多 16 个集合。');
          return;
        }
        save.disabled = true;
        const ok = await putCatalog(t, {domain: domain.value, flow: domain.value ? flow.value : '', collections: cols});
        if (ok) {
          d.close();
          say('已保存「' + fallbackName(t) + '」的分类。', 'success');
        } else {
          save.disabled = false;
          d.close();
        }
      },
      'primary',
      'check'
    );
    d.foot.append(
      btn('取消', () => d.close()),
      save
    );
    d.focusFirst();
  }

  // ---- the mapping table: each field slot of a sample to a column of the current snapshot's data set, or to a literal ----
  const fits = (slot, col) =>
    slot.type === 'text' ||
    (slot.type === 'integer' && col.type === 'integer') ||
    (slot.type === 'decimal' && (col.type === 'decimal' || col.type === 'integer')) ||
    (slot.type === 'date' && /date|time/.test(col.type));
  // Column types of a data set are the platform's (string, integer, decimal, date, ...); a sample's slots are text, integer, decimal or date.
  const kind = t => (/^(string|text)$/.test(t) ? 'text' : /date|time/.test(t) ? 'date' : t);
  const norm = s =>
    String(s)
      .toLowerCase()
      .replace(/[\s_.\-/]+/g, '');
  // Pre-fill: a column whose label or id matches the slot's (of a type that fits), else the first unused column of the same type, else a literal.
  function suggest(slots, columns) {
    const choices = {},
      used = new Set();
    for (const slot of slots) {
      const ok = columns.filter(c => fits(slot, c) && !used.has(c.id));
      const byLabel = ok.find(c => norm(c.label) === norm(slot.label)),
        byID = ok.find(
          c => norm(c.id) === norm(slot.id) || (norm(c.id).endsWith(norm(slot.id)) && norm(slot.id).length >= 3)
        );
      const same = ok.find(c => kind(c.type) === kind(slot.type));
      const pick = byLabel || byID || same;
      if (pick) {
        used.add(pick.id);
        choices[slot.id] = {column: pick.id, literal: '[' + slot.label + ']'};
      } else choices[slot.id] = {column: '', literal: '[' + slot.label + ']'};
    }
    return choices;
  }
  function mappingTable(slots, columns, choices) {
    const body = h('tbody');
    for (const slot of slots) {
      const ch = choices[slot.id];
      const sel = h(
        'select',
        {'aria-label': '「' + slot.label + '」对应的字段'},
        h('option', {value: '', text: '字面文字'}),
        columns.filter(c => fits(slot, c)).map(c => h('option', {value: c.id, text: c.label + '（' + c.id + '）'}))
      );
      sel.value = ch.column;
      const lit = h('input', {
        type: 'text',
        'aria-label': '「' + slot.label + '」的文字',
        value: ch.literal,
        maxlength: '200',
        autocomplete: 'off',
      });
      const sync = () => {
        ch.column = sel.value;
        ch.literal = lit.value;
        lit.hidden = sel.value !== '';
      };
      sel.addEventListener('change', sync);
      lit.addEventListener('input', sync);
      sync();
      body.append(
        h(
          'tr',
          null,
          h('th', {scope: 'row'}, slot.label, h('small', {text: slot.id})),
          h('td', {text: slot.scope === 'row' ? '明细' : '单据'}),
          h('td', {text: {text: '文字', integer: '整数', decimal: '小数', date: '日期'}[slot.type] || slot.type}),
          h('td', {class: 'map-to'}, h('div', {class: 'map-pair'}, sel, lit))
        )
      );
    }
    return h(
      'div',
      {class: 'table-wrap'},
      h(
        'table',
        {class: 'data-table'},
        h('caption', {class: 'sr-only', text: '示例字段与数据集字段的对应'}),
        h(
          'thead',
          null,
          h(
            'tr',
            null,
            ['示例字段', '范围', '类型', '对应到'].map(x => h('th', {scope: 'col', text: x}))
          )
        ),
        body
      )
    );
  }
  const payload = (slots, choices) =>
    Object.fromEntries(
      slots.map(s => [s.id, choices[s.id].column ? {column: choices[s.id].column} : {literal: choices[s.id].literal}])
    );
  function datasetLine() {
    const s = studio(),
      b = s && s.ready() ? s.binding() : null;
    return b
      ? h(
          'p',
          {class: 'dataset-line'},
          h('b', {text: '数据集'}),
          ' ' + b.dataset.id + '@' + b.dataset.version + ' · ' + s.columns().length + ' 个字段'
        )
      : h('div', {class: 'callout warn', text: reason(null)});
  }
  const COPY_MESSAGES = {
    400: '名称或映射不被接受：名称 1–80 个字符、单行、前后无空白；文字要能放进文本框；表格至少要有一列对应到字段。',
    403: '当前身份无权使用所选的某个字段。',
    404: '没有找到这个示例。',
    413: '已达到每个身份 64 个模板的上限。',
    409: '请求冲突，请重试。',
  };
  async function copySample(sample, name, mapping) {
    const b = studio().binding();
    const v = await ports.call(
      '/document-templates/samples/' + encodeURIComponent(sample.kind) + '/copy',
      {
        method: 'POST',
        body: {
          dataset: {integration_id: b.integration_id, id: b.dataset.id, version: b.dataset.version},
          name,
          mapping,
        },
      },
      '复制示例',
      COPY_MESSAGES
    );
    if (!v || !validID(v.id)) throw Error('无法验证保存结果。');
    return v;
  }
  function openCreated(v, text) {
    say(text, 'success');
    refresh();
    studio().openServer(v.id, v.version || 1, () => ports.open('documents'));
  }
  async function startCopy(sample) {
    const why = reason(null);
    if (why) {
      say(why, 'error');
      return;
    }
    let detail;
    try {
      detail = await sampleDetail(sample.kind);
    } catch (e) {
      if (!(e && e.stale)) say('无法读取示例。', 'error');
      return;
    }
    const columns = studio().columns(),
      choices = suggest(detail.slots, columns);
    const d = dialog(
      'copy',
      '复制到我的模板 · ' + sample.name,
      '示例只读；复制后是您自己的普通模板，可随意修改。',
      'wide'
    );
    const name = h('input', {
      type: 'text',
      maxlength: '80',
      value: sample.name,
      autocomplete: 'off',
      'aria-describedby': 'lib-copy-name-help',
    });
    const err = errorLine();
    d.body.append(
      field('模板名称 *', name),
      h('p', {id: 'lib-copy-name-help', class: 'help', text: '1–80 个字符，单行，前后不留空白。'}),
      datasetLine(),
      h('h3', {class: 'section-title', text: '字段对应'}),
      h('p', {
        class: 'help',
        text: '已按名称与类型预先对应；选“字面文字”则在模板里写入该文字。表格里没有对应字段的列会被去掉。',
      }),
      mappingTable(detail.slots, columns, choices),
      err
    );
    const go = btn(
      '复制并进入设计器',
      async () => {
        const v = name.value;
        if (!studio().nameOk(v)) {
          showError(err, '模板名称须为 1–80 个字符，单行，前后不留空白。');
          name.focus();
          return;
        }
        go.disabled = true;
        showError(err, '');
        try {
          const made = await copySample(sample, v, payload(detail.slots, choices));
          d.close();
          openCreated(made, '已复制示例「' + sample.name + '」为「' + v + '」。');
        } catch (e) {
          go.disabled = false;
          if (e && e.stale) d.close();
          else if (e && !e.status) showError(err, e.message || '复制失败。');
        }
      },
      'primary',
      'check'
    );
    d.foot.append(
      h('span', {class: 'footer-note', text: '复制路由在服务端校验并保存为新模板的版本 1'}),
      btn('取消', () => d.close()),
      go
    );
    d.focusFirst();
  }
  async function previewDialog(sample) {
    const d = dialog(
      'preview',
      '预览示例 · ' + sample.name,
      sample.flow ? sample.domain + ' / ' + sample.flow : sample.domain,
      'medium'
    );
    d.body.append(
      h('p', {text: sample.description || '示例模板的版式。'}),
      h('p', {
        class: 'help',
        text:
          presetText(sample.preset) +
          ' · ' +
          sample.elements +
          ' 个元素 · ' +
          sample.fields +
          ' 个字段。预览用示例自带的数据由服务端生成 PDF（带 PREVIEW 标记），不需要数据集，也不会保存任何东西。',
      })
    );
    const list = h('div', {class: 'table-wrap'});
    d.body.append(list);
    sampleDetail(sample.kind)
      .then(detail => {
        if (!d.overlay.isConnected) return;
        list.append(
          h(
            'table',
            {class: 'data-table'},
            h('caption', {class: 'sr-only', text: '示例的字段'}),
            h(
              'thead',
              null,
              h(
                'tr',
                null,
                ['字段', '范围', '类型'].map(x => h('th', {scope: 'col', text: x}))
              )
            ),
            h(
              'tbody',
              null,
              detail.slots
                .slice(0, 12)
                .map(s =>
                  h(
                    'tr',
                    null,
                    h('th', {scope: 'row', text: s.label}),
                    h('td', {text: s.scope === 'row' ? '明细' : '单据'}),
                    h('td', {text: s.type})
                  )
                )
            )
          )
        );
        if (detail.slots.length > 12)
          list.append(h('p', {class: 'help', text: '… 另有 ' + (detail.slots.length - 12) + ' 个字段'}));
      })
      .catch(() => {});
    const can = ports.previews();
    const go = btn(
      '生成 PDF 预览',
      async () => {
        go.disabled = true;
        try {
          await ports.pdf(
            '/document-templates/samples/' + encodeURIComponent(sample.kind) + '/previews',
            '示例预览',
            sample.kind + '-sample-preview.pdf'
          );
        } catch (e) {}
        go.disabled = false;
        if (d.overlay.isConnected) go.focus();
      },
      'primary',
      'eye',
      can ? {} : {'aria-disabled': 'true', title: '服务端未启用 PDF 渲染，不提供预览。'}
    );
    if (!can)
      go.addEventListener(
        'click',
        e => {
          e.stopImmediatePropagation();
          say('服务端未启用 PDF 渲染，不提供预览。', 'error');
        },
        true
      );
    d.foot.append(
      btn(
        '复制到我的模板',
        () => {
          d.close();
          startCopy(sample);
        },
        null,
        'copy'
      ),
      btn('关闭', () => d.close()),
      go
    );
    d.focusFirst();
  }

  // ---- own templates: open, duplicate ----
  function openTemplate(t) {
    const why = reason(t);
    if (why) {
      say(why, 'error');
      return;
    }
    studio().openServer(t.id, t.versions, () => ports.open('documents'));
  }
  async function duplicate(t) {
    const why = reason(null);
    if (why) {
      say(why, 'error');
      return;
    }
    try {
      const v = await ports.call(
        window.reportingTemplateSpace.path('/document-templates/') + t.id + '?version=' + t.versions,
        undefined,
        '复制模板'
      );
      if (!v || v.id !== t.id || !v.config || typeof v.config !== 'object') throw Error('无法验证模板。');
      const base = fallbackName(t),
        suffix = ' 副本',
        name =
          [...base]
            .slice(0, 80 - [...suffix].length)
            .join('')
            .trimEnd() + suffix;
      const config = {...v.config, name};
      const saved = await ports.call(
        window.reportingTemplateSpace.path('/document-templates'),
        {method: 'POST', body: {id: '', expected_version: 0, config}},
        '复制模板',
        {
          413: '已达到每个身份 64 个模板的上限。',
          400: '复制的模板无法保存。',
          403: '当前身份无权使用这个模板的数据集。',
        }
      );
      if (!saved || !validID(saved.id)) throw Error('无法验证保存结果。');
      const c = t.catalog;
      try {
        await ports.call(
          window.reportingTemplateSpace.path('/document-templates/') + saved.id + '/catalog',
          {
            method: 'PUT',
            body: {
              domain: c.domain,
              flow: c.flow,
              favourite: false,
              archived: false,
              collections: c.collections,
              expected_revision: 0,
            },
          },
          '模板分类'
        );
      } catch (e) {}
      say('已复制为「' + name + '」。', 'success');
      refresh();
    } catch (e) {
      if (e && !e.stale && !e.status) say('复制失败。', 'error');
    }
  }

  // ---- the new-template wizard: 业务流程, 版式, 数据, 名称 ----
  const STEPS = ['业务流程', '版式', '数据', '名称'];
  function wizard() {
    const w = {
      step: 0,
      domain: '',
      flow: '',
      blank: false,
      layout: '',
      preset: 'a4',
      choices: null,
      choicesFor: '',
      name: '',
      named: false,
    };
    const d = dialog('wizard', '创建一份新的业务模板', '先确定业务流程、版式和数据，再进入精细设计', 'wide');
    const strip = h('ol', {class: 'steps', 'aria-label': '步骤'});
    const err = errorLine();
    const back = btn('上一步', () => go(w.step - 1)),
      next = btn('下一步', () => advance(), 'primary'),
      cancel = btn('取消', () => d.close());
    const note = h('span', {class: 'footer-note'});
    d.foot.append(note, cancel, back, next);
    const samplesOf = () => state.samples.filter(s => s.domain === w.domain && s.flow === w.flow);
    function go(n) {
      showError(err, '');
      w.step = Math.max(0, Math.min(3, n));
      draw();
    }
    function radioCard(name, value, checked, title, small, onchange) {
      const input = h('input', {type: 'radio', name, value, checked: checked ? true : null});
      input.addEventListener('change', onchange);
      return h(
        'label',
        {class: 'radio-card'},
        input,
        h('span', null, h('strong', {text: title}), small ? h('small', {text: small}) : null)
      );
    }
    function draw() {
      strip.replaceChildren(
        ...STEPS.flatMap((label, i) => [
          h(
            'li',
            {
              class: 'step' + (i === w.step ? ' current' : i < w.step ? ' done' : ''),
              'aria-current': i === w.step ? 'step' : null,
            },
            h('b', {text: i < w.step ? '✓' : String(i + 1)}),
            label
          ),
        ])
      );
      note.textContent = w.step + 1 + ' / 4 · 创建后在设计器中打开';
      back.hidden = w.step === 0;
      cancel.hidden = w.step !== 0;
      next.textContent = w.step === 3 ? '创建并进入设计器' : '下一步';
      next.disabled = false;
      const body = h('div', {class: 'wizard-body'});
      if (w.step === 0) {
        body.append(
          h('h3', {class: 'section-title', text: '选择业务流程'}),
          h(
            'div',
            {class: 'flow-grid'},
            DOMAINS.map(dom =>
              h(
                'fieldset',
                {class: 'flow-group'},
                h('legend', {text: dom.id}),
                dom.flows.map(f => {
                  const n = state.samples.filter(s => s.domain === dom.id && s.flow === f).length;
                  return radioCard(
                    'lib-flow',
                    dom.id + '|' + f,
                    !w.blank && w.domain === dom.id && w.flow === f,
                    f,
                    n ? n + ' 个示例' : '空白起点',
                    () => {
                      w.domain = dom.id;
                      w.flow = f;
                      w.blank = false;
                      w.layout = '';
                    }
                  );
                })
              )
            )
          ),
          h(
            'fieldset',
            {class: 'flow-group'},
            h('legend', {text: '不属于任何流程'}),
            radioCard('lib-flow', 'blank', w.blank, '空白模板', '不选流程，从页面预设与明细表开始', () => {
              w.blank = true;
              w.domain = '';
              w.flow = '';
              w.layout = 'blank';
            })
          )
        );
      }
      if (w.step === 1) {
        const list = w.blank ? [] : samplesOf();
        if (!w.layout || (w.layout !== 'blank' && !list.some(s => s.kind === w.layout)))
          w.layout = list.length ? list[0].kind : 'blank';
        const preset = h(
          'select',
          {'aria-label': '页面预设'},
          Object.entries(PRESETS).map(([id, t]) => h('option', {value: id, text: t[0] + ' · ' + t[1]}))
        );
        preset.value = w.preset;
        preset.addEventListener('change', () => {
          w.preset = preset.value;
        });
        body.append(
          h('h3', {class: 'section-title', text: w.blank ? '空白模板' : w.domain + ' / ' + w.flow + ' 的版式'}),
          h(
            'div',
            {class: 'radio-cards'},
            list.map(s =>
              radioCard(
                'lib-layout',
                s.kind,
                w.layout === s.kind,
                s.name,
                presetText(s.preset) + ' · ' + s.elements + ' 个元素' + (s.description ? ' · ' + s.description : ''),
                () => {
                  w.layout = s.kind;
                }
              )
            ),
            radioCard(
              'lib-layout',
              'blank',
              w.layout === 'blank',
              list.length ? '空白模板' : '空白模板（这个流程暂无示例）',
              '从页面预设与明细表开始',
              () => {
                w.layout = 'blank';
              }
            )
          ),
          field('页面预设（空白模板用）', preset)
        );
        if (!list.length && !w.blank)
          body.append(h('div', {class: 'callout', text: '这个流程还没有示例版式，可以从空白模板开始。'}));
      }
      if (w.step === 2) {
        body.append(h('h3', {class: 'section-title', text: '绑定当前数据集'}), datasetLine());
        const s = studio();
        if (!s || !s.ready()) {
          next.disabled = true;
        } else if (w.layout === 'blank')
          body.append(
            h('div', {
              class: 'callout',
              text: '空白模板绑定当前数据集，初始明细表用结果的前几个字段；之后在设计器里选择字段。',
            })
          );
        else {
          const holder = h('div', null, h('p', {class: 'help', text: '正在读取示例字段…'}));
          body.append(holder);
          next.disabled = true;
          sampleDetail(w.layout)
            .then(detail => {
              if (!holder.isConnected) return;
              if (w.choicesFor !== detail.kind) {
                w.choices = suggest(detail.slots, s.columns());
                w.choicesFor = detail.kind;
              }
              holder.replaceChildren(
                h('p', {class: 'help', text: '已按名称与类型预先对应；选“字面文字”则在模板里写入该文字。'}),
                mappingTable(detail.slots, s.columns(), w.choices)
              );
              next.disabled = false;
              w.detail = detail;
            })
            .catch(() => {
              if (holder.isConnected)
                holder.replaceChildren(h('div', {class: 'callout warn', text: '无法读取示例字段。'}));
            });
        }
      }
      if (w.step === 3) {
        const sample = state.samples.find(x => x.kind === w.layout);
        if (!w.named) w.name = sample ? sample.name : '新建模板';
        const name = h('input', {
          type: 'text',
          maxlength: '80',
          value: w.name,
          autocomplete: 'off',
          id: 'lib-wizard-name',
        });
        name.addEventListener('input', () => {
          w.name = name.value;
          w.named = true;
        });
        const rows = [
          ['业务流程', w.blank ? '空白（不属于任何流程）' : w.domain + ' / ' + w.flow],
          ['版式', sample ? sample.name + ' · ' + presetText(sample.preset) : '空白模板 · ' + presetText(w.preset)],
          [
            '数据集',
            (() => {
              const b = studio() && studio().binding();
              return b ? b.dataset.id + '@' + b.dataset.version : '—';
            })(),
          ],
        ];
        body.append(
          field('模板名称 *', name),
          h(
            'dl',
            {class: 'summary'},
            rows.flatMap(([k, v]) => [h('dt', {text: k}), h('dd', {text: v})])
          ),
          h('div', {class: 'callout', text: '创建后是您自己的普通模板（保存版本 1），可在设计器里修改与发布。'})
        );
      }
      d.body.replaceChildren(strip, body, err);
      const first = body.querySelector('input:checked,input,select');
      if (first) first.focus();
    }
    async function advance() {
      showError(err, '');
      if (w.step === 0 && !w.blank && !w.domain) {
        showError(err, '请选择一个业务流程，或选择空白模板。');
        return;
      }
      if (w.step < 3) {
        go(w.step + 1);
        return;
      }
      if (!studio().nameOk(w.name)) {
        showError(err, '模板名称须为 1–80 个字符，单行，前后不留空白。');
        return;
      }
      next.disabled = true;
      try {
        let made;
        if (w.layout === 'blank') {
          const config = studio().blank(w.name, w.preset);
          made = await ports.call(
            window.reportingTemplateSpace.path('/document-templates'),
            {method: 'POST', body: {id: '', expected_version: 0, config}},
            '新建模板',
            {413: '已达到每个身份 64 个模板的上限。', 400: '模板不被接受。', 403: '当前身份无权使用所选的数据集。'}
          );
          if (!made || !validID(made.id)) throw Error('无法验证保存结果。');
          if (w.domain)
            try {
              await ports.call(
                window.reportingTemplateSpace.path('/document-templates/') + made.id + '/catalog',
                {
                  method: 'PUT',
                  body: {
                    domain: w.domain,
                    flow: w.flow,
                    favourite: false,
                    archived: false,
                    collections: [],
                    expected_revision: 0,
                  },
                },
                '模板分类'
              );
            } catch (e) {}
        } else {
          made = await copySample({kind: w.layout}, w.name, payload(w.detail.slots, w.choices));
        }
        d.close();
        openCreated(made, '已创建「' + w.name + '」。');
      } catch (e) {
        next.disabled = false;
        if (e && e.stale) d.close();
        else if (e && !e.status) showError(err, e.message || '创建失败。');
      }
    }
    draw();
    return d;
  }
  function newTemplate() {
    wizard();
  }

  // ---- import ----
  function importDialog() {
    const d = dialog('import', '导入模板', '读入模板 JSON 到设计器（未保存的修改），之后在设计器里保存', 'medium');
    const text = h('textarea', {
      'aria-label': '模板 JSON',
      rows: '10',
      spellcheck: 'false',
      placeholder: '粘贴模板 JSON，或选择文件',
    });
    const file = h('input', {type: 'file', accept: 'application/json,.json', 'aria-label': '选择模板 JSON 文件'});
    const err = errorLine();
    file.addEventListener('change', async () => {
      const f = file.files[0];
      if (!f) return;
      if (f.size > 65536) {
        showError(err, '模板超过64KiB。');
        return;
      }
      try {
        text.value = await f.text();
        showError(err, '');
      } catch (e) {
        showError(err, '无法读取文件。');
      }
    });
    d.body.append(
      datasetLine(),
      h('div', {class: 'field-full'}, h('span', {class: 'field-label', text: '文件'}), file),
      field('JSON', text),
      err
    );
    const go = btn(
      '导入到设计器',
      () => {
        const s = studio();
        if (!s || !s.ready()) {
          showError(err, reason(null));
          return;
        }
        const r = s.importJSON(text.value);
        if (!r.ok) {
          showError(err, r.message);
          return;
        }
        d.close();
        say('模板 JSON 已读入设计器，尚未保存。', 'success');
        ports.open('documents');
      },
      'primary',
      'upload'
    );
    d.foot.append(
      btn('取消', () => d.close()),
      go
    );
    d.focusFirst();
  }

  // ---- the page ----
  function build() {
    const status = h(
      'select',
      {'aria-label': '模板状态'},
      [
        ['all', '全部状态'],
        ['draft', '草稿'],
        ['published', '已发布'],
        ['archived', '已归档'],
      ].map(([v, t]) => h('option', {value: v, text: t}))
    );
    const sort = h(
      'select',
      {'aria-label': '模板排序'},
      [
        ['name', '名称'],
        ['recent', '最近打开'],
        ['updated', '最近更新'],
      ].map(([v, t]) => h('option', {value: v, text: t}))
    );
    const flow = h('select', {'aria-label': '业务流程'});
    const search = h('input', {
      type: 'search',
      'aria-label': '搜索模板',
      placeholder: '搜索名称、业务流程、集合或编码…',
      autocomplete: 'off',
    });
    const viewList = h(
      'button',
      {
        type: 'button',
        'aria-label': '列表视图',
        title: '列表视图',
        'aria-pressed': 'true',
        onclick: () => {
          state.view = 'list';
          render();
        },
      },
      icon('table')
    );
    const viewGrid = h(
      'button',
      {
        type: 'button',
        'aria-label': '卡片视图',
        title: '卡片视图',
        'aria-pressed': 'false',
        onclick: () => {
          state.view = 'grid';
          render();
        },
      },
      icon('grid')
    );
    const pageSize = h(
      'select',
      {'aria-label': '每页模板数'},
      [12, 24, 48, 96].map(n => h('option', {value: String(n), text: String(n)}))
    );
    const available = h('strong', {text: '0'}),
      domains = h('strong', {text: '4'});
    ui.title = h('h1', {tabindex: '-1', text: '模板中心'});
    ui.side = h('aside', {class: 'catalog-sidebar', 'aria-label': '模板分类'});
    ui.stats = {available, domains};
    ui.flow = flow;
    ui.status = status;
    ui.search = search;
    ui.view = {list: viewList, grid: viewGrid};
    ui.count = h('span', {id: 'lib-count', role: 'status', text: ''});
    ui.reset = btn('清空筛选', clearFilters, 'small ghost');
    ui.chips = h('span');
    ui.items = h('ul', {class: 'catalog-items is-list', role: 'list', 'aria-label': '我的模板'});
    ui.prev = btn(
      '上一页',
      () => {
        state.page--;
        render();
      },
      'small'
    );
    ui.next = btn(
      '下一页',
      () => {
        state.page++;
        render();
      },
      'small'
    );
    ui.pageInfo = h('span');
    ui.pageSize = pageSize;
    ui.pagination = h(
      'div',
      {class: 'catalog-pagination'},
      h('label', null, '每页 ', pageSize, ' 个'),
      h('span', {class: 'flex'}),
      ui.prev,
      ui.pageInfo,
      ui.next
    );
    ui.samplesCount = h('span', {class: 'muted'});
    ui.sampleBody = h('div');
    ui.samples = h(
      'section',
      {class: 'sample-section', 'aria-labelledby': 'lib-samples-title'},
      h('div', {class: 'sample-head'}, h('h2', {id: 'lib-samples-title', text: '示例模板'}), ui.samplesCount),
      h('p', {
        class: 'help',
        text: '原型的 37 个模板，按四个业务域分组，只读；“复制到我的模板”按您当前数据集的字段对应后保存为自己的模板。示例不会出现在收藏与归档里。',
      }),
      ui.sampleBody
    );
    ui.notice = h('p', {class: 'catalog-notice-line', role: 'status', hidden: true});
    const header = h(
      'header',
      {class: 'library-header'},
      h(
        'div',
        {class: 'wordmark'},
        h('span', {class: 'brand-icon', 'aria-hidden': 'true'}, icon('layers')),
        h('span', null, 'IDP ', h('b', {text: 'REPORTING'}), h('small', {text: '模板中心'}))
      ),
      h('span', {class: 'flex'}),
      ...(ports.data
        ? [
            (ui.dataChip = btn('未选择数据', () => ports.data(ui.dataChip), 'data-chip is-empty', 'database', {
              id: 'lib-data',
              title: '当前数据：选择产品连接、数据集、公司范围与时点',
            })),
          ]
        : []),
      // S12-C: 我的模板 / 团队模板. The team's templates are the tenant's shared space; publishing one there is another publisher's approval.
      h(
        'div',
        {class: 'space-switch', role: 'group', 'aria-label': '模板空间'},
        (ui.spaceOwn = btn('我的模板', () => setSpace(''), 'small', null, {
          id: 'lib-space-own',
          'aria-pressed': 'true',
        })),
        (ui.spaceTeam = btn('团队模板', () => setSpace('team'), 'small', null, {
          id: 'lib-space-team',
          'aria-pressed': 'false',
        }))
      ),
      btn('返回设计器', () => ports.open('documents'), null, 'left'),
      btn('数据中心', e => (ports.data ? ports.data(e.currentTarget) : ports.open('workspace')), null, 'database', {
        id: 'lib-datacenter',
        title: '打开数据面板：数据集、字段与币种，以及查询',
      }),
      btn('新建模板', newTemplate, 'primary needs-designer', 'plus', {id: 'lib-new'})
    );
    const main = h(
      'main',
      {class: 'catalog-main', 'aria-labelledby': 'lib-title'},
      h(
        'div',
        {class: 'catalog-top'},
        // S09-C (ADR 0014 decision 7): the title block is the title and its lead; the prototype's English eyebrow is gone.
        h('div', null, ui.title, h('p', {text: '从业务流程找到模板，再选择版式、数据和版本。'})),
        h(
          'div',
          {class: 'catalog-stats'},
          available,
          h('span', {text: '可用模板'}),
          domains,
          h('span', {text: '业务域'})
        )
      ),
      h(
        'div',
        {class: 'catalog-search'},
        h('label', null, icon('search'), search),
        btn('导入模板', importDialog, 'needs-designer', 'upload', {id: 'lib-import'})
      ),
      h(
        'div',
        {class: 'catalog-filters'},
        h('label', null, '业务流程 ', flow),
        h('label', null, '状态 ', status),
        h('label', null, '排序 ', sort),
        h('span', {class: 'flex'}),
        h('div', {class: 'catalog-view', role: 'group', 'aria-label': '视图'}, viewList, viewGrid)
      ),
      ui.notice,
      h('div', {class: 'catalog-results'}, ui.count, ui.chips, ui.reset),
      ui.items,
      ui.pagination,
      ui.samples,
      h('div', {class: 'catalog-notice', text: '分类不改变模板内容或保存版本；示例模板只读，复制后才是您的模板。'})
    );
    ui.title.id = 'lib-title';
    ui.shell = h('div', {class: 'library-shell'}, header, h('div', {class: 'catalog-layout'}, ui.side, main));
    root.replaceChildren(sprite(), ui.shell);
    search.addEventListener('input', () => {
      state.search = search.value;
      state.page = 0;
      renderResults();
    });
    flow.addEventListener('change', () => {
      state.flow = flow.value;
      state.page = 0;
      render();
    });
    status.addEventListener('change', () => {
      state.status = status.value;
      state.page = 0;
      render();
    });
    sort.addEventListener('change', () => {
      state.sort = sort.value;
      render();
    });
    pageSize.addEventListener('change', () => {
      state.pageSize = Number(pageSize.value) || 24;
      state.page = 0;
      render();
    });
    ui.sort = sort;
  }
  root.addEventListener(
    'click',
    e => {
      const b = e.target.closest && e.target.closest('button');
      if (b) trigger = b;
    },
    true
  );
  build();
  const api = {
    // The page is shown: (re)read the identity's templates (their last-opened times and revisions move) and, once, the sample pack.
    show() {
      alive = 1;
      api.data();
      refresh();
      if (!state.samplesLoaded) loadSamples();
      ui.title.focus();
    },
    // The data chip in the header (S07-A): the console's current data context, refreshed whenever it changes.
    data() {
      const d = ports.dataSummary ? ports.dataSummary() : null;
      if (ui.dataChip) {
        ui.dataChip.replaceChildren(
          icon('database'),
          document.createTextNode(d ? d.text + ' · ' + d.kind : '未选择数据')
        );
        ui.dataChip.classList.toggle('is-empty', !d);
        ui.dataChip.title = d
          ? '当前数据：' + d.text + ' · ' + d.kind + '（点击修改）'
          : '当前数据：选择产品连接、数据集、公司范围与时点';
      }
      if (alive) render();
    },
    clear() {
      // S12-C: a new session starts in its own space.
      window.reportingTemplateSpace.set('');
      if (ui.spaceOwn) {
        ui.spaceOwn.setAttribute('aria-pressed', 'true');
        ui.spaceTeam.setAttribute('aria-pressed', 'false');
        ui.title.textContent = '模板中心';
      }
      alive = 0;
      for (const d of dialogs.slice()) d.close();
      dialogs = [];
      ui.shell.inert = false;
      Object.assign(state, {
        own: [],
        samples: [],
        loaded: false,
        loading: false,
        samplesLoaded: false,
        failed: '',
        quick: 'all',
        domain: '',
        flow: '',
        collection: '',
        search: '',
        status: 'all',
        sort: 'name',
        view: 'list',
        page: 0,
        pageSize: 24,
      });
      state.details.clear();
      queue = Promise.resolve();
      ui.search.value = '';
      ui.status.value = 'all';
      ui.sort.value = 'name';
      root.querySelectorAll('.catalog-items').forEach(e => e.replaceChildren());
      ui.side.replaceChildren();
    },
    refresh,
  };
  return api;
};
