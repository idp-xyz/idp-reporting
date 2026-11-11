'use strict';
// The versions and publish flow (S09-E, ADR 0014 decision 9; the dialogs of ADR 0011; the words of ADR 0014 decision 11, S09-F): the 历史
// dialog and the 发布前确认 of one designer, which the report layout designer (app.js), the document studio (studio.js) and the dashboard
// studio (board.js) each had as a copy of their own. The component owns the picked version, the list of cards (each marked 已发布 and
// 当前打开 as it is), the detail card with 打开 (its content into the editor as the draft: saving after it makes the next version),
// 发布此版本 (that saved version as it is, a rollback when it is an older one) and 撤回发布 on the published one, the notes, the summary
// and the decision of what a publish does: 保存并发布 when the editor holds unsaved changes or nothing is saved yet (S07-B), the picked
// version as it is when one was picked (S07-G), 重试 when the server found the release state changed (S09-F: the dialog reads on open, so
// there is no refresh button). The host supplies, as ports, what differs between the three: its dialogs, its state, its requests in its
// own sequence, its guard and its words. It writes text and DOM nodes only, loads nothing and stores nothing.
//
// ports.prefix: the ids' prefix (ly, bd, st): <prefix>-version-list, -version-empty, -version-card, -vd-title, -vd-note, -ver-restore,
//   -ver-publish, -version-note, -publish-summary, -publish-note, -publish-confirm; ports.ids names the ones that differ, the host's
//   撤回发布 button (withdraw) and the bar's 历史 and 发布 buttons (versionsButton, publishButton) when the component is to bind them.
// ports.dialogs: {versions, publish}, each {open(opener), close(), isOpen()} of the host's own dialog mechanism.
// ports.state(): {saved: {id, version} | null, latest, dirty, release: {revision, published_version} | null, pointer?, name?, dataset, count}.
// ports.texts: thing (布局 / 看板 / 模板), content (列 / 内容), dirty (what has unsaved changes), count ([label, unit] of the summary's count
//   row); unread (the footer's text while the release state is being read, '' when the host's pointer is always an answer).
// ports.run(fn): the host's guard around opening a dialog (a refusal says what is missing). Optional.
// ports.readVersions(then), ports.readRelease(then): the list and the release state, the release state alone; then() when the host is done.
// ports.restore(version), ports.publish({version, historical, saveFirst, retry}): the host's requests, in its own sequence; version is the
//   picked version to publish as it is (0: the editor's, saved first when saveFirst).
// ports.readsWhileOpen: the confirmation opens at once and the release state is read while it is open (the document studio); otherwise
//   it is read first and the dialog opens once it is known.
// ports.card(button, version, picked), ports.picked(version), ports.detail(version), ports.renderPublish(plan, state): a host's own
//   additions to a card, to a pick, to the detail card and to the confirmation. Optional.
// The host tells conflict() when its publish was refused because the release state had changed: the confirmation comes back, reads the
// state again and offers 重试.
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
  const texts = Object.assign({unread: '读取中…'}, ports.texts);
  const dialogs = ports.dialogs,
    run = ports.run || (fn => fn()),
    opener = id => (id ? $(id) : undefined);
  // The confirm button's label is its last text node (the document studio's button carries an icon before it).
  const label = b => (b.lastChild && b.lastChild.nodeType === Node.TEXT_NODE ? b.lastChild : b);
  const revision = release => (release ? '修订 ' + release.revision : '');
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
      const top = node('div', undefined, 'between'),
        marks = node('span', undefined, 'marks');
      if (v === pub) marks.append(node('span', '已发布', 'pill green'));
      if (v === saved.version) marks.append(node('span', '当前打开', 'pill blue'));
      top.append(node('strong', 'v' + v), marks);
      b.append(top);
      if (v === saved.version && s.dirty) b.append(node('p', '有未保存的修改'));
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
      const own = pick === saved.version;
      $(ids.title).textContent = (s.name !== undefined ? s.name : texts.thing) + ' · v' + pick;
      $(ids.note).textContent = own
        ? s.dirty
          ? '当前打开的版本，有未保存的修改；发布它会先保存为下一个版本。'
          : '当前打开的版本。'
        : pick === pub
          ? '当前发布版本。打开后保存会成为下一个版本（v' + (latest + 1) + '）。'
          : '打开后保存会成为下一个版本（v' + (latest + 1) + '）；发布此版本会让它成为当前发布版本。';
      $(ids.restore).disabled = own && !s.dirty;
      $(ids.restore).title = own && !s.dirty ? '编辑器已经打开这个版本' : '打开后保存会成为下一个版本';
      if (ids.withdraw) $(ids.withdraw).hidden = !pub || pick !== pub;
      if (ports.detail) ports.detail(pick);
    }
    const note = $(ids.versionNote);
    note.textContent = saved
      ? '当前发布：' + (release || !texts.unread ? (pub ? 'v' + pub : '无') : texts.unread)
      : '尚未保存：保存后这里列出每个版本。';
    note.title = saved ? revision(release) : '';
  }
  function renderPublish() {
    if (!plan) return;
    const {s, saved, release, pub} = read(),
      {historical, saveFirst, retry} = plan,
      next = saved ? saved.version + 1 : 1,
      target = plan.version || (saved ? saved.version : 0),
      // A saved version is published against the release state's revision: until it is known the confirmation cannot confirm.
      blocked = !saveFirst && !release;
    const summary = $(ids.summary);
    if (summary) {
      summary.replaceChildren();
      const rows = [
        [texts.thing, saved ? saved.id : '新' + texts.thing],
        [
          '将发布的版本',
          saveFirst ? '先保存为版本 ' + next + '，再发布它' : '版本 ' + target + (historical ? '（历史版本）' : ''),
        ],
        ['数据集', s.dataset],
      ];
      // The editor's count stands for the version only while the editor holds it as it was saved.
      if (!historical && !(plan.version && s.dirty)) rows.push([texts.count[0], s.count + ' ' + texts.count[1]]);
      rows.push([
        '当前发布',
        release ? (release.published_version ? '版本 ' + release.published_version : '未发布') : '保存后读取',
      ]);
      for (const [k, v] of rows) {
        const dd = node('dd', v);
        if (k === '当前发布') dd.title = revision(release);
        summary.append(node('dt', k), dd);
      }
      $(ids.publishNote).textContent =
        (retry
          ? '发布状态已改变（另一处发布或撤回过）：' + (blocked ? '正在重新读取…' : '已重新读取，请核对后重试。')
          : '') +
        (saveFirst
          ? saved
            ? texts.dirty + '：先保存为版本 ' + next + '，再发布它。'
            : '这个' + texts.thing + '还没有保存过：先保存为版本 1，再发布它。'
          : historical
            ? '发布历史版本是一次回退：当前发布从 ' +
              (pub ? 'v' + pub : '无') +
              ' 变为 v' +
              historical +
              '；编辑器里的' +
              texts.content +
              '不变。'
            : plan.version && s.dirty
              ? '发布的是已保存的 v' + target + '；编辑器里未保存的修改不在其中。'
              : release && release.published_version === saved.version
                ? '这个版本已经是当前发布版本；再次发布不改变发布的内容。'
                : '发布后这个版本就是当前发布版本；之后的保存不改变它，直到再次发布。');
    }
    if (ports.renderPublish)
      ports.renderPublish({version: plan.version, historical, saveFirst, retry, target, next, blocked}, s);
    const confirm = $(ids.confirm);
    confirm.disabled = blocked;
    label(confirm).textContent = saveFirst ? '保存并发布' : retry ? '重试' : '确认发布';
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
      // Opening reads the list (the latest version) and the release state: there is nothing to refresh by hand.
      if (saved) ports.readVersions(sync);
    });
  }
  function showPublish() {
    renderPublish();
    dialogs.publish.open(opener(ids.publishButton));
    $(ids.confirm).focus();
  }
  // version: a version picked in 历史, published as it is (an older one is a rollback): nothing is saved and the editor does not change.
  // Without it the editor's version; unsaved changes, or no saved version yet, are saved as the next version first (保存并发布).
  function openPublish(version) {
    run(() => {
      const {s, saved} = read();
      const picked = saved && version ? version : 0;
      plan = {
        version: picked,
        historical: picked && picked !== saved.version ? picked : 0,
        saveFirst: !picked && (!saved || s.dirty),
        retry: false,
      };
      if (plan.saveFirst) showPublish();
      else if (ports.readsWhileOpen) {
        showPublish();
        if (!read().release) ports.readRelease(sync);
      } else
        ports.readRelease(() => {
          const now = read();
          if (now.saved && now.release) showPublish();
        });
    });
  }
  // The host's publish was refused because the release state had changed (another publish or withdrawal): the confirmation comes back
  // for the same version, blocked until the state is read again, and its button says 重试. A 保存并发布 has saved by then: the retry
  // publishes the saved version and saves nothing more.
  function conflict() {
    if (!plan) return;
    const {saved} = read();
    if (!saved) return;
    plan = {version: plan.version, historical: plan.historical, saveFirst: false, retry: true};
    showPublish();
    ports.readRelease(sync);
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
    conflict,
    sync,
    picked: () => pick,
    clear() {
      pick = 0;
      pickFor = '';
      plan = null;
    },
  };
};
