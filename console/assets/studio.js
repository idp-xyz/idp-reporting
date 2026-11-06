'use strict';
// The document studio (S06-A, ADR 0008): the shell of the prototype IDP_Reporting_Designer_UIUX_V3.0 around the designer's functions.
// It has no business logic of its own. Every control here either calls a function documents.js or canvas.js already had (through `api`, or by
// clicking the button that function was bound to), or is disabled with its reason. It writes text and DOM nodes only: no markup strings, no
// style attributes, no storage; positions are SVG attributes. docs/STUDIO.md maps each region to its function.
window.createReportingStudio = function (api) {
  const $ = id => document.getElementById(id),
    canvas = api.canvas,
    root = $('page-documents'),
    studio = $('doc-canvas-panel');
  const node = (tag, text, cls) => {
    const e = document.createElement(tag);
    if (text !== undefined) e.textContent = text;
    if (cls) e.className = cls;
    return e;
  };
  const NS = 'http://www.w3.org/2000/svg',
    svgNode = (tag, attrs) => {
      const e = document.createElementNS(NS, tag);
      for (const k of Object.keys(attrs || {})) e.setAttribute(k, String(attrs[k]));
      return e;
    };
  const icon = name => {
    const s = svgNode('svg', {class: 'ic', 'aria-hidden': 'true', focusable: 'false'});
    s.append(svgNode('use', {href: '#i-' + name}));
    return s;
  };
  const say = text => {
    $('doc-status').textContent = text;
  };
  const wide = () => !window.matchMedia || !window.matchMedia('(max-width:950px)').matches;
  const editing = t => !!t && !!t.closest && !!t.closest('input,textarea,select,[contenteditable="true"]');
  const focusable = el =>
    [...el.querySelectorAll('a[href],button,input,select,textarea,summary,[tabindex]')].filter(
      e =>
        !e.disabled &&
        e.getAttribute('tabindex') !== '-1' &&
        !e.closest('[hidden]') &&
        e.type !== 'hidden' &&
        e.getAttribute('aria-hidden') !== 'true'
    );
  let S = api.state(),
    layout = {fit: true};
  S.labelMap = {};
  const kinds = {
      literal: '固定文字',
      field: '字段文本',
      group_sum: '分组合计',
      page: '页码文字',
      issue_number: '出具编号',
      row: '行字段',
      total: '合计',
    },
    types = {
      text: '文本',
      line: '分隔线',
      rect: '矩形容器',
      table: '明细表',
      barcode: '条形码',
      qrcode: '二维码',
      image: '图片 / 标识',
    };
  const iconOf = it =>
    it.type === 'table'
      ? 'table'
      : it.type === 'line'
        ? 'minus'
        : it.type === 'rect'
          ? 'rect'
          : it.type === 'barcode'
            ? 'barcode'
            : it.type === 'qrcode'
              ? 'qr'
              : it.type === 'image'
                ? 'image'
                : it.source === 'field' || it.source === 'row'
                  ? 'field'
                  : it.source === 'group_sum' || it.source === 'total'
                    ? 'aggregate'
                    : it.source === 'page'
                      ? 'pageNumber'
                      : 'text';
  const nameOfItem = it =>
    it.type === 'table'
      ? '明细表 ' + it.key
      : ['line', 'rect', 'barcode', 'qrcode', 'image'].includes(it.type)
        ? it.key + ' · ' + types[it.type]
        : it.key +
          ' · ' +
          (kinds[it.source] || '文本') +
          (it.source === 'literal' && it.text
            ? ' “' + it.text.slice(0, 16) + '”'
            : it.field
              ? ' ' + (S.labelMap[it.field] || it.field)
              : '');

  // ---- dialogs: one at a time, labelled, modal, Tab stays inside, Esc and the backdrop close them, the focus goes back -----------------------------
  const dialogs = {};
  let openId = '';
  for (const el of root.querySelectorAll('.overlay[data-dialog]'))
    dialogs[el.getAttribute('data-dialog')] = {el, previous: null, onClose: null};
  const parked = [];
  function open(id, onOpen, onClose) {
    const d = dialogs[id];
    if (!d || openId === id) return;
    if (openId) close(openId, true);
    d.previous = document.activeElement;
    d.onClose = onClose || null;
    d.el.classList.add('open');
    openId = id;
    studio.inert = true;
    if (onOpen) onOpen();
    const body = d.el.querySelector('.dialog'),
      list = focusable(body),
      target = list.find(e => !e.matches('[data-act="close"]')) || list[0] || body;
    if (!body.hasAttribute('tabindex')) body.setAttribute('tabindex', '-1');
    target.focus({preventScroll: true});
  }
  function close(id, keepFocus) {
    const d = dialogs[id];
    if (!d || openId !== id) return;
    d.el.classList.remove('open');
    openId = '';
    studio.inert = false;
    if (d.onClose) d.onClose();
    d.onClose = null;
    const back = d.previous;
    d.previous = null;
    if (!keepFocus && back && back.isConnected && back.focus && !back.closest('[hidden]'))
      back.focus({preventScroll: true});
  }
  for (const [id, d] of Object.entries(dialogs)) {
    d.el.addEventListener('keydown', ev => {
      if (ev.key === 'Escape') {
        ev.preventDefault();
        ev.stopPropagation();
        close(id);
        return;
      }
      if (ev.key === 'Tab') {
        const list = focusable(d.el.querySelector('.dialog'));
        if (!list.length) {
          ev.preventDefault();
          return;
        }
        const first = list[0],
          last = list[list.length - 1],
          at = document.activeElement;
        if (ev.shiftKey && (at === first || !d.el.contains(at))) {
          ev.preventDefault();
          last.focus();
        } else if (!ev.shiftKey && (at === last || !d.el.contains(at))) {
          ev.preventDefault();
          first.focus();
        }
      }
    });
    d.el.addEventListener('pointerdown', ev => {
      if (ev.target === d.el) close(id);
    });
  }
  // A focus that fell out of a dialog (an element it was on was rebuilt) must not leave the keyboard without Esc or Tab: the document watches while a dialog is open.
  document.addEventListener(
    'keydown',
    ev => {
      if (!openId || dialogs[openId].el.contains(ev.target) || document.querySelector('.cv-backdrop:not([hidden])'))
        return;
      if (ev.key === 'Escape') {
        ev.preventDefault();
        close(openId);
        return;
      }
      if (ev.key === 'Tab') {
        const list = focusable(dialogs[openId].el.querySelector('.dialog'));
        if (list.length) {
          ev.preventDefault();
          (ev.shiftKey ? list.at(-1) : list[0]).focus();
        }
      }
    },
    true
  );
  // A panel of the console that the studio shows in a drawer (the query form, the rows of the snapshot) is moved there while the drawer is open and put back after.
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
  const openQuery = () =>
    open(
      'query',
      () => park(document.querySelector('.query-panel'), $('st-query-slot')),
      () => unpark($('st-query-slot'))
    );
  const openRows = () =>
    open(
      'rows',
      () => {
        const slot = $('st-rows-slot');
        for (const id of ['result-table-wrap', 'result-pager']) park($(id), slot);
      },
      () => unpark($('st-rows-slot'))
    );
  const saveFile = (name, text, type) => {
    const url = URL.createObjectURL(new Blob([text], {type}));
    const a = document.createElement('a');
    a.href = url;
    a.download = name;
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  };

  // ---- tabs: the ribbon, the rail and the inspector (roving tabindex, arrows, Home and End) ----------------------------------------------------------
  function tabs(list, select) {
    list.forEach(t =>
      t.addEventListener('keydown', ev => {
        const at = list.indexOf(t),
          keys = t.closest('[aria-orientation="vertical"]') ? ['ArrowDown', 'ArrowUp'] : ['ArrowRight', 'ArrowLeft'];
        let to = -1;
        if (ev.key === keys[0]) to = (at + 1) % list.length;
        else if (ev.key === keys[1]) to = (at + list.length - 1) % list.length;
        else if (ev.key === 'Home') to = 0;
        else if (ev.key === 'End') to = list.length - 1;
        if (to < 0) return;
        ev.preventDefault();
        select(list[to]);
        list[to].focus();
      })
    );
  }
  const ribbonTabs = [...root.querySelectorAll('[data-ribbon]')];
  function showRibbon(tab) {
    for (const t of ribbonTabs) {
      const on = t === tab;
      t.classList.toggle('active', on);
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      t.tabIndex = on ? 0 : -1;
    }
    for (const p of root.querySelectorAll('.ribbon-panel'))
      p.classList.toggle('active', p.getAttribute('data-panel') === tab.getAttribute('data-ribbon'));
  }
  tabs(ribbonTabs, showRibbon);
  ribbonTabs.forEach(t => t.addEventListener('click', () => showRibbon(t)));
  const railTabs = [...root.querySelectorAll('[data-side]')];
  function showSide(tab, fromClick) {
    const view = tab.getAttribute('data-side'),
      side = $('sidebar'),
      was = railTabs.find(t => t.getAttribute('aria-selected') === 'true');
    for (const t of railTabs) {
      const on = t === tab;
      t.classList.toggle('active', on);
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      t.tabIndex = on ? 0 : -1;
    }
    for (const v of side.querySelectorAll('.side-view')) {
      const on = v.getAttribute('data-view') === view;
      v.hidden = !on;
      v.classList.toggle('active', on);
    }
    side.setAttribute('aria-labelledby', tab.id);
    if (!wide() && fromClick) side.classList.toggle('open', was !== tab || !side.classList.contains('open'));
    render();
  }
  tabs(railTabs, t => showSide(t, false));
  railTabs.forEach(t => t.addEventListener('click', () => showSide(t, true)));
  const inspTabs = [...root.querySelectorAll('[data-prop-tab]')];
  function showInspector(tab) {
    const name = tab.getAttribute('data-prop-tab');
    for (const t of inspTabs) {
      const on = t === tab;
      t.classList.toggle('active', on);
      t.setAttribute('aria-selected', on ? 'true' : 'false');
      t.tabIndex = on ? 0 : -1;
    }
    for (const n of ['properties', 'rules', 'check']) $('st-ipanel-' + n).hidden = n !== name;
  }
  tabs(inspTabs, showInspector);
  inspTabs.forEach(t => t.addEventListener('click', () => showInspector(t)));
  const openInspector = name => {
    showInspector($('st-itab-' + name));
    if (!wide()) $('inspector').classList.add('open');
    else studio.classList.remove('no-inspector');
    syncToggles();
  };

  // ---- view switches (session only) ---------------------------------------------------------------------------------------------------------
  const pressed = (id, on) => {
    const b = $(id);
    if (b) b.setAttribute('aria-pressed', on ? 'true' : 'false');
  };
  const sets = {grid: false, snap: true, guides: true, reading: false};
  function syncToggles() {
    pressed('st-grid', sets.grid);
    pressed('st-snap', sets.snap);
    pressed('st-guides', sets.guides);
    pressed('st-reading', sets.reading);
    pressed('st-painter', S.painter);
    pressed(
      'st-inspector-toggle',
      wide() ? !studio.classList.contains('no-inspector') : $('inspector').classList.contains('open')
    );
    $('doc-canvas-host').classList.toggle('studio-nobands', !sets.guides);
    studio.classList.toggle('studio-reading', sets.reading);
  }
  function toggle(name) {
    sets[name] = !sets[name];
    if (name === 'grid') canvas.setGrid(sets.grid);
    if (name === 'snap') {
      canvas.setSnap(sets.snap);
      say(sets.snap ? '吸附已开：拖动时吸附网格与其他项目的边。' : '吸附已关：拖动按 0.1 毫米移动，不吸附。');
    }
    if (name === 'guides') say(sets.guides ? '已显示区段标签与分界线。' : '已隐藏区段标签与分界线。');
    syncToggles();
  }

  // ---- locks and hidden items (session only: nothing here is in the template) --------------------------------------------------------------------
  function toggleLock(keys) {
    keys = keys || canvas.selection();
    if (!keys.length) {
      say('请先选择要锁定的项目。');
      return;
    }
    const all = keys.every(k => canvas.isLocked(k));
    canvas.setLocked(keys, !all);
    say(
      all
        ? '已解除锁定：' + keys.join('、') + '。'
        : '已锁定：' +
            keys.join('、') +
            '。锁定只在本次会话有效，不写入模板；锁定的项目不会被指针或方向键移动或调整大小。'
    );
    api.touch();
  }
  function toggleHidden(key) {
    const on = !canvas.hidden().includes(key);
    canvas.setHidden([key], on);
    say(on ? key + ' 已在画布上隐藏（仍在模板中，预览与输出照常包含它）。' : key + ' 已在画布上显示。');
    api.touch();
  }

  // ---- zoom, ruler, space-drag panning ----------------------------------------------------------------------------------------------------------
  const steps = [50, 75, 100, 150, 200],
    scroll = $('doc-canvas-scroll');
  function fitZoom() {
    const avail = scroll.clientWidth - 90;
    if (!(avail > 0)) return false;
    const want = Math.max(50, Math.min(200, Math.floor(((avail - 24 - 16) / 630) * 100)));
    return want === canvas.zoom() ? true : canvas.setZoom(want);
  }
  function zoomTo(value) {
    if (value === 'fit') {
      layout.fit = true;
      fitZoom();
    } else {
      layout.fit = false;
      canvas.setZoom(Number(value));
    }
    syncZoom();
  }
  function syncZoom() {
    const z = canvas.zoom(),
      select = $('zoom');
    if (layout.fit) {
      select.value = 'fit';
      return;
    }
    if (steps.includes(z)) {
      select.value = String(z);
      return;
    }
    let o = [...select.options].find(x => x.getAttribute('data-custom') === '1');
    if (!o) {
      o = node('option');
      o.setAttribute('data-custom', '1');
      select.append(o);
    }
    o.value = 'z' + z;
    o.textContent = z + '%';
    select.value = 'z' + z;
  }
  function stepZoom(dir) {
    const z = canvas.zoom(),
      next = dir > 0 ? steps.find(v => v > z) : [...steps].reverse().find(v => v < z);
    if (next === undefined) return;
    layout.fit = false;
    canvas.setZoom(next);
    syncZoom();
  }
  $('zoom').addEventListener('change', () => {
    const v = $('zoom').value;
    if (v === 'fit' || /^\d+$/.test(v)) zoomTo(v);
  });
  $('st-zoom-in').addEventListener('click', () => stepZoom(1));
  $('st-zoom-out').addEventListener('click', () => stepZoom(-1));
  // A change of the range input (the canvas's own zoom control, kept for assistive tools and tests) is a zoom too.
  $('doc-zoom').addEventListener('input', () => {
    layout.fit = false;
    syncZoom();
  });
  // The ruler: millimetres along the page, as SVG, following the canvas's scroll position.
  function ruler() {
    const svg = $('st-ruler'),
      host = $('doc-canvas-host').querySelector('svg'),
      box = svg.parentNode,
      width = Math.max(0, Math.round(box.clientWidth) || 0);
    svg.setAttribute('width', String(width));
    const left = host ? host.getBoundingClientRect().left - box.getBoundingClientRect().left + canvas.origin.x : 0,
      sig = width + ':' + canvas.zoom() + ':' + left;
    if (svg.getAttribute('data-sig') === sig) return;
    svg.setAttribute('data-sig', sig);
    svg.replaceChildren();
    if (!host || !width) return;
    const p = (canvas.scale * canvas.zoom()) / 100,
      every = p >= 2.4 ? 10 : 20;
    for (let mm = 0; mm <= 210; mm += 5) {
      const x = left + mm * p;
      if (x < -20 || x > width + 20) continue;
      const major = mm % every === 0;
      svg.append(svgNode('line', {x1: x, x2: x, y1: 21, y2: major ? 17 : 19}));
      if (major) {
        const t = svgNode('text', {x: x + 2, y: 12});
        t.textContent = String(mm);
        svg.append(t);
      }
    }
  }
  scroll.addEventListener('scroll', ruler);
  const relayout = () => {
    if (layout.fit) fitZoom();
    ruler();
  };
  window.addEventListener('resize', relayout);
  let space = false,
    pan = null;
  document.addEventListener('keydown', ev => {
    if (ev.key === ' ' && !editing(ev.target) && !root.hidden) space = true;
  });
  document.addEventListener('keyup', ev => {
    if (ev.key === ' ') {
      space = false;
      if (!pan) scroll.classList.remove('panning');
    }
  });
  scroll.addEventListener(
    'pointerdown',
    ev => {
      if (!space || (ev.button !== undefined && ev.button !== 0)) return;
      ev.stopPropagation();
      ev.preventDefault();
      pan = {x: ev.clientX, y: ev.clientY, l: scroll.scrollLeft, t: scroll.scrollTop};
      scroll.classList.add('panning');
      try {
        scroll.setPointerCapture(ev.pointerId);
      } catch (_) {}
    },
    true
  );
  scroll.addEventListener('pointermove', ev => {
    if (!pan) return;
    scroll.scrollLeft = pan.l - (ev.clientX - pan.x);
    scroll.scrollTop = pan.t - (ev.clientY - pan.y);
  });
  const endPan = () => {
    pan = null;
    if (!space) scroll.classList.remove('panning');
  };
  scroll.addEventListener('pointerup', endPan);
  scroll.addEventListener('pointercancel', endPan);

  // ---- dragging a card or a field onto the page (pointer events; the canvas's own pointer handling is untouched) --------------------------------------
  let drag = null,
    suppress = null;
  function dragSource(el, drop) {
    el.addEventListener('pointerdown', ev => {
      if ((ev.button !== undefined && ev.button !== 0) || el.getAttribute('aria-disabled') === 'true') return;
      drag = {el, x: ev.clientX, y: ev.clientY, moved: false, drop, id: ev.pointerId};
    });
  }
  document.addEventListener('pointermove', ev => {
    if (!drag || (drag.id !== undefined && ev.pointerId !== undefined && ev.pointerId !== drag.id)) return;
    if (!drag.moved && Math.hypot(ev.clientX - drag.x, ev.clientY - drag.y) < 6) return;
    if (!drag.moved) {
      drag.moved = true;
      studio.classList.add('studio-dragging');
    }
    canvas.dropAt(canvas.pageAt(ev.clientX, ev.clientY));
  });
  function endDrag(ev, cancel) {
    if (!drag) return;
    const d = drag;
    drag = null;
    studio.classList.remove('studio-dragging');
    if (!d.moved) return;
    suppress = d.el;
    setTimeout(() => {
      suppress = null;
    }, 0);
    const at = cancel ? null : canvas.pageAt(ev.clientX, ev.clientY);
    canvas.dropAt(null);
    if (at) d.drop(at);
    else say('放到页面上才会插入；已取消。');
  }
  document.addEventListener('pointerup', ev => endDrag(ev, false));
  document.addEventListener('pointercancel', ev => endDrag(ev, true));
  // The click a browser sends after a drag belongs to the card or field that was dragged: it does not insert a second copy.
  document.addEventListener(
    'click',
    ev => {
      if (suppress && suppress.contains(ev.target)) {
        ev.stopPropagation();
        ev.preventDefault();
      }
    },
    true
  );
  for (const card of root.querySelectorAll('[data-insert]'))
    dragSource(card, at => afterInsert(api.insert(card.getAttribute('data-insert'), {at})));

  // ---- clicks ------------------------------------------------------------------------------------------------------------------------------------
  const afterInsert = r => {
    if (r === 'table-options') open('insert');
  };
  const acts = {
    library: () => (api.library ? api.library() : openChooser()),
    chooser: () => openChooser(),
    help: () => open('help'),
    versions: () => openVersions(),
    preview: () => openPreview(),
    publish: () => openPublish(),
    binding: openQuery,
    parameters: openQuery,
    'data-preview': openRows,
    'page-settings': () => open('page'),
    bands: () => open('bands'),
    grouping: () => open('bands'),
    'print-settings': () => open('print'),
    'insert-options': () => open('insert'),
    issue: () => open('issue'),
    'export-template': () => {
      $('doc-json-save').click();
      open('json');
    },
    'import-template': () => open('json'),
    'export-sample': () => {
      const v = api.sample();
      if (!v) {
        say('先查询并创建冻结快照。');
        return;
      }
      saveFile('snapshot-sample.json', JSON.stringify(v, null, 2), 'application/json');
      say('已导出冻结快照的前 ' + v.rows.length + ' 行（JSON，只含当前授权的数据）。');
    },
    validate: () => {
      api.validateNow();
      openInspector('check');
    },
    'edge-sample': b => {
      if (b.id !== 'doc-check') $('doc-check').click();
      openInspector('check');
    },
    copy: () => api.clipboard.copy(),
    cut: () => api.clipboard.cut(),
    paste: () => api.clipboard.paste(),
    'format-brush': () => api.painter.toggle(),
    lock: () => toggleLock(),
    grid: () => toggle('grid'),
    snap: () => toggle('snap'),
    guides: () => toggle('guides'),
    reading: () => toggle('reading'),
    delete: b => {
      if (b.id !== 'doc-del') $('doc-del').click();
    },
    rename: () => startRename(),
    'toggle-inspector': () => {
      if (wide()) studio.classList.toggle('no-inspector');
      else $('inspector').classList.toggle('open');
      syncToggles();
      relayout();
    },
    'toggle-sidebar': () => $('sidebar').classList.remove('open'),
    'mark-refusal': () => {
      if (S.refusal) canvas.mark(S.refusal);
    },
    close: b => {
      const o = b.closest('.overlay[data-dialog]');
      if (o) close(o.getAttribute('data-dialog'));
    },
  };
  root.addEventListener('click', ev => {
    const card = ev.target.closest && ev.target.closest('[data-insert]');
    if (card && root.contains(card)) {
      if (card.getAttribute('aria-disabled') === 'true') {
        say(card.getAttribute('data-unsupported') || card.title);
        return;
      }
      afterInsert(api.insert(card.getAttribute('data-insert')));
      return;
    }
    const b = ev.target.closest && ev.target.closest('[data-act]');
    if (!b || !root.contains(b)) return;
    if (b.getAttribute('aria-disabled') === 'true') {
      ev.preventDefault();
      say(b.getAttribute('data-unsupported') || b.title);
      return;
    }
    const act = b.getAttribute('data-act');
    if (act.startsWith('insert-') && act !== 'insert-options') {
      afterInsert(api.insert(act.slice(7)));
      return;
    }
    if (acts[act]) acts[act](b);
  });
  for (const b of root.querySelectorAll('[data-style-size]'))
    b.addEventListener('click', () =>
      api.styleSelected({
        font_size: Number(b.getAttribute('data-style-size')),
        bold: b.getAttribute('data-style-bold') === 'true',
      })
    );
  for (const b of root.querySelectorAll('[data-text-align]'))
    b.addEventListener('click', () => api.styleSelected({align: b.getAttribute('data-text-align')}));
  // Keyboard: save, the painter's cancel, closing the narrow drawers.
  document.addEventListener('keydown', ev => {
    if (root.hidden) return;
    if ((ev.ctrlKey || ev.metaKey) && !ev.altKey && !ev.shiftKey && (ev.key || '').toLowerCase() === 's') {
      ev.preventDefault();
      if (!openId) $('doc-server-save').click();
      return;
    }
    if (ev.key === 'Escape' && !openId) {
      if (S.painter) {
        api.painter.toggle();
        return;
      }
      if ($('sidebar').classList.contains('open')) {
        $('sidebar').classList.remove('open');
        return;
      }
      if ($('inspector').classList.contains('open')) {
        $('inspector').classList.remove('open');
        syncToggles();
      }
    }
  });

  // ---- the title: the name, editable in place ----------------------------------------------------------------------------------------------------
  function startRename() {
    if (!S.ready) {
      say('先查询当前授权数据并打开模板。');
      return;
    }
    const input = $('st-name-input');
    input.value = S.named ? S.config.name : '';
    input.removeAttribute('aria-invalid');
    input.hidden = false;
    $('titleButton').hidden = true;
    input.focus();
    input.select();
  }
  function endRename(commit) {
    const input = $('st-name-input');
    if (input.hidden) return;
    if (commit) {
      const err = api.rename(input.value);
      if (err) {
        input.setAttribute('aria-invalid', 'true');
        say(err);
        return;
      }
    }
    input.hidden = true;
    $('titleButton').hidden = false;
    $('titleButton').focus({preventScroll: true});
  }
  $('st-name-input').addEventListener('keydown', ev => {
    if (ev.key === 'Enter') {
      ev.preventDefault();
      endRename(true);
    } else if (ev.key === 'Escape') {
      ev.preventDefault();
      ev.stopPropagation();
      endRename(false);
    }
  });
  $('st-name-input').addEventListener('blur', () => endRename(true));

  // ---- the dialogs ---------------------------------------------------------------------------------------------------------------------------------
  let versionPick = 0,
    publishPick = 0;
  function openVersions() {
    open('versions', () => {
      versionPick = S.saved ? S.saved.version : 0;
      if (S.saved) api.loadVersions(() => render());
      render();
    });
  }
  const openPreview = () => open('preview', () => render());
  function openPublish(version) {
    publishPick = version || 0;
    open('publish', () => {
      if (S.saved && !S.release) api.readRelease(() => render());
      render();
    });
  }
  const openChooser = () =>
    open('chooser', () => {
      api.loadChooser(() => render());
      render();
    });
  $('st-ver-preview').addEventListener('click', () => {
    if (versionPick) api.previewVersion(versionPick);
  });
  $('st-ver-restore').addEventListener('click', () => {
    if (versionPick) {
      api.restoreVersion(versionPick);
      close('versions');
    }
  });
  $('st-ver-publish').addEventListener('click', () => {
    if (versionPick) openPublish(versionPick);
  });
  $('st-pub-confirm').addEventListener('click', () => {
    if ($('st-pub-confirm').disabled) return;
    const n = publishPick || (S.saved ? S.saved.version : 0),
      cur = !!S.saved && n === S.saved.version,
      stale = !S.saved || (cur && S.dirty);
    // 保存并发布 (S07-B): the unsaved changes become the next version, its release state is read, and that version is published.
    if (stale && api.save) {
      api.save(() => api.readRelease(() => api.publish()));
      close('publish');
      return;
    }
    api.publish(cur ? undefined : n);
    close('publish');
  });
  $('st-chooser-new').addEventListener('click', () => {
    $('doc-reset').click();
    close('chooser');
  });
  $('st-chooser-import').addEventListener('click', () => open('json'));
  $('st-chooser-refresh').addEventListener('click', () => api.loadChooser(() => render()));
  $('st-json-download').addEventListener('click', () => {
    if (!$('doc-json').value) $('doc-json-save').click();
    if ($('doc-json').value) saveFile('template.json', $('doc-json').value, 'application/json');
  });
  $('st-json-file').addEventListener('change', async () => {
    const f = $('st-json-file').files[0];
    $('st-json-file').value = '';
    if (!f) return;
    if (f.size > 65536) {
      say('模板超过64KiB。');
      return;
    }
    $('doc-json').value = await f.text();
    say('已读入 ' + f.name + '；检查内容后点“打开模板JSON”。');
  });

  // ---- rendering from the designer's state ------------------------------------------------------------------------------------------------------
  const text = (id, v) => {
    const e = $(id);
    if (e && e.textContent !== v) e.textContent = v;
  };
  const setAria = (el, on, reason) => {
    if (on) {
      el.setAttribute('aria-disabled', 'true');
      el.setAttribute('data-unsupported', reason);
      el.title = reason;
    } else {
      el.removeAttribute('aria-disabled');
      el.removeAttribute('data-unsupported');
    }
  };
  const one = () => !!S.schema && S.schema.startsWith('1.');
  const unsupported1x = '1.x 模板不支持；先转换为 2.0 分带模板（页面 › 区段设置）';
  const cardRules = {
    watermark: () => '正式文件不支持水印',
    signature: () => (one() ? unsupported1x : ''),
    line: () => (one() ? unsupported1x : ''),
    rect: () => (one() ? unsupported1x : ''),
    image: () => (one() ? unsupported1x : ''),
    barcode: () => (one() ? unsupported1x : ''),
    qr: () => (one() ? unsupported1x : ''),
    pageNumber: () => (one() ? unsupported1x : ''),
  };
  const cardTitles = {
    aggregate: '仅支持“分组合计”：服务端对分组内各行精确求和（group_sum）',
    summary: '仅支持“分组合计”：服务端对分组内各行精确求和（group_sum）',
    signature: '签章与签名属于后续阶段',
  };
  function renderControls() {
    const sel = canvas.selection(),
      n = sel.length;
    for (const el of root.querySelectorAll('[data-insert],[data-act^="insert-"]')) {
      const kind = el.hasAttribute('data-insert')
        ? el.getAttribute('data-insert')
        : el.getAttribute('data-act').slice(7);
      if (kind === 'options') continue;
      const reason = cardRules[kind] ? cardRules[kind]() : '';
      if (reason) setAria(el, true, reason);
      else {
        setAria(el, false);
        el.title = cardTitles[kind] || el.getAttribute('aria-label') || el.textContent.trim();
      }
    }
    const off = (id, v) => {
      const b = $(id);
      if (b) b.disabled = !!v;
    };
    const onlyTable =
      S.schema &&
      S.schema.startsWith('2') &&
      S.config &&
      n === 1 &&
      !!S.config.bands.detail &&
      S.config.bands.detail.elements[0].id === sel[0];
    const pickable =
      S.ready && n > 0 && !onlyTable && !(S.schema && S.schema.startsWith('1.') && n === 1 && sel[0] === 'table');
    off('st-copy', !pickable);
    off('st-cut', !pickable);
    off('st-paste', !S.ready || !S.clip);
    off('st-painter', !S.ready);
    off('st-lock', !S.ready || !n);
    off('st-insp-lock', !n);
    off('st-insp-delete', !n);
    const allLocked = n > 0 && sel.every(k => canvas.isLocked(k));
    pressed('st-lock', allLocked);
    pressed('st-insp-lock', allLocked);
    $('st-insp-lock').setAttribute('aria-label', allLocked ? '解除锁定所选项目' : '锁定所选项目');
    syncToggles();
  }
  const last = {};
  // A list that is built again keeps the focus: the element that had it is found again by its data-k.
  function rebuild(box, fill) {
    const active = document.activeElement,
      k = active && box.contains(active) ? active.getAttribute('data-k') : null;
    box.replaceChildren();
    fill();
    if (k) {
      const again = [...box.querySelectorAll('[data-k]')].find(e => e.getAttribute('data-k') === k);
      if (again) again.focus({preventScroll: true});
    }
  }
  const once = (k, sig, fn) => {
    if (last[k] === sig) return;
    last[k] = sig;
    fn();
  };
  function renderLayers() {
    const groups = api.elementsOf(),
      hidden = canvas.hidden(),
      sel = canvas.selection();
    once('layers', JSON.stringify([groups, hidden, canvas.locked(), sel, S.labelMap]), () => {
      const box = $('st-layers');
      rebuild(box, () => {
        const pills = {
          page_header: '每页',
          document_header: '单据首页',
          detail: '流式',
          document_footer: '单据末页',
          page_footer: '每页',
          elements: '文本框',
          table: '明细表',
        };
        for (const g of groups) {
          const wrap = node('div', undefined, 'outline-band'),
            head = node('button', undefined, 'outline-band-head'),
            label = api.bandLabels[g.band] || (g.band === 'table' ? '明细表' : '文本框');
          head.type = 'button';
          head.setAttribute('data-k', 'band:' + g.band);
          head.append(icon('layers'), document.createTextNode(' ' + label), node('span', pills[g.band] || '', 'pill'));
          head.setAttribute('aria-label', '选择' + label + '的全部元素');
          head.addEventListener('click', () => canvas.select(g.items.map(i => i.key)));
          wrap.append(head);
          for (const it of g.items) {
            const isLocked = canvas.isLocked(it.key),
              isHidden = hidden.includes(it.key),
              on = sel.includes(it.key);
            const row = node('div', undefined, 'layer-row' + (on ? ' active' : '')),
              name = node('button', nameOfItem(it), 'layer-label'),
              lock = node('button', undefined, 'quiet'),
              eye = node('button', undefined, 'quiet');
            name.type = lock.type = eye.type = 'button';
            name.setAttribute('data-k', 'name:' + it.key);
            lock.setAttribute('data-k', 'lock:' + it.key);
            eye.setAttribute('data-k', 'eye:' + it.key);
            name.setAttribute('aria-pressed', on ? 'true' : 'false');
            name.title = nameOfItem(it);
            lock.append(icon(isLocked ? 'lock' : 'unlock'));
            lock.setAttribute('aria-label', (isLocked ? '解锁 ' : '锁定 ') + it.key);
            lock.setAttribute('aria-pressed', isLocked ? 'true' : 'false');
            lock.title = isLocked ? '解除锁定（本次会话）' : '锁定位置与尺寸（本次会话，不写入模板）';
            eye.append(icon('eye'));
            eye.setAttribute('aria-label', (isHidden ? '显示 ' : '隐藏 ') + it.key);
            eye.setAttribute('aria-pressed', isHidden ? 'true' : 'false');
            eye.title = isHidden ? '在画布上显示' : '在画布上隐藏：仍在模板中，预览与输出照常包含';
            name.addEventListener('click', () => canvas.select([it.key]));
            lock.addEventListener('click', () => toggleLock([it.key]));
            eye.addEventListener('click', () => toggleHidden(it.key));
            row.append(icon(iconOf(it)), name, lock, eye);
            wrap.append(row);
          }
          box.append(wrap);
        }
        if (!groups.length) box.append(node('p', '先查询并打开模板。', 'resource-status'));
      });
    });
  }
  function renderFields() {
    const term = $('st-field-search').value.trim().toLowerCase(),
      cols = S.result ? S.result.columns : [],
      doc = S.config && S.config.group ? S.config.group.fields : [];
    once('fields', JSON.stringify([cols.map(c => c.id + c.label), doc, term, S.schema]), () => {
      const box = $('st-field-list');
      rebuild(box, () => {
        const row = (c, detail) => {
          const b = node('button', undefined, 'data-field'),
            t =
              c.type === 'decimal' || c.type === 'integer'
                ? '123'
                : c.type === 'date' || c.type === 'timestamp'
                  ? '日'
                  : 'T',
            label = node('span', c.label);
          b.type = 'button';
          b.setAttribute('data-k', 'field:' + (detail ? 'd' : 's') + ':' + c.id);
          label.append(node('small', c.id, 'field-code'));
          b.append(node('span', t, 'field-type'), label, node('span', '+', 'plus'));
          b.setAttribute('aria-label', '添加字段 ' + c.label + (detail ? '（明细级）' : '（单据级）'));
          b.title = '点击添加' + (detail ? '；选中明细表时添加为一列' : '') + '；也可拖到页面上';
          b.addEventListener('click', () => api.insertField(c.id, detail));
          dragSource(b, at => api.insertField(c.id, detail, at));
          return b;
        };
        const match = c => (c.label + c.id).toLowerCase().includes(term);
        box.append(node('div', '单据级字段', 'field-group-label'));
        const docFields = cols.filter(c => doc.includes(c.id) && match(c));
        if (docFields.length) for (const c of docFields) box.append(row(c, false));
        else
          box.append(
            node(
              'p',
              doc.length
                ? '没有匹配的字段。'
                : '未分组：整个快照一份文档。在“页面 › 区段设置”中选择分组字段后，分组字段列在这里。',
              'resource-status'
            )
          );
        box.append(node('div', '明细级字段', 'field-group-label'));
        for (const c of cols.filter(match)) box.append(row(c, true));
      });
    });
  }
  $('st-field-search').addEventListener('input', () => renderFields());
  function renderInspector() {
    const sel = canvas.selection(),
      items = api.elementsOf().flatMap(g => g.items.map(i => ({...i, band: g.band}))),
      picked = items.filter(i => sel.includes(i.key));
    $('st-insp-page').hidden = picked.length > 0;
    $('st-insp-element').hidden = picked.length === 0;
    if (!picked.length) {
      text('st-insp-title', '页面属性');
      text('st-insp-sub', '纸张、区段与打印设置');
    } else if (picked.length > 1) {
      text('st-insp-title', '已选择 ' + picked.length + ' 个组件');
      text('st-insp-sub', '属性一次编辑一个元素；可对齐、等距、复制或锁定');
    } else {
      const it = picked[0];
      text('st-insp-title', it.type === 'table' ? '明细表' : nameOfItem(it).replace(/^[a-z]\d+ · /, ''));
      text(
        'st-insp-sub',
        (api.bandLabels[it.band] || (it.band === 'table' ? '明细表' : '文本框')) + ' · ' + (types[it.type] || '文本')
      );
    }
    $('st-insp-icon')
      .querySelector('use')
      .setAttribute('href', '#i-' + (picked.length === 1 ? iconOf(picked[0]) : 'page'));
    const ds = S.query ? S.query.dataset.id + '@' + S.query.dataset.version : '数据集待绑定';
    text('st-page-dataset', ds);
    text('st-page-name', S.name);
    text('st-page-meta', 'A4 · 自动分页 · 重复表头');
    const info = $('st-page-info'),
      line = [
        S.name,
        ds,
        S.pages + ' 页（最近一次预览） / ' + (S.result ? S.result.rows.length : 0) + ' 条当前示例',
      ].join('\n');
    if (info.getAttribute('data-text') !== line) {
      info.setAttribute('data-text', line);
      info.replaceChildren();
      line.split('\n').forEach((t, i) => {
        if (i) info.append(document.createElement('br'));
        info.append(document.createTextNode(t));
      });
    }
    once('rules', JSON.stringify([S.schema, S.config && S.config.group]), () => {
      const list = $('st-rules');
      list.replaceChildren();
      const add = t => list.append(node('li', t));
      add('安全区 x 10–200 · y 25–270 毫米，元素不得越界；10–18 毫米是横幅（出具印章）保留区。');
      if (S.schema && S.schema.startsWith('2')) {
        add('每个模板至多 128 个元素；每带至多 96 个；元素编号 e1–e128。');
        add('同一带内的文本框、条码、二维码与图片不能重叠。');
        add('固定带高度 5–245 毫米（0.1 毫米步长）；明细带必有一个明细表，高度随页面伸展。');
        add(
          S.config && S.config.group
            ? '分组字段：' +
                S.config.group.fields.join('、') +
                '（每个不同取值一份文档；字段文本须是分组字段或组内取值一致）。'
            : '无分组：整个快照一份文档；字段文本须在全部行取值相同。'
        );
        add('分组合计需要当前结果中的指标字段；条码与二维码的值须在其字符集内。');
      } else if (S.schema) {
        add('至多 24 个文本框，文本框之间不能重叠；变量层至少需要一个行字段、合计或明细表。');
        add('套打偏移限 ±10 毫米，步长 0.1 毫米。');
      } else add('先查询并打开模板。');
    });
    text('st-refusal', S.refusal ? window.reportingRefusal.text(S.refusal, S.labelMap) : '尚无。');
    $('st-mark-refusal').disabled = !S.refusal;
    const issues = S.issues;
    once('issues', JSON.stringify(issues.map(i => [i.title, i.detail])), () => {
      const box = $('st-issues');
      rebuild(box, () => {
        if (!issues.length) {
          const pass = node('div', undefined, 'validation-pass'),
            ico = node('span', undefined, 'pass-icon');
          ico.append(icon('check'));
          pass.append(
            ico,
            node('strong', '当前版式与绑定检查通过'),
            node('p', '已检查字段、纸张范围、分带与明细规则。边界样本见下。', 'tiny')
          );
          box.append(pass);
        }
        issues.forEach((i, n) => {
          const card = node('button', undefined, 'validation-card error');
          card.type = 'button';
          card.setAttribute('data-k', 'issue:' + n);
          card.append(node('strong', i.title), node('p', i.detail || ''));
          if (i.mark) card.addEventListener('click', () => canvas.mark(i.mark));
          box.append(card);
        });
      });
    });
    $('st-issue-count').hidden = !issues.length;
    text('st-issue-count', String(issues.length));
  }
  function renderChrome() {
    text('st-name', S.name);
    text('st-state', S.badge);
    $('st-state').title = S.pointer ? '发布指针：v' + S.pointer : '';
    text('autosaveStatus', S.slot);
    text('datasetName', S.name);
    text('recordBadge', (S.result ? S.result.rows.length : 0) + ' 条示例');
    text('pageCount', '1 / ' + S.pages);
    const bad = S.issues.length;
    text('st-validation-text', bad ? '版式检查未通过 · ' + bad + ' 项' : '版式检查通过');
    $('st-validation').classList.toggle('bad', bad > 0);
    text(
      'st-source',
      S.snapshot
        ? '冻结快照 ' + String(S.snapshot).slice(0, 8) + (S.query ? ' · ' + S.query.dataset.id : '')
        : S.query
          ? '查询预览（未冻结） · ' + S.query.dataset.id
          : '尚无冻结快照'
    );
    $('st-start').hidden = S.ready;
    $('doc-canvas-host').classList.toggle('banded', !!S.schema && S.schema.startsWith('2'));
    const ds = S.query ? S.query.dataset.id + '@' + S.query.dataset.version : '尚无数据集';
    text('st-comp-dataset', S.query ? S.query.dataset.id : '尚无数据集');
    text(
      'st-comp-rows',
      S.result
        ? S.result.rows.length + ' 条示例 · ' + S.result.columns.length + ' 个字段 · ' + ds
        : '先查询并冻结当前授权数据。'
    );
    text('st-data-name', S.query ? S.query.dataset.id : '未绑定数据集');
    text('st-data-id', ds);
    text(
      'st-data-count',
      (S.result ? S.result.rows.length : 0) + ' 条 / ' + (S.result ? S.result.columns.length : 0) + ' 字段'
    );
  }
  function renderVersions() {
    const n = S.saved ? Math.max(S.latest, S.saved.version) : 0,
      pub = S.pointer;
    once('versions', JSON.stringify([n, pub, versionPick, S.saved && S.saved.version, S.dirty]), () => {
      const list = $('st-version-list');
      rebuild(list, () => {
        for (let v = n; v >= 1; v--) {
          const b = node('button', undefined, 'version-card' + (v === versionPick ? ' active' : '')),
            top = node('div', undefined, 'between');
          b.type = 'button';
          b.setAttribute('data-k', 'ver:' + v);
          top.append(
            node('strong', 'v' + v),
            node('span', v === pub ? '已发布' : '已保存', 'pill ' + (v === pub ? 'green' : 'blue'))
          );
          b.append(
            top,
            node(
              'p',
              S.saved && v === S.saved.version ? '编辑器打开的版本' + (S.dirty ? '（有未保存修改）' : '') : '保存版本'
            )
          );
          b.setAttribute('aria-current', v === versionPick ? 'true' : 'false');
          b.addEventListener('click', () => {
            versionPick = v;
            render();
            api.fetchVersion(v, () => render());
          });
          list.append(b);
        }
      });
    });
    const has = !!versionPick && !!S.saved;
    $('st-version-empty').hidden = has;
    $('st-version-card').hidden = !has;
    if (has) {
      text('st-vd-title', S.name + ' · v' + versionPick);
      text('st-vd-note', '恢复该版本只改变当前草稿；发布指针仍指向 ' + (pub ? 'v' + pub : '未发布') + '。');
      const cached = api.cachedVersion(versionPick),
        cur =
          S.saved.version === versionPick && S.config
            ? {
                count: S.count,
                dataset: S.query ? S.query.dataset.id + '@' + S.query.dataset.version : '—',
                schema: 'schema ' + S.config.schema_version,
              }
            : null,
        info = cached || cur;
      text('st-vd-elements', info ? info.count + ' 个组件' : '读取版本内容…');
      text('st-vd-dataset', info ? info.dataset : '—');
      text('st-vd-schema', info ? info.schema : '—');
      $('st-ver-preview').disabled = !S.previews;
      $('st-ver-preview').title = S.previews ? '' : '服务端未启用 PDF 渲染';
      $('doc-release-withdraw').disabled = !pub;
    }
    text('st-version-note', S.saved ? '保存版本不可变；当前发布指针：' + (pub ? 'v' + pub : '无') : '尚未保存任何版本');
  }
  function setCheck(id, bad, title, detail) {
    const el = $(id),
      d = node('div', title);
    d.append(node('p', detail));
    el.classList.toggle('bad', bad);
    el.replaceChildren(icon(bad ? 'warning' : 'check'), d);
  }
  function renderPublish() {
    if (openId !== 'publish') return;
    const n = publishPick || (S.saved ? S.saved.version : 0),
      cur = !!S.saved && n === S.saved.version,
      stale = !S.saved || (cur && S.dirty),
      bad = S.issues.length;
    text('st-pub-version', n ? 'v' + n : '草稿');
    text('st-pub-name', S.name);
    text(
      'st-pub-pointer',
      (n && S.pointer && n < S.pointer ? '历史版本回退 · ' : '') +
        '当前发布 ' +
        (S.pointer ? 'v' + S.pointer : S.release ? '无' : '（发布状态未读取）') +
        ' → ' +
        (n ? 'v' + n : '待保存')
    );
    setCheck(
      'st-pub-saved',
      stale,
      stale ? '需要先保存当前修改' : '已明确引用不可变保存版本',
      stale
        ? S.saved
          ? '画布存在未保存的修改，自动草稿不等于保存版本。'
          : '先保存第一个版本。'
        : '本次发布固定引用 v' + n + '。'
    );
    setCheck(
      'st-pub-valid',
      bad > 0,
      bad ? bad + ' 项检查未通过' : '版式与数据绑定检查通过',
      bad ? S.issues[0].detail || S.issues[0].title : '设计器的校验与最近一次边界样本检查均无问题；PDF 预览另行确认。'
    );
    text('st-pub-dataset', S.query ? S.query.dataset.id + '@' + S.query.dataset.version : '—');
    text('st-pub-elements', cur ? S.count + ' 个组件' : '该版本的组件数见“版本”对话框');
    text('st-pub-schema', S.config ? 'schema ' + S.config.schema_version : '—');
    const combined = stale && !!api.save && (!S.saved || cur),
      blocked = combined ? false : stale || !S.release;
    $('st-pub-confirm').disabled = blocked;
    $('st-pub-confirm').lastChild.textContent = combined ? '保存并发布' : '确认发布';
    text(
      'st-pub-footnote',
      blocked ? (stale ? '先保存再发布' : '正在读取发布状态…') : combined ? '先保存为新版本，再发布它' : ''
    );
  }
  function renderChooser() {
    once(
      'chooser',
      JSON.stringify([S.chooser, S.query && S.query.dataset, S.saved && S.saved.id, S.serverList.length]),
      () => {
        const body = $('st-chooser-body');
        rebuild(body, () => {
          for (const r of S.chooser) {
            const tr = node('tr'),
              same =
                r.binding &&
                S.query &&
                r.binding.integration_id === S.query.integration_id &&
                r.binding.dataset.id === S.query.dataset.id &&
                r.binding.dataset.version === S.query.dataset.version;
            const name = node('td', r.name || '模板 ' + r.id.slice(0, 8));
            if (S.saved && S.saved.id === r.id) name.append(node('span', ' 当前', 'pill blue'));
            const act = node('td'),
              a = node('button', '打开', 'btn small'),
              b = node('button', '打开已发布版本', 'btn small'),
              label = r.name || r.id.slice(0, 8);
            a.type = b.type = 'button';
            a.setAttribute('data-k', 'open:' + r.id);
            b.setAttribute('data-k', 'pub:' + r.id);
            a.setAttribute('aria-label', '打开 ' + label + ' 的最新版本');
            b.setAttribute('aria-label', '打开 ' + label + ' 的已发布版本');
            const reason = !r.known ? '正在读取' : !same ? '绑定的数据集与当前冻结快照不同' : '';
            if (reason) {
              for (const x of [a, b]) {
                x.setAttribute('aria-disabled', 'true');
                x.title = reason;
              }
            } else if (!r.published) {
              b.setAttribute('aria-disabled', 'true');
              b.title = '尚无已发布版本';
            }
            a.addEventListener('click', () => {
              if (a.getAttribute('aria-disabled') === 'true') {
                say(a.title);
                return;
              }
              $('doc-server-listing').value = r.id;
              $('doc-server-version').value = String(r.versions);
              $('doc-server-open').click();
              close('chooser');
            });
            b.addEventListener('click', () => {
              if (b.getAttribute('aria-disabled') === 'true') {
                say(b.title);
                return;
              }
              api.openPublishedOf(r.id);
              close('chooser');
            });
            act.append(a, b);
            const id = node('td', r.id.slice(0, 8));
            id.title = r.id;
            tr.append(
              name,
              id,
              node('td', String(r.versions)),
              node('td', r.published ? 'v' + r.published : '—'),
              node('td', r.binding ? r.binding.dataset.id + '@' + r.binding.dataset.version : '…'),
              act
            );
            body.append(tr);
          }
        });
        text(
          'st-chooser-status',
          S.chooser.length
            ? '当前身份有 ' + S.chooser.length + ' 个服务器模板。'
            : S.serverList.length
              ? '正在读取模板信息…'
              : '尚无服务器模板，或尚未读取。'
        );
      }
    );
  }
  function render() {
    renderChrome();
    renderControls();
    renderLayers();
    renderFields();
    renderInspector();
    renderVersions();
    renderPublish();
    renderChooser();
    text('st-pv-name', S.name);
    text(
      'st-pv-source',
      S.snapshot ? String(S.snapshot).slice(0, 8) + (S.query ? ' · ' + S.query.dataset.id : '') : '尚无冻结快照'
    );
    text('st-pv-pages', S.previewed ? S.pages + ' 页' : '尚未预览');
  }
  // The status lines are toasts: shown for a few seconds, and a polite live region all the time.
  for (const id of ['doc-status', 'doc-canvas-status']) {
    const el = $(id);
    let timer = 0;
    new MutationObserver(() => {
      if (!el.textContent) return;
      el.classList.add('show');
      clearTimeout(timer);
      timer = setTimeout(() => el.classList.remove('show'), 6500);
    }).observe(el, {childList: true, characterData: true, subtree: true});
  }
  if (window.matchMedia) {
    const m = window.matchMedia('(max-width:950px)');
    if (m.addEventListener)
      m.addEventListener('change', () => {
        $('sidebar').classList.remove('open');
        $('inspector').classList.remove('open');
        syncToggles();
      });
  }
  return {
    // Told after every change of the template, the saved state, the checks, the selection and the preview count.
    update() {
      S = api.state();
      S.labelMap = S.ready ? api.labels() : {};
      render();
      if (layout.fit) fitZoom();
      ruler();
    },
    // Told after every draw of the canvas (not during a drag).
    drawn() {
      syncZoom();
      ruler();
      if (layout.fit) fitZoom();
      syncToggles();
    },
    reset() {
      for (const id of Object.keys(dialogs)) close(id, true);
      for (const slot of [$('st-query-slot'), $('st-rows-slot')]) unpark(slot);
      const i = $('st-name-input');
      if (!i.hidden) {
        i.hidden = true;
        $('titleButton').hidden = false;
      }
      for (const k of Object.keys(last)) delete last[k];
      layout.fit = true;
      sets.grid = false;
      sets.snap = true;
      sets.reading = false;
      sets.guides = true;
      canvas.setGrid(false);
      canvas.setSnap(true);
      $('sidebar').classList.remove('open');
      $('inspector').classList.remove('open');
      S = api.state();
      S.labelMap = {};
      render();
    },
    open,
    close,
  };
};
