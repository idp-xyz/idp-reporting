'use strict';
// The dashboard studio (S06-C, ADR 0008 §4; docs/STUDIO.md "看板工作室"): the document studio's regions (topbar, tool rail, side panel, work area,
// inspector, status bar, dialogs) laid around the dashboard's existing controls. dashboard.js keeps every function and every id; this layer only
// switches panels and tabs, derives the state badge from the dashboard's own state, mirrors its texts into the regions, proxies the cards' buttons
// in the structure panel, runs the publish confirmation and the help dialog, and opens the drawers at narrow widths. It writes no markup and no
// style attribute, loads nothing and stores nothing. In the embed page it runs the same way; the controls that open another console page are hidden there.
window.createReportingBoardStudio = function (ports) {
  const $ = id => document.getElementById(id);
  const root = $('page-dashboard');
  if (!root || !$('bd-studio')) return null;
  const studio = $('bd-studio');
  const qa = sel => Array.from(root.querySelectorAll(sel));
  const SVG = 'http://www.w3.org/2000/svg';
  function h(tag, attrs, ...children) {
    const n = document.createElement(tag);
    if (attrs)
      for (const [k, v] of Object.entries(attrs)) {
        if (v === undefined || v === null || v === false) continue;
        if (k === 'text') n.textContent = v;
        else if (k === 'onclick') n.addEventListener('click', v);
        else n.setAttribute(k, v === true ? '' : String(v));
      }
    for (const c of children.flat()) if (c !== undefined && c !== null) n.append(c);
    return n;
  }
  function icon(name) {
    const s = document.createElementNS(SVG, 'svg');
    s.setAttribute('class', 'ic');
    s.setAttribute('aria-hidden', 'true');
    s.setAttribute('focusable', 'false');
    const u = document.createElementNS(SVG, 'use');
    u.setAttribute('href', '#s-' + name);
    s.append(u);
    return s;
  }
  const narrowSidebar = () => window.matchMedia && window.matchMedia('(max-width:660px)').matches;
  const narrowInspector = () => window.matchMedia && window.matchMedia('(max-width:950px)').matches;

  // ---- the tool rail and the side views ----
  const rail = qa('.rail-tabs [role=tab]'),
    views = qa('.side-view');
  function showSide(name) {
    for (const b of rail) {
      const on = b.dataset.side === name;
      b.classList.toggle('active', on);
      b.setAttribute('aria-selected', String(on));
      b.tabIndex = on ? 0 : -1;
    }
    for (const v of views) {
      const on = v.dataset.view === name;
      v.hidden = !on;
      v.classList.toggle('active', on);
    }
    $('bd-sidebar').setAttribute('aria-labelledby', 'bd-rail-' + name);
  }
  for (const b of rail) {
    b.addEventListener('click', () => {
      showSide(b.dataset.side);
      if (narrowSidebar()) toggleSidebar(true);
    });
    b.addEventListener('keydown', e => {
      const i = rail.indexOf(b);
      let j = null;
      if (e.key === 'ArrowDown' || e.key === 'ArrowRight') j = (i + 1) % rail.length;
      else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') j = (i - 1 + rail.length) % rail.length;
      else if (e.key === 'Home') j = 0;
      else if (e.key === 'End') j = rail.length - 1;
      if (j === null) return;
      e.preventDefault();
      showSide(rail[j].dataset.side);
      rail[j].focus();
    });
  }
  // ---- the inspector's tabs ----
  const tabs = qa('.inspector-tabs [role=tab]');
  function showTab(name) {
    for (const t of tabs) {
      const on = t.dataset.propTab === name;
      t.classList.toggle('active', on);
      t.setAttribute('aria-selected', String(on));
      t.tabIndex = on ? 0 : -1;
      const panel = $(t.getAttribute('aria-controls'));
      if (panel) panel.hidden = !on;
    }
  }
  for (const t of tabs) {
    t.addEventListener('click', () => showTab(t.dataset.propTab));
    t.addEventListener('keydown', e => {
      const i = tabs.indexOf(t);
      let j = null;
      if (e.key === 'ArrowRight' || e.key === 'ArrowDown') j = (i + 1) % tabs.length;
      else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') j = (i - 1 + tabs.length) % tabs.length;
      else if (e.key === 'Home') j = 0;
      else if (e.key === 'End') j = tabs.length - 1;
      if (j === null) return;
      e.preventDefault();
      showTab(tabs[j].dataset.propTab);
      tabs[j].focus();
    });
  }
  // ---- the drawers (narrow widths; the classes do nothing at full width) ----
  function toggleSidebar(force) {
    const el = $('bd-sidebar');
    const open = force === undefined ? !el.classList.contains('open') : !!force;
    el.classList.toggle('open', open);
    if (open) $('bd-inspector').classList.remove('open');
  }
  function toggleInspector(force) {
    const el = $('bd-inspector');
    const open = force === undefined ? !el.classList.contains('open') : !!force;
    el.classList.toggle('open', open);
    if (open) $('bd-sidebar').classList.remove('open');
    if (open && narrowInspector()) {
      const t = tabs.find(x => x.classList.contains('active'));
      if (t) t.focus();
    }
  }
  // ---- dialogs: modal, focus-trapped, Esc closes, focus returns ----
  let openDialog = null;
  const focusable = el =>
    Array.from(
      el.querySelectorAll(
        'button:not([disabled]),a[href],input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'
      )
    ).filter(e => !e.hidden && !e.closest('[hidden]'));
  function open(id, opener) {
    close();
    const overlay = $(id);
    overlay.hidden = false;
    studio.inert = true;
    openDialog = {overlay, opener: opener || document.activeElement};
    const first = focusable(overlay.querySelector('[role=dialog]'));
    (first.find(e => e.dataset.act !== 'close') || first[0] || overlay).focus();
    document.addEventListener('keydown', trap, true);
  }
  function close() {
    if (!openDialog) return;
    const {overlay, opener} = openDialog;
    openDialog = null;
    overlay.hidden = true;
    studio.inert = false;
    document.removeEventListener('keydown', trap, true);
    if (opener && opener.isConnected && typeof opener.focus === 'function') opener.focus();
  }
  function trap(e) {
    if (!openDialog) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
      return;
    }
    if (e.key !== 'Tab') return;
    const list = focusable(openDialog.overlay.querySelector('[role=dialog]'));
    if (!list.length) return;
    const first = list[0],
      last = list[list.length - 1];
    if (e.shiftKey && (document.activeElement === first || !openDialog.overlay.contains(document.activeElement))) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }
  for (const b of qa('.overlay [data-act=close]')) b.addEventListener('click', close);
  for (const o of qa('.overlay'))
    o.addEventListener('mousedown', e => {
      if (e.target === o) close();
    });
  // Esc at the studio level closes an open drawer.
  studio.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || openDialog) return;
    if ($('bd-sidebar').classList.contains('open')) {
      toggleSidebar(false);
      const t = rail.find(x => x.classList.contains('active'));
      if (t) t.focus();
    } else if ($('bd-inspector').classList.contains('open')) {
      toggleInspector(false);
      $('bd-ctx-inspector').focus();
    }
  });

  // ---- what the dashboard says, where the studio shows it ----
  const say = text => {
    $('board-status').textContent = text;
  };
  function badge(s) {
    if (!s.result) return ['未查询', 'neutral'];
    if (!s.saved) return ['草稿', 'neutral'];
    const published = s.release && s.release.published_version === s.saved.version;
    const base = published ? '已发布 v' + s.saved.version : '已保存 v' + s.saved.version;
    return [
      s.dirty ? base + ' · 有未保存修改' : base,
      published ? (s.dirty ? 'amber' : 'green') : s.dirty ? 'amber' : '',
    ];
  }
  function sync() {
    const s = ports.dashboard.state();
    const [text, tone] = badge(s);
    const el = $('bd-state');
    el.textContent = text;
    el.className = 'state-badge' + (tone ? ' ' + tone : '');
    const st = $('bd-status-state');
    st.replaceChildren(h('span', {class: 'status-dot', 'aria-hidden': 'true'}), document.createTextNode(text));
    $('bd-start').hidden = !!s.result;
    $('bd-desk').hidden = !s.result;
    const source = $('board-source').textContent;
    for (const id of ['bd-ctx-source', 'bd-status-source']) $(id).textContent = source;
    const name = s.binding ? s.binding.dataset.id + '@' + s.binding.dataset.version : '尚无当前授权结果';
    $('bd-side-dataset').textContent = name;
    $('bd-data-name').textContent = name;
    $('bd-insp-dataset').textContent = s.binding
      ? s.binding.dataset.id + '@' + s.binding.dataset.version
      : '数据集待绑定';
    $('bd-side-rows').textContent = s.result
      ? (s.snapshot ? '冻结快照当前页' : '当前查询预览') + ' · ' + s.rows + ' 行 · ' + s.columns + ' 个字段'
      : '选择数据后在此组合组件。';
    const count = $('board-count').textContent;
    $('bd-status-count').textContent = count;
    $('bd-insp-meta').textContent =
      count + (s.saved ? ' · 看板 ' + s.saved.id.slice(0, 8) + ' · 版本 ' + s.saved.version : ' · 尚未保存到服务器');
    structure();
    versions.sync();
  }
  function structure() {
    const cards = Array.from($('board-widgets').children),
      list = $('bd-layers');
    list.replaceChildren();
    $('bd-layers-empty').hidden = cards.length > 0;
    cards.forEach((card, index) => {
      const title = card.querySelector('h3'),
        controls = Array.from(card.querySelectorAll('.board-controls button'));
      // A chart (bar, line or pie: S21-A) shares the chart icon; a table and a metric have their own.
      const kind = card.querySelector('.board-chart,.board-line,.board-pie')
        ? 'chart'
        : card.querySelector('table')
          ? 'table'
          : 'metric';
      const row = h(
        'div',
        {class: 'layer-row', role: 'listitem'},
        icon(kind === 'chart' ? 'aggregate' : kind === 'table' ? 'table' : 'summary'),
        h('span', {text: index + 1 + ' · ' + (title ? title.textContent : '组件')})
      );
      [
        ['上移', 'up'],
        ['下移', 'down'],
        ['移除', 'trash'],
      ].forEach(([label, ic], i) => {
        const b = controls[i];
        if (!b) return;
        row.append(
          h(
            'button',
            {
              type: 'button',
              class: 'quiet' + (i === 2 ? ' danger' : ''),
              'aria-label': label + '：' + (title ? title.textContent : '组件'),
              title: label,
              disabled: b.disabled || undefined,
              onclick: () => {
                b.click();
                const again =
                  $('bd-layers').children[
                    Math.min(
                      Math.max(i === 0 ? index - 1 : i === 1 ? index + 1 : index, 0),
                      Math.max($('bd-layers').children.length - 1, 0)
                    )
                  ];
                if (again) again.querySelectorAll('button')[i]?.focus();
              },
            },
            icon(ic)
          )
        );
      });
      list.append(row);
    });
  }
  // ---- the topbar: 历史 opens the version dialog (S07-G, ADR 0011; the words of S09-F, ADR 0014 decision 11); 发布 reads the release state and asks ----
  // Both are the shared component (versions.js, S09-E); these ports are the dashboard's state, requests and words. Opening 历史 reads the
  // list (the latest version) and the release state; a publish that finds the state changed comes back as a 重试.
  const dialog = id => ({
    open: opener => open(id, opener),
    close,
    isOpen: () => !!openDialog && openDialog.overlay === $(id),
  });
  const versions = window.createReportingVersions({
    prefix: 'bd',
    ids: {withdraw: 'board-release-withdraw', versionsButton: 'bd-versions', publishButton: 'bd-publish'},
    dialogs: {versions: dialog('bd-d-versions'), publish: dialog('bd-d-publish')},
    texts: {
      thing: '看板',
      content: '内容',
      dirty: '画布有未保存的修改',
      count: ['组件', '个'],
    },
    run(fn) {
      if (!ports.dashboard.state().result) {
        say('请先选择数据：预览或冻结快照。');
        return;
      }
      fn();
    },
    state() {
      const s = ports.dashboard.state();
      return {
        saved: s.saved,
        latest: s.latest,
        dirty: s.dirty,
        release: s.release,
        dataset: s.binding ? s.binding.dataset.id + '@' + s.binding.dataset.version : '—',
        count: s.widgets,
      };
    },
    async readVersions(then) {
      await ports.dashboard.listVersions();
      await ports.dashboard.readRelease();
      then();
    },
    async readRelease(then) {
      await ports.dashboard.readRelease();
      then();
    },
    async restore(version) {
      await ports.dashboard.restoreVersion(version);
      sync();
    },
    // 保存并发布 (S07-B): unsaved changes, or no saved version yet, are saved as the next version first, its release state is read, and it is
    // published; a step that did not happen ends the sequence. A version picked in 历史 is published as it is (an older one is a rollback).
    // A publish the server refused because the release state changed hands the confirmation back for a 重试 (S09-F).
    async publish(plan) {
      if (plan.saveFirst) {
        const was = ports.dashboard.state().saved;
        await ports.dashboard.save();
        const now = ports.dashboard.state();
        if (!now.saved || (was && now.saved.version === was.version)) {
          sync();
          return;
        }
        await ports.dashboard.readRelease();
        if (!ports.dashboard.state().release) {
          sync();
          return;
        }
      }
      await ports.dashboard.publish(plan.version || undefined, () => versions.conflict());
      sync();
    },
  });
  for (const b of qa('[data-act=help]')) b.addEventListener('click', () => open('bd-d-help', b));
  // The data buttons (the start card, the context bar, the data panel) open the console's data drawer (S07-A); without one (the embed page) they go to the query form on the page.
  for (const b of qa('[data-act=data]'))
    b.addEventListener('click', () => {
      if (ports.data) ports.data(b);
      else {
        const f = document.getElementById('query-companies');
        if (f) f.focus();
      }
    });
  for (const b of qa('[data-act=toggle-sidebar]')) b.addEventListener('click', () => toggleSidebar());
  for (const b of qa('[data-act=toggle-inspector]')) b.addEventListener('click', () => toggleInspector());
  sync();
  return {
    sync,
    show() {
      sync();
    },
    clear() {
      close();
      versions.clear();
      toggleSidebar(false);
      toggleInspector(false);
      showSide('components');
      showTab('properties');
      sync();
    },
    showTab,
    showSide,
  };
};
