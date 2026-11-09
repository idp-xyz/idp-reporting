'use strict';
(function () {
  const base = '/api/reporting/v1';
  // Embed mode (S03-N, ADR 0006, docs/EMBEDDING.md): the page served under /embed/ runs this same script for one view, with no token form. The
  // access token arrives from the parent page by postMessage (embed.js) and lives in state.token like the console's: in memory only, and sent only
  // as the Bearer header of requests to this origin.
  const embedded = location.pathname.startsWith('/embed/');
  let embed = null,
    embedRequest = null;
  const $ = id => document.getElementById(id);
  const pages = {
    workspace: '报表工作台',
    dashboard: '看板工作室',
    library: '模板中心',
    documents: '文档工作室',
    tasks: '任务工作区',
    audit: '操作记录',
    system: '能力与环境',
  };
  // The shell's wordmark names the page (S06-C) in the page's own words (S09-C, ADR 0014 decision 7); the studios and the centre carry
  // their own wordmarks, hidden inside the console.
  let board = null;
  const state = {
    token: '',
    epoch: 0,
    view: 0,
    catalogOperation: 0,
    catalogLoading: false,
    pageLimit: '25',
    session: null,
    capabilities: null,
    product: '',
    datasets: [],
    catalogs: new Map(),
    roles: null,
    audit: {events: [], more: '', scope: ''},
    connections: new Map(),
    jobs: new Map(),
    jobsFilter: {kind: '', state: ''},
    jobsMore: '',
    controllers: new Set(),
    releaseView: null,
    savedLayout: null,
    layoutLatest: 0,
    layout: null,
    snapshot: null,
    query: null,
    page: 0,
    cursor: '',
    lastResult: null,
    pending: null,
    busy: 0,
    starting: false,
    resource: null,
    opened: false,
  };
  const dashboard = window.createReportingDashboard({
    active: () => !!state.session && !!state.lastResult && !state.catalogLoading && !state.busy,
    changed: () => {
      if (board) board.sync();
    },
    exchange: (path, options, apply, label) =>
      perform(
        async m => {
          const value = await request(path, options);
          if (!current(m)) throw new Stale();
          apply(value);
        },
        {label: label, uncertain: '操作结果尚未确认，保存操作请刷新服务器看板列表；发布操作请刷新发布状态核对。'}
      ),
  });
  const documents = window.createReportingDocuments({
    // The studio's crumb, wordmark and rail go to the template centre (S06-B part 3); the embed page has no centre and keeps the template chooser.
    ...(embedded ? {} : {library: () => openPage('library')}),
    // Private images (schema 2.1): an upload like a background's; the listing and the bytes of an image are read in the background (no busy state, no notice: the canvas shows a labelled box until they arrive).
    uploadImage: (file, binding, apply) =>
      perform(
        async m => {
          if (file.size < 1 || file.size > 524288) throw Error('图片超过512KiB。');
          const bytes = new Uint8Array(await file.arrayBuffer());
          if (!current(m)) throw new Stale();
          if (bytes.length !== file.size) throw Error('文件读取长度不一致。');
          let raw = '';
          for (const b of bytes) raw += String.fromCharCode(b);
          let value;
          try {
            value = await request('/document-images', {method: 'POST', body: {binding, image: btoa(raw)}});
          } catch (error) {
            if (error && error.status === 400) error.code = 'IMAGE_INVALID';
            if (error && error.status === 413) error.code = 'IMAGE_LIMIT';
            if (error && error.status === 422) error.code = 'IMAGE_REFUSED';
            throw error;
          }
          if (!current(m)) throw new Stale();
          apply(value);
        },
        {label: '图片上传', uncertain: '上传结果未确认；请刷新图片列表核对，勿直接重复上传。'}
      ),
    background: (path, apply, failed) => {
      if (!state.session) return false;
      const m = mark();
      request(path)
        .then(value => {
          if (current(m)) apply(value);
        })
        .catch(error => {
          if (!(error instanceof Stale) && current(m)) failed(error);
        });
      return true;
    },
    fetchImage: (known, apply) => {
      const media = {png: 'image/png', jpeg: 'image/jpeg'}[known.format];
      if (!state.session || !media || !isID(known.id)) return false;
      const m = mark();
      request('/document-images/' + known.id + '/content', {binary: media})
        .then(async bytes => {
          if (!current(m)) return;
          const actual = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)))
            .map(b => b.toString(16).padStart(2, '0'))
            .join('');
          if (!current(m)) return;
          if (actual !== known.sha256) {
            apply(null);
            return;
          }
          let raw = '';
          for (const b of bytes) raw += String.fromCharCode(b);
          apply('data:' + media + ';base64,' + btoa(raw));
        })
        .catch(() => {
          if (current(m)) apply(null);
        });
      return true;
    },
    upload: (file, binding, apply) =>
      perform(
        async m => {
          if (file.size < 1 || file.size > 524288) throw Error('PNG超过512KiB。');
          const bytes = new Uint8Array(await file.arrayBuffer());
          if (!current(m)) throw new Stale();
          if (bytes.length !== file.size) throw Error('文件读取长度不一致。');
          let raw = '';
          for (const b of bytes) raw += String.fromCharCode(b);
          const value = await request('/document-backgrounds', {method: 'POST', body: {binding, png: btoa(raw)}});
          if (!current(m)) throw new Stale();
          apply(value);
        },
        {label: '底稿上传', uncertain: '上传结果未确认；请刷新底稿列表核对，勿直接重复上传。'}
      ),
    active: () => !!state.session && !!state.lastResult && !state.catalogLoading && !state.busy,
    previews: () =>
      !!state.capabilities && Array.isArray(state.capabilities.formats) && state.capabilities.formats.includes('pdf'),
    issuance: () =>
      !!state.capabilities &&
      Array.isArray(state.capabilities.formats) &&
      state.capabilities.formats.includes('pdf') &&
      state.capabilities.issuance === true,
    snapshot: () => state.snapshot,
    issue: (body, banded) =>
      perform(m => createIssue(m, body, banded === true), {
        label: '正式出具',
        snapshot: body.snapshot_id,
        uncertain: '出具任务提交结果未确认；请刷新出具记录核对，勿直接重复提交（同一待确认提交会复用原请求标识）。',
      }),
    issued: (id, number, sha256) =>
      perform(m => download(id, m, 'pdf', issuedName(number), sha256), {label: '出具下载'}),
    exchange: (path, options, apply, label) =>
      perform(
        async m => {
          const value = await request(path, options);
          if (!current(m)) throw new Stale();
          apply(value);
          if (embedded) announce(path, options, value);
        },
        {label, uncertain: '写入结果未确认；保存请刷新模板列表，发布请刷新发布状态核对。'}
      ),
    check: (body, apply) =>
      perform(
        async m => {
          let value;
          try {
            value = await request('/document-checks', {method: 'POST', body});
          } catch (error) {
            if (error && error.status === 404 && error.code === 'REQUEST_FAILED') {
              error.status = 0;
              error.code = 'CHECK_UNAVAILABLE';
            }
            if (error && error.status === 422) error.code = 'CHECK_UNSUPPORTED';
            throw error;
          }
          if (!current(m)) throw new Stale();
          apply(value);
        },
        {label: '样本检查', snapshot: body.snapshot_id}
      ),
    download: (body, path = '/document-previews', onRefusal) =>
      perform(
        async m => {
          let bytes;
          try {
            bytes = await request(path, {method: 'POST', body, binary: 'application/pdf'});
          } catch (error) {
            if (error && error.status === 422 && error.code === 'UNSUPPORTED_COMBINATION') {
              error.code = 'DOCUMENT_LAYOUT';
              if (error.detail && current(m) && onRefusal) onRefusal(error.detail);
            }
            throw error;
          }
          if (!current(m)) throw new Stale();
          if (String.fromCharCode(...bytes.subarray(0, 5)) !== '%PDF-') throw new Failure(409, 'INTEGRITY_FAILURE');
          documents.pdfPages(pdfPageCount(bytes));
          const url = URL.createObjectURL(new Blob([bytes], {type: 'application/pdf'}));
          const link = node('a');
          link.href = url;
          link.download = 'document-preview.pdf';
          document.body.append(link);
          link.click();
          link.remove();
          setTimeout(() => URL.revokeObjectURL(url), 1000);
          notify('PDF预览已通过摘要校验，下载已发起。', 'success');
        },
        {label: 'PDF预览', snapshot: body.snapshot_id, uncertain: '预览生成结果未确认；可重新生成，不会正式出具。'}
      ),
  });
  // The template centre (library.js; the console only, the embed page has no such page): its ports are the request function, one verified PDF download, the notice and the studio's bridge.
  const library =
    !embedded && window.createReportingLibrary && $('page-library')
      ? window.createReportingLibrary({
          call: async (path, options, label, messages) => {
            const m = mark(),
              stale = () => Object.assign(Error('stale'), {stale: true});
            try {
              const value = await request(path, options);
              if (!current(m)) throw stale();
              return value;
            } catch (error) {
              if (error instanceof Stale || error.stale || !current(m)) throw stale();
              notify(label + '：' + ((messages && messages[error.status]) || errorMessage(error)), 'error');
              throw error;
            }
          },
          pdf: async (path, label, name) => {
            const m = mark();
            try {
              const bytes = await request(path, {method: 'POST', binary: 'application/pdf'});
              if (!current(m)) throw new Stale();
              if (String.fromCharCode(...bytes.subarray(0, 5)) !== '%PDF-') throw new Failure(409, 'INTEGRITY_FAILURE');
              const url = URL.createObjectURL(new Blob([bytes], {type: 'application/pdf'}));
              const link = node('a');
              link.href = url;
              link.download = name || 'sample-preview.pdf';
              document.body.append(link);
              link.click();
              link.remove();
              setTimeout(() => URL.revokeObjectURL(url), 1000);
              notify('PDF预览已通过摘要校验，下载已发起。', 'success');
            } catch (error) {
              if (error instanceof Stale || !current(m)) throw Object.assign(Error('stale'), {stale: true});
              notify(label + '：' + errorMessage(error), 'error');
              throw error;
            }
          },
          notify,
          open: page => openPage(page),
          previews: () =>
            !!state.capabilities &&
            Array.isArray(state.capabilities.formats) &&
            state.capabilities.formats.includes('pdf'),
          studio: () => documents.bridge,
          data: from => drawer.open(from),
          dataSummary: () => dataSummary(),
        })
      : null;
  // The dashboard studio (board.js, S06-C): the studio's regions around the dashboard's controls, in the console and in the embed page alike.
  board = window.createReportingBoardStudio
    ? window.createReportingBoardStudio({
        dashboard,
        open: page => openPage(page),
        ...(embedded ? {} : {data: from => drawer.open(from)}),
      })
    : null;
  // S07-E: the most jobs the console keeps (the list, the pages read with 加载更多) and the size of a page of the server's listing.
  // The roles of ADR 0018 in the console's words (S12-A2).
  const roleNames = {
    viewer: '查看',
    designer: '设计',
    publisher: '发布',
    issuer: '正式出具',
    administrator: '审计管理',
  };
  const maxKnownJobs = 500,
    jobsPage = 50;
  const statuses = {
    queued: '排队中',
    running: '执行中',
    succeeded: '已完成',
    failed: '失败',
    cancelled: '已取消',
    restricted: '访问受限',
  };
  // detail is a layout or binding refusal's reason and place (S03-J), when the server named them and the shape is the known one.
  class Failure extends Error {
    constructor(status, code, detail) {
      super(code);
      this.status = status;
      this.code = code;
      if (detail !== undefined) this.detail = detail;
    }
  }
  class Stale extends Error {}
  function node(tag, text, className) {
    const n = document.createElement(tag);
    if (text !== undefined) n.textContent = text;
    if (className) n.className = className;
    return n;
  }
  function button(text, fn, className) {
    const b = node('button', text, 'button ' + (className || 'quiet small'));
    b.type = 'button';
    b.addEventListener('click', fn);
    return b;
  }
  // An icon of the shell's sprite (index.html and embed.html hold the same symbols).
  function icon(name) {
    const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    s.setAttribute('class', 'ic');
    s.setAttribute('aria-hidden', 'true');
    s.setAttribute('focusable', 'false');
    const u = document.createElementNS('http://www.w3.org/2000/svg', 'use');
    u.setAttribute('href', '#s-' + name);
    s.append(u);
    return s;
  }
  function iconBox(name, className) {
    const box = node('span', undefined, className);
    box.append(icon(name));
    return box;
  }
  // S07-A (ADR 0009): one data context, reachable from everywhere. The workbench's query panel is moved into the data drawer while it is open and put
  // back after (the parking the document studio uses for its own 参数与筛选 drawer); the chip in the header, the centre and the dashboard studio names
  // the context the console already holds (state.query, state.lastResult, state.snapshot). The embed page has neither chip nor drawer.
  const parked = [];
  function park(el, slot) {
    if (!el || !slot || el.parentNode === slot) return;
    parked.push({el, parent: el.parentNode, next: el.nextSibling, slot});
    slot.append(el);
  }
  function unpark(slot) {
    for (let i = parked.length - 1; i >= 0; i--) {
      const p = parked[i];
      if (p.slot !== slot) continue;
      if (p.parent && p.parent.isConnected) {
        if (p.next && p.next.parentNode === p.parent) p.parent.insertBefore(p.el, p.next);
        else p.parent.append(p.el);
      }
      parked.splice(i, 1);
    }
  }
  // A drawer of the shell: modal, focus-trapped, Esc closes and the focus returns; while it is open the named console elements are parked in its slots and put back on close.
  function makeDrawer(id, parks, focusIn, onToggle) {
    const overlay = $(id);
    if (!overlay) return {open() {}, close() {}, isOpen: () => false};
    let opener = null;
    const focusable = () =>
      Array.from(
        overlay.querySelectorAll(
          'button:not([disabled]),a[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'
        )
      ).filter(e => !e.hidden && !e.closest('[hidden]'));
    function trap(e) {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        close();
        return;
      }
      if (e.key !== 'Tab') return;
      const list = focusable();
      if (!list.length) return;
      const first = list[0],
        last = list[list.length - 1];
      if (e.shiftKey && (document.activeElement === first || !overlay.contains(document.activeElement))) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
    function open(from) {
      if (!overlay.hidden || !state.session) return;
      for (const d of drawers) if (d !== api) d.close();
      opener = from && from.focus ? from : document.activeElement;
      for (const [sel, slot] of parks) park(document.querySelector(sel), $(slot));
      overlay.hidden = false;
      document.querySelector('.shell').inert = true;
      document.addEventListener('keydown', trap, true);
      (focusIn() || focusable()[0] || overlay).focus();
      if (onToggle) onToggle(true);
    }
    function close() {
      if (overlay.hidden) return;
      overlay.hidden = true;
      document.removeEventListener('keydown', trap, true);
      for (const [, slot] of parks) unpark($(slot));
      document.querySelector('.shell').inert = false;
      if (onToggle) onToggle(false);
      if (opener && opener.isConnected) opener.focus();
      opener = null;
    }
    overlay.querySelectorAll('[data-act=close]').forEach(b => b.addEventListener('click', close));
    overlay.addEventListener('mousedown', e => {
      if (e.target === overlay) close();
    });
    const api = {open, close, isOpen: () => !overlay.hidden};
    drawers.push(api);
    return api;
  }
  const drawers = [];
  const drawer = makeDrawer('sh-d-data', [['.query-panel', 'sh-query-slot']], () =>
    $('query-companies').value ? $('preview') : $('query-companies')
  );
  // The tasks panel (S07-B): the tasks page's restore row, list and refresh button, reachable from every shell through the rail.
  const tasksPanel = makeDrawer(
    'sh-d-tasks',
    [
      ['#page-tasks .restore-panel', 'sh-tasks-slot'],
      ['#page-tasks article.panel', 'sh-tasks-slot'],
      ['#refresh-tasks', 'sh-tasks-foot'],
    ],
    () => $('tasks-body').querySelector('button') || $('restore-id'),
    on => {
      const b = document.querySelector('[data-panel="tasks"]');
      if (b) b.setAttribute('aria-expanded', String(on));
      // Opening the panel reads the server's list again (S07-C), quietly: it does not hold the page, and what is on screen stays until the answer.
      if (on && state.session && !state.starting) {
        const m = mark();
        loadJobs().catch(error => {
          if (!(error instanceof Stale)) fail(error, m, {label: '任务列表'});
        });
      }
    }
  );
  // The layout designer's version dialog and 发布前确认 are shell drawers; the shared component (versions.js) puts the focus in them.
  const lyDialogs = {
    versions: makeDrawer('ly-d-versions', [], () => null),
    publish: makeDrawer('ly-d-publish', [], () => null),
  };
  // The name of the dataset a query names, in the user's words (S09-C): the catalogue entry's title where the console has it, the id otherwise.
  function datasetName(dataset) {
    const known = state.datasets.find(d => d.dataset.id === dataset.id && d.dataset.version === dataset.version);
    return known ? datasetTitle(known) : dataset.id;
  }
  function dataSummary() {
    if (!state.lastResult || !state.query) return null;
    const q = state.query;
    const dataset = q.dataset.id + '@' + q.dataset.version;
    const text = datasetName(q.dataset) + ' · ' + q.company_ids.join('、') + ' · ' + q.as_of;
    return {
      dataset,
      scope: q.company_ids.join('、'),
      date: q.as_of,
      kind: state.snapshot ? '冻结快照' : '查询预览',
      text,
    };
  }
  function updateChip() {
    const chip = $('sh-data-chip');
    if (!chip) return;
    const s = dataSummary();
    chip.classList.toggle('is-empty', !s);
    $('sh-data-text').textContent = s ? s.text : '未选择数据';
    $('sh-data-kind').hidden = !s;
    $('sh-data-kind').textContent = s ? s.kind : '';
    $('sh-data-kind').className = 'pill ' + (s && s.kind === '冻结快照' ? 'green' : 'blue');
    // The technical id and version stay in the title (decision 7: identifiers are secondary text).
    chip.title = s
      ? '当前数据：' + s.text + ' · ' + s.kind + ' · 数据集 ' + s.dataset + '（点击修改）'
      : '选择产品连接、数据集、公司范围与时点';
    if (library && library.data) library.data();
  }
  // The page count of a PDF the server rendered, read from its page tree (/Type /Page objects, or the /Count of /Pages), for the studio's pager; 1 when it cannot be read.
  function pdfPageCount(bytes) {
    let text = '';
    for (let i = 0; i < bytes.length; i += 8192) text += String.fromCharCode(...bytes.subarray(i, i + 8192));
    const leaves = (text.match(/\/Type\s*\/Page(?![A-Za-z])/g) || []).length,
      counts = [...text.matchAll(/\/Type\s*\/Pages[^>]*?\/Count\s+(\d+)/g)]
        .map(m => Number(m[1]))
        .concat([...text.matchAll(/\/Count\s+(\d+)[^>]*?\/Type\s*\/Pages/g)].map(m => Number(m[1])));
    const n = Math.max(leaves, ...counts, 1);
    return Number.isSafeInteger(n) && n <= 1000 ? n : 1;
  }
  function copy(value) {
    return JSON.parse(JSON.stringify(value));
  }
  function isID(id) {
    return typeof id === 'string' && /^[a-f0-9]{32}$/.test(id);
  }
  // The workbook is the export without a format; other formats are named in the request.
  const exportFormats = {
    xlsx: {
      label: 'Excel',
      media: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      extension: 'xlsx',
      magic: 'PK',
    },
    pdf: {label: 'PDF', media: 'application/pdf', extension: 'pdf', magic: '%PDF-'},
  };
  function current(mark) {
    return mark.epoch === state.epoch && mark.view === state.view;
  }
  function mark() {
    return {epoch: state.epoch, view: state.view};
  }
  function notify(message, type) {
    $('notice').textContent = message;
    $('notice').className = 'notice' + (type ? ' ' + type : '');
    $('notice').hidden = false;
  }
  // One notification stack (ADR 0014 decision 3): on a studio page the shell's notice joins the studio's own toast stack (the document
  // studio's #st-toasts, the dashboard studio's .toast-stack), where the toasts are laid out in one column and never overlap; on every
  // other page it is the banner at the top of the main column, where it was. The element, its id, its classes and its role do not change.
  const noticeHome = {parent: null, next: null};
  function placeNotice(page) {
    const notice = $('notice');
    if (!notice) return;
    if (!noticeHome.parent) {
      noticeHome.parent = notice.parentNode;
      noticeHome.next = notice.nextSibling;
    }
    const stack =
      page === 'documents'
        ? $('st-toasts')
        : page === 'dashboard'
          ? document.querySelector('#page-dashboard .toast-stack')
          : null;
    if (stack) {
      if (notice.parentNode !== stack) stack.append(notice);
    } else if (notice.parentNode !== noticeHome.parent) {
      if (noticeHome.next && noticeHome.next.parentNode === noticeHome.parent)
        noticeHome.parent.insertBefore(notice, noticeHome.next);
      else noticeHome.parent.append(notice);
    }
  }
  // A disabled export says why (ADR 0014 decision 6): while a preview is on screen and no snapshot exists, the buttons carry the reason as
  // their title and the sentence stands beside them; with a snapshot, or before any result, nothing is said.
  function exportHint() {
    const needsSnapshot = !!state.session && !!state.lastResult && !state.snapshot;
    for (const id of ['export', 'export-pdf']) $(id).title = needsSnapshot ? '导出需要冻结快照：先创建快照' : '';
    if ($('export-hint')) $('export-hint').hidden = !needsSnapshot;
  }
  function busy(change) {
    state.busy = Math.max(0, state.busy + change);
    const blocked = !state.session || state.busy > 0 || state.catalogLoading;
    document.querySelectorAll('.action').forEach(b => (b.disabled = blocked));
    $('export').disabled = blocked || !state.snapshot;
    $('export-pdf').disabled = blocked || !state.snapshot;
    exportHint();
    $('next-page').disabled = blocked || !state.cursor;
    $('first-page').disabled = blocked || state.page <= 1;
    $('page-size').disabled = blocked || !state.snapshot;
    for (const id of ['query-dataset', 'query-companies', 'query-date', 'refresh-catalog'])
      $(id).disabled = !state.session || state.catalogLoading;
    // The centre tells what can be opened from whether the console is idle: a result that arrives while it is on screen (from the data drawer, S07-H) is shown once the work is done.
    if (change < 0 && !state.busy && library && library.data && !$('page-library').hidden) library.data();
  }
  function clearResult() {
    dashboard.clear();
    documents.clear();
    $('designer-columns').replaceChildren();
    $('layout-status').textContent = '查询数据后可设计布局。';
    state.snapshot = null;
    state.query = null;
    state.page = 0;
    state.cursor = '';
    state.lastResult = null;
    renderLayoutState();
    $('result-table').tHead.replaceChildren();
    $('result-table').tBodies[0].replaceChildren();
    $('result-summary').replaceChildren();
    $('result-summary').hidden = true;
    $('result-table-wrap').hidden = true;
    $('result-empty').hidden = false;
    $('start-steps').hidden = false;
    $('result-empty').replaceChildren(
      iconBox('table', 'empty-icon'),
      node('b', '从一个已发布的数据集开始'),
      node('p', '金额与大整数按原始精度显示。')
    );
    $('result-pager').hidden = true;
    $('result-evidence').replaceChildren();
    $('result-evidence').hidden = true;
    $('result-kind').textContent = '尚未查询';
    $('result-kind').className = 'badge neutral';
    $('result-title').textContent = '查询结果';
    updateChip();
    $('result-subtitle').textContent = '选择数据集并填写公司范围后开始查询。';
    $('refresh-result').disabled = true;
    $('export').disabled = true;
    $('export-pdf').disabled = true;
    exportHint();
  }
  function disconnect(message) {
    for (const d of drawers) d.close();
    dashboard.clear(true);
    documents.clear(true);
    if (library) library.clear();
    if (board) board.clear();
    state.epoch++;
    state.view++;
    state.catalogOperation++;
    state.catalogLoading = false;
    state.pageLimit = '25';
    $('page-size').value = '25';
    for (const c of state.controllers) c.abort();
    state.controllers.clear();
    state.releaseView = null;
    $('layout-release-info').textContent = '尚未读取发布状态。';
    $('layout-release-history').replaceChildren();
    state.savedLayout = null;
    state.layoutLatest = 0;
    $('layout-server-listing').replaceChildren();
    $('layout-server-status').textContent = '尚未关联服务器布局。';
    state.layout = null;
    $('layout-json').value = '';
    $('designer-columns').replaceChildren();
    $('layout-status').textContent = '查询数据后可设计布局。';
    state.token = '';
    state.session = null;
    state.capabilities = null;
    state.product = '';
    state.datasets = [];
    state.catalogs.clear();
    state.connections.clear();
    state.roles = null;
    for (const role of ['designer', 'publisher', 'issuer', 'administrator'])
      document.body.classList.remove('no-' + role);
    state.audit = {events: [], more: '', scope: ''};
    if ($('audit-scope')) $('audit-scope').value = '';
    if ($('audit-body')) renderAudit();
    if ($('connection-list')) $('connection-list').replaceChildren();
    if ($('sh-no-products')) $('sh-no-products').hidden = true;
    state.jobs.clear();
    state.jobsMore = '';
    state.jobsFilter = {kind: '', state: ''};
    for (const id of ['tasks-kind', 'tasks-state']) if ($(id)) $(id).value = '';
    state.pending = null;
    state.busy = 0;
    state.starting = false;
    state.resource = null;
    state.opened = false;
    clearResult();
    renderJobs();
    renderFields();
    $('identity-details').replaceChildren();
    $('capability-details').replaceChildren();
    $('export-pdf').hidden = true;
    for (const id of ['query-product', 'query-dataset']) $(id).replaceChildren();
    $('query-companies').value = '';
    $('query-date').value = '';
    $('query-timezone').textContent = '—';
    $('query-currency').textContent = '—';
    $('query-shape').textContent = '选择数据集后显示批准组合';
    $('restore-id').value = '';
    if (!embedded) $('access-token').value = '';
    $('actor-label').textContent = '未连接';
    $('tenant-label').textContent = '访问令牌仅保留在本次会话';
    $('connection-label').textContent = '尚未连接';
    $('connection-dot').className = 'status-dot';
    if (!embedded) $('logout').disabled = true;
    $('connection-overlay').hidden = false;
    document.querySelector('.shell').inert = true;
    if (!embedded) $('connect-button').disabled = false;
    $('notice').hidden = true;
    $('connect-error').hidden = !message;
    $('connect-error').textContent = message || '';
    busy(0);
    // The embed has nobody to sign in and must not take the focus from the product's page: it shows a card and waits for the parent's token.
    if (embedded) {
      document.querySelector('.shell').hidden = true;
      if (embed) embed.forget();
    } else $('access-token').focus();
  }
  // The quotas of an identity (S04-D): the notice names the quota and gives the number the server states in its capabilities.
  const quotaNames = ['jobs_in_flight', 'stored_results', 'admissions_per_hour', 'queries_in_flight'];
  function validQuotas(q) {
    return (
      !!q &&
      typeof q === 'object' &&
      !Array.isArray(q) &&
      Object.keys(q).sort().join() === [...quotaNames].sort().join() &&
      quotaNames.every(
        k => Number.isSafeInteger(q[k]) && q[k] >= 1 && q[k] <= (k === 'queries_in_flight' ? 64 : 100000)
      )
    );
  }
  function quotaMessage(name) {
    const n =
        state.capabilities && validQuotas(state.capabilities.quotas) ? state.capabilities.quotas[name] : undefined,
      at = n === undefined ? '' : ' (' + n + ')';
    return {
      jobs_in_flight: '已达到进行中任务上限' + at + '，请等待任务完成或取消后再试',
      stored_results: '已达到保留结果上限' + at + '，结果过期后自动释放',
      admissions_per_hour: '已达到每小时提交上限' + at + '，请稍后再试',
      queries_in_flight: '查询进行中过多' + at + '，请稍后再试',
    }[name];
  }
  function errorMessage(error) {
    if (error.code === 'QUOTA_EXCEEDED' && error.status === 429 && quotaNames.includes(error.quota))
      return quotaMessage(error.quota);
    if (error.status === 401) return '访问令牌无效或已失效，请重新连接。';
    // S12-A (ADR 0018): the binding's roles do not allow the action; the operator grants roles in the bindings.
    // S12-C: a version of the team's space is published by another publisher than the one who saved it.
    if (error.code === 'APPROVAL_REQUIRED')
      return '团队模板需要另一位发布者审批：保存这一版本的人不能发布它，请请同事发布。';
    if (error.status === 403 && error.code === 'AUTH_ROLE_DENIED')
      return '当前身份没有执行此操作的角色（设计、发布或正式出具），请联系平台运营方在身份绑定中授予。';
    if (error.status === 403)
      return error.code === 'INSUFFICIENT_TOKEN_SCOPE'
        ? '令牌缺少报表访问范围。'
        : '当前身份无权访问，或原数据范围已被撤销。';
    if (error.status === 404) return '未找到当前身份有权访问的资源；它也可能已过期。';
    if (error.code === 'OVERLOADED' && error.status === 503) return '服务繁忙，请稍后再试';
    if (error.status === 503) return '当前授权来源不可用，请稍后重试。';
    if (error.code === 'INTEGRITY_FAILURE') return '资源完整性校验失败，内容已停止显示。';
    if (error.code === 'TEMPLATE_NOT_PUBLISHED')
      return '只能正式出具当前已发布的模板版本；请刷新发布状态、打开已发布模板后重试。';
    if (error.status === 409) return '请求冲突、资源失效或完整性检查未通过，请重新查询。';
    if (error.status === 413) return '结果超过当前限额，请缩小范围。';
    if (error.code === 'IMAGE_INVALID')
      return '图片无效：只接受 PNG 或 JPEG（不含 CMYK），最多 2000 × 2000 像素，格式以内容为准。';
    if (error.code === 'IMAGE_LIMIT') return '图片超过限额：每张最多 512KiB，每个数据集最多 16 张。';
    if (error.code === 'IMAGE_REFUSED') return '渲染器无法嵌入这张图片，已拒绝上传。';
    if (error.code === 'CHECK_UNSUPPORTED') return '样本检查被拒绝：模板用到的底稿在当前服务端不可用。';
    if (error.code === 'CHECK_UNAVAILABLE') return '服务端未启用样本检查（它随 PDF 渲染器的版面检查提供）。';
    if (error.code === 'DOCUMENT_LAYOUT')
      return (
        '模板无法排版：金额、累计金额或承前/过次/合计标签放不下所在列，表头或行高超过表区，条码或二维码需要的模块窄于最小值（条码 0.25 毫米、二维码 0.4 毫米）、条高不足或值无法编码，或超过 100 页；请加宽列或码框、减小字号或改用更短的值。' +
        (error.detail
          ? '位置：' +
            window.reportingRefusal.text(
              error.detail,
              state.lastResult ? Object.fromEntries(state.lastResult.columns.map(c => [c.id, c.label])) : {}
            ) +
            '。'
          : '')
      );
    if (error.code === 'ISSUE_UNSUPPORTED')
      return '该数据集的来源产品不支持正式出具，或服务端未启用出具；2.0 分带模板按分组出具，还要求来源产品声明支持分组出具（合同版本 2）；也可能是所选行或其分组含有文档无法打印的值，或模板用到的底稿在当前服务端不可用。';
    if (error.status === 422) return '当前数据集不支持此查询组合。';
    if (error.status === 400) return '请检查数据集、公司范围及数据时点。';
    if (error.code === 'INVALID_RESPONSE') return '服务返回了无法验证的响应。';
    return '请求结果尚未确认，请稍后重试；同一待确认提交会复用原请求标识。';
  }
  function fail(error, m, context) {
    if (error instanceof Stale || (m && !current(m)) || (error.name === 'AbortError' && !context?.uncertain)) return;
    const affectsResult = !context || (context.snapshot && context.snapshot === state.snapshot);
    if (
      affectsResult &&
      error.code !== 'TEMPLATE_NOT_PUBLISHED' &&
      error.code !== 'OVERLOADED' &&
      ([403, 404, 409, 503].includes(error.status) || error.code === 'INVALID_RESPONSE')
    )
      clearResult();
    notify(
      (context ? context.label + '：' : '') +
        (context?.uncertain && !error.status && error.code !== 'INVALID_RESPONSE'
          ? context.uncertain
          : errorMessage(error)),
      'error'
    );
    // A quota refusal is the caller's own limit, not an error of the frame: the host is not told.
    if (embed && error.code !== 'QUOTA_EXCEEDED') embed.error(embedCode(error));
  }
  async function readBounded(response, limit) {
    const declared = response.headers.get('Content-Length');
    if (declared && (!/^\d+$/.test(declared) || Number(declared) > limit)) throw new Failure(0, 'INVALID_RESPONSE');
    if (!response.body) throw new Failure(0, 'INVALID_RESPONSE');
    const reader = response.body.getReader();
    const chunks = [];
    let length = 0;
    try {
      while (true) {
        const next = await reader.read();
        if (next.done) break;
        length += next.value.length;
        if (length > limit) throw new Failure(0, 'INVALID_RESPONSE');
        chunks.push(next.value);
      }
    } finally {
      await reader.cancel().catch(() => {});
      reader.releaseLock();
    }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.length;
    }
    return bytes;
  }
  class Unauthorized extends Error {}
  // One try of one request with the given token: the only place a token leaves this page, as the Bearer header of a request to this origin
  // (never in a URL, a cookie, storage or a message). A 401 is not decided here: what it ends depends on the mode (request below).
  async function attempt(path, options, token) {
    if (!token) throw new Stale();
    const epoch = state.epoch;
    const controller = new AbortController();
    state.controllers.add(controller);
    const timer = setTimeout(() => controller.abort(), 25000);
    const headers = {Authorization: 'Bearer ' + token, Accept: 'application/json'};
    const settings = options || {};
    if (settings.body !== undefined) headers['Content-Type'] = 'application/json';
    if (settings.key) headers['Idempotency-Key'] = settings.key;
    try {
      const response = await fetch(base + path, {
        method: settings.method || 'GET',
        headers: headers,
        body: settings.body === undefined ? undefined : JSON.stringify(settings.body),
        signal: controller.signal,
        credentials: embedded ? 'omit' : 'same-origin',
        cache: 'no-store',
      });
      const bytes = await readBounded(response, settings.binary ? 8 << 20 : 2 << 20);
      if (epoch !== state.epoch) throw new Stale();
      if (!response.ok) {
        let code = 'REQUEST_FAILED',
          detail,
          problem;
        try {
          problem = JSON.parse(new TextDecoder().decode(bytes));
          if (typeof problem.error === 'string') code = problem.error;
          if (window.reportingRefusal.valid(problem.detail)) detail = problem.detail;
        } catch (_) {}
        if (response.status === 401) throw new Unauthorized();
        // A quota refusal (S04-D) is exactly {error:'QUOTA_EXCEEDED',quota:<one of the three>} with status 429; the code in any other shape, or a
        // 429 with any other body, cannot be verified.
        if (response.status === 429 || code === 'QUOTA_EXCEEDED') {
          if (
            response.status !== 429 ||
            !problem ||
            typeof problem !== 'object' ||
            Array.isArray(problem) ||
            Object.keys(problem).sort().join() !== 'error,quota' ||
            code !== 'QUOTA_EXCEEDED' ||
            !quotaNames.includes(problem.quota)
          )
            throw new Failure(0, 'INVALID_RESPONSE');
          const refusal = new Failure(429, 'QUOTA_EXCEEDED');
          refusal.quota = problem.quota;
          throw refusal;
        }
        // The cap on requests in flight (S04-G) is exactly {error:'OVERLOADED'} with status 503; it is not the authorization source's 503.
        if (code === 'OVERLOADED') {
          if (
            response.status !== 503 ||
            !problem ||
            typeof problem !== 'object' ||
            Array.isArray(problem) ||
            Object.keys(problem).join() !== 'error'
          )
            throw new Failure(0, 'INVALID_RESPONSE');
          throw new Failure(503, 'OVERLOADED');
        }
        throw new Failure(response.status, code, detail);
      }
      if (settings.binary) {
        if (response.headers.get('Content-Type') !== settings.binary) throw new Failure(409, 'INTEGRITY_FAILURE');
        const expected = response.headers.get('X-Content-SHA256');
        if (!/^[a-f0-9]{64}$/.test(expected || '') || !window.crypto.subtle)
          throw new Failure(409, 'INTEGRITY_FAILURE');
        const actual = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)))
          .map(b => b.toString(16).padStart(2, '0'))
          .join('');
        if (epoch !== state.epoch) throw new Stale();
        if (actual !== expected) throw new Failure(409, 'INTEGRITY_FAILURE');
        return bytes;
      }
      if (!response.headers.get('Content-Type')?.includes('application/json')) throw new Failure(0, 'INVALID_RESPONSE');
      try {
        return JSON.parse(new TextDecoder().decode(bytes));
      } catch (_) {
        throw new Failure(0, 'INVALID_RESPONSE');
      }
    } finally {
      clearTimeout(timer);
      state.controllers.delete(controller);
    }
  }
  async function request(path, options) {
    if (!state.token) throw new Stale();
    const used = state.token,
      epoch = state.epoch;
    try {
      return await attempt(path, options, used);
    } catch (error) {
      if (!(error instanceof Unauthorized)) throw error;
      // Embedded: a refused token is asked of the parent once; the request is retried once with the fresh one. A second 401 is signed out.
      if (embedded && (await refreshToken(used, epoch))) {
        try {
          return await attempt(path, options, state.token);
        } catch (second) {
          if (!(second instanceof Unauthorized)) throw second;
        }
      }
      if (epoch !== state.epoch) throw new Stale();
      disconnect(embedded ? '访问令牌无效或已失效；请重新向产品页面请求令牌。' : '访问令牌无效或已失效，请重新连接。');
      if (embed) embed.error('UNAUTHENTICATED');
      throw new Stale();
    }
  }
  let refreshing = null;
  // Embed: a request was refused with 401. One fresh token is asked of the parent and waited for (a request that finds the token already
  // replaced just retries); false when none comes, or when it is not the same user's.
  function refreshToken(failed, epoch) {
    if (!embed || epoch !== state.epoch) return Promise.resolve(false);
    if (state.token && state.token !== failed) return Promise.resolve(true);
    if (!refreshing)
      refreshing = (async () => {
        try {
          const fresh = await embed.refresh(failed);
          return !!fresh && (await adoptToken(fresh, epoch));
        } finally {
          refreshing = null;
        }
      })();
    return refreshing;
  }
  // Embed: may this token replace the one in use? It must be accepted for /session and name the same user (tenant, issuer, subject) as the session
  // on the page. Another user's token is not a refresh: the page starts over with it, so that nothing of the previous user stays on screen.
  async function adoptToken(token, epoch) {
    let session;
    try {
      session = await attempt('/session', undefined, token);
    } catch (_) {
      return false;
    }
    if (epoch !== state.epoch || !embed) return false;
    const known = state.session && state.session.identity,
      now = session && session.identity;
    if (
      known &&
      (!now || now.tenant !== known.tenant || now.issuer !== known.issuer || now.subject !== known.subject)
    ) {
      start(token);
      return false;
    }
    state.token = token;
    embed.expect(token);
    return true;
  }
  // A token the parent sent unasked (the first one, a refresh it chose to push, one after being signed out).
  function embedToken(token) {
    if (state.starting) return;
    if (!state.session) {
      start(token);
      return;
    }
    adoptToken(token, state.epoch);
  }
  // The class of a failure, for the parent: a closed code, never the server's text.
  function embedCode(error) {
    if (['RESOURCE_UNAVAILABLE', 'INVALID_RESPONSE', 'INTEGRITY_FAILURE'].includes(error.code)) return error.code;
    return (
      {401: 'UNAUTHENTICATED', 403: 'FORBIDDEN', 404: 'NOT_FOUND', 503: 'UNAVAILABLE'}[error.status] || 'REQUEST_FAILED'
    );
  }
  // Template saves and publishes, and issues, are told to the parent by id and number only.
  function announce(path, options, value) {
    if (!options || options.method !== 'POST' || !value || typeof value !== 'object') return;
    if (path === '/document-templates') {
      embed.event('saved', {template_id: value.id, version: value.version});
      return;
    }
    const release = /^\/document-templates\/([a-f0-9]{32})\/release$/.exec(path);
    if (release && options.body && options.body.action === 'publish')
      embed.event('published', {template_id: release[1], version: value.published_version});
  }
  let drawerAsked = false;
  function openPage(page) {
    if (embedded) return;
    for (const d of drawers) d.close();
    const asked = page;
    if (!pages[page]) page = 'workspace';
    for (const name of Object.keys(pages)) $('page-' + name).hidden = name !== page;
    document.querySelectorAll('.nav-item').forEach(b => {
      const active = b.dataset.page === page;
      b.classList.toggle('active', active);
      if (active) b.setAttribute('aria-current', 'page');
      else b.removeAttribute('aria-current');
    });
    $('shell-label').textContent = pages[page] || '报表工作区';
    document.title = 'IDP Reporting · ' + pages[page];
    history.replaceState(null, '', '#' + page);
    document.body.classList.toggle('studio-on', page === 'documents');
    document.body.classList.toggle('library-on', page === 'library');
    document.body.classList.toggle('board-on', page === 'dashboard');
    placeNotice(page);
    if (page === 'library' && library && state.session) library.show();
    if (page === 'dashboard' && board) board.show();
    if (page === 'audit' && state.session) loadAudit(false).catch(error => fail(error, mark(), {label: '操作记录'}));
    if (asked === 'catalog' && !embedded) {
      if (state.session) setTimeout(() => drawer.open($('sh-data-chip')), 0);
      else drawerAsked = true;
    }
  }
  function selected() {
    return state.datasets.find(d => d.dataset.id + '@' + d.dataset.version === $('query-dataset').value);
  }
  function datasetTitle(d) {
    const labels = d.columns.filter(c => ['decimal', 'integer'].includes(c.type)).map(c => c.label);
    return labels.length ? labels.join(' / ') : d.dataset.id;
  }
  function validDataset(d) {
    return (
      d &&
      typeof d.dataset?.id === 'string' &&
      typeof d.dataset.version === 'string' &&
      Array.isArray(d.columns) &&
      d.columns.length <= 100 &&
      d.columns.every(c => typeof c.id === 'string' && typeof c.label === 'string' && typeof c.type === 'string') &&
      Array.isArray(d.metrics) &&
      Array.isArray(d.dimensions) &&
      typeof d.timezone === 'string'
    );
  }
  function dateInZone(zone) {
    try {
      const parts = new Intl.DateTimeFormat('en', {
        timeZone: zone,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
      }).formatToParts(new Date());
      const get = t => parts.find(p => p.type === t).value;
      return get('year') + '-' + get('month') + '-' + get('day');
    } catch (_) {
      return '';
    }
  }
  function queryChanged() {
    if (state.catalogLoading) return;
    state.view++;
    clearResult();
    renderFields();
    const d = selected();
    $('query-timezone').textContent = d?.timezone || '—';
    $('query-currency').textContent = d?.currency_policy
      ? d.currency_policy.id + ' @ ' + d.currency_policy.version
      : '无换算政策';
    $('query-shape').textContent = d
      ? d.dimensions.length + ' 个维度 / ' + d.metrics.length + ' 个指标'
      : '选择数据集后显示批准组合';
  }
  // S07-H (ADR 0011 decision 6): the data centre's column table is the data drawer's, for the dataset chosen in the query panel.
  function renderCatalog() {
    renderFields();
  }
  function renderFields() {
    const list = $('sh-fields-list'),
      meta = $('sh-fields-meta');
    if (!list || !meta) return;
    list.replaceChildren();
    const d = selected();
    if (!d) {
      list.hidden = true;
      meta.textContent = state.catalogLoading
        ? '正在读取当前授权目录…'
        : state.session
          ? state.datasets.length
            ? '选择数据集后显示字段、类型与币种。'
            : '当前身份尚无可用数据集。'
          : '连接工作区后加载当前授权目录。';
      return;
    }
    meta.textContent =
      datasetTitle(d) +
      ' · ' +
      d.dataset.id +
      ' · v' +
      d.dataset.version +
      ' · ' +
      d.dimensions.length +
      ' 个维度 · ' +
      d.metrics.length +
      ' 个指标 · 已发布组合';
    const head = node('div', undefined, 'column-line');
    head.append(node('b', '字段'), node('code', '类型 · 币种或单位'));
    list.append(head);
    for (const c of d.columns) {
      const line = node('div', undefined, 'column-line');
      const type = node(
        'code',
        typeName(c.type) + (c.currency ? ' · ' + c.currency : '') + (c.unit ? ' · ' + c.unit : '')
      );
      type.title = c.type;
      line.append(node('b', c.label), type);
      list.append(line);
    }
    list.hidden = false;
  }
  // The column types of the catalogue (docs/s00/INTERFACES.md) in the user's words; an unknown type is shown as it comes (S09-C).
  const typeNames = {
    string: '文本',
    decimal: '小数',
    integer: '整数',
    date: '日期',
    time: '时间',
    boolean: '是/否',
    text: '文本',
  };
  function typeName(type) {
    return Object.hasOwn(typeNames, type) ? typeNames[type] : type;
  }
  // ADR 0016 (S10-B): each product of the session and what its catalog answered: the list on 能力与环境 and the marks of the product select.
  // A catalog read is the source's own answer for this identity (DescribeCatalog), cached as loadProduct caches it.
  function connectionState(error) {
    if (!error) return null;
    if (error.code === 'INVALID_RESPONSE') return {tone: 'danger', text: '回答无法验证'};
    if (error.status === 403) return {tone: 'danger', text: '来源拒绝访问'};
    if (
      error.status === 0 ||
      error.status === 502 ||
      error.status === 503 ||
      error.status === 504 ||
      error.name === 'AbortError'
    )
      return {tone: 'warning', text: '来源暂时不可用'};
    return {tone: 'danger', text: '无法读取（' + (error.code || error.status) + '）'};
  }
  function renderConnections() {
    const list = $('connection-list');
    if (!list) return;
    list.replaceChildren();
    const products = state.session ? state.session.integrations : [];
    if (!products.length) {
      list.append(node('li', '当前身份没有可用的产品连接。请联系平台运营方：先为租户配置连接，再把产品加入身份绑定。'));
      return;
    }
    for (const id of products) {
      const row = node('li');
      const name = node('code', id);
      const known = state.connections.get(id);
      const badge = known
        ? known.ok
          ? node('span', '可用 · ' + known.count + ' 个数据集', 'badge')
          : node('span', known.state.text, 'badge ' + known.state.tone)
        : node('span', '检查中…', 'badge neutral');
      row.append(name, badge);
      list.append(row);
    }
    for (const option of $('query-product').options) {
      const known = state.connections.get(option.value);
      option.textContent = known && !known.ok ? option.value + '（' + known.state.text + '）' : option.value;
    }
  }
  async function checkConnections(fresh) {
    const epoch = state.epoch,
      products = state.session ? state.session.integrations.slice() : [];
    if (fresh) state.connections.clear();
    renderConnections();
    for (const id of products) {
      let entry;
      try {
        let datasets = fresh ? undefined : state.catalogs.get(id);
        if (!datasets) datasets = await request('/catalog?integration_id=' + encodeURIComponent(id));
        if (!Array.isArray(datasets) || datasets.length > 100 || !datasets.every(validDataset))
          throw new Failure(0, 'INVALID_RESPONSE');
        if (epoch !== state.epoch) return;
        if (!state.catalogs.has(id)) state.catalogs.set(id, datasets);
        entry = {ok: true, count: datasets.length};
      } catch (error) {
        if (error instanceof Stale || epoch !== state.epoch) return;
        entry = {ok: false, state: connectionState(error)};
      }
      state.connections.set(id, entry);
      renderConnections();
    }
  }
  // S12-B (ADR 0018): the audit of people's actions. Everyone reads their own; an administrator may read the tenant's, each with its subject.
  const auditActions = {
    'design.change': '保存设计',
    'design.release': '发布或撤回',
    'issue.submit': '提交正式出具',
    'export.submit': '提交导出',
    'artifact.download': '下载文件',
    'role.refused': '被角色拒绝',
  };
  const auditKinds = {
    layout: '报表布局',
    dashboard: '看板',
    template: '单据模板',
    background: '页面底图',
    image: '图片',
    issue: '正式出具',
    export: '导出',
    artifact: '文件',
    route: '操作',
  };
  const auditOutcomes = {requested: '已申请', succeeded: '成功', refused: '被拒绝', failed: '失败'};
  function checkAudit(list) {
    if (!Array.isArray(list) || list.length > 200) throw new Failure(0, 'INVALID_RESPONSE');
    for (const e of list)
      if (
        !e ||
        !isID(e.id) ||
        typeof e.at !== 'string' ||
        !auditActions[e.action] ||
        !auditKinds[e.object_kind] ||
        !auditOutcomes[e.outcome] ||
        (e.subject !== undefined && (typeof e.subject !== 'string' || e.subject.length > 256)) ||
        (e.object_id !== undefined && !isID(e.object_id))
      )
        throw new Failure(0, 'INVALID_RESPONSE');
  }
  function renderAudit() {
    const a = state.audit,
      tenant = a.scope === 'tenant';
    $('audit-title').textContent = tenant ? '全租户操作' : '我的操作';
    $('audit-lead').textContent = tenant
      ? '当前租户所有人的操作，最新在前（仅管理员可见）。'
      : '当前身份的操作，最新在前。';
    $('audit-who-head').hidden = !tenant;
    $('audit-empty').hidden = a.events.length > 0;
    $('audit-table-wrap').hidden = a.events.length === 0;
    $('audit-more').hidden = !a.more;
    const body = $('audit-body');
    body.replaceChildren();
    for (const e of a.events) {
      const row = node('tr');
      const when = new Date(e.at);
      row.append(node('td', isNaN(when) ? e.at : when.toLocaleString('zh-CN', {hour12: false})));
      if (tenant) row.append(node('td', e.subject || ''));
      row.append(node('td', auditActions[e.action]));
      const what = node('td', auditKinds[e.object_kind]);
      if (e.object_id) what.append(node('small', ' ' + e.object_id.slice(0, 8), 'task-id'));
      row.append(what);
      const result = node('td');
      result.append(
        node(
          'span',
          auditOutcomes[e.outcome],
          'badge' + (e.outcome === 'succeeded' ? '' : e.outcome === 'requested' ? ' neutral' : ' danger')
        )
      );
      row.append(result);
      body.append(row);
    }
  }
  let auditReads = 0;
  async function loadAudit(more) {
    const epoch = state.epoch,
      read = ++auditReads,
      a = state.audit,
      before = more ? a.more : '';
    if (more && !before) return;
    const list = await request(
      '/audit?limit=50' + (a.scope ? '&scope=' + a.scope : '') + (before ? '&before=' + before : '')
    );
    if (epoch !== state.epoch || read !== auditReads) throw new Stale();
    checkAudit(list);
    a.events = more ? a.events.concat(list.filter(e => !a.events.some(x => x.id === e.id))) : list;
    a.more = list.length === 50 ? list.at(-1).id : '';
    renderAudit();
  }
  async function loadProduct(id, refresh) {
    const epoch = state.epoch,
      operation = ++state.catalogOperation;
    state.product = id;
    state.view++;
    state.catalogLoading = true;
    clearResult();
    busy(0);
    $('query-product').value = id;
    state.datasets = [];
    renderCatalog();
    $('query-dataset').replaceChildren();
    const active = () => epoch === state.epoch && operation === state.catalogOperation && id === state.product;
    try {
      let datasets = state.catalogs.get(id);
      if (!datasets || refresh) datasets = await request('/catalog?integration_id=' + encodeURIComponent(id));
      if (!active()) throw new Stale();
      if (!Array.isArray(datasets) || datasets.length > 100 || !datasets.every(validDataset))
        throw new Failure(0, 'INVALID_RESPONSE');
      state.catalogs.set(id, datasets);
      state.datasets = datasets;
      $('query-dataset').replaceChildren();
      for (const d of datasets) {
        const option = node('option', datasetTitle(d) + ' · v' + d.dataset.version);
        option.value = d.dataset.id + '@' + d.dataset.version;
        $('query-dataset').append(option);
      }
      state.catalogLoading = false;
      renderCatalog();
      queryChanged();
      $('query-date').value = selected() ? dateInZone(selected().timezone) : '';
    } catch (error) {
      if (!active()) throw new Stale();
      throw error;
    } finally {
      if (active()) {
        state.catalogLoading = false;
        busy(0);
      }
    }
  }
  function addDetails(id, pairs) {
    const target = $(id);
    target.replaceChildren();
    for (const [label, value] of pairs) {
      const row = node('div');
      row.append(node('dt', label), node('dd', String(value)));
      target.append(row);
    }
  }
  function connect(event) {
    event.preventDefault();
    const token = $('access-token').value.trim();
    if (!token) return;
    return start(token);
  }
  // Starts a session with this token: the console's form gives it, in the embed the parent page does (embedToken).
  async function start(token) {
    disconnect();
    state.token = token;
    state.starting = true;
    if (!embedded) {
      $('connect-error').hidden = true;
      $('connect-button').disabled = true;
    }
    const epoch = state.epoch;
    try {
      const session = await request('/session');
      const caps = await request('/capabilities');
      if (epoch !== state.epoch) throw new Stale();
      if (
        !session.identity ||
        !['tenant', 'issuer', 'subject'].every(k => typeof session.identity[k] === 'string' && session.identity[k]) ||
        !Array.isArray(session.integrations) ||
        session.integrations.length > 32 ||
        !session.integrations.every(i => typeof i === 'string' && i.length <= 128) ||
        !Array.isArray(caps.formats) ||
        !validQuotas(caps.quotas)
      )
        throw new Failure(0, 'INVALID_RESPONSE');
      // The embed shows one integration, the one its address names (and must be one the token's identity has).
      const offered = embedded
        ? session.integrations.filter(i => i === embedRequest.integration)
        : session.integrations;
      // ADR 0016: the server lists only the products it serves; an identity with none signs in and is told so (the embed needs its one).
      if (!offered.length && embedded) throw new Failure(403, 'AUTH_SCOPE_DENIED');
      // S12-A2 (ADR 0018): the roles the server enforces, so that the console does not offer what it would refuse. A session without the
      // field is a server before roles: the rights every identity had then.
      const roles = session.roles === undefined ? ['viewer', 'designer', 'publisher', 'issuer'] : session.roles;
      if (!Array.isArray(roles) || roles.length > 5 || !roles.every(role => roleNames[role]))
        throw new Failure(0, 'INVALID_RESPONSE');
      for (const role of ['designer', 'publisher', 'issuer', 'administrator'])
        document.body.classList.toggle('no-' + role, !roles.includes(role));
      state.roles = roles;
      state.session = session;
      state.capabilities = caps;
      $('export-pdf').hidden = !caps.formats.includes('pdf');
      for (const integration of offered) {
        const option = node('option', integration);
        option.value = integration;
        $('query-product').append(option);
      }
      $('actor-label').textContent = session.identity.subject;
      $('tenant-label').textContent = session.identity.tenant;
      $('connection-label').textContent = 'API 已连接';
      $('connection-dot').className = 'status-dot connected';
      if (!embedded) $('logout').disabled = false;
      addDetails('identity-details', [
        ['平台租户', session.identity.tenant],
        ['当前主体', session.identity.subject],
        ['发行方', session.identity.issuer],
        ['角色', roles.map(role => roleNames[role]).join('、')],
        ['产品连接', session.integrations.length ? session.integrations.length + ' 个（状态见下方）' : '无'],
      ]);
      addDetails('capability-details', [
        ['来源配置', caps.profile],
        ['数据类别', '合同模拟器 / 合成数据'],
        ['查询组合', caps.query_shapes],
        ['导出格式', caps.formats.join('、')],
        [
          '提交限额',
          '进行中任务 ' +
            caps.quotas.jobs_in_flight +
            ' · 保留结果 ' +
            caps.quotas.stored_results +
            ' · 每小时提交 ' +
            caps.quotas.admissions_per_hour,
        ],
        ['生产准入', caps.production_approved === true ? '已准入' : '尚未准入'],
      ]);
      $('connection-overlay').hidden = true;
      document.querySelector('.shell').inert = false;
      busy(0);
      if (library && !$('page-library').hidden) library.show();
      if (embedded) {
        document.querySelector('.shell').hidden = false;
        showEmbedView();
        embed.expect(state.token);
      } else {
        openPage(location.hash.slice(1));
        $('main').focus({preventScroll: true});
        if (drawerAsked) {
          drawerAsked = false;
          setTimeout(() => drawer.open($('sh-data-chip')), 0);
        }
      }
      state.starting = false;
      if (!embedded) {
        $('sh-no-products').hidden = offered.length > 0;
        renderConnections();
      }
      if (!offered.length) {
        notify('当前身份没有可用的产品连接，暂时不能查询。请联系平台运营方开通。', 'error');
        return;
      }
      try {
        await loadProduct(offered[0]);
        if (!embedded) checkConnections().catch(() => {});
        // The resource is prepared before anything can be started: a dataset selected under a query in flight would discard that query.
        if (embedded) {
          busy(1);
          try {
            await prepareResource();
          } finally {
            if (epoch === state.epoch) busy(-1);
          }
        }
        // The console's task list is the server's (S07-C); the embed shows one view and has no panel to fill.
        else await loadJobs();
      } catch (error) {
        fail(error);
      }
    } catch (error) {
      if (!(error instanceof Stale)) {
        disconnect(errorMessage(error));
        if (embed) embed.error(embedCode(error));
      }
    } finally {
      if (epoch === state.epoch) state.starting = false;
      if (!embedded && !state.session) $('connect-button').disabled = false;
    }
  }
  // Embed: only the view the address names is shown; the query panel (company scope and date) comes with a view that is not the report viewer.
  function showEmbedView() {
    for (const name of ['workspace', 'dashboard', 'documents']) $('page-' + name).hidden = name !== embedRequest.page;
    document.body.classList.toggle('studio-on', embedRequest.page === 'documents');
    document.body.classList.toggle('board-on', embedRequest.page === 'dashboard');
    placeNotice(embedRequest.page);
    if (embedRequest.page === 'dashboard' && board) board.show();
    const panel = document.querySelector('.query-panel'),
      target = $('page-' + embedRequest.page);
    if (panel.closest('.page') !== target) target.querySelector('.page-heading').after(panel);
  }
  function selectDataset(d) {
    $('query-dataset').value = d.dataset.id + '@' + d.dataset.version;
    queryChanged();
    $('query-date').value = dateInZone(d.timezone);
  }
  // Embed: the resource the product asked for. A dataset is selected. A dashboard or a template is read once to learn which dataset it is bound to
  // (its latest version unless the address names one), and that dataset is selected; the user still gives the company scope and the date, and
  // openResource opens the saved version when the authorized result is on the page. Anything unknown, not the user's own or bound to another
  // integration is the same refusal.
  async function prepareResource() {
    const wanted = embedRequest.resource,
      missing = () => new Failure(404, 'RESOURCE_UNAVAILABLE');
    const only = (id, version) => {
      const found = state.datasets.filter(
        d => d.dataset.id === id && (version === undefined || d.dataset.version === version)
      );
      if (found.length !== 1) throw missing();
      return found[0];
    };
    if (wanted.kind === 'dataset') {
      const [id, version] = wanted.id.split('@');
      selectDataset(only(id, version));
      return;
    }
    const route = wanted.kind === 'dashboard' ? '/dashboards' : '/document-templates';
    let version = wanted.version;
    if (!version) {
      const list = await request(route);
      const known = Array.isArray(list) ? list.find(v => v && v.id === wanted.id) : null;
      if (!known || !Number.isSafeInteger(known.version) || known.version < 1 || known.version > 20) throw missing();
      version = known.version;
    }
    const entry = await request(route + '/' + wanted.id + '?version=' + version),
      binding = entry && entry.config && entry.config.binding;
    if (
      !entry ||
      entry.id !== wanted.id ||
      entry.version !== version ||
      !binding ||
      binding.integration_id !== embedRequest.integration ||
      !binding.dataset ||
      typeof binding.dataset.id !== 'string' ||
      typeof binding.dataset.version !== 'string'
    )
      throw missing();
    const d = only(binding.dataset.id, binding.dataset.version);
    state.resource = {kind: wanted.kind, id: wanted.id, version};
    selectDataset(d);
  }
  function openResource() {
    const wanted = state.resource;
    if (!embedded || !wanted || state.opened || !state.lastResult) return;
    state.opened = true;
    (wanted.kind === 'dashboard' ? dashboard : documents).openServer(wanted.id, wanted.version);
  }
  function startEmbed() {
    disconnect();
    embedRequest = window.reportingEmbedRequest(location.pathname, location.search);
    const stop = message => {
      $('connect-title').textContent = '此视图无法显示';
      $('embed-state').hidden = true;
      $('connect-error').textContent = message;
      $('connect-error').hidden = false;
      $('embed-retry').hidden = true;
    };
    if (!embedRequest) {
      stop('嵌入地址无效。');
      return;
    }
    try {
      embed = window.createReportingEmbed({
        parent: window.parent,
        origin: embedRequest.parent,
        token: embedToken,
        theme: name => document.documentElement.setAttribute('data-theme', name),
      });
    } catch (_) {
      stop('此视图只能由产品页面以框架嵌入。');
      return;
    }
    $('embed-retry').addEventListener('click', () => embed.tokenRequest());
    embed.watch(document.body);
    embed.ready();
  }
  function buildQuery() {
    const d = selected();
    const companies = $('query-companies')
      .value.split(/[,，]/)
      .map(s => s.trim())
      .filter(Boolean);
    if (
      !d ||
      !companies.length ||
      companies.length > 16 ||
      new Set(companies).size !== companies.length ||
      companies.some(s => s.length > 128) ||
      !$('query-date').value
    )
      throw new Failure(400, 'INVALID_REQUEST');
    const q = {
      schema_version: '1.0',
      integration_id: state.product,
      dataset: copy(d.dataset),
      company_ids: companies,
      metrics: copy(d.metrics),
      dimensions: copy(d.dimensions),
      filters: [],
      sort: [],
      as_of: $('query-date').value,
      timezone: d.timezone,
      page: {limit: 50},
    };
    if (d.currency_policy) q.currency_policy = copy(d.currency_policy);
    return q;
  }
  function checkResult(result) {
    if (
      !result ||
      !Array.isArray(result.columns) ||
      result.columns.length > 100 ||
      !result.columns.every(
        c => typeof c.id === 'string' && typeof c.label === 'string' && typeof c.type === 'string'
      ) ||
      !Array.isArray(result.rows) ||
      result.rows.length > 1000 ||
      !result.rows.every(
        row =>
          Array.isArray(row) && row.length === result.columns.length && row.every(value => typeof value === 'string')
      ) ||
      !result.totals ||
      typeof result.totals !== 'object' ||
      !Object.values(result.totals).every(v => typeof v === 'string')
    )
      throw new Failure(0, 'INVALID_RESPONSE');
  }
  function layoutBinding() {
    return {integration_id: state.query.integration_id, dataset: copy(state.query.dataset)};
  }
  function matchingLayout() {
    const binding = state.layout?.binding;
    return (
      binding &&
      binding.integration_id === state.query.integration_id &&
      binding.dataset.id === state.query.dataset.id &&
      binding.dataset.version === state.query.dataset.version
    );
  }
  function visibleColumns(result) {
    return matchingLayout()
      ? state.layout.columns
      : result.columns.map(c => ({id: c.id, align: ['decimal', 'integer'].includes(c.type) ? 'right' : 'left'}));
  }
  function renderTable(result) {
    const columns = visibleColumns(result);
    const header = node('tr');
    const body = $('result-table').tBodies[0];
    body.replaceChildren();
    for (const entry of columns) {
      const c = result.columns.find(c => c.id === entry.id);
      header.append(node('th', c.label + (c.currency ? ' (' + c.currency + ')' : c.unit ? ' (' + c.unit + ')' : '')));
    }
    $('result-table').tHead.replaceChildren(header);
    for (const values of result.rows) {
      const row = node('tr');
      for (const entry of columns) {
        const i = result.columns.findIndex(c => c.id === entry.id);
        const cell = node('td', values[i], entry.align === 'right' ? 'layout-right' : 'layout-left');
        cell.title = values[i];
        row.append(cell);
      }
      body.append(row);
    }
  }
  function applyLayout(layout, allowPending = false) {
    if (!state.session || !state.lastResult || state.catalogLoading || (state.busy && !allowPending))
      throw new Failure(400, 'INVALID_REQUEST');
    const keys = (value, names) =>
      value &&
      typeof value === 'object' &&
      !Array.isArray(value) &&
      Object.keys(value).sort().join(',') === names.sort().join(',');
    if (
      !keys(layout, ['schema_version', 'binding', 'columns']) ||
      layout.schema_version !== '1.0' ||
      !keys(layout.binding, ['integration_id', 'dataset']) ||
      !keys(layout.binding.dataset, ['id', 'version']) ||
      layout.binding.integration_id !== state.query.integration_id ||
      layout.binding.dataset.id !== state.query.dataset.id ||
      layout.binding.dataset.version !== state.query.dataset.version ||
      !Array.isArray(layout.columns) ||
      !layout.columns.length ||
      layout.columns.length > 100 ||
      new Set(layout.columns.map(c => c?.id)).size !== layout.columns.length ||
      !layout.columns.every(
        c =>
          keys(c, ['id', 'align']) &&
          typeof c.id === 'string' &&
          state.lastResult.columns.some(known => known.id === c.id) &&
          ['left', 'right'].includes(c.align)
      )
    )
      throw new Failure(400, 'INVALID_REQUEST');
    state.layout = copy(layout);
    renderTable(state.lastResult);
    renderDesigner();
    $('layout-status').textContent = '布局已应用；数据和授权范围保持当前查询。';
  }
  function renderDesigner() {
    const target = $('designer-columns');
    target.replaceChildren();
    if (!state.lastResult) return;
    if (state.savedLayout && !savedBindingMatches(state.savedLayout)) {
      state.savedLayout = null;
      state.releaseView = null;
      $('layout-release-info').textContent = '尚未关联当前数据集的服务器布局。';
      $('layout-release-history').replaceChildren();
      $('layout-server-status').textContent = '首次保存会为当前数据集创建新布局。';
    }
    if (!matchingLayout() || !state.layout.columns.every(c => state.lastResult.columns.some(k => k.id === c.id)))
      state.layout = {
        schema_version: '1.0',
        binding: layoutBinding(),
        columns: state.lastResult.columns.map(c => ({
          id: c.id,
          align: ['decimal', 'integer'].includes(c.type) ? 'right' : 'left',
        })),
      };
    const entries = state.layout.columns;
    const ordered = [
      ...entries.map(c => state.lastResult.columns.find(k => k.id === c.id)),
      ...state.lastResult.columns.filter(c => !entries.some(k => k.id === c.id)),
    ];
    for (const c of ordered) {
      const row = node('div', undefined, 'designer-row');
      const label = node('label');
      const check = node('input');
      check.type = 'checkbox';
      check.checked = entries.some(k => k.id === c.id);
      check.setAttribute('aria-label', '显示 ' + c.label);
      label.append(check, document.createTextNode(c.label));
      row.append(label);
      const update = fn => {
        try {
          const next = copy(state.layout);
          fn(next.columns);
          applyLayout(next);
        } catch (error) {
          $('layout-status').textContent = errorMessage(error);
          renderDesigner();
        }
      };
      check.addEventListener('change', () =>
        update(cols => {
          if (check.checked) cols.push({id: c.id, align: 'left'});
          else
            cols.splice(
              cols.findIndex(k => k.id === c.id),
              1
            );
        })
      );
      const index = entries.findIndex(k => k.id === c.id);
      const up = button('上移', () =>
        update(cols => {
          [cols[index - 1], cols[index]] = [cols[index], cols[index - 1]];
        })
      );
      up.disabled = index <= 0;
      row.append(up);
      const align = node('select');
      align.setAttribute('aria-label', c.label + ' 对齐');
      for (const [value, text] of [
        ['left', '左对齐'],
        ['right', '右对齐'],
      ]) {
        const option = node('option', text);
        option.value = value;
        align.append(option);
      }
      align.value = entries[index]?.align || 'left';
      align.disabled = index < 0;
      align.addEventListener('change', () =>
        update(cols => {
          cols[index].align = align.value;
        })
      );
      row.append(align);
      target.append(row);
    }
    renderLayoutState();
  }
  // A refusal of the layout designer names what is missing (S07-G, ADR 0011 §4); the server's answers keep the console's messages.
  class LayoutRefusal extends Error {}
  const refuse = reason => {
    throw new LayoutRefusal(reason);
  };
  function layoutAction(fn) {
    try {
      if (!state.session || state.catalogLoading) refuse('请先连接工作区。');
      if (!state.lastResult || typeof state.query?.integration_id !== 'string')
        refuse('请先查询当前授权数据，再设计布局。');
      if (state.busy) refuse('请等待当前操作结束后再试。');
      fn();
    } catch (error) {
      $('layout-status').textContent = error instanceof LayoutRefusal ? error.message : errorMessage(error);
    } finally {
      renderLayoutState();
    }
  }
  $('layout-reset').addEventListener('click', () =>
    layoutAction(() => {
      state.layout = null;
      renderDesigner();
      renderTable(state.lastResult);
      $('layout-status').textContent = '已恢复全部列。';
    })
  );
  $('layout-save').addEventListener('click', () =>
    layoutAction(() => {
      const text = JSON.stringify(state.layout, null, 2);
      if (new TextEncoder().encode(text).length > 65536) throw new Failure(400, 'INVALID_REQUEST');
      $('layout-json').value = text;
      const url = URL.createObjectURL(new Blob([$('layout-json').value], {type: 'application/json'}));
      const link = node('a');
      link.href = url;
      link.download = 'report-layout.json';
      document.body.append(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 1000);
      $('layout-status').textContent = '布局文件已生成，不含业务行或访问令牌。';
    })
  );
  $('layout-open').addEventListener('click', () =>
    layoutAction(() => {
      const text = $('layout-json').value;
      if (new TextEncoder().encode(text).length > 65536) throw new Failure(400, 'INVALID_REQUEST');
      let layout;
      try {
        layout = JSON.parse(text);
      } catch (_) {
        throw new Failure(400, 'INVALID_REQUEST');
      }
      applyLayout(layout);
    })
  );
  // Without a query (cleared, signed out) no saved layout matches: the badge and the dialogs ask for this before reading state.query.
  function savedBindingMatches(saved) {
    if (!state.query || typeof state.query.integration_id !== 'string' || !state.query.dataset) return false;
    const b = saved?.layout?.binding;
    const current = layoutBinding();
    return (
      b &&
      b.integration_id === current.integration_id &&
      b.dataset.id === current.dataset.id &&
      b.dataset.version === current.dataset.version
    );
  }
  function saveServerLayout(fresh) {
    layoutAction(() => {
      const layout = copy(state.layout);
      const saved = !fresh && savedBindingMatches(state.savedLayout) ? state.savedLayout : null;
      perform(
        async m => {
          const entry = await request('/layouts', {
            method: 'POST',
            body: {id: saved?.id || '', expected_version: saved?.version || 0, layout: layout},
          });
          if (!current(m)) throw new Stale();
          if (!isID(entry.id) || !Number.isSafeInteger(entry.version) || entry.version < 1)
            throw new Failure(0, 'INVALID_RESPONSE');
          setSavedLayout(entry);
          $('layout-server-status').textContent =
            '已保存 ' + entry.id + ' · 版本 ' + entry.version + '；再次保存会创建新版本。';
        },
        {label: '布局保存', uncertain: '保存结果尚未确认，请先刷新我的布局核对，再决定是否保存。'}
      );
    });
  }
  $('layout-server-save').addEventListener('click', () => saveServerLayout(false));
  $('layout-server-new').addEventListener('click', () => saveServerLayout(true));
  $('layout-server-list').addEventListener('click', () =>
    perform(
      async m => {
        const list = await request('/layouts');
        if (!current(m)) throw new Stale();
        if (
          !Array.isArray(list) ||
          list.length > 16 ||
          !list.every(v => isID(v.id) && Number.isSafeInteger(v.version) && v.version >= 1 && v.version <= 20)
        )
          throw new Failure(0, 'INVALID_RESPONSE');
        $('layout-server-listing').replaceChildren();
        for (const item of list) {
          const option = node('option', item.id + ' · 最新版本 ' + item.version);
          option.value = item.id;
          option.dataset.version = String(item.version);
          $('layout-server-listing').append(option);
        }
        $('layout-server-version').value = String(list[0]?.version || 1);
        $('layout-server-status').textContent = '当前身份可访问 ' + list.length + ' 个服务器布局。';
      },
      {label: '布局目录'}
    )
  );
  $('layout-server-listing').addEventListener('change', () => {
    $('layout-server-version').value = $('layout-server-listing').selectedOptions[0]?.dataset.version || '1';
  });
  $('layout-server-open').addEventListener('click', () =>
    layoutAction(() => {
      const id = $('layout-server-listing').value,
        version = Number($('layout-server-version').value);
      if (!isID(id) || !Number.isSafeInteger(version) || version < 1 || version > 20)
        throw new Failure(400, 'INVALID_REQUEST');
      perform(
        async m => {
          const entry = await request('/layouts/' + id + '?version=' + version);
          if (!current(m)) throw new Stale();
          if (entry.id !== id || entry.version !== version) throw new Failure(0, 'INVALID_RESPONSE');
          applyLayout(entry.layout, true);
          setSavedLayout(entry);
          $('layout-server-status').textContent =
            '正在编辑 ' + id + ' · 版本 ' + version + '；旧版本保存若已过期会返回冲突。';
        },
        {label: '布局打开'}
      );
    })
  );
  function setSavedLayout(entry) {
    if (state.savedLayout?.id !== entry.id) {
      state.releaseView = null;
      state.layoutLatest = 0;
      $('layout-release-info').textContent = '尚未读取此布局的发布状态。';
      $('layout-release-history').replaceChildren();
    }
    state.savedLayout = entry;
    state.layoutLatest = Math.max(state.layoutLatest || 0, entry.version);
    renderLayoutState();
  }
  function releaseTarget() {
    const saved = state.savedLayout;
    if (!saved || !savedBindingMatches(saved)) refuse('请先保存或打开当前数据集的服务器布局。');
    return saved;
  }
  const sameColumns = (a, b) =>
    a.columns.length === b.columns.length &&
    a.columns.every((c, i) => c.id === b.columns[i].id && c.align === b.columns[i].align);
  const layoutDirty = () =>
    !!state.layout &&
    !!state.savedLayout &&
    savedBindingMatches(state.savedLayout) &&
    !sameColumns(state.layout, state.savedLayout.layout);
  function showRelease(id, value) {
    if (
      !value ||
      !Number.isSafeInteger(value.revision) ||
      value.revision < 0 ||
      value.revision > 64 ||
      !Number.isSafeInteger(value.published_version) ||
      value.published_version < 0 ||
      value.published_version > 20 ||
      !Array.isArray(value.history) ||
      value.history.length !== value.revision ||
      !value.history.every(
        (e, i) =>
          e.revision === i + 1 &&
          ['publish', 'withdraw'].includes(e.action) &&
          typeof e.at === 'string' &&
          Number.isSafeInteger(e.version)
      )
    )
      throw new Failure(0, 'INVALID_RESPONSE');
    state.releaseView = {id: id, revision: value.revision, published: value.published_version};
    renderLayoutState();
    $('layout-release-info').textContent = value.published_version
      ? '已发布版本 ' + value.published_version + ' · 发布修订 ' + value.revision
      : '未发布 · 发布修订 ' + value.revision;
    $('layout-release-history').replaceChildren();
    for (const event of value.history)
      $('layout-release-history').append(
        node(
          'li',
          event.at +
            ' · ' +
            (event.action === 'publish' ? '发布版本 ' + event.version : '撤回发布') +
            ' · 修订 ' +
            event.revision
        )
      );
  }
  $('layout-release-status').addEventListener('click', () =>
    layoutAction(() => {
      const saved = releaseTarget();
      perform(
        async m => {
          const value = await request('/layouts/' + saved.id + '/release');
          if (!current(m)) throw new Stale();
          showRelease(saved.id, value);
        },
        {label: '发布状态'}
      );
    })
  );
  // pick: a historical version from the version dialog, published as it is (a rollback); without it the editor's saved version, which must match the editor.
  function changeRelease(action, pick) {
    layoutAction(() => {
      const saved = releaseTarget(),
        release = state.releaseView,
        other = action === 'publish' && pick && pick !== saved.version;
      if (other && (!Number.isSafeInteger(pick) || pick < 1 || pick > state.layoutLatest))
        refuse('请选择有效的保存版本。');
      if (!release || release.id !== saved.id) refuse('请先刷新发布状态；冲突或结果未确认时必须重新读取。');
      if (action === 'publish' && !other && !sameColumns(state.layout, saved.layout))
        refuse('列或对齐有未保存的修改：请先保存版本，或用“发布”的保存并发布。');
      if (action === 'withdraw' && !release.published) refuse('当前没有已发布版本。');
      perform(
        async m => {
          const value = await request('/layouts/' + saved.id + '/release', {
            method: 'POST',
            body: {
              action: action,
              version: action === 'publish' ? (other ? pick : saved.version) : 0,
              expected_revision: release.revision,
            },
          });
          if (!current(m)) throw new Stale();
          showRelease(saved.id, value);
        },
        {label: action === 'publish' ? '布局发布' : '撤回发布', uncertain: '操作结果尚未确认，请重新读取发布状态核对。'}
      );
    });
  }
  $('layout-release-publish').addEventListener('click', () => changeRelease('publish'));
  $('layout-release-withdraw').addEventListener('click', () => changeRelease('withdraw'));
  // ---- S07-G (ADR 0011): the layout designer's bar, its version dialog and 发布前确认, as the document studio has them ----
  // The saved layout of the current data set, and its release state once it was read (another layout's does not count).
  const layoutSaved = () => (state.savedLayout && savedBindingMatches(state.savedLayout) ? state.savedLayout : null);
  const layoutRelease = saved =>
    state.releaseView && saved && state.releaseView.id === saved.id ? state.releaseView : null;
  function renderLayoutState() {
    const el = $('layout-state');
    if (!el) return;
    const saved = layoutSaved(),
      release = layoutRelease(saved);
    let text = '未查询',
      tone = 'neutral';
    if (state.lastResult) {
      if (!saved) {
        text = '草稿';
      } else {
        const published = !!release && release.published === saved.version;
        text = (published ? '已发布 v' : '已保存 v') + saved.version;
        tone = published ? 'green' : 'blue';
        if (layoutDirty()) {
          text += ' · 有未保存修改';
          tone = 'amber';
        }
      }
    }
    el.textContent = text;
    el.className = 'pill ' + tone;
    layoutVersions.sync();
  }
  // The latest version of this layout, from the listing (the dialog lists every saved version up to it).
  function readLayoutList(m) {
    return request('/layouts').then(list => {
      if (!current(m)) throw new Stale();
      if (
        !Array.isArray(list) ||
        list.length > 16 ||
        !list.every(v => isID(v.id) && Number.isSafeInteger(v.version) && v.version >= 1 && v.version <= 20)
      )
        throw new Failure(0, 'INVALID_RESPONSE');
      const mine = state.savedLayout && list.find(v => v.id === state.savedLayout.id);
      if (mine) state.layoutLatest = Math.max(mine.version, state.savedLayout.version);
      return list;
    });
  }
  function readLayoutRelease(m, saved) {
    return request('/layouts/' + saved.id + '/release').then(value => {
      if (!current(m)) throw new Stale();
      showRelease(saved.id, value);
    });
  }
  function restoreLayoutVersion(version) {
    layoutAction(() => {
      const saved = releaseTarget();
      if (!Number.isSafeInteger(version) || version < 1 || version > state.layoutLatest)
        refuse('请选择有效的保存版本。');
      perform(
        async m => {
          const entry = await request('/layouts/' + saved.id + '?version=' + version);
          if (!current(m)) throw new Stale();
          if (entry.id !== saved.id || entry.version !== version) throw new Failure(0, 'INVALID_RESPONSE');
          applyLayout(entry.layout, true);
          $('layout-status').textContent = '已把保存版本 v' + version + ' 载入编辑器，是未保存的修改；发布指针不变。';
          renderLayoutState();
        },
        {label: '布局版本'}
      );
    });
  }
  // The confirmed publish (S07-G): a saved version is published as it is; unsaved changes, or no saved version yet, become the next
  // version first (保存并发布, as the studios since S07-B).
  function publishLayout(plan) {
    layoutAction(() => {
      const fresh = !state.savedLayout || !savedBindingMatches(state.savedLayout),
        historical = plan.historical;
      if (!plan.saveFirst) {
        changeRelease('publish', historical || undefined);
        return;
      }
      // Save, read the new version's release state, publish it: the same three requests a person would make, in order, as one step.
      const layout = copy(state.layout),
        prior = fresh ? null : state.savedLayout;
      perform(
        async m => {
          const entry = await request('/layouts', {
            method: 'POST',
            body: {id: prior?.id || '', expected_version: prior?.version || 0, layout},
          });
          if (!current(m)) throw new Stale();
          if (!isID(entry.id) || !Number.isSafeInteger(entry.version) || entry.version < 1)
            throw new Failure(0, 'INVALID_RESPONSE');
          setSavedLayout(entry);
          $('layout-server-status').textContent =
            '已保存 ' + entry.id + ' · 版本 ' + entry.version + '；再次保存会创建新版本。';
          const state0 = await request('/layouts/' + entry.id + '/release');
          if (!current(m)) throw new Stale();
          showRelease(entry.id, state0);
          const value = await request('/layouts/' + entry.id + '/release', {
            method: 'POST',
            body: {action: 'publish', version: entry.version, expected_revision: state0.revision},
          });
          if (!current(m)) throw new Stale();
          showRelease(entry.id, value);
          renderLayoutState();
        },
        {
          label: '布局保存并发布',
          uncertain: '保存或发布的结果尚未确认：请打开“版本”核对保存版本与发布状态，再决定是否重试。',
        }
      );
    });
  }
  // The version dialog and 发布前确认 are the shared component (versions.js, S09-E); these ports are the layout designer's state,
  // requests and words. Opening reads the list and the release state; 刷新发布状态 stays as the retry after a conflict.
  const layoutVersions = window.createReportingVersions({
    prefix: 'ly',
    ids: {withdraw: 'layout-release-withdraw', versionsButton: 'layout-versions', publishButton: 'layout-publish'},
    dialogs: lyDialogs,
    texts: {
      thing: '布局',
      opened: '打开已发布版本',
      content: '列',
      dirty: '列或对齐有未保存的修改',
      count: ['列', '列'],
    },
    run: layoutAction,
    state() {
      const saved = layoutSaved(),
        release = layoutRelease(saved),
        dataset = state.query && state.query.dataset;
      return {
        saved,
        latest: state.layoutLatest,
        dirty: layoutDirty(),
        release: release ? {revision: release.revision, published_version: release.published} : null,
        dataset: dataset ? dataset.id + '@' + dataset.version : '—',
        count: state.layout ? state.layout.columns.length : 0,
      };
    },
    readVersions(then) {
      const saved = layoutSaved();
      perform(
        async m => {
          await readLayoutList(m);
          await readLayoutRelease(m, saved);
          renderLayoutState();
          then();
        },
        {label: '布局版本'}
      );
    },
    readRelease(then) {
      const saved = layoutSaved();
      perform(
        async m => {
          await readLayoutRelease(m, saved);
          then();
        },
        {label: '发布状态'}
      );
    },
    restore: restoreLayoutVersion,
    publish: publishLayout,
  });
  $('layout-release-open').addEventListener('click', () =>
    layoutAction(() => {
      const saved = releaseTarget();
      perform(
        async m => {
          const entry = await request('/layouts/' + saved.id + '/published');
          if (!current(m)) throw new Stale();
          if (entry.id !== saved.id || !Number.isSafeInteger(entry.version) || entry.version < 1 || entry.version > 20)
            throw new Failure(0, 'INVALID_RESPONSE');
          applyLayout(entry.layout, true);
          setSavedLayout(entry);
          $('layout-server-status').textContent = '已打开发布版本 ' + entry.version + '；当前数据仍来自授权查询。';
        },
        {label: '打开发布版本'}
      );
    })
  );
  function renderResult(result, query, meta) {
    checkResult(result);
    state.lastResult = result;
    state.query = copy(query);
    renderDesigner();
    renderTable(result);
    dashboard.refresh(result, query, !!meta);
    documents.refresh(result, query);
    const summary = $('result-summary');
    summary.replaceChildren();
    for (const [id, value] of Object.entries(result.totals).slice(0, 20)) {
      const column = result.columns.find(c => c.id === id);
      const card = node('div', undefined, 'summary-card');
      card.append(
        node(
          'small',
          (column?.label || id) +
            ' · 原范围合计' +
            (column?.currency ? ' / ' + column.currency : column?.unit ? ' / ' + column.unit : '')
        ),
        node('strong', value)
      );
      summary.append(card);
    }
    summary.hidden = !summary.children.length;
    $('start-steps').hidden = true;
    $('result-table-wrap').hidden = !result.rows.length;
    $('result-empty').hidden = !!result.rows.length;
    if (!result.rows.length) {
      $('result-empty').replaceChildren(node('b', '当前授权范围没有结果'), node('p', '请检查数据时点与公司选择。'));
    }
    $('result-title').textContent = datasetTitle({columns: result.columns, dataset: query.dataset});
    $('result-kind').textContent = meta ? '冻结快照' : '查询预览';
    $('result-kind').className = 'badge' + (meta ? '' : ' neutral');
    $('result-subtitle').textContent = meta
      ? '同一快照分页 · ' + query.as_of + ' · ' + query.timezone
      : '当前显示 ' +
        result.rows.length +
        ' 行 · ' +
        (result.completeness === 'preview' ? '有界预览，不能作为完整导出' : '当前批准组合的完整结果');
    const evidence = $('result-evidence');
    evidence.replaceChildren();
    if (result.evidence)
      for (const [label, key] of [
        ['查询摘要', 'query_hash'],
        ['来源引用', 'source_snapshot_ref'],
        ['来源水位', 'source_watermark'],
      ]) {
        const value = result.evidence[key];
        if (typeof value === 'string') {
          const span = node('span', label + ' ' + (value.length > 40 ? value.slice(0, 36) + '…' : value));
          span.title = value;
          evidence.append(span);
        }
      }
    evidence.hidden = !evidence.children.length;
    $('refresh-result').disabled = false;
    $('result-pager').hidden = !meta;
    if (meta) {
      $('page-info').textContent = '第 ' + state.page + ' 页 · 共 ' + meta.total + ' 行';
      $('next-page').disabled = !state.cursor;
      $('first-page').disabled = state.page <= 1;
    }
    $('export').disabled = !state.snapshot || state.busy > 0;
    $('export-pdf').disabled = !state.snapshot || state.busy > 0;
    exportHint();
    updateChip();
    if (drawer.isOpen()) drawer.close();
    if (embedded) setTimeout(openResource, 0);
  }
  function renderJobs() {
    // S07-E: the rail's badge counts every job the console knows; the list and its note show the ones the filter keeps.
    const all = Array.from(state.jobs.values()).reverse(),
      f = state.jobsFilter,
      filtered = !!(f.kind || f.state);
    const values = all.filter(j => (!f.kind || j.kind === f.kind) && (!f.state || j.state === f.state));
    $('task-count').textContent = String(all.length);
    $('tasks-note').textContent = values.length + ' 项';
    $('tasks-empty').hidden = !!values.length;
    $('tasks-table-wrap').hidden = !values.length;
    const body = $('tasks-body');
    body.replaceChildren();
    const emptyTitle = $('tasks-empty').querySelector('b');
    if (emptyTitle) emptyTitle.textContent = filtered ? '没有符合筛选的任务' : '尚无任务';
    if ($('tasks-more')) {
      const full = state.jobs.size >= maxKnownJobs;
      $('tasks-more').hidden = !state.jobsMore && !full;
      $('tasks-more-button').hidden = !state.jobsMore || full;
      $('tasks-more-note').textContent = full ? '已显示 ' + maxKnownJobs + ' 项；更早的任务请用任务 ID 打开。' : '';
    }
    for (const j of values) {
      const row = node('tr');
      const title = node('td');
      title.append(
        node(
          'b',
          j.kind === 'export'
            ? exportFormats[j.format || 'xlsx'].label + ' 导出'
            : j.kind === 'snapshot'
              ? '报表快照'
              : j.kind === 'issue'
                ? issueTitle(j)
                : '私有任务'
        )
      );
      const id = node('td', j.job_id, 'task-id');
      id.title = j.job_id;
      const status = node('td');
      status.append(
        node(
          'span',
          (statuses[j.state] || '待刷新') + (j.state === 'failed' && j.error_code ? ' · ' + j.error_code : ''),
          'badge' +
            (j.state === 'failed' || j.state === 'restricted' ? ' danger' : j.state === 'succeeded' ? '' : ' neutral')
        )
      );
      const actions = node('td');
      actions.append(button('刷新', () => refreshTask(j.job_id)));
      // A job whose read has expired no longer has a result to view or a file to download (S04-B): what the server says is kept for good is the job, its ids and, for an issue, its record.
      if (j.expired && j.state === 'succeeded') actions.append(node('span', '已过期并清除', 'badge neutral expired'));
      if (j.snapshot_id && j.state === 'succeeded' && !j.expired)
        actions.append(
          button('查看结果', () =>
            perform(
              async m => {
                await loadSnapshot(j.snapshot_id, j.query || null, m, true);
                if (current(m)) openPage('workspace');
              },
              taskContext(j.job_id, '任务查看')
            )
          )
        );
      if (j.artifact_id && j.state === 'succeeded' && !j.expired)
        actions.append(
          button('下载', () =>
            perform(
              m => download(j.artifact_id, m, j.format, j.kind === 'issue' ? issuedName(j.issue.number) : undefined),
              taskContext(j.job_id, '任务下载')
            )
          )
        );
      // A running issue job sits between the product's reservation and its confirmation: it finishes, or fails and voids. Only a queued one is cancelled.
      if (j.state === 'queued' || (j.state === 'running' && j.kind !== 'issue'))
        actions.append(button('取消', () => cancelJob(j.job_id), 'danger'));
      row.append(title, id, status, actions);
      body.append(row);
    }
  }
  // The number is the source product's: 1..32 of A-Za-z0-9._- starting with a letter or digit. A job view carries it only once the job succeeded.
  const validNumber = n => typeof n === 'string' && /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/.test(n);
  const issuedName = n => {
    if (!validNumber(n)) throw new Failure(0, 'INVALID_RESPONSE');
    return 'issued-' + n + '.pdf';
  };
  // A task shows its number once it has one; until then it waits for the product, and one that failed or was cancelled was never issued.
  // A 2.0 issue names its group instead of a row (S03-K): the title says which, with the key values the caller asked for.
  const issueGroup = j => (j.issue.group ? ' · 分组 ' + window.reportingIssueGroup.text(j.issue.group) : '');
  const issueTitle = j =>
    (validNumber(j.issue.number)
      ? '正式出具 No. ' + j.issue.number
      : j.state === 'failed' || j.state === 'cancelled'
        ? '正式出具（未出具）'
        : '正式出具（编号待来源产品确认）') + issueGroup(j);
  const validCode = c => typeof c === 'string' && /^[A-Z][A-Z0-9_]{0,63}$/.test(c);
  function checkIssue(v, state) {
    return (
      !!v &&
      typeof v === 'object' &&
      (state === 'succeeded' ? validNumber(v.number) : v.number === undefined) &&
      isID(v.template_id) &&
      Number.isSafeInteger(v.template_version) &&
      v.template_version >= 1 &&
      typeof v.template_sha256 === 'string' &&
      /^[a-f0-9]{64}$/.test(v.template_sha256) &&
      (v.group === undefined
        ? Number.isSafeInteger(v.row_index) && v.row_index >= 0
        : v.row_index === undefined && window.reportingIssueGroup.valid(v.group))
    );
  }
  function checkJob(j) {
    if (
      !j ||
      !isID(j.job_id) ||
      !['snapshot', 'export', 'issue'].includes(j.kind) ||
      !Object.hasOwn(statuses, j.state) ||
      (j.snapshot_id && !isID(j.snapshot_id)) ||
      (j.artifact_id && !isID(j.artifact_id)) ||
      (j.format !== undefined &&
        (j.kind === 'snapshot' || j.format === 'xlsx' || !Object.hasOwn(exportFormats, j.format))) ||
      (j.kind === 'issue') !== (j.issue !== undefined) ||
      (j.kind === 'issue' && (j.format !== 'pdf' || !checkIssue(j.issue, j.state))) ||
      (j.error_code !== undefined && (j.state !== 'failed' || !validCode(j.error_code))) ||
      (j.expired !== undefined && (j.expired !== true || j.state !== 'succeeded'))
    )
      throw new Failure(0, 'INVALID_RESPONSE');
  }
  // The server's listing of the identity's jobs (S07-C): a list of at most 100 job views, each checked as a poll's answer is, no id twice.
  function checkJobs(list) {
    if (!Array.isArray(list) || list.length > 100) throw new Failure(0, 'INVALID_RESPONSE');
    const seen = new Set();
    for (const j of list) {
      checkJob(j);
      if (seen.has(j.job_id)) throw new Failure(0, 'INVALID_RESPONSE');
      seen.add(j.job_id);
    }
  }
  // loadJobs makes the server's list the task list: the identity's newest 50 jobs in the server's order (GET /jobs), each keeping what this
  // session knew of it (the query behind a snapshot); what the session knows and the server did not list (a restored job outside the window)
  // stays, older than the listed. The list is read after sign-in, when the panel opens and by 刷新状态; what it says of a result is not an
  // authorization: 查看结果 and 下载 ask the server, which asks the source, as they always did.
  // S07-E: the page is the filter's (kind, state) and the next one starts after its last job (加载更多), until the console knows maxKnownJobs.
  function jobsPath(before) {
    const f = state.jobsFilter;
    return (
      '/jobs?limit=' +
      jobsPage +
      (f.kind ? '&kind=' + f.kind : '') +
      (f.state ? '&state=' + f.state : '') +
      (before ? '&before=' + encodeURIComponent(before) : '')
    );
  }
  // Only the latest read of the first page counts: one started by a filter, the panel or 刷新状态 may still be on its way when the next starts.
  let jobsReads = 0;
  async function loadJobs() {
    const epoch = state.epoch,
      read = ++jobsReads;
    const list = await request(jobsPath(''));
    if (epoch !== state.epoch || read !== jobsReads) throw new Stale();
    checkJobs(list);
    const next = new Map();
    for (const [id, j] of state.jobs) if (!list.some(v => v.job_id === id)) next.set(id, j);
    for (const j of list.slice().reverse()) next.set(j.job_id, Object.assign({}, state.jobs.get(j.job_id) || {}, j));
    state.jobs = next;
    state.jobsMore = list.length === jobsPage ? list.at(-1).job_id : '';
    renderJobs();
    return list.length;
  }
  // The older page goes before everything the console knows; a job it already knows keeps its place and what it knew.
  async function loadMoreJobs() {
    const epoch = state.epoch,
      before = state.jobsMore,
      read = jobsReads;
    if (!before) return 0;
    const list = await request(jobsPath(before));
    if (epoch !== state.epoch || state.jobsMore !== before || read !== jobsReads) throw new Stale();
    checkJobs(list);
    const next = new Map();
    for (const j of list.slice().reverse()) if (!state.jobs.has(j.job_id)) next.set(j.job_id, j);
    for (const [id, j] of state.jobs) next.set(id, j);
    while (next.size > maxKnownJobs) next.delete(next.keys().next().value);
    state.jobs = next;
    state.jobsMore = list.length === jobsPage ? list.at(-1).job_id : '';
    renderJobs();
    return list.length;
  }
  function rememberJob(j, context) {
    checkJob(j);
    const previous = state.jobs.get(j.job_id) || {};
    state.jobs.set(j.job_id, Object.assign({}, previous, context || {}, j));
    if (state.jobs.size > maxKnownJobs) state.jobs.delete(state.jobs.keys().next().value);
    renderJobs();
    return state.jobs.get(j.job_id);
  }
  async function refreshJob(id) {
    const epoch = state.epoch;
    try {
      const j = await request('/jobs/' + encodeURIComponent(id));
      if (epoch !== state.epoch) throw new Stale();
      return rememberJob(j);
    } catch (error) {
      if (epoch !== state.epoch) throw new Stale();
      const j = state.jobs.get(id);
      if (j && [403, 404, 503].includes(error.status) && error.code !== 'OVERLOADED') {
        j.state = 'restricted';
        delete j.snapshot_id;
        delete j.artifact_id;
        renderJobs();
      }
      throw error;
    }
  }
  function taskContext(id, label) {
    return {snapshot: state.jobs.get(id)?.snapshot_id, label: label};
  }
  async function refreshTask(id, m = mark()) {
    const context = taskContext(id, '任务刷新');
    try {
      await refreshJob(id);
      return true;
    } catch (error) {
      fail(error, m, context);
      return false;
    }
  }
  async function cancelJob(id) {
    const m = mark(),
      context = taskContext(id, '任务取消');
    try {
      const j = await request('/jobs/' + encodeURIComponent(id) + '/cancel', {method: 'POST'});
      rememberJob(j);
      if (current(m))
        notify(
          j.state === 'cancelled'
            ? '任务已取消。'
            : j.kind === 'issue' && j.state === 'running'
              ? '出具任务已开始执行，不能取消；它会完成，或失败并通知来源产品作废预留。'
              : '任务已结束，未更改已发布结果。'
        );
    } catch (error) {
      fail(error, m, context);
    }
  }
  async function submit(kind, payload, context, verify) {
    const fingerprint = kind + JSON.stringify(payload);
    if (!state.pending || state.pending.fingerprint !== fingerprint)
      state.pending = {fingerprint: fingerprint, key: crypto.randomUUID()};
    const key = state.pending.key;
    try {
      const job = await request(
        kind === 'snapshot'
          ? '/snapshot-jobs'
          : kind === 'issue'
            ? window.reportingTemplateSpace.path('/issues')
            : '/exports',
        {
          method: 'POST',
          body: payload,
          key: key,
        }
      );
      checkJob(job);
      if (verify) verify(job);
      rememberJob(job, context);
      state.pending = null;
      return job;
    } catch (error) {
      if (error.status >= 400 && error.status < 500) state.pending = null;
      throw error;
    }
  }
  async function poll(id, m) {
    for (let i = 0; i < 100; i++) {
      if (!current(m)) throw new Stale();
      const job = await refreshJob(id);
      if (!current(m)) throw new Stale();
      if (!['queued', 'running'].includes(job.state)) return job;
      await new Promise(resolve => setTimeout(resolve, 300));
    }
    if (!current(m)) throw new Stale();
    notify('任务仍在服务器执行，可在任务工作区继续查看。');
    return null;
  }
  async function perform(fn, context) {
    if (state.busy || state.catalogLoading) return;
    const m = mark();
    busy(1);
    try {
      await fn(m);
    } catch (error) {
      fail(error, m, context);
    } finally {
      if (m.epoch === state.epoch) busy(-1);
    }
  }
  async function preview(m) {
    const q = buildQuery();
    clearResult();
    const result = await request('/queries', {method: 'POST', body: q});
    if (!current(m)) throw new Stale();
    renderResult(result, q, null);
    notify('预览已更新。完整冻结导出请先创建快照。', 'success');
  }
  async function loadSnapshot(id, query, m, first) {
    if (!isID(id)) throw new Failure(400, 'INVALID_REQUEST');
    if (!first && (state.snapshot !== id || !state.cursor)) throw new Failure(400, 'INVALID_REQUEST');
    const next = first ? '' : state.cursor,
      targetPage = first ? 1 : state.page + 1;
    const limit = $('page-size').value;
    if (!['2', '25', '50'].includes(limit)) throw new Failure(400, 'INVALID_REQUEST');
    state.pageLimit = limit;
    const page = await request(
      '/snapshots/' +
        id +
        '/rows?limit=' +
        encodeURIComponent(limit) +
        (next ? '&cursor=' + encodeURIComponent(next) : '')
    );
    if (!current(m)) throw new Stale();
    checkResult(page);
    if (
      page.snapshot_id !== id ||
      !Number.isSafeInteger(page.total_row_count) ||
      page.total_row_count < 0 ||
      page.total_row_count > 1000 ||
      (page.next_cursor && typeof page.next_cursor !== 'string')
    )
      throw new Failure(0, 'INVALID_RESPONSE');
    if (!query) {
      const known = Array.from(state.jobs.values()).find(j => j.snapshot_id === id);
      query = known?.query;
    }
    if (!query) {
      query = {dataset: {id: '已保存的快照', version: ''}, as_of: '原数据时点', timezone: '原快照时区'};
    }
    state.snapshot = id;
    state.cursor = page.next_cursor || '';
    state.page = targetPage;
    renderResult(page, query, {total: page.total_row_count});
    busy(0);
  }
  async function createSnapshot(m) {
    // The result on show is cleared once the submission is answered, or refused for any reason but a quota: a quota refusal leaves the page as it was.
    const q = buildQuery();
    let job;
    try {
      job = await submit('snapshot', q, {query: copy(q), integration: q.integration_id});
    } catch (error) {
      if (error.code !== 'QUOTA_EXCEEDED' && error.code !== 'OVERLOADED') clearResult();
      throw error;
    }
    clearResult();
    if (!current(m)) throw new Stale();
    notify('快照任务已提交，正在后台执行。');
    const done = await poll(job.job_id, m);
    if (!done) return;
    if (done.state === 'succeeded' && done.snapshot_id) {
      await loadSnapshot(done.snapshot_id, q, m, true);
      if (current(m)) notify('冻结快照已生成，可分页查看或导出。', 'success');
    } else
      notify(
        done.state === 'cancelled' ? '快照任务已取消。' : '快照任务失败，请检查当前授权与查询范围。',
        done.state === 'cancelled' ? '' : 'error'
      );
  }
  async function createExport(m, format) {
    const id = state.snapshot;
    const query = copy(state.query);
    const kind = exportFormats[format];
    if (!id || !kind) throw new Failure(400, 'INVALID_REQUEST');
    const job = await submit('export', format === 'xlsx' ? {snapshot_id: id} : {snapshot_id: id, format: format}, {
      query: query,
      integration: query.integration_id,
    });
    if (!current(m)) throw new Stale();
    if (job.kind !== 'export' || (job.format || 'xlsx') !== format) throw new Failure(0, 'INVALID_RESPONSE');
    notify(kind.label + ' 导出任务已提交。');
    const done = await poll(job.job_id, m);
    if (!done) return;
    if (done.state === 'succeeded' && done.artifact_id) await download(done.artifact_id, m, done.format);
    else
      notify(
        done.state === 'cancelled' ? '导出任务已取消。' : '导出任务失败，未生成可下载文件。',
        done.state === 'cancelled' ? '' : 'error'
      );
  }
  async function createIssue(m, body, banded) {
    const query = copy(state.query);
    if (!state.snapshot || body.snapshot_id !== state.snapshot) throw new Failure(400, 'INVALID_REQUEST');
    // The view must be the job that was asked for, before the key is spent or the task kept.
    const job = await submit('issue', body, {query: query, integration: query.integration_id}, j => {
      if (
        j.kind !== 'issue' ||
        j.issue.template_id !== body.template_id ||
        j.issue.template_version !== body.version ||
        (banded
          ? j.issue.group === undefined || j.issue.row_index !== undefined
          : j.issue.group !== undefined || j.issue.row_index !== body.row_index)
      )
        throw new Failure(0, 'INVALID_RESPONSE');
    }).catch(error => {
      // The product or the deployment cannot issue (or the row or the template cannot be printed): not the dataset's query shape, which is what a 422 otherwise says.
      if (error && error.status === 422 && error.code === 'UNSUPPORTED_COMBINATION') error.code = 'ISSUE_UNSUPPORTED';
      throw error;
    });
    if (!current(m)) throw new Stale();
    // The job view carries no number until the source product has confirmed the issue, so there is nothing to name before then.
    notify(
      '正式出具任务已提交' +
        (job.issue.group ? '（分组 ' + window.reportingIssueGroup.text(job.issue.group) + '）' : '') +
        '，正在等待来源产品确认编号；任务成功后才有编号和可下载的正式文件。'
    );
    const done = await poll(job.job_id, m);
    if (!done) return;
    if (done.state === 'succeeded') {
      if (!done.artifact_id) throw new Failure(0, 'INVALID_RESPONSE');
      if (embed) embed.event('issued', {job_id: done.job_id});
      await download(
        done.artifact_id,
        m,
        'pdf',
        issuedName(done.issue.number),
        undefined,
        '来源产品已确认编号 No. ' +
          done.issue.number +
          (done.issue.group ? '（分组 ' + window.reportingIssueGroup.text(done.issue.group) + '）' : '') +
          '；'
      );
    } else
      notify(
        done.state === 'cancelled' ? '出具任务已取消，未生成正式文件。' : issueFailure(done),
        done.state === 'cancelled' ? '' : 'error'
      );
  }
  // Why an issue job failed, in the source product's terms.
  function issueFailure(j) {
    if (j.error_code === 'AUTH_SCOPE_DENIED')
      return '出具失败（AUTH_SCOPE_DENIED）：来源产品拒绝出具——无出具权限、单据未获批准，或来源数据已变化。未生成正式文件。';
    if (j.error_code === 'ISSUE_UNRECONCILED')
      return (
        '出具失败（ISSUE_UNRECONCILED）：平台未能通知来源产品作废其为本任务预留的编号，来源产品处可能仍保留该预留。请联系管理员，并提供任务 ID ' +
        j.job_id +
        '。'
      );
    return '出具任务失败（' + (j.error_code || '未知原因') + '）；未生成正式文件。';
  }
  async function download(id, m, format, name, expectedDigest, prefix) {
    const kind = exportFormats[format || 'xlsx'];
    if (!isID(id) || !kind || format === 'xlsx') throw new Failure(400, 'INVALID_REQUEST');
    const bytes = await request('/artifacts/' + id + '/content', {binary: kind.media});
    if (!current(m)) throw new Stale();
    if (String.fromCharCode(...bytes.subarray(0, kind.magic.length)) !== kind.magic)
      throw new Failure(409, 'INTEGRITY_FAILURE');
    if (expectedDigest) {
      const sum = new Uint8Array(await crypto.subtle.digest('SHA-256', bytes));
      if (!current(m)) throw new Stale();
      if (Array.from(sum, b => b.toString(16).padStart(2, '0')).join('') !== expectedDigest)
        throw new Failure(409, 'INTEGRITY_FAILURE');
    }
    const url = URL.createObjectURL(new Blob([bytes], {type: kind.media}));
    const link = node('a');
    link.href = url;
    link.download = name || 'report-' + id + '.' + kind.extension;
    document.body.append(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    notify((prefix || '') + kind.label + ' 已通过摘要校验，下载已发起。', 'success');
  }
  document.querySelectorAll('[data-page]').forEach(b => b.addEventListener('click', () => openPage(b.dataset.page)));
  document.querySelectorAll('[data-open]').forEach(b => b.addEventListener('click', () => openPage(b.dataset.open)));
  if (!embedded) {
    $('connect-form').addEventListener('submit', connect);
    $('logout').addEventListener('click', () => disconnect());
    $('sh-data-chip').addEventListener('click', () => drawer.open($('sh-data-chip')));
    document
      .querySelectorAll('[data-panel="tasks"]')
      .forEach(b => b.addEventListener('click', () => (tasksPanel.isOpen() ? tasksPanel.close() : tasksPanel.open(b))));
  }
  $('query-product').addEventListener('change', () =>
    loadProduct($('query-product').value).catch(error => fail(error))
  );
  $('query-dataset').addEventListener('change', () => {
    queryChanged();
    $('query-date').value = selected() ? dateInZone(selected().timezone) : '';
  });
  for (const id of ['query-companies', 'query-date']) $(id).addEventListener('input', queryChanged);
  // ADR 0016 (S10-B): 重新检查 asks every product's source again, without the cache.
  if ($('refresh-connections'))
    $('refresh-connections').addEventListener('click', () => {
      if (state.session) checkConnections(true).catch(() => {});
    });
  if ($('audit-refresh')) {
    $('audit-refresh').addEventListener('click', () => perform(() => loadAudit(false), {label: '操作记录'}));
    $('audit-more-button').addEventListener('click', () => perform(() => loadAudit(true), {label: '操作记录'}));
    $('audit-scope').addEventListener('change', () => {
      state.audit = {events: [], more: '', scope: $('audit-scope').value};
      renderAudit();
      perform(() => loadAudit(false), {label: '操作记录'});
    });
  }
  $('refresh-catalog').addEventListener('click', () => {
    if (state.product)
      loadProduct(state.product, true)
        .then(() => notify('数据目录已刷新。', 'success'))
        .catch(error => fail(error));
  });
  $('preview').addEventListener('click', () => perform(preview));
  $('snapshot').addEventListener('click', () => perform(createSnapshot));
  $('export').addEventListener('click', () => perform(m => createExport(m, 'xlsx')));
  $('export-pdf').addEventListener('click', () => perform(m => createExport(m, 'pdf')));
  $('refresh-result').addEventListener('click', () =>
    perform(m => (state.snapshot ? loadSnapshot(state.snapshot, state.query, m, true) : preview(m)))
  );
  $('first-page').addEventListener('click', () => perform(m => loadSnapshot(state.snapshot, state.query, m, true)));
  $('next-page').addEventListener('click', () => perform(m => loadSnapshot(state.snapshot, state.query, m, false)));
  $('page-size').addEventListener('change', () => {
    if (state.busy || state.catalogLoading) {
      $('page-size').value = state.pageLimit;
      return;
    }
    if (state.snapshot) {
      state.view++;
      perform(m => loadSnapshot(state.snapshot, state.query, m, true));
    }
  });
  $('restore-job').addEventListener('click', () => {
    const id = $('restore-id').value.trim();
    perform(
      async m => {
        if (!isID(id)) throw new Failure(400, 'INVALID_REQUEST');
        await refreshJob(id);
        if (current(m)) notify('已恢复当前身份可访问的任务。', 'success');
      },
      taskContext(id, '任务恢复')
    );
  });
  // S07-E: a filter is applied at once to what is known and the server's first page under it is read; 加载更多 reads the next page.
  for (const id of ['tasks-kind', 'tasks-state'])
    if ($(id))
      $(id).addEventListener('change', () => {
        state.jobsFilter = {kind: $('tasks-kind').value, state: $('tasks-state').value};
        state.jobsMore = '';
        renderJobs();
        if (!state.session) return;
        const m = mark();
        loadJobs().catch(error => {
          if (!(error instanceof Stale)) fail(error, m, {label: '任务列表'});
        });
      });
  if ($('tasks-more-button'))
    $('tasks-more-button').addEventListener('click', () =>
      perform(
        async m => {
          const n = await loadMoreJobs();
          if (current(m)) notify(n ? '已加载更早的 ' + n + ' 项任务。' : '没有更早的任务了。', 'success');
        },
        {label: '任务列表'}
      )
    );
  $('refresh-tasks').addEventListener('click', () =>
    perform(
      async m => {
        await loadJobs();
        if (!current(m)) throw new Stale();
        let complete = true;
        for (const id of Array.from(state.jobs.keys())) {
          if (!current(m)) throw new Stale();
          if (!(await refreshTask(id, m))) complete = false;
        }
        if (complete && current(m)) notify('已知任务状态已刷新。', 'success');
      },
      {label: '任务刷新'}
    )
  );
  if (!embedded)
    $('connection-overlay').addEventListener('keydown', event => {
      if (event.key !== 'Tab') return;
      const first = $('access-token'),
        last = $('connect-button');
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    });
  if (!embedded) window.addEventListener('hashchange', () => openPage(location.hash.slice(1)));
  if (embedded) startEmbed();
  else {
    openPage(location.hash.slice(1));
    disconnect();
  }
})();
