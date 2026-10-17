/* Lucille Sight, background. Runs in Chromium/Chrome/Brave (service worker) and Firefox (event page).
   It never reads a page by itself: a tab is read only after the user presses the toolbar button / Alt+L,
   or after the user ran a search FROM Lucille's own panel (that one tab is then read once, when it loads). */
const X = globalThis.browser || globalThis.chrome;
const DEFAULT_SERVER = 'http://127.0.0.1:8403';
const autoTabs = new Set();

const get = keys => new Promise(res => X.storage.local.get(keys, res));
const set = obj => new Promise(res => X.storage.local.set(obj, res));

async function inject(tabId, msg) {
  try {
    await X.scripting.executeScript({ target: { tabId }, files: ['content.js'] });
    await X.tabs.sendMessage(tabId, msg);
  } catch (e) { /* a page the browser does not let extensions touch (about:, the web store, ...) */ }
}

X.action.onClicked.addListener(tab => { if (tab && tab.id != null) inject(tab.id, { type: 'toggle' }); });

X.tabs.onUpdated.addListener((tabId, info) => {
  if (info.status === 'complete' && autoTabs.has(tabId)) { autoTabs.delete(tabId); setTimeout(() => inject(tabId, { type: 'auto' }), 700); }
});

async function call(path, method, body) {
  const { server, token } = await get(['server', 'token']);
  const base = (server || DEFAULT_SERVER).replace(/\/+$/, '');
  try {
    const r = await fetch(base + path, {
      method: method || (body ? 'POST' : 'GET'),
      headers: Object.assign({ 'Content-Type': 'application/json' }, token ? { 'X-Swarm-Auth': token } : {}),
      body: body ? JSON.stringify(body) : undefined
    });
    let json = null; try { json = await r.json(); } catch (_) {}
    return { status: r.status, json };
  } catch (e) { return { status: 0, error: 'I cannot reach Lucille at ' + base + '. Is her service running?' }; }
}

X.runtime.onMessage.addListener((msg, sender, reply) => {
  (async () => {
    if (msg.type === 'cfg') { const c = await get(['server', 'token', 'label']); reply({ server: c.server || DEFAULT_SERVER, paired: !!c.token, label: c.label || '' }); }
    else if (msg.type === 'pair') {
      await set({ server: (msg.server || DEFAULT_SERVER).replace(/\/+$/, ''), token: '' });
      const r = await call('/api/pair', 'POST', { code: String(msg.code || '').trim(), label: msg.label || 'Browser' });
      if (r.json && r.json.ok && r.json.token) { await set({ token: r.json.token, label: msg.label || 'Browser' }); reply({ ok: true }); }
      else reply({ ok: false, error: (r.json && r.json.error) || r.error || 'That code did not work.' });
    }
    else if (msg.type === 'unpair') { await set({ token: '' }); reply({ ok: true }); }
    else if (msg.type === 'call') reply(await call(msg.path, msg.method, msg.body));
    else if (msg.type === 'search') {
      const q = encodeURIComponent(String(msg.q || '').slice(0, 200));
      const url = msg.engine === 'google' ? 'https://www.google.com/search?q=' + q : 'https://duckduckgo.com/?q=' + q;
      const tab = await X.tabs.create({ url });
      autoTabs.add(tab.id); reply({ ok: true });
    }
  })();
  return true;
});
