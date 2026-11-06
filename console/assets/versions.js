'use strict';
// The versions and publish flow (S09-E, ADR 0014 decision 9; the dialogs of ADR 0011): the version dialog and the 发布前确认 of one
// designer, which the report layout designer (app.js), the document studio (studio.js) and the dashboard studio (board.js) each had as a
// copy of their own. The component owns the picked version, the list of cards, the detail card, the notes, the summary and the decision
// of what a publish does: 保存并发布 when the editor holds unsaved changes or nothing is saved yet (S07-B), a rollback when a historical
// version is picked (S07-G). The host supplies, as ports, what differs between the three: its dialogs, its state, its requests in its own
// sequence, its guard and its words. It writes text and DOM nodes only, loads nothing and stores nothing.
//
// ports.prefix: the ids' prefix (ly, bd, st): <prefix>-version-list, -version-empty, -version-card, -vd-title, -vd-note, -ver-restore,
//   -ver-publish, -version-note, -publish-summary, -publish-note, -publish-confirm; ports.ids names the ones that differ, the host's
//   撤回发布 button (withdraw) and the bar's 版本 and 发布 buttons (versionsButton, publishButton) when the component is to bind them.
// ports.dialogs: {versions, publish}, each {open(opener), close(), isOpen()} of the host's own dialog mechanism.
// ports.state(): {saved: {id, version} | null, latest, dirty, release: {revision, published_version} | null, pointer?, name?, dataset, count}.
// ports.texts: thing (布局 / 看板 / 模板), opened (打开已发布版本 / 打开已发布看板), content (列 / 内容), dirty (what has unsaved changes),
//   count ([label, unit] of the summary's count row); editor ({clean, dirty} notes of the editor's version, null when the host does not
//   single it out) and unread (the note when the release state was not read, '' when the host's pointer is always an answer).
// ports.run(fn): the host's guard around opening a dialog (a refusal says what is missing). Optional.
// ports.readVersions(then), ports.readRelease(then): the list and the release state, the release state alone; then() when the host is done.
// ports.restore(version), ports.publish({historical, saveFirst}): the host's requests, in its own sequence.
// ports.readsWhileOpen: the confirmation opens at once and the release state is read while it is open (the document studio); otherwise
//   it is read first and the dialog opens once it is known.
// ports.card(button, version, picked), ports.picked(version), ports.detail(version), ports.renderPublish(plan, state): a host's own
//   additions to a card, to a pick, to the detail card and to the confirmation. Optional.
window.createReportingVersions = function (ports) {
  const $ = id => document.getElementById(id);
  const node = (tag, text, className) => {
    const n = document.createElement(tag);
    if (text !== undefined) n.textContent = text;
    if (className) n.className = className;
    return n;
  };
  const prefix = ports.prefix;
  const ids = Object.assign(
    {
      list: prefix + '-version-list',
      empty: prefix + '-version-empty',
      card: prefix + '-version-card',
      title: prefix + '-vd-title',
      note: prefix + '-vd-note',
      restore: prefix + '-ver-restore',
      publish: prefix + '-ver-publish',
      versionNote: prefix + '-version-note',
      summary: prefix + '-publish-summary',
      publishNote: prefix + '-publish-note',
      confirm: prefix + '-publish-confirm',
    },
    ports.ids
  );
  const texts = Object.assign(
    {
      editor: {clean: '编辑器打开的版本。', dirty: '编辑器打开的版本，有未保存的修改；发布它会先保存为下一个版本。'},
      unread: '（发布状态未读取）',
    },
    ports.texts
  );
  const dialogs = ports.dialogs,
    run = ports.run || (fn => fn()),
    opener = id => (id ? $(id) : undefined);
  // The confirm button's label is its last text node (the document studio's button carries an icon before it).
  const label = b => (b.lastChild && b.lastChild.nodeType === Node.TEXT_NODE ? b.lastChild : b);
  let pick = 0,
    pickFor = '',
    plan = null;
  // The host's state, read afresh for every render: the version the editor holds, the latest saved one, whether the editor differs from
  // it, the release state once it was read, and the published version as the host knows it.
  function read() {
    const s = ports.state(),
      saved = s.saved || null,
      release = s.release || null;
    return {
      s,
      saved,
      release,
      latest: saved ? Math.max(s.latest || 0, saved.version) : 0,
      pub: s.pointer !== undefined ? s.pointer : release ? release.published_version : 0,
    };
  }
  function focusPick() {
    const c = $(ids.list).querySelector('[aria-current="true"]');
    if (c) c.focus();
  }
  function renderVersions() {
    const {s, saved, release, latest, pub} = read();
    // When the editor's version changes (saved, opened, another one), the pick follows it; otherwise it stays where the person put it.
    const editor = saved ? saved.id + ':' + saved.version : '';
    if (editor !== pickFor || !pick || pick > latest) {
      pick = saved ? saved.version : 0;
      pickFor = editor;
    }
    const list = $(ids.list),
      focused = list.contains(document.activeElement);
    list.replaceChildren();
    for (let v = latest; v >= 1; v--) {
      const b = node('button', undefined, 'version-card');
      b.type = 'button';
      b.setAttribute('aria-current', String(v === pick));
      if (ports.card) ports.card(b, v, v === pick);
      const top = node('div', undefined, 'between');
      top.append(
        node('strong', 'v' + v),
        node('span', v === pub ? '已发布' : '已保存', 'pill ' + (v === pub ? 'green' : 'blue'))
      );
      b.append(
        top,
        node('p', v === saved.version ? '编辑器打开的版本' + (s.dirty ? '（有未保存修改）' : '') : '保存版本')
      );
      b.addEventListener('click', () => {
        pick = v;
        renderVersions();
        if (ports.picked) ports.picked(v);
      });
      list.append(b);
    }
    if (focused) focusPick();
    const has = !!saved && pick >= 1;
    $(ids.empty).hidden = has;
    $(ids.card).hidden = !has;
    if (has) {
      const own = !!texts.editor && pick === saved.version;
      $(ids.title).textContent = (s.name !== undefined ? s.name : texts.thing) + ' · v' + pick;
      $(ids.note).textContent = own
        ? s.dirty
          ? texts.editor.dirty
          : texts.editor.clean
        : '恢复该版本只改变当前草稿；发布指针仍指向 ' + (pub ? 'v' + pub : '未发布') + '。';
      $(ids.restore).disabled = own && !s.dirty;
      if (ids.withdraw) $(ids.withdraw).disabled = !pub;
      if (ports.detail) ports.detail(pick);
    }
    $(ids.versionNote).textContent = saved
      ? '保存版本不可变；当前发布指针：' + (release || !texts.unread ? (pub ? 'v' + pub : '无') : texts.unread)
      : '尚未保存任何版本';
  }
  function renderPublish() {
    if (!plan) return;
    const {s, saved, release} = read(),
      {historical, saveFirst} = plan,
      next = saved ? saved.version + 1 : 1,
      target = historical || (saved ? saved.version : 0),
      // A saved version is published against the release state's revision: until it is known the confirmation cannot confirm.
      blocked = !saveFirst && !release;
    const summary = $(ids.summary);
    if (summary) {
      summary.replaceChildren();
      const rows = [
        [texts.thing, saved ? saved.id : '新' + texts.thing],
        [
          '将发布的版本',
          saveFirst ? '先保存为版本 ' + next + '，再发布它' : '保存版本 ' + target + (historical ? '（历史版本）' : ''),
        ],
        ['数据集', s.dataset],
      ];
      if (!historical) rows.push([texts.count[0], s.count + ' ' + texts.count[1]]);
      rows.push(
        [
          '当前发布',
          release ? (release.published_version ? '版本 ' + release.published_version : '未发布') : '保存后读取',
        ],
        ['发布修订', release ? String(release.revision) : '保存后读取']
      );
      for (const [k, v] of rows) summary.append(node('dt', k), node('dd', v));
      $(ids.publishNote).textContent = saveFirst
        ? saved
          ? texts.dirty + '：先保存为新的不可变版本，再把发布指针移到它。'
          : '这个' + texts.thing + '还没有保存版本：先保存为版本 1，再发布它。'
        : historical
          ? '发布历史版本是一次明确的回退：' +
            texts.opened +
            '得到 v' +
            historical +
            '；编辑器里的' +
            texts.content +
            '不变。'
          : release && release.published_version === saved.version
            ? '这个保存版本已经是当前发布版本；再次发布会记一次新的发布修订。'
            : '发布后，' + texts.opened + '得到的就是这个版本；之后的保存不改变发布指针。';
    }
    if (ports.renderPublish) ports.renderPublish({historical, saveFirst, target, next, blocked}, s);
    const confirm = $(ids.confirm);
    confirm.disabled = blocked;
    label(confirm).textContent = saveFirst ? '保存并发布' : '确认发布';
  }
  // Told whenever the host's state may have changed: an open dialog of the component is drawn again from it.
  function sync() {
    if (dialogs.versions.isOpen()) renderVersions();
    if (dialogs.publish.isOpen()) renderPublish();
  }
  function openVersions() {
    run(() => {
      const {saved} = read();
      pick = 0;
      renderVersions();
      dialogs.versions.open(opener(ids.versionsButton));
      focusPick();
      // Opening reads the list (the latest version) and the release state; the dialog's 刷新发布状态 stays as the retry after a conflict.
      if (saved) ports.readVersions(sync);
    });
  }
  // version: a historical version from the version dialog, published as it is (a rollback): nothing is saved and the editor does not
  // change. Without it the editor's version; unsaved changes, or no saved version yet, are saved as the next version first (保存并发布).
  function openPublish(version) {
    run(() => {
      const {s, saved} = read();
      const historical = saved && version && version !== saved.version ? version : 0;
      plan = {historical, saveFirst: !historical && (!saved || s.dirty)};
      const show = () => {
        renderPublish();
        dialogs.publish.open(opener(ids.publishButton));
        $(ids.confirm).focus();
      };
      if (plan.saveFirst) show();
      else if (ports.readsWhileOpen) {
        show();
        if (!read().release) ports.readRelease(sync);
      } else
        ports.readRelease(() => {
          const now = read();
          if (now.saved && now.release) show();
        });
    });
  }
  if (ids.versionsButton) $(ids.versionsButton).addEventListener('click', () => openVersions());
  if (ids.publishButton) $(ids.publishButton).addEventListener('click', () => openPublish());
  $(ids.restore).addEventListener('click', () => {
    if (!pick) return;
    const v = pick;
    dialogs.versions.close();
    ports.restore(v);
  });
  $(ids.publish).addEventListener('click', () => {
    if (!pick) return;
    const v = pick;
    dialogs.versions.close();
    openPublish(v);
  });
  $(ids.confirm).addEventListener('click', () => {
    if ($(ids.confirm).disabled || !plan) return;
    const p = plan;
    dialogs.publish.close();
    ports.publish(p);
  });
  return {
    openVersions,
    openPublish,
    sync,
    picked: () => pick,
    clear() {
      pick = 0;
      pickFor = '';
      plan = null;
    },
  };
};
