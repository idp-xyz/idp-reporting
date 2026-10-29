'use strict';
// How a product page shows an IDP Reporting view (ADR 0006, docs/EMBEDDING.md): an iframe, a token handed over by postMessage when the frame asks,
// and a listener for its size and events. The marked lines are the whole integration; the rest is this demo's page.
(function () {
  const params = new URLSearchParams(location.search);
  const platform = new URL(params.get('platform')).origin;       // the platform's origin, from the product's configuration
  const view = params.get('view') || 'dashboard';                  // report | dashboard | document-designer
  const frame = document.getElementById('frame');
  const heard = [];                                                // what the frame said, for the browser test
  window.heard = heard;
  let waiting = false;

  // --- the integration ---------------------------------------------------------------------------------------------------------------
  const address = new URL('/embed/' + view, platform);
  address.searchParams.set('integration', params.get('integration'));
  address.searchParams.set('parent', location.origin);             // the product's own origin, exactly as the platform lists it
  for (const key of ['dataset', 'dashboard', 'template', 'version']) if (params.has(key)) address.searchParams.set(key, params.get(key));
  frame.src = address.href;
  const send = message => frame.contentWindow.postMessage(message, platform);    // never '*': only the platform's origin may read it
  const token = () => document.getElementById('token').value;      // a real product: its backend's answer, for the signed-in user
  function answer() { if (token()) { send({type: 'reporting:token', v: 1, token: token()}); waiting = false; } else waiting = true; }
  window.addEventListener('message', event => {
    if (event.source !== frame.contentWindow || event.origin !== platform) return;   // this frame, this origin, nothing else
    const m = event.data;
    if (!m || typeof m !== 'object' || m.v !== 1) return;
    if (m.type === 'reporting:ready' || m.type === 'reporting:token-request') answer();
    else if (m.type === 'reporting:resize') frame.style.height = m.height + 'px';
    else if (m.type === 'reporting:event' || m.type === 'reporting:error') note(m);
  });
  // --- end of the integration --------------------------------------------------------------------------------------------------------

  function note(m) {
    heard.push(m);
    const item = document.createElement('li');
    item.textContent = [m.type, m.name || m.code, m.template_id, m.version, m.job_id].filter(v => v !== undefined).join(' ');
    document.getElementById('log').append(item);
  }
  document.getElementById('token').addEventListener('change', () => { if (waiting) answer(); });
  for (const theme of ['dark', 'light']) document.getElementById('theme-' + theme).addEventListener('click', () => send({type: 'reporting:theme', v: 1, theme}));
})();
