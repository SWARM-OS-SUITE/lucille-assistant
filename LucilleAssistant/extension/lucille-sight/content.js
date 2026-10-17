/* Lucille Sight, in-page panel. Injected only when the user presses the toolbar button / Alt+L (or after a search that
   was started from this panel). Builds the panel and the highlights with DOM methods only (page text is never put in as
   HTML). Works the same in Firefox and Chromium: no side-panel API, no site cooperation needed. */
(function () {
  'use strict';
  if (window.__lucilleSight) return;               // already here: the background just sends it a message
  const X = globalThis.browser || globalThis.chrome;
  const send = msg => new Promise(res => X.runtime.sendMessage(msg, r => res(r)));
  const S = { host: null, root: null, body: null, cfg: null, items: [], ranges: [], marks: [], overlay: null, busy: false, game: null };
  window.__lucilleSight = S;

  /* ── tiny DOM helper ─────────────────────────────────────────── */
  function h(tag, attrs, ...kids) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (k === 'class') el.className = v; else if (k.startsWith('on')) el.addEventListener(k.slice(2), v); else if (v !== false && v != null) el.setAttribute(k, v === true ? '' : v);
    }
    for (const kid of kids.flat()) if (kid != null && kid !== false) el.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
    return el;
  }

  const CSS = `
  :host{all:initial}
  .p{position:fixed;top:14px;right:14px;width:370px;max-width:calc(100vw - 28px);max-height:86vh;overflow:auto;box-sizing:border-box;z-index:2147483647;
    background:#0b100d;color:#eef1ee;border:1px solid #ff7a29;border-radius:14px;box-shadow:0 10px 40px rgba(0,0,0,.6);font:14px/1.45 system-ui,sans-serif;padding:12px 14px 14px}
  header{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:8px}
  header b{font:700 11px/1.2 ui-monospace,monospace;letter-spacing:.12em;text-transform:uppercase;color:#ffa35c}
  header div{display:flex;gap:6px;flex:none}
  button{font:600 12px/1 system-ui,sans-serif;border:1px solid rgba(255,255,255,.25);background:rgba(255,255,255,.07);color:inherit;border-radius:999px;padding:8px 12px;cursor:pointer}
  button.hot{background:linear-gradient(135deg,#ff7a29,#b8470c);border-color:#ff7a29}
  button.on{outline:2px solid #ffa35c}
  input,select{box-sizing:border-box;font:13px system-ui,sans-serif;background:rgba(255,255,255,.07);color:inherit;border:1px solid rgba(255,122,41,.4);border-radius:8px;padding:8px 10px}
  input.w{width:100%}
  .row{display:flex;gap:6px;align-items:center;margin-top:8px}.row input{flex:1;min-width:0}
  p{margin:6px 0}.sum{font-size:14px}.muted{opacity:.65;font-size:12px}
  h4{margin:12px 0 4px;font:700 10px/1 ui-monospace,monospace;letter-spacing:.14em;text-transform:uppercase;color:#ffa35c}
  .it{display:block;width:100%;text-align:left;border-radius:10px;margin:4px 0;padding:8px 10px;line-height:1.35;white-space:normal}
  .it small{display:block;opacity:.7;margin-top:2px;font-weight:400}
  .best{border-color:#39d98a;background:rgba(57,217,138,.1)}.good{border-color:#ffc04d;background:rgba(255,192,77,.08)}.skip{opacity:.55}
  .hl{border-color:#ffd54a;background:rgba(255,214,74,.1)}
  .card{border:1px solid rgba(255,255,255,.15);border-radius:10px;padding:8px 10px;margin-top:8px}
  .err{color:#ff8a7a}
  .think::after{content:'';display:inline-block;width:1.4em;animation:d 1.2s steps(4,end) infinite}@keyframes d{0%{content:''}25%{content:'.'}50%{content:'..'}75%{content:'...'}}`;

  function build() {
    S.host = h('div', { id: 'lucille-sight-host' });
    S.root = S.host.attachShadow({ mode: 'closed' });
    S.root.append(h('style', {}, CSS));
    S.body = h('div', { class: 'p' });
    S.root.append(S.body);
    document.documentElement.append(S.host);
  }
  function show() { if (!S.host) build(); S.host.style.display = ''; }
  function hide() { if (S.host) S.host.style.display = 'none'; }
  function toggle() { if (S.host && S.host.style.display !== 'none') { hide(); } else { show(); main(); } }

  /* ── the page highlights ─────────────────────────────────────── */
  const hlRule = () => { if (!document.getElementById('lucille-sight-style')) document.head.append(h('style', { id: 'lucille-sight-style' }, '::highlight(lucille-sight){background-color:#ffd54a;color:#111}')); };

  function clearMarks() {
    if (window.CSS && CSS.highlights) CSS.highlights.delete('lucille-sight');
    if (S.overlay) { S.overlay.remove(); S.overlay = null; }
    for (const m of S.marks) { m.el.style.outline = m.outline; m.el.style.outlineOffset = m.off; m.el.style.opacity = m.opacity; if (m.badge) m.badge.remove(); }
    S.marks = []; S.ranges = [];
  }

  function textIndex() {
    const block = el => { for (let i = 0; el && i < 8; i++, el = el.parentElement) { const d = getComputedStyle(el).display; if (d !== 'inline' && d !== 'inline-block' && d !== 'contents') return el; } return null; };
    const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, { acceptNode(n) {
      const p = n.parentElement; if (!p || p.closest('#lucille-sight-host,script,style,noscript,textarea')) return NodeFilter.FILTER_REJECT;
      return p.getClientRects().length ? NodeFilter.FILTER_ACCEPT : NodeFilter.FILTER_REJECT; } });
    let norm = '', map = [], lastBlk = null, n;
    while ((n = w.nextNode())) {
      const blk = block(n.parentElement);
      if (blk !== lastBlk && norm && norm[norm.length - 1] !== ' ') { norm += ' '; map.push({ node: n, off: 0 }); }
      lastBlk = blk;
      const v = n.nodeValue;
      for (let k = 0; k < v.length; k++) {
        if (/\s/.test(v[k])) { if (norm && norm[norm.length - 1] !== ' ') { norm += ' '; map.push({ node: n, off: k }); } }
        else { norm += v[k].toLowerCase(); map.push({ node: n, off: k }); }
      }
    }
    return { norm, map };
  }

  function findRange(idx, quote) {
    const q = quote.replace(/\s+/g, ' ').trim().toLowerCase(); if (!q) return null;
    const i = idx.norm.indexOf(q); if (i < 0) return null;
    const a = idx.map[i], b = idx.map[i + q.length - 1]; if (!a || !b) return null;
    try { const r = document.createRange(); r.setStart(a.node, a.off); r.setEnd(b.node, b.off + 1); return r; } catch (_) { return null; }
  }

  function drawRanges(ranges) {
    if (window.CSS && CSS.highlights && typeof Highlight === 'function') { hlRule(); CSS.highlights.set('lucille-sight', new Highlight(...ranges)); return; }
    S.overlay = h('div', { style: 'position:absolute;left:0;top:0;pointer-events:none;z-index:2147483646' });
    for (const r of ranges) for (const b of r.getClientRects())
      S.overlay.append(h('div', { style: `position:absolute;left:${b.left + scrollX}px;top:${b.top + scrollY}px;width:${b.width}px;height:${b.height}px;background:rgba(255,213,74,.45);border-radius:3px` }));
    document.body.append(S.overlay);
  }

  function scrollTo(el) { try { el.scrollIntoView({ block: 'center', behavior: 'smooth' }); } catch (_) {} }

  function applyHighlights(list) {
    const idx = textIndex(), found = [];
    list.forEach((hl, i) => { const r = findRange(idx, hl.quote); if (r) { hl.range = r; found.push(r); } });
    S.ranges = found; if (found.length) { drawRanges(found); scrollTo(found[0].startContainer.parentElement); }
  }

  function applyPicks(picks) {
    const colors = { best: '#39d98a', good: '#ffc04d' };
    for (const p of picks) {
      const it = S.items[p.i]; if (!it || !it.el) continue;
      const el = it.el, m = { el, outline: el.style.outline, off: el.style.outlineOffset, opacity: el.style.opacity, badge: null };
      if (p.verdict === 'skip') el.style.opacity = '0.4';
      else {
        el.style.outline = `3px solid ${colors[p.verdict]}`; el.style.outlineOffset = '5px';
        m.badge = h('div', { style: `display:inline-block;margin:0 0 6px;padding:3px 9px;border-radius:999px;background:${colors[p.verdict]};color:#111;font:700 12px system-ui,sans-serif` }, (p.verdict === 'best' ? '★ Lucille: ' : 'Lucille: ') + (p.why || p.verdict));
        el.prepend(m.badge);
      }
      S.marks.push(m);
    }
    const first = picks.find(p => p.verdict === 'best' && S.items[p.i]);
    if (first) scrollTo(S.items[first.i].el);
  }

  /* ── reading the page ────────────────────────────────────────── */
  function searchKind() {
    if (window.__lsKind) return window.__lsKind;      // page-world test hook: invisible to the extension's isolated world in real use
    const hn = location.hostname, q = new URLSearchParams(location.search).get('q');
    if (!q) return null;
    if (/(^|\.)duckduckgo\.com$/.test(hn)) return { engine: 'ddg', q };
    if (/(^|\.)google\.[a-z.]+$/.test(hn) && location.pathname === '/search') return { engine: 'google', q };
    return null;
  }

  function collectItems(kind) {
    const sel = kind.engine === 'ddg' ? 'article[data-testid="result"], li[data-layout="organic"], .result' : '#rso div.g, #search div.g';
    let els = [...document.querySelectorAll(sel)].filter(e => e.querySelector('a[href^="http"]') && e.querySelector('h2,h3'));
    els = els.filter(e => !els.some(o => o !== e && o.contains(e)));
    S.items = els.slice(0, 20).map((el, i) => {
      const a = el.querySelector('h2 a[href^="http"], h3 a[href^="http"], a[href^="http"]'), t = (el.querySelector('h2,h3') || a).innerText.trim();
      return { i, el, title: t, url: a.href, snippet: el.innerText.replace(t, '').replace(/\s+/g, ' ').trim().slice(0, 280) };
    });
    return S.items.map(({ i, title, url, snippet }) => ({ i, title, url, snippet }));
  }

  async function look(question) {
    if (S.busy) return; S.busy = true; clearMarks();
    status('Lucille is reading this page', true);
    const kind = searchKind(), local = !!(S.cfg && S.cfg.local);
    const body = { url: window.__lsURL || location.href, title: document.title, selection: String(getSelection() || ''), text: (document.body.innerText || '').slice(0, 20000), question: question || '', local, hasPassword: !!document.querySelector('input[type=password]') };
    if (kind) { body.query = kind.q; body.items = collectItems(kind); }
    const r = await send({ type: 'call', path: '/api/sight', body });
    S.busy = false;
    if (!r || r.status === 0) return status((r && r.error) || 'I cannot reach Lucille.', false, true);
    if (r.status === 401) { S.cfg.paired = false; return main(); }
    const j = r.json || {};
    if (!j.ok) return status(j.error || 'Something went wrong.', false, true);
    if (j.refused) return status(j.text, false);
    showAnswer(j, kind);
  }

  S.look = look; S.main = main;                       // handles for the tests; the page's own scripts cannot see this isolated world

  function status(text, thinking, bad) {
    S.body.replaceChildren(header(), h('p', { class: (thinking ? 'think ' : '') + (bad ? 'err' : '') }, text), searchRow(), ...(thinking ? [] : [askRow()]));
  }

  function header() {
    return h('header', {}, h('b', {}, 'Lucille · sees this page'), h('div', {}, h('button', { onclick: () => { clearMarks(); } , title: 'Remove my highlights' }, 'Clear'), h('button', { onclick: hide, title: 'Close' }, '✕')));
  }

  function askRow() {
    const inp = h('input', { class: 'w', placeholder: 'Ask me about this page…', onkeydown: e => { if (e.key === 'Enter') look(inp.value); } });
    const loc = h('input', { type: 'checkbox', onchange: e => { S.cfg.local = e.target.checked; X.storage.local.set({ local: S.cfg.local }); } });
    loc.checked = !!(S.cfg && S.cfg.local);
    return h('div', {}, h('div', { class: 'row' }, inp, h('button', { class: 'hot', onclick: () => look(inp.value) }, 'Ask')),
      h('label', { class: 'muted', style: 'display:flex;gap:6px;align-items:center;margin-top:8px' }, loc, 'Keep it on this PC (offline model, slower)'));
  }

  function searchRow() {
    const eng = h('select', {}, h('option', { value: 'ddg' }, 'DuckDuckGo'), h('option', { value: 'google' }, 'Google'));
    const q = h('input', { placeholder: 'Search the web, I\'ll read the results…', onkeydown: e => { if (e.key === 'Enter') go(); } });
    const go = () => { if (q.value.trim()) send({ type: 'search', engine: eng.value, q: q.value.trim() }); };
    return h('div', { class: 'row' }, eng, q, h('button', { onclick: go }, 'Go'));
  }

  function jump(el) { if (el) scrollTo(el); }

  function showAnswer(j, kind) {
    const kids = [header(), h('p', { class: 'sum' }, j.text || '')];
    if ((j.picks || []).length) {
      kids.push(h('h4', {}, 'My read of the results'));
      const order = { best: 0, good: 1, skip: 2 };
      [...j.picks].sort((a, b) => order[a.verdict] - order[b.verdict]).forEach(p => {
        const it = S.items[p.i]; if (!it) return;
        kids.push(h('button', { class: 'it ' + p.verdict, onclick: () => jump(it.el) }, (p.verdict === 'best' ? '★ ' : p.verdict === 'skip' ? '✕ ' : '') + it.title, h('small', {}, p.why || '')));
      });
    }
    const placed = (j.highlights || []).filter(hl => true);
    if (placed.length) {
      applyHighlights(placed);
      kids.push(h('h4', {}, 'What I highlighted for you'));
      placed.forEach(hl => kids.push(h('button', { class: 'it hl', onclick: () => hl.range && jump(hl.range.startContainer.parentElement) }, '“' + (hl.quote.length > 110 ? hl.quote.slice(0, 110) + '…' : hl.quote) + '”', h('small', {}, hl.why || '')))); 
    }
    if ((j.picks || []).length) applyPicks(j.picks);
    if (j.game) kids.push(gameCard(j.game));
    kids.push(h('p', { class: 'muted' }, (j.kept_on_pc ? 'Answered on this PC. ' : '') + (j.note || '')));
    kids.push(searchRow(), askRow());
    S.body.replaceChildren(...kids);
  }

  function gameCard(g) {
    const m = g.mark || {};
    const mk = async (body, btn) => { const r = await send({ type: 'call', path: '/api/games/mark', body: Object.assign({ appid: g.appid || 0, name: g.name, image: g.image || '' }, body) }); if (r && r.json && r.json.ok) { g.mark = (r.json.mark && (r.json.mark.wish || r.json.mark.like)) ? r.json.mark : null; showCardUpdate(); } };
    const showCardUpdate = () => { const old = S.body.querySelector('.card'); if (old) old.replaceWith(gameCard(g)); };
    return h('div', { class: 'card' }, h('b', {}, g.name), h('p', { class: 'muted' }, [g.review_word && `${g.review_word} (${g.review_pct}%)`, g.metacritic && `Metacritic ${g.metacritic}`, g.price, g.lowest_ever && `lowest ever ${g.lowest_ever}`].filter(Boolean).join(' · ')),
      h('div', { class: 'row' },
        h('button', { class: m.like === 'like' ? 'on' : '', onclick: () => mk({ like: m.like === 'like' ? '' : 'like' }) }, '👍'),
        h('button', { class: m.like === 'dislike' ? 'on' : '', onclick: () => mk({ like: m.like === 'dislike' ? '' : 'dislike' }) }, '👎'),
        h('button', { class: m.wish ? 'on' : '', onclick: () => mk({ wish: !m.wish }) }, m.wish ? '♥ Wish list' : '♡ Wish list')));
  }

  /* ── first run: pair with Lucille ────────────────────────────── */
  function pairView(msg) {
    const server = h('input', { class: 'w', value: S.cfg.server || 'http://127.0.0.1:8403' });
    const code = h('input', { class: 'w', placeholder: '6-digit code', inputmode: 'numeric', maxlength: '6' });
    const go = async () => {
      const r = await send({ type: 'pair', server: server.value.trim(), code: code.value.trim(), label: 'Browser' });
      if (r && r.ok) { S.cfg.paired = true; S.cfg.server = server.value.trim(); main(); } else pairView((r && r.error) || 'That did not work.');
    };
    S.body.replaceChildren(header(), h('p', {}, 'Hi. Let\'s pair this browser with me once. In Lucille, make a pairing code (Settings), then type it here.'),
      h('p', { class: 'muted' }, 'I only look at a page when you press my button. I never look at banking, mail, health, login or payment pages, and I save nothing.'),
      h('div', { class: 'muted' }, 'Lucille\'s address'), server, h('div', { class: 'muted', style: 'margin-top:8px' }, 'Pairing code'), code,
      msg ? h('p', { class: 'err' }, msg) : null, h('div', { class: 'row' }, h('button', { class: 'hot', onclick: go }, 'Pair')));
  }

  async function main(auto) {
    show();
    const cfg = await send({ type: 'cfg' }); const pref = await new Promise(r => X.storage.local.get(['local'], r));
    S.cfg = Object.assign(cfg || {}, { local: !!pref.local });
    if (!S.cfg.paired) return pairView();
    const kind = searchKind();
    if (auto && kind) return look('Which of these results is really what I am looking for?');
    status(kind ? 'This is a search page. I can read the results and show you the best ones.' : 'I can read this page and help. Ask me something, or just let me look.', false);
    S.body.append(h('div', { class: 'row' }, h('button', { class: 'hot', onclick: () => look('') }, kind ? 'Read the results' : 'Look at this page')));
  }

  X.runtime.onMessage.addListener(msg => {
    if (msg.type === 'toggle') toggle();
    else if (msg.type === 'auto') main(true);
  });
})();
