'use strict';
// The embed protocol (ADR 0006, docs/EMBEDDING.md): this page is shown in a frame by one product page, which gives it the user's access
// token and hears of its size and of a closed list of events. Everything about who may talk to whom is here and nowhere else:
//  - every message to the parent goes through post(), whose target origin is the exact parent origin the page was served for, never '*';
//  - a message is read only when its source is window.parent and its origin is that same origin, and only the closed list of types
//    of version 1 is read at all;
//  - what leaves the page is built from closed constructors: a type, v:1, a height, an event name with ids, an error code; nothing else
//    can be said, so no value, no token and no text can cross.
// The token is held by app.js, in memory only; this module hands it over and keeps no copy.
(function () {
  const views = {'/embed/report': {page: 'workspace', key: 'dataset'}, '/embed/dashboard': {page: 'dashboard', key: 'dashboard'}, '/embed/document-designer': {page: 'documents', key: 'template'}};
  const ID = /^[a-f0-9]{32}$/;
  const INTEGRATION = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
  const DATASET = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}(@[A-Za-z0-9][A-Za-z0-9._-]{0,31})?$/;
  const VERSION = /^([1-9]|1[0-9]|20)$/;
  // RFC 6750 b64token: what an access token may consist of; the access-token module accepts 32 KiB at most.
  const TOKEN = /^[A-Za-z0-9._~+/-]+=*$/;
  const eventNames = new Set(['saved', 'published', 'issued']);
  // Closed list of what the page may tell its parent about a failure: a class, never a message and never a server's text.
  const errorCodes = new Set(['UNAUTHENTICATED', 'FORBIDDEN', 'NOT_FOUND', 'RESOURCE_UNAVAILABLE', 'UNAVAILABLE', 'INVALID_RESPONSE', 'INTEGRITY_FAILURE', 'REQUEST_FAILED']);

  // The exact serialization of an https origin, or of a loopback http origin (the demo): what the server only ever serves a page for.
  function validOrigin(text) {
    if (typeof text !== 'string' || text.length < 1 || text.length > 255 || /[\s*,;'"\\%]/.test(text)) return false;
    let url;
    try { url = new URL(text); } catch (_) { return false; }
    if (url.origin !== text) return false;
    if (url.protocol === 'https:') return true;
    return url.protocol === 'http:' && /^(127(\.[0-9]{1,3}){3}|\[::1\])$/.test(url.hostname);
  }

  // The request of the embed page: which view, for which integration, framed by which origin, about which resource. Null when the
  // address is not exactly one the server serves (the server checked it too: this is the page checking what it will act on).
  window.reportingEmbedRequest = function (pathname, search) {
    const view = views[pathname];
    if (!view || typeof search !== 'string') return null;
    const params = new URLSearchParams(search);
    const keys = [...params.keys()];
    if (new Set(keys).size !== keys.length) return null;
    const take = key => params.get(key);
    const allowed = ['integration', 'parent', view.key].concat(view.key === 'template' ? ['version'] : []);
    if (!keys.every(k => allowed.includes(k)) || !params.has('integration') || !params.has('parent') || !params.has(view.key)) return null;
    const integration = take('integration'), parent = take('parent'), id = take(view.key);
    if (!INTEGRATION.test(integration) || !validOrigin(parent)) return null;
    if (!(view.key === 'dataset' ? DATASET : ID).test(id)) return null;
    let version = 0;
    if (params.has('version')) { if (!VERSION.test(take('version'))) return null; version = Number(take('version')); }
    return {page: view.page, integration, parent, resource: {kind: view.key, id, version}};
  };

  // ports: parent (window.parent), origin (the exact parent origin), token(token) and theme(name) for what the parent sends, and,
  // for tests, window, timers {set, clear}, now and ResizeObserver.
  window.createReportingEmbed = function (ports) {
    const win = ports.window || window, parent = ports.parent, origin = ports.origin;
    if (!parent || parent === win || !validOrigin(origin)) throw new Error('NOT_FRAMED');
    const timers = ports.timers || {set: (fn, ms) => win.setTimeout(fn, ms), clear: id => win.clearTimeout(id)};
    const now = ports.now || (() => Date.now());
    let waiting = null, renewal = 0;

    // The only door out. The target is the parent origin the page was served for: if the parent is somewhere else, the browser drops
    // the message instead of delivering it.
    function post(fields) { parent.postMessage(Object.assign({type: '', v: 1}, fields), origin); }

    function onMessage(event) {
      if (event.source !== parent || event.origin !== origin) return;
      const m = event.data;
      if (!m || typeof m !== 'object' || Array.isArray(m) || m.v !== 1 || typeof m.type !== 'string') return;
      if (m.type === 'reporting:token') {
        const token = m.token;
        if (typeof token !== 'string' || token.length > 32768 || !TOKEN.test(token)) return;
        // While a fresh token is awaited it goes to the request that waits for it; the token that just failed is never "fresh".
        if (waiting) { if (token !== waiting.failed) { const w = waiting; waiting = null; timers.clear(w.timer); w.resolve(token); } return; }
        ports.token(token);
      } else if (m.type === 'reporting:theme') {
        if (m.theme === 'light' || m.theme === 'dark') ports.theme(m.theme);
      }
    }
    win.addEventListener('message', onMessage);

    // The expiry a JWT states about itself, in milliseconds, or 0: read only to decide when to ask again; nothing is verified or trusted here.
    function expiryOf(token) {
      const parts = token.split('.');
      if (parts.length !== 3) return 0;
      try {
        const claims = JSON.parse(win.atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')));
        return claims && Number.isSafeInteger(claims.exp) && claims.exp > 0 && claims.exp < 1e11 ? claims.exp * 1000 : 0;
      } catch (_) { return 0; }
    }

    const api = {
      ready() { post({type: 'reporting:ready'}); },
      tokenRequest() { post({type: 'reporting:token-request'}); },
      error(code) { post({type: 'reporting:error', code: errorCodes.has(code) ? code : 'REQUEST_FAILED'}); },
      resize(height) { if (Number.isSafeInteger(height) && height >= 0 && height <= 100000) post({type: 'reporting:resize', height}); },
      // saved and published say which template and version; issued says which job. Ids and numbers only, or nothing is sent.
      event(name, fields) {
        if (!eventNames.has(name) || !fields || typeof fields !== 'object') return false;
        if (name === 'issued') { if (!ID.test(fields.job_id)) return false; post({type: 'reporting:event', name, job_id: fields.job_id}); return true; }
        if (!ID.test(fields.template_id) || !Number.isSafeInteger(fields.version) || fields.version < 1 || fields.version > 20) return false;
        post({type: 'reporting:event', name, template_id: fields.template_id, version: fields.version});
        return true;
      },
      // Asks the parent for a token other than the one that failed and waits for it: the token, or '' when none comes in time.
      refresh(failed, limit) {
        if (waiting) return waiting.promise;
        const waiter = {failed};
        waiter.promise = new Promise(resolve => { waiter.resolve = resolve; });
        waiter.timer = timers.set(() => { if (waiting === waiter) { waiting = null; waiter.resolve(''); } }, limit || ports.refreshTimeout || 10000);
        waiting = waiter;
        api.tokenRequest();
        return waiter.promise;
      },
      // Called with the token that is now in use: asks for the next one a minute before it says it expires (at most every 15 seconds).
      expect(token) {
        timers.clear(renewal); renewal = 0;
        const expiry = expiryOf(token);
        if (!expiry) return;
        const tick = () => {
          const left = expiry - 60000 - now();
          if (left > 15000) { renewal = timers.set(tick, Math.min(left, 2147483647)); return; }
          renewal = 0;
          api.tokenRequest();
        };
        renewal = timers.set(tick, Math.max(15000, Math.min(expiry - 60000 - now(), 2147483647)));
      },
      forget() { timers.clear(renewal); renewal = 0; },
      // Tells the parent the height of the element, in whole pixels, whenever it changes (coalesced to one message every 50 ms).
      watch(element) {
        let last = -1, timer = 0;
        const send = () => { timer = 0; const height = Math.ceil(element.getBoundingClientRect().height); if (height !== last) { last = height; api.resize(height); } };
        const schedule = () => { if (!timer) timer = timers.set(send, 50); };
        const Observer = ports.ResizeObserver || win.ResizeObserver;
        if (typeof Observer === 'function') new Observer(schedule).observe(element);
        schedule();
      },
      close() { win.removeEventListener('message', onMessage); api.forget(); }
    };
    return api;
  };
})();
