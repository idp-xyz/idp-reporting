'use strict';
// The issue page (S09-G, ADR 0014 decision 12): 正式出具 as a page of its own, reached from the rail, in the console only (the embed page has
// no pages and keeps the document studio's dialog). Issuing is the operator's recurring task and the register (出具记录) its audit record;
// both lived in the design tool's 检查与发布 dialog, behind the designer's own ritual (版本 › 打开已发布模板). Here the page takes the data
// context the console already holds (a frozen snapshot, from the chip's drawer), lists the published templates bound to that dataset by
// name — one's own and the tenant's shared space (S12-C), each card saying which —, takes the row (a banded 2.x template issues the group
// that holds it, as the studio did), asks once (the template, its version, its space, the row or group, the product that gives the number)
// and issues through the same request the studio made (POST /issues or /team/issues with an Idempotency-Key, the job polled, the file
// downloaded under its number after the digest check), then refreshes the register, which is the page's main table. The rules do not
// move: only a published version is issued (the server refuses anything else, TEMPLATE_NOT_PUBLISHED), the number is the source
// product's, nothing is stored in the browser. The issue is an issuer's (S12-A2, ADR 0018): without the role the rail hides the page
// and the page, reached by its address, says so; the register stays readable.
//
// Ports (app.js): call(path, options, label) → the server's answer or a thrown, already notified error; snapshot() → the current frozen
// snapshot id or null; dataSummary() → the chip's summary or null; query() → the current query (integration_id, dataset) or null;
// issuance() → the server offers issuing; previews() → PDF rendering is on; issuer() → the session's roles allow the issue;
// issue(body, banded, then, space) → the studio's issue port (submit, poll, download, notify) for a template of that space;
// issued(id, number, sha256) → the studio's download port; preview(body, path) → the studio's PDF download port; records →
// documents.js's {check, rows} so the register is one rendering; data(from) → opens the data drawer; open(page) → a page. The shell's
// 出具前确认 drawer ({open, close, isOpen}) is given after construction through dialog(d), once the shell's drawers exist.
window.createReportingIssuePage = function (ports) {
  const $ = id => document.getElementById(id);
  const node = (tag, text, className) => {
    const n = document.createElement(tag);
    if (text !== undefined) n.textContent = text;
    if (className) n.className = className;
    return n;
  };
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
  const button = (text, fn, className, iconName, id) => {
    const b = node('button', undefined, className || 'btn');
    b.type = 'button';
    if (iconName) b.append(icon(iconName));
    b.append(document.createTextNode(text));
    if (id) b.id = id;
    b.addEventListener('click', fn);
    return b;
  };
  const root = $('page-issue');
  if (!root) return null;
  const validID = id => typeof id === 'string' && /^[a-f0-9]{32}$/.test(id);
  const is2 = c => c && (c.schema_version === '2.0' || c.schema_version === '2.1');
  // The two template spaces (S12-C): one's own and the tenant's shared one, each with its routes and its word on a card.
  const spaces = [
    {space: '', word: '我的', pill: 'pill neutral'},
    {space: 'team', word: '团队', pill: 'pill blue'},
  ];
  const spaceOf = value => (value === 'team' ? 'team' : '');
  const spaceWord = space => spaces.find(s => s.space === space).word;
  const pathOf = (path, space) => window.reportingTemplateSpace.path(path, space);

  // ---- the page's regions -----------------------------------------------------------------------------------------------------------------
  const heading = node('div', undefined, 'page-heading');
  const titleBlock = node('div');
  titleBlock.append(
    node('h1', '正式出具'),
    node('p', '用已发布的模板，按冻结快照的一行出具带编号的正式文件；编号由来源产品确认。')
  );
  const state = node('span', '未选择数据', 'badge neutral');
  state.id = 'issue-state';
  heading.append(titleBlock, state);

  // The data context: what the chip holds, and the way to change it.
  const context = node('div', undefined, 'restore-panel');
  const contextLabel = node('label', '当前数据');
  contextLabel.htmlFor = 'issue-data-text';
  const contextText = node('span', '未选择数据', 'help');
  contextText.id = 'issue-data-text';
  const contextButton = button('选择数据…', () => ports.data(contextButton), 'btn', 'database', 'issue-data-open');
  context.append(contextLabel, contextText, node('span', undefined, 'flex'), contextButton);

  // The templates and the row: the form of the page.
  const form = node('article', undefined, 'panel');
  const formHead = node('div', undefined, 'panel-heading');
  const formTitle = node('div');
  formTitle.append(
    node('h2', '出具'),
    node('p', '当前数据集的已发布模板，我的与团队空间的都在；一张模板一个编号序列由来源产品决定。')
  );
  const refreshTemplates = button(
    '刷新模板',
    () => loadTemplates(),
    'btn ghost small',
    'refresh',
    'issue-templates-refresh'
  );
  formHead.append(formTitle, refreshTemplates);
  const templates = node('div', undefined, 'issue-templates');
  templates.id = 'issue-templates';
  templates.setAttribute('role', 'radiogroup');
  templates.setAttribute('aria-label', '已发布模板');
  const templatesNote = node('p', '', 'help');
  templatesNote.id = 'issue-templates-note';
  const rowField = node('div', undefined, 'issue-row');
  const rowLabel = node('label', '冻结快照的行号（从 1 开始）');
  rowLabel.htmlFor = 'issue-row';
  const rowInput = node('input');
  rowInput.id = 'issue-row';
  rowInput.type = 'number';
  rowInput.min = '1';
  rowInput.max = '1000';
  rowInput.value = '1';
  const rowNote = node('span', '分带模板（2.x）按这一行所在的分组出具。', 'help');
  rowNote.id = 'issue-row-note';
  rowField.append(rowLabel, rowInput, rowNote);
  const actions = node('div', undefined, 'query-actions');
  const actionsNote = node('span', '', 'help');
  actionsNote.id = 'issue-note';
  const actionButtons = node('div');
  const previewButton = button('PDF 预览', () => preview(), 'btn', 'eye', 'issue-preview');
  // The issue is an issuer's (S12-A2): the stylesheet hides what carries needs-issuer when the session lacks the role.
  const goButton = button('正式出具', () => confirmIssue(), 'btn primary needs-issuer', 'stamp', 'issue-go');
  actionButtons.append(previewButton, goButton);
  actions.append(actionsNote, actionButtons);
  form.append(formHead, templates, templatesNote, rowField, actions);

  // The register: the page's main table.
  const register = node('article', undefined, 'panel');
  const registerHead = node('div', undefined, 'panel-heading');
  const registerTitle = node('div');
  registerTitle.append(
    node('h2', '出具记录'),
    node('p', '当前身份的出具记录，最新在前，最多 50 条；记录永久保留，文件按保留期清除。')
  );
  const registerCount = node('span', '尚未读取', 'badge neutral');
  registerCount.id = 'issue-records-note';
  const registerRefresh = button(
    '刷新记录',
    () => loadRecords(),
    'btn ghost small',
    'refresh',
    'issue-records-refresh'
  );
  const registerTools = node('div', undefined, 'row');
  registerTools.append(registerCount, registerRefresh);
  registerHead.append(registerTitle, registerTools);
  const registerEmpty = node('div', undefined, 'empty');
  registerEmpty.id = 'issue-records-empty';
  registerEmpty.append(node('b', '尚无出具记录'), node('p', '出具的文件在这里按编号列出，可再次下载。'));
  const registerWrap = node('div', undefined, 'table-wrap');
  registerWrap.id = 'issue-records-wrap';
  registerWrap.hidden = true;
  const table = node('table', undefined, 'tasks-table');
  const thead = node('thead');
  const headRow = node('tr');
  for (const h of [
    '编号',
    '状态',
    '出具时间（UTC）',
    '模板 · 版本',
    '快照 · 行/分组',
    'PDF 摘要 · 页数',
    '来源确认',
    '操作',
  ])
    headRow.append(node('th', h));
  thead.append(headRow);
  const tbody = node('tbody');
  tbody.id = 'issue-records-body';
  table.append(thead, tbody);
  registerWrap.append(table);
  register.append(registerHead, registerEmpty, registerWrap);

  root.append(heading, context, form, register);

  // ---- state --------------------------------------------------------------------------------------------------------------------------------
  let dialog = {open() {}, close() {}, isOpen: () => false};
  // A template is known by its space and id (an id is unique, the space names its routes).
  const keyOf = (space, id) => space + ':' + id;
  let list = [], // the published templates of the current dataset, both spaces: {key, id, space, version, name, config}
    picked = '', // the key of the picked template
    preselect = null, // {id, space} the studio asked for, until the list has it
    loading = false,
    generation = 0, // the latest reading of the list: an earlier one that answers later is dropped
    loaded = '', // the dataset the list was loaded for (integration@id@version)
    recordsLoaded = false;
  const datasetKey = () => {
    const q = ports.query();
    return q ? q.integration_id + '@' + q.dataset.id + '@' + q.dataset.version : '';
  };
  const chosen = () => list.find(t => t.key === picked) || null;
  const wanted = t => !!preselect && t.id === preselect.id && t.space === preselect.space;
  // Ready to issue: a snapshot, a picked template whose published version is known (its config decides row or group), a server that
  // issues and a session whose roles allow it.
  const ready = () => !!ports.snapshot() && !!chosen() && !!chosen().config && ports.issuance() && ports.issuer();

  // ---- rendering ----------------------------------------------------------------------------------------------------------------------------
  function renderContext() {
    const s = ports.dataSummary();
    const snapshot = !!ports.snapshot();
    contextText.textContent = s ? s.text + ' · ' + s.kind : '未选择数据';
    state.textContent = !s ? '未选择数据' : snapshot ? '冻结快照' : '查询预览';
    state.className = 'badge ' + (snapshot ? '' : 'neutral');
  }
  function renderTemplates() {
    templates.replaceChildren();
    const q = ports.query();
    if (!q) {
      templatesNote.textContent = '先选择数据并冻结快照：出具按冻结快照的行进行。';
      return;
    }
    if (loading) {
      templatesNote.textContent = '正在读取已发布模板…';
      return;
    }
    if (!list.length) {
      templatesNote.textContent = '当前数据集没有已发布的模板。到模板中心复制一个示例，在文档工作室发布后再来。';
      return;
    }
    for (const t of list) {
      const label = node('label', undefined, 'issue-template');
      const radio = node('input');
      radio.type = 'radio';
      radio.name = 'issue-template';
      radio.value = t.id;
      radio.dataset.space = t.space;
      radio.checked = t.key === picked;
      radio.addEventListener('change', () => {
        picked = t.key;
        render();
        loadPicked();
      });
      const text = node('span', undefined, 'issue-template-text');
      text.append(
        node('b', t.name || '未命名模板'),
        node(
          'small',
          '已发布 v' +
            t.version +
            (t.config ? ' · ' + (is2(t.config) ? '分带模板 · 按分组出具' : '单页模板 · 按行出具') : '')
        )
      );
      // The space the template is in (S12-C): its word, and the title says what that means for the issue.
      const space = node('span', spaceWord(t.space), spaces.find(s => s.space === t.space).pill);
      space.title = t.space === 'team' ? '团队模板：租户共享空间的已发布版本' : '我的模板：当前身份自己的已发布版本';
      const id = node('code', t.id);
      id.title = '模板标识';
      label.append(radio, text, space, id);
      templates.append(label);
    }
    templatesNote.textContent = list.length + ' 个已发布模板；只有已发布版本可以出具。';
  }
  function renderActions() {
    const t = chosen();
    goButton.disabled = !ready();
    previewButton.disabled = !ports.snapshot() || !t || !ports.previews();
    previewButton.hidden = !ports.previews();
    rowNote.hidden = !t || !t.config || !is2(t.config);
    if (!ports.issuer())
      actionsNote.textContent =
        '当前角色不能出具：正式出具需要“正式出具”角色，请联系平台运营方在身份绑定中授予；出具记录仍可查看。';
    else if (!ports.issuance()) actionsNote.textContent = '服务端未启用正式出具（需要 PDF 渲染与来源产品的出具能力）。';
    else if (!ports.snapshot()) actionsNote.textContent = '出具需要冻结快照：先在“选择数据…”里创建快照。';
    else if (!t) actionsNote.textContent = '选择一个已发布模板。';
    else if (!t.config) actionsNote.textContent = '正在读取已发布版本…';
    else actionsNote.textContent = '出具后，编号与文件进入下面的出具记录。';
    goButton.title = goButton.disabled ? actionsNote.textContent : '';
  }
  function render() {
    renderContext();
    renderTemplates();
    renderActions();
  }

  // ---- the published templates of the current dataset, in both spaces -----------------------------------------------------------------
  function loadTemplates() {
    const q = ports.query();
    if (!q) {
      list = [];
      picked = '';
      loaded = '';
      render();
      return;
    }
    const key = datasetKey(),
      reading = ++generation;
    loading = true;
    render();
    Promise.all(spaces.map(s => ports.call(pathOf('/document-templates', s.space), undefined, '已发布模板')))
      .then(listings => {
        if (reading !== generation || key !== datasetKey()) return;
        const own = [];
        listings.forEach((entries, i) => {
          if (!Array.isArray(entries) || entries.length > 1000) throw Error('无法验证模板列表。');
          for (const e of entries)
            if (
              e &&
              validID(e.id) &&
              Number.isSafeInteger(e.published_version) &&
              e.published_version >= 1 &&
              e.binding &&
              e.binding.integration_id === q.integration_id &&
              e.binding.dataset &&
              e.binding.dataset.id === q.dataset.id &&
              e.binding.dataset.version === q.dataset.version
            )
              own.push({entry: e, space: spaces[i].space});
        });
        // The listing names each template, its latest version's name and its published version; the published version itself is read
        // when a template is picked (loadPicked), since it decides whether the issue is by row or by group.
        list = own.map(({entry, space}) => ({
          key: keyOf(space, entry.id),
          id: entry.id,
          space,
          version: entry.published_version,
          name: typeof entry.name === 'string' ? entry.name : '',
          config: null,
        }));
        loaded = key;
        const asked = list.find(wanted);
        if (asked) picked = asked.key;
        else if (!list.some(t => t.key === picked)) picked = list.length === 1 ? list[0].key : '';
        preselect = null;
        loadPicked();
      })
      .catch(error => {
        if (reading === generation && (!error || !error.stale)) {
          list = [];
          picked = '';
        }
      })
      .finally(() => {
        if (reading !== generation) return;
        if (key === datasetKey()) loading = false;
        render();
      });
  }

  // The published version of the picked template: what will be issued (its name and whether it is banded), read once per template
  // through the routes of its space.
  function loadPicked() {
    const t = chosen();
    if (!t || t.config || t.loading) return;
    t.loading = true;
    const key = datasetKey();
    ports
      .call(pathOf('/document-templates/' + t.id + '/published', t.space), undefined, '已发布模板')
      .then(v => {
        if (key !== datasetKey() || !list.includes(t)) return;
        if (!v || v.id !== t.id || !Number.isSafeInteger(v.version) || v.version !== t.version || !v.config)
          throw Error('无法验证已发布模板。');
        t.config = v.config;
        if (typeof v.config.name === 'string' && v.config.name) t.name = v.config.name;
      })
      .catch(() => {})
      .finally(() => {
        t.loading = false;
        render();
      });
  }

  // ---- the register ----------------------------------------------------------------------------------------------------------------------
  function showRecords(values) {
    try {
      ports.records.check(values);
    } catch (e) {
      tbody.replaceChildren();
      registerWrap.hidden = true;
      registerEmpty.hidden = false;
      registerEmpty.replaceChildren(node('b', '出具记录无法验证'), node('p', e.message));
      registerCount.textContent = '无法验证';
      return;
    }
    ports.records.rows(tbody, values, v => ports.issued(v.artifact_id, v.number, v.sha256));
    registerWrap.hidden = !values.length;
    registerEmpty.hidden = !!values.length;
    registerEmpty.replaceChildren(node('b', '尚无出具记录'), node('p', '出具的文件在这里按编号列出，可再次下载。'));
    registerCount.textContent = values.length + ' 条';
    recordsLoaded = true;
  }
  function loadRecords() {
    ports
      .call('/issues?limit=50', undefined, '出具记录')
      .then(showRecords)
      .catch(() => {});
  }

  // ---- preview and issue ---------------------------------------------------------------------------------------------------------------------
  const rowIndex = () => {
    const row = Number(rowInput.value);
    if (!Number.isSafeInteger(row) || row < 1 || row > 1000) throw Error('请填写 1–1000 之间的行号。');
    return row - 1;
  };
  function preview() {
    const t = chosen();
    if (!t || !t.config || !ports.snapshot()) return;
    let row;
    try {
      row = rowIndex();
    } catch (e) {
      actionsNote.textContent = e.message;
      return;
    }
    ports.preview(
      {version: t.version, snapshot_id: ports.snapshot(), row_index: row},
      pathOf('/document-templates/' + t.id + '/previews', t.space)
    );
  }
  let plan = null;
  function confirmIssue() {
    const t = chosen();
    if (!ready()) return;
    let row;
    try {
      row = rowIndex();
    } catch (e) {
      actionsNote.textContent = e.message;
      return;
    }
    // A 2.x template is issued for the row group that holds the row (S03-K), as the studio does; a form without group fields by row.
    const banded = is2(t.config) && !(is2(t.config) && !t.config.bands.detail && !t.config.group);
    plan = {template: t, row, banded};
    const s = ports.dataSummary();
    const dl = $('issue-summary');
    dl.replaceChildren();
    for (const [k, v] of [
      ['模板', (t.name || '未命名模板') + ' · 已发布 v' + t.version],
      ['空间', t.space === 'team' ? '团队模板（租户共享空间）' : '我的模板'],
      ['数据', s ? s.text : '—'],
      ['快照', ports.snapshot()],
      [
        banded ? '分组' : '行',
        banded ? '第 ' + (row + 1) + ' 行所在的分组（服务端按模板的分组字段确定）' : '第 ' + (row + 1) + ' 行',
      ],
      ['编号', '由来源产品分配并确认；任务成功后才有编号和文件'],
    ])
      dl.append(node('dt', k), node('dd', v));
    $('issue-confirm-note').textContent = '出具是一次提交：同一待确认提交会复用原请求标识，不会重复出具。';
    dialog.open(goButton);
  }
  $('issue-confirm').addEventListener('click', () => {
    if (!plan) return;
    const p = plan;
    plan = null;
    dialog.close();
    ports.issue(
      {template_id: p.template.id, version: p.template.version, snapshot_id: ports.snapshot(), row_index: p.row},
      p.banded,
      () => loadRecords(),
      p.template.space
    );
  });

  return {
    // The shell's 出具前确认 drawer, once the shell has made it.
    dialog(d) {
      dialog = d;
    },
    // Told when the page opens, with the template to preselect ({id, space}) when the studio led here: the context is drawn, the
    // templates read for the current dataset, the register read once.
    show(detail) {
      if (detail && validID(detail.id)) preselect = {id: detail.id, space: spaceOf(detail.space)};
      render();
      if (datasetKey() !== loaded) loadTemplates();
      else if (preselect) {
        const asked = list.find(wanted);
        if (asked) {
          picked = asked.key;
          preselect = null;
        } else loadTemplates();
        render();
        loadPicked();
      }
      if (!recordsLoaded) loadRecords();
    },
    // Told when the data context changed (a query, a snapshot, a cleared result): the page follows it when it is on screen.
    sync() {
      if (root.hidden) {
        if (datasetKey() !== loaded) {
          list = [];
          picked = '';
          loaded = '';
        }
        return;
      }
      render();
      if (datasetKey() !== loaded) loadTemplates();
    },
    // After an issue finished: the register is the page's record of it.
    issued: () => loadRecords(),
    clear() {
      generation++;
      list = [];
      picked = '';
      preselect = null;
      loaded = '';
      loading = false;
      recordsLoaded = false;
      plan = null;
      tbody.replaceChildren();
      registerWrap.hidden = true;
      registerEmpty.hidden = false;
      registerCount.textContent = '尚未读取';
      rowInput.value = '1';
      render();
    },
  };
};
