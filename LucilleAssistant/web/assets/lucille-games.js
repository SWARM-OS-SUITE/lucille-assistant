/* lucille-games.js — 2026-10-14, dev-claude (user: "I want to read a description, a review, and like/dislike and add to
   wish list of any game it shows; search the web for info on games I look up").

   Any game on the page (a deal, a recently played game, a search result, a wish-list entry) opens a card:
   description, genres, Metacritic, Steam's review summary and a few real reviews, a Wikipedia summary, every store's
   price, and 👍 / 👎 / ♡ buttons. Marks are kept on the PC (games.go); Lucille's prompt knows them.
   Also adds "Look up a game" and "My games" panels to the Games tab (hook: gamesHTML()).
   Uses the page's globals: api, esc, toast, live, tab. */
(function(){
  'use strict';
  const css = `
  .gc-search{display:flex;gap:7px}.gc-search input{flex:1;min-width:0}
  .gc-list{list-style:none;margin:10px 0 0;padding:0;display:grid;gap:6px}
  .gc-row{display:flex;gap:10px;align-items:center;padding:7px 9px;border:1px solid rgba(255,255,255,.1);border-radius:10px;cursor:pointer;background:rgba(255,255,255,.02)}
  .gc-row:hover{border-color:var(--plasma,#ff7a29)}
  .gc-row img{width:92px;height:35px;object-fit:cover;border-radius:5px;flex:none;background:#111}
  .gc-row .t{flex:1;min-width:0}.gc-row b{display:block;font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
  .gc-row small{opacity:.6;font-size:11px}
  .gc-badge{font-size:15px;flex:none}
  .gc-chips{display:flex;gap:6px;flex-wrap:wrap;margin:10px 0 0}
  .gc-chip{font:600 10px/1 var(--mono,monospace);letter-spacing:.07em;text-transform:uppercase;border:1px solid rgba(255,255,255,.2);border-radius:999px;padding:5px 9px;background:none;color:inherit;cursor:pointer}
  .gc-chip.on{background:var(--plasma,#ff7a29);color:#120700;border-color:var(--plasma,#ff7a29)}
  #gc-sheet{position:fixed;inset:0;z-index:90;display:none;align-items:flex-end;justify-content:center;background:rgba(0,0,0,.62)}
  #gc-sheet.on{display:flex}
  #gc-sheet .box{width:100%;max-width:640px;max-height:92vh;overflow:auto;background:var(--panel-fill,#0b100d);border:1px solid var(--plasma,#ff7a29);border-radius:16px 16px 0 0;padding:0 0 18px}
  #gc-sheet .hero{position:relative;min-height:90px;background:#111 center/cover}
  #gc-sheet .hero img{display:block;width:100%;max-height:240px;object-fit:cover}
  #gc-sheet .hero button.x{position:absolute;right:10px;top:10px}
  #gc-sheet .in{padding:12px 16px 0}
  #gc-sheet h3{margin:0 0 3px;font:600 20px/1.2 Cinzel,serif;letter-spacing:.04em}
  #gc-sheet .sub{font-size:12px;opacity:.65;margin:0}
  #gc-sheet h4{margin:16px 0 6px;font:600 10px/1 var(--mono,monospace);letter-spacing:.14em;text-transform:uppercase;color:var(--plasma-hi,#ffa35c)}
  #gc-sheet p{margin:0 0 6px;font-size:13.5px;line-height:1.5;overflow-wrap:anywhere}
  .gc-score{display:inline-flex;gap:6px;align-items:baseline;font:600 12px/1 var(--mono,monospace);border:1px solid currentColor;border-radius:999px;padding:6px 10px;margin:8px 8px 0 0}
  .gc-score.good{color:var(--ok,#4ade80)}.gc-score.mid{color:var(--warn,#fc4)}.gc-score.bad{color:var(--crit,#f43)}
  .gc-rev{border-left:3px solid var(--ok,#4ade80);padding:4px 0 4px 10px;margin:8px 0;font-size:12.8px;line-height:1.45;overflow-wrap:anywhere}
  .gc-rev.down{border-color:var(--crit,#f43)}.gc-rev small{display:block;opacity:.55;margin-top:2px}
  .gc-price{display:flex;justify-content:space-between;gap:8px;padding:6px 0;border-bottom:1px dashed rgba(255,255,255,.1);font-size:13px;color:inherit;text-decoration:none}
  .gc-act{display:flex;gap:8px;flex-wrap:wrap;margin-top:14px}
  .gc-act button{border-radius:999px;padding:10px 16px;font-size:13px}
  .gc-act .on{outline:2px solid var(--plasma-hi,#ffa35c)}
  .gc-link{color:var(--plasma-hi,#ffa35c)}
  .gc-shots{display:flex;gap:8px;overflow-x:auto;padding-bottom:6px;scroll-snap-type:x proximity}
  .gc-shots img{height:92px;width:auto;border-radius:8px;flex:none;cursor:zoom-in;border:1px solid rgba(255,255,255,.14);scroll-snap-align:start;background:#111}
  .gc-shots img:hover{border-color:var(--plasma,#ff7a29)}
  #gc-lb{position:fixed;inset:0;z-index:120;display:none;flex-direction:column;align-items:center;justify-content:center;background:rgba(0,0,0,.94)}
  #gc-lb.on{display:flex}
  #gc-lb img{max-width:96vw;max-height:84vh;object-fit:contain;border-radius:6px;box-shadow:0 0 40px rgba(0,0,0,.8)}
  #gc-lb .bar2{display:flex;gap:10px;align-items:center;margin-top:12px;color:#ddd;font:600 12px/1 var(--mono,monospace);letter-spacing:.1em}
  #gc-lb button{border:1px solid rgba(255,255,255,.35);background:rgba(255,255,255,.08);color:#fff;border-radius:999px;padding:10px 16px;font-size:15px;cursor:pointer}
  #gc-lb button.x{position:absolute;right:14px;top:14px}
  #gc-lb .nav{position:absolute;top:50%;transform:translateY(-50%);padding:14px 16px}
  #gc-lb .nav.l{left:10px}#gc-lb .nav.r{right:10px}
  .deal,.bar[data-gapp]{cursor:pointer}`;
  const st = document.createElement('style'); st.textContent = css; document.head.appendChild(st);

  const G = { lb: -1, q: '', results: null, busy: false, err: '', marks: null, filter: 'wish', card: null, loading: false };

  async function loadMarks(){
    try { const r = await api('/api/games/marks'); if (r && r.ok) { G.marks = r.games || []; paintGames(); } } catch (_) {}
  }

  const badge = m => m ? (m.like === 'like' ? '👍' : m.like === 'dislike' ? '👎' : '') + (m.wish ? '♡' : '') : '';
  const rowHTML = g => `<li class="gc-row" data-gapp="${esc(g.appid || '')}" data-gtitle="${esc(g.name)}">${g.image ? `<img src="${esc(g.image)}" alt="" loading="lazy">` : '<span style="width:92px;flex:none"></span>'}
    <div class="t"><b>${esc(g.name)}</b><small>${esc([g.price, g.metascore ? 'Metacritic ' + g.metascore : ''].filter(Boolean).join(' · '))}</small></div><span class="gc-badge">${badge(g.mark || g)}</span></li>`;

  function myGamesHTML(){
    const all = G.marks || [];
    const f = { wish: m => m.wish, like: m => m.like === 'like', dislike: m => m.like === 'dislike' }[G.filter];
    const list = all.filter(f);
    const chip = (k, l) => `<button class="gc-chip${G.filter === k ? ' on' : ''}" type="button" data-gfilter="${k}">${l} ${all.filter({ wish: m => m.wish, like: m => m.like === 'like', dislike: m => m.like === 'dislike' }[k]).length}</button>`;
    return `<div class="gc-chips" style="margin:0">${chip('wish', '♡ Wish list')}${chip('like', '👍 Liked')}${chip('dislike', '👎 Disliked')}</div>
      <ul class="gc-list">${list.map(rowHTML).join('') || `<li class="muted" style="font-size:12.5px">${G.marks ? 'Nothing here yet. Open any game and press the buttons.' : 'Loading…'}</li>`}</ul>`;
  }

  function searchHTML(){
    return `<form id="gc-form" class="gc-search"><input class="field" name="q" value="${esc(G.q)}" placeholder="Any game: Hades, Elden Ring, Stardew…" autocomplete="off" required><button class="btn hot" type="submit">Look up</button></form>
      ${G.err ? `<p class="muted" style="font-size:12px;margin:8px 0 0">${esc(G.err)}</p>` : ''}
      ${G.busy ? '<p class="muted" style="font-size:12px;margin:8px 0 0">Searching…</p>' : ''}
      ${G.results ? `<ul class="gc-list">${G.results.map(rowHTML).join('') || '<li class="muted" style="font-size:12.5px">Steam has nothing by that name.</li>'}</ul>
        <p style="font-size:12px;margin:8px 0 0"><a class="gc-link" href="${esc(G.searchUrl)}" target="_blank" rel="noopener">Search the whole web for it ↗</a></p>` : ''}`;
  }

  function paintGames(){
    const s = document.getElementById('gc-search'), m = document.getElementById('gc-mine');
    if (s && !(document.activeElement && s.contains(document.activeElement) && document.activeElement.tagName === 'INPUT' && !G.busy && !G.results)) s.innerHTML = searchHTML();
    if (m) m.innerHTML = myGamesHTML();
  }

  window.gamesHTML = function(){
    if (!live) return '';
    if (!G.marks) loadMarks();
    return `<section class="panel"><div class="in"><h2>Look up a game<small>description · reviews · wish list</small></h2><div id="gc-search">${searchHTML()}</div></div></section>
      <section class="panel"><div class="in"><h2>My games</h2><div id="gc-mine">${myGamesHTML()}</div></div></section>`;
  };

  /* ── the card ─────────────────────────────────────────────────── */
  function sheet(){ let el = document.getElementById('gc-sheet'); if (!el) { el = document.createElement('div'); el.id = 'gc-sheet'; document.body.appendChild(el); } return el; }
  const tone = p => p >= 75 ? 'good' : p >= 50 ? 'mid' : 'bad';

  function cardHTML(){
    const c = G.card;
    if (G.loading) return `<div class="box"><div class="in"><p class="muted">Reading up on ${esc(G.loading)}…</p><div class="gc-act"><button class="btn" type="button" data-gclose="1">Close</button></div></div></div>`;
    if (!c) return '';
    const m = c.mark || {};
    const score = [c.metacritic ? `<span class="gc-score ${tone(c.metacritic)}">Metacritic ${c.metacritic}</span>` : '',
      c.review_word ? `<span class="gc-score ${tone(c.review_pct)}">Steam: ${esc(c.review_word)} · ${c.review_pct}% of ${c.review_total.toLocaleString()}</span>` : ''].join('');
    return `<div class="box"><div class="hero">${c.image ? `<img src="${esc(c.image)}" alt="">` : ''}<button class="btn x" type="button" data-gclose="1">Close</button></div><div class="in">
      <h3>${esc(c.name)}</h3><p class="sub">${esc([c.release, (c.developers || []).join(', '), (c.genres || []).join(' · '), c.price].filter(Boolean).join(' · '))}</p>
      <div class="gc-act"><button class="btn ${m.like === 'like' ? 'hot on' : ''}" type="button" data-gmark="like">👍 Like</button><button class="btn ${m.like === 'dislike' ? 'red on' : ''}" type="button" data-gmark="dislike">👎 Dislike</button><button class="btn ${m.wish ? 'hot on' : ''}" type="button" data-gmark="wish">${m.wish ? '♥ On wish list' : '♡ Wish list'}</button></div>
      ${score ? `<div>${score}</div>` : ''}
      ${(c.screenshots || []).length ? `<h4>In-game screenshots · tap to enlarge</h4><div class="gc-shots">${c.screenshots.map((s, i) => `<img src="${esc(s.thumb)}" alt="Screenshot ${i + 1}" loading="lazy" data-gshot="${i}">`).join('')}</div>` : ''}
      ${c.description ? `<h4>Description</h4><p>${esc(c.description)}</p>` : ''}
      ${(c.review_samples || []).length ? `<h4>What players say</h4>${c.review_samples.map(r => `<div class="gc-rev${r.up ? '' : ' down'}">${esc(r.text)}<small>${r.up ? '👍 recommended' : '👎 not recommended'}${r.hours ? ' · ' + r.hours + ' h played' : ''}</small></div>`).join('')}` : ''}
      ${c.web_extract ? `<h4>From the web</h4><p>${esc(c.web_extract)}</p><p><a class="gc-link" href="${esc(c.web_url)}" target="_blank" rel="noopener">Read more on Wikipedia ↗</a></p>` : ''}
      <p style="margin-top:6px"><a class="gc-link" href="${esc(c.search_url)}" target="_blank" rel="noopener">Search the whole web for reviews ↗</a></p>
      ${(c.prices || []).length ? `<h4>Where to buy${c.lowest_ever ? ' · lowest ever ' + esc(c.lowest_ever) : ''}</h4>${c.prices.slice(0, 6).map(p => `<a class="gc-price" href="${esc(p.url)}" target="_blank" rel="noopener"><span>${esc(p.store)}</span><b>$${esc(p.sale_price)}${p.savings_pct ? ` <small>-${p.savings_pct}%</small>` : ''}</b></a>`).join('')}` : (c.store_url ? `<p><a class="gc-link" href="${esc(c.store_url)}" target="_blank" rel="noopener">Open the Steam page ↗</a></p>` : '')}
      ${(c.notes || []).length ? `<p class="muted" style="font-size:11px;margin-top:12px">Couldn't reach: ${esc(c.notes.map(n => n.split(':')[0]).join(', '))}.</p>` : ''}
      </div></div>`;
  }
  function paintCard(){ if (!G.card) { G.lb = -1; if (typeof paintLb === 'function') paintLb(); } const el = sheet(); const html = cardHTML(); el.classList.toggle('on', !!html); el.innerHTML = html; }

  /* ── full-screen screenshot viewer ──────────────────────────── */
  function lbEl(){ let el = document.getElementById('gc-lb'); if (!el) { el = document.createElement('div'); el.id = 'gc-lb'; document.body.appendChild(el); } return el; }
  function paintLb(){
    const el = lbEl(), shots = (G.card && G.card.screenshots) || [];
    if (G.lb < 0 || !shots[G.lb]) { el.classList.remove('on'); el.innerHTML = ''; return; }
    el.classList.add('on');
    el.innerHTML = `<button class="x" type="button" data-glb="close">✕</button><button class="nav l" type="button" data-glb="prev">‹</button><img src="${esc(shots[G.lb].full)}" alt="Screenshot ${G.lb + 1}"><button class="nav r" type="button" data-glb="next">›</button>
      <div class="bar2"><span>${G.lb + 1} / ${shots.length}</span><a href="${esc(shots[G.lb].full)}" target="_blank" rel="noopener" style="color:#ffa35c">open original ↗</a></div>`;
  }
  function lbStep(d){ const n = ((G.card && G.card.screenshots) || []).length; if (!n) return; G.lb = (G.lb + d + n) % n; paintLb(); }
  document.addEventListener('keydown', e => {
    if (G.lb < 0) return;
    if (e.key === 'Escape') { G.lb = -1; paintLb(); e.stopPropagation(); } else if (e.key === 'ArrowRight') lbStep(1); else if (e.key === 'ArrowLeft') lbStep(-1);
  }, true);
  let tx = null;
  document.addEventListener('touchstart', e => { if (G.lb >= 0) tx = e.touches[0].clientX; }, { passive: true });
  document.addEventListener('touchend', e => { if (G.lb < 0 || tx === null) return; const dx = e.changedTouches[0].clientX - tx; tx = null; if (Math.abs(dx) > 50) lbStep(dx < 0 ? 1 : -1); }, { passive: true });

  async function openGame(appid, title){
    if (!live) return;
    G.card = null; G.loading = title || 'this game'; paintCard();
    try {
      const r = await api('/api/game?' + (appid ? 'appid=' + encodeURIComponent(appid) : 'title=' + encodeURIComponent(title)) + (appid && title ? '&title=' + encodeURIComponent(title) : ''));
      if (!r || !r.ok) throw new Error((r && r.error) || 'no answer');
      G.card = r.game;
    } catch (e) { toast('Could not read up on that game: ' + (e.message || e)); G.card = null; }
    G.loading = false; paintCard();
  }

  async function mark(kind){
    const c = G.card; if (!c) return;
    const m = c.mark || {};
    const body = { appid: c.appid || 0, name: c.name, image: c.image || '' };
    if (kind === 'wish') body.wish = !m.wish; else body.like = m.like === kind ? '' : kind;
    try {
      const r = await api('/api/games/mark', body);
      if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
      c.mark = r.mark && (r.mark.wish || r.mark.like) ? r.mark : null;
      paintCard(); loadMarks();
    } catch (e) { toast('Could not save: ' + (e.message || e)); }
  }

  async function search(q){
    G.q = q; G.busy = true; G.err = ''; G.results = null; paintGames();
    try {
      const r = await api('/api/game/search?q=' + encodeURIComponent(q));
      if (!r || !r.ok) throw new Error((r && r.error) || 'no answer');
      G.results = r.results || []; G.searchUrl = r.search_url;
    } catch (e) { G.err = 'Could not search: ' + (e.message || e); }
    G.busy = false; paintGames();
  }

  document.addEventListener('submit', e => { if (e.target.id === 'gc-form') { e.preventDefault(); const q = new FormData(e.target).get('q'); if (String(q).trim()) search(String(q).trim()); } });
  document.addEventListener('click', e => {
    const t = e.target; if (!t.closest) return;
    if (window.__taPick && window.__taPick()) return;           // comment-anywhere mode owns clicks
    const lb = t.closest('[data-glb]');
    if (lb) { if (lb.dataset.glb === 'close') { G.lb = -1; paintLb(); } else lbStep(lb.dataset.glb === 'next' ? 1 : -1); return; }
    if (t.id === 'gc-lb') { G.lb = -1; paintLb(); return; }
    const shot = t.closest('[data-gshot]'); if (shot) { G.lb = +shot.dataset.gshot; paintLb(); return; }
    const close = t.closest('[data-gclose]'); if (close) { G.card = null; G.loading = false; paintCard(); return; }
    if (t.id === 'gc-sheet') { G.card = null; G.loading = false; paintCard(); return; }
    const mk = t.closest('[data-gmark]'); if (mk) { mark(mk.dataset.gmark); return; }
    const fl = t.closest('[data-gfilter]'); if (fl) { G.filter = fl.dataset.gfilter; paintGames(); return; }
    const g = t.closest('[data-gapp],[data-gtitle]');
    if (g && !t.closest('#gc-sheet')) { e.preventDefault(); openGame(g.dataset.gapp || '', g.dataset.gtitle || ''); }
  }, true);
})();
