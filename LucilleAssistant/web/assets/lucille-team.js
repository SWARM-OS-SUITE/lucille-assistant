/* lucille-team.js — 2026-10-14, dev-claude (user: "a pretty little artifact GUI, interactive; I can comment on any
   statement and talk to Boltz about it ... the three of us work together from any device, all up to date").

   The team Brief: what needs you, what is critical, who is on what, how each project stands, whether everything
   started. Every statement has a comment thread (💬). In a thread you write, and you can ask Lucille or Dr. Boltz
   to answer; their replies land in the same thread, on every device, live (threads.go: /api/artifact, /api/threads/*).

   Hook: index.html's Brief tab calls artifactHTML(). Uses the page's globals: api, esc, toast, live, tab, cfg, ago, render.
   It repaints only its own box when a thread changes, so a half-typed comment is never lost to a refresh. */
(function(){
  'use strict';

  const css = `
  .ta{display:grid;gap:14px}
  .ta-hero{display:flex;flex-wrap:wrap;gap:10px;align-items:center;justify-content:space-between}
  .ta-hero h2{margin:0;font-family:Cinzel,serif;letter-spacing:.06em}
  .ta-pills{display:flex;flex-wrap:wrap;gap:6px}
  .ta-pill{font:600 10.5px/1 var(--mono,monospace);letter-spacing:.08em;text-transform:uppercase;padding:6px 10px;border-radius:999px;
    border:1px solid currentColor;white-space:nowrap}
  .ta-pill.bad{color:var(--crit,#f43)}.ta-pill.warn{color:var(--warn,#fc4)}.ta-pill.ok{color:var(--ok,#4ade80)}.ta-pill.info{color:var(--plasma-hi,#ffa35c)}
  .ta-sec{border:1px solid rgba(255,122,41,.18);border-radius:14px;background:rgba(255,255,255,.02);overflow:hidden}
  .ta-sec>header{display:flex;align-items:baseline;justify-content:space-between;gap:8px;padding:10px 14px 8px;border-bottom:1px solid rgba(255,122,41,.12)}
  .ta-sec>header b{font:600 11px/1 var(--mono,monospace);letter-spacing:.14em;text-transform:uppercase;color:var(--plasma-hi,#ffa35c)}
  .ta-sec>header i{font-style:normal;font-size:11px;opacity:.55}
  .ta-st{position:relative;padding:10px 14px 10px 18px;border-top:1px solid rgba(255,255,255,.05)}
  .ta-st:first-of-type{border-top:0}
  .ta-st::before{content:"";position:absolute;left:0;top:10px;bottom:10px;width:3px;border-radius:0 3px 3px 0;background:var(--smoke-dim,#4a524d)}
  .ta-st.bad::before{background:var(--crit,#f43)}.ta-st.warn::before{background:var(--warn,#fc4)}.ta-st.ok::before{background:var(--ok,#4ade80)}.ta-st.info::before{background:var(--plasma,#ff7a29)}
  .ta-row{display:flex;gap:10px;align-items:flex-start}
  .ta-txt{flex:1;min-width:0}
  .ta-txt p{margin:0;font-size:13.5px;line-height:1.45;overflow-wrap:anywhere}
  .ta-txt small{display:block;margin-top:3px;font-size:11.5px;opacity:.6;overflow-wrap:anywhere}
  .ta-tags{display:flex;flex-wrap:wrap;gap:5px;margin-top:6px}
  .ta-tag{font:600 9.5px/1 var(--mono,monospace);letter-spacing:.07em;text-transform:uppercase;padding:4px 7px;border-radius:999px;
    border:1px solid rgba(255,255,255,.22);opacity:.85;white-space:nowrap}
  .ta-tag.done{color:#5c5;border-color:#5c5}.ta-tag.skipped{color:#999}.ta-tag.later{color:#c9c;border-color:#c9c}.ta-tag.due{color:var(--plasma-hi,#ffa35c);border-color:var(--plasma-hi,#ffa35c)}
  .ta-cm{flex:none;display:flex;align-items:center;gap:5px;cursor:pointer;border:1px solid rgba(255,255,255,.16);background:transparent;color:inherit;
    border-radius:999px;padding:6px 10px;font:600 11px/1 var(--mono,monospace);opacity:.75}
  .ta-cm:hover,.ta-cm[aria-expanded="true"]{opacity:1;border-color:var(--plasma,#ff7a29)}
  .ta-cm.has{color:var(--plasma-hi,#ffa35c);opacity:1;border-color:rgba(255,122,41,.55)}
  .ta-cm svg{width:13px;height:13px;stroke:currentColor;fill:none;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
  .ta-th{margin:10px 0 2px;display:grid;gap:8px}
  .ta-msg{max-width:92%;padding:8px 11px;border-radius:12px 12px 12px 4px;font-size:12.8px;line-height:1.45;overflow-wrap:anywhere;
    background:rgba(255,255,255,.05);border:1px solid rgba(255,255,255,.1)}
  .ta-msg .who{display:flex;gap:8px;align-items:baseline;font:600 9.5px/1 var(--mono,monospace);letter-spacing:.14em;text-transform:uppercase;margin-bottom:5px;opacity:.9}
  .ta-msg .who em{font-style:normal;opacity:.5;letter-spacing:.04em}
  .ta-msg.me{margin-left:auto;border-radius:12px 12px 4px 12px;background:rgba(255,122,41,.1);border-color:rgba(255,122,41,.4)}
  .ta-msg.me .who{color:var(--plasma-hi,#ffa35c)}
  .ta-msg.lucille{background:rgba(255,95,168,.08);border-color:rgba(255,95,168,.4)}.ta-msg.lucille .who{color:#ff7fbd}
  .ta-msg.boltz{background:rgba(120,10,10,.3);border-color:rgba(230,60,50,.55);font-family:var(--mono,monospace);font-size:12.3px}.ta-msg.boltz .who{color:#ff8a7a}
  .ta-msg.err{opacity:.7;border-style:dashed}
  .ta-think{font-size:12px;opacity:.7;padding:2px 4px}
  .ta-think::after{content:"";display:inline-block;width:1.4em;text-align:left;animation:ta-dots 1.2s steps(4,end) infinite}
  @keyframes ta-dots{0%{content:""}25%{content:"."}50%{content:".."}75%{content:"..."}}
  .ta-in{display:grid;gap:7px;margin-top:8px}
  .ta-in textarea{width:100%;min-height:42px;resize:vertical;background:rgba(255,255,255,.05);border:1px solid rgba(255,122,41,.28);
    border-radius:8px;padding:8px 10px;font:13px/1.45 inherit;color:inherit}
  .ta-btns{display:flex;flex-wrap:wrap;gap:6px}
  .ta-btns button{border-radius:999px;padding:8px 13px;font-size:12px}
  .ta-btns .lu{background:linear-gradient(135deg,#ff5fa8,#a3126f);box-shadow:0 0 14px rgba(255,95,168,.35)}
  .ta-btns .bo{background:linear-gradient(135deg,#ff6a5a,#8c1010);box-shadow:0 0 14px rgba(230,60,50,.35)}
  .ta-live{font:600 9px/1 var(--mono,monospace);letter-spacing:.14em;text-transform:uppercase;opacity:.55}
  .ta-live::before{content:"";display:inline-block;width:6px;height:6px;border-radius:50%;margin-right:6px;background:var(--smoke-dim,#4a524d);vertical-align:1px}
  .ta-live.on::before{background:var(--ok,#4ade80);box-shadow:0 0 8px var(--ok,#4ade80)}
  .ta-row{cursor:pointer}
  .ta-msg.coder{background:rgba(57,217,138,.07);border-color:rgba(57,217,138,.4)}.ta-msg.coder .who{color:var(--arc,#39d98a)}
  .ta-msg.job{opacity:.8;font-size:12px}
  .ta-jobs{display:flex;flex-wrap:wrap;gap:6px;margin-top:8px}
  .ta-job{display:flex;gap:7px;align-items:center;font:600 10px/1 var(--mono,monospace);letter-spacing:.06em;text-transform:uppercase;padding:5px 9px;border-radius:999px;border:1px solid rgba(57,217,138,.5);color:var(--arc,#39d98a)}
  .ta-job button{background:none;border:0;color:inherit;font:inherit;text-decoration:underline;cursor:pointer;padding:0}
  .ta-btns .co{background:linear-gradient(135deg,#39d98a,#0b6b45);box-shadow:0 0 12px rgba(57,217,138,.3);color:#031}
  .ta-draft textarea{min-height:170px;font:12px/1.5 var(--mono,monospace)}
  .ta-pickbtn{position:fixed;left:14px;bottom:78px;z-index:60;display:flex;gap:6px;align-items:center;border:1px solid var(--plasma,#ff7a29);background:rgba(7,9,8,.92);color:var(--plasma-hi,#ffa35c);
    border-radius:999px;padding:9px 13px;font:600 11px/1 var(--mono,monospace);letter-spacing:.08em;text-transform:uppercase;cursor:pointer}
  .ta-pickbtn svg{width:14px;height:14px;stroke:currentColor;fill:none;stroke-width:2;stroke-linecap:round;stroke-linejoin:round}
  .ta-pickbtn[aria-pressed="true"]{background:var(--plasma,#ff7a29);color:#120700}
  body.ta-pick *:hover{outline:1px dashed var(--plasma,#ff7a29);outline-offset:2px;cursor:comment,pointer}
  body.ta-pick #ta-sheet *:hover,body.ta-pick .ta-pickbtn:hover{outline:0}
  #ta-sheet{position:fixed;inset:0;z-index:80;display:none;align-items:flex-end;justify-content:center;background:rgba(0,0,0,.55)}
  #ta-sheet.on{display:flex}
  #ta-sheet .box{width:100%;max-width:560px;max-height:88vh;overflow:auto;background:var(--panel-fill,#0b100d);border:1px solid var(--plasma,#ff7a29);border-radius:16px 16px 0 0;padding:14px 16px 18px}
  #ta-sheet .q{font-size:12.5px;opacity:.75;margin:0 0 6px;padding-left:10px;border-left:3px solid var(--plasma,#ff7a29);overflow-wrap:anywhere}
  #ta-sheet header{display:flex;justify-content:space-between;align-items:center;margin-bottom:8px}
  #ta-sheet header b{font:600 10px/1 var(--mono,monospace);letter-spacing:.14em;text-transform:uppercase;color:var(--plasma-hi,#ffa35c)}
  @media (max-width:520px){.ta-msg{max-width:100%}.ta-row{flex-direction:column}.ta-cm{align-self:flex-start}}`;
  const st = document.createElement('style'); st.textContent = css; document.head.appendChild(st);

  const T = { data: null, open: {}, drafts: {}, busy: false, err: '', es: null, seq: -1, on: false, poll: 0, loadedAt: 0, sheet: null, quotes: {}, draft: null, pick: false, pair: null };
  const WHO = { lucille: 'Lucille', boltz: 'Dr. Boltz', claude: 'Claude', coder: 'Claude' };
  const BUBBLE = '<svg viewBox="0 0 24 24"><path d="M21 12a8 8 0 01-11.6 7.1L4 20l1-4.6A8 8 0 1121 12z"/></svg>';

  async function load(){
    if (T.busy || !live) return;
    T.busy = true;
    try {
      const r = await api('/api/artifact');
      if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
      T.data = r; T.seq = r.seq; T.err = ''; T.loadedAt = Date.now();
    } catch (e) {
      T.err = /404|not found|Unexpected token/i.test(String(e.message)) ? 'This Lucille Assistant is older than the team Brief. Install the new build and reload.' : 'Could not read the Brief: ' + (e.message || e);
    } finally { T.busy = false; paint(); }
  }

  function startStream(){
    if (T.es || !window.EventSource || !cfg.base) return;
    try {
      T.es = new EventSource(cfg.base + '/api/threads/stream' + (cfg.token ? '?token=' + encodeURIComponent(cfg.token) : ''));
      T.es.onopen = () => { T.on = true; paintLive(); };
      T.es.onerror = () => { T.on = false; paintLive(); };
      T.es.addEventListener('threads', ev => {
        let s = -1; try { s = JSON.parse(ev.data).seq; } catch (_) {}
        if (s !== T.seq) load();
      });
    } catch (_) { T.es = null; }
    if (!T.poll) T.poll = setInterval(() => {
      if (tab !== 'brief') { if (T.es) { T.es.close(); T.es = null; T.on = false; } return; }
      if (!T.es) startStream();
      if (Date.now() - T.loadedAt > 20000) load();   // belt and braces: a missed event heals within 20 s
    }, 5000);
  }

  function paintLive(){ document.querySelectorAll('.ta-live').forEach(el => { el.classList.toggle('on', T.on); el.textContent = T.on ? 'live' : 'reconnecting'; }); }

  function paint(){
    const box = document.getElementById('ta-body'); if (!box) return;
    const ae = document.activeElement, keep = ae && box.contains(ae) && ae.dataset && ae.dataset.draft ? { a: ae.dataset.draft, s: ae.selectionStart, e: ae.selectionEnd } : null;
    box.innerHTML = bodyHTML();
    if (keep) { const n = box.querySelector(`[data-draft="${CSS.escape(keep.a)}"]`); if (n) { n.focus(); try { n.setSelectionRange(keep.s, keep.e); } catch (_) {} } }
    paintLive();
    paintSheet();
  }

  function pills(secs){
    const count = id => ((secs.find(s => s.id === id) || {}).items || []).length;
    const health = ((secs.find(s => s.id === 'health') || {}).items || [])[0];
    const out = [];
    if (count('needs')) out.push(`<span class="ta-pill bad">${count('needs')} waiting on you</span>`);
    if (count('critical')) out.push(`<span class="ta-pill warn">${count('critical')} critical</span>`);
    if (count('team')) out.push(`<span class="ta-pill info">${count('team')} AI claim${count('team') === 1 ? '' : 's'}</span>`);
    if (health) out.push(`<span class="ta-pill ${health.level}">${esc(health.tag || '')}</span>`);
    return out.join('');
  }

  function msgHTML(m){
    const who = m.by === 'lucille' || m.by === 'boltz' ? m.by : (m.kind === 'coder' ? 'coder' : 'me');
    const label = who === 'me' ? (m.by === 'you' ? 'You' : m.by) : (WHO[who] || m.by);
    return `<div class="ta-msg ${who}${m.kind === 'job' ? ' job' : ''}${m.err ? ' err' : ''}"><div class="who">${esc(label)}<em>${esc(ago(m.at))}${m.model ? ' · ' + esc(String(m.model).split('/').pop()) : ''}</em></div>${esc(m.text)}</div>`;
  }

  function jobChips(anchor){
    const js = ((T.data.jobs || {})[anchor] || []);
    if (!js.length) return '';
    return `<div class="ta-jobs">${js.map(j => {
      const st = j.status === 'running' ? 'running now (started by Lucille)' : j.status === 'failed' ? 'failed' : j.status === 'claimed' ? 'picked up by ' + esc(j.claimed_by) : j.status === 'done' ? 'done' : j.status === 'manual' ? 'paste into ChatGPT' : 'waiting for ' + esc(j.to);
      const extra = j.status === 'manual' ? `<button type="button" data-ta="jobcopy" data-j="${esc(j.id)}">copy prompt</button>` : (j.status === 'queued' || j.status === 'running') ? `<button type="button" data-ta="jobcancel" data-j="${esc(j.id)}">${j.status === 'running' ? 'stop it' : 'take back'}</button>` : '';
      return `<span class="ta-job">→ ${esc(j.to)} · ${st} ${extra}</span>`;
    }).join('')}</div>`;
  }

  function threadHTML(anchor){
    const th = (T.data.threads || {})[anchor] || [];
    const pend = (T.data.pending || []).filter(p => p.anchor === anchor).map(p => p.who);
    const d = T.draft && T.draft.anchor === anchor ? T.draft : null;
    const draft = d ? `<div class="ta-in ta-draft"><p class="muted" style="margin:0;font-size:12px">Lucille's draft prompt for ${esc(d.to)}. Edit it if you want, then approve.</p>
      <textarea data-jobprompt="1" rows="9">${esc(d.prompt)}</textarea>
      <div class="ta-btns"><button class="btn co" type="button" data-ta="jobsend" data-a="${esc(anchor)}">${d.to === 'chatgpt' ? 'Approve and copy' : 'Approve and send to ' + esc(d.to)}</button>
      ${d.to === 'claude' || d.to === 'antigravity' ? `<button class="btn hot" type="button" data-ta="jobstart" data-a="${esc(anchor)}" title="Lucille starts ${esc(d.to)} herself: one job at a time, 30 minutes at most, no commits or pushes">Approve and start it now</button>` : ''}
      <button class="btn" type="button" data-ta="jobdrop">Cancel</button></div></div>` : '';
    return `<div class="ta-th">${th.map(msgHTML).join('')}${pend.map(w => `<div class="ta-think">${esc(WHO[w] || w)} is thinking</div>`).join('')}</div>${jobChips(anchor)}${draft}
      <div class="ta-in"><textarea data-draft="${esc(anchor)}" rows="2" maxlength="1500" placeholder="Say something or answer on this statement. Then send, deal, or ask.">${esc(T.drafts[anchor] || '')}</textarea>
      <div class="ta-btns"><button class="btn hot" type="button" data-ta="send" data-a="${esc(anchor)}">Send</button>
      <button class="btn hot" type="button" data-ta="deal" data-a="${esc(anchor)}" title="Lucille shares your comment and tells Claude or next available model to deal with it now">⚡ Deal with it</button>
      <button class="btn lu" type="button" data-ta="ask" data-who="lucille" data-a="${esc(anchor)}">Ask Lucille</button>
      <button class="btn co" type="button" data-ta="ask" data-who="claude" data-a="${esc(anchor)}">Ask Claude</button>
      <button class="btn bo" type="button" data-ta="ask" data-who="boltz" data-a="${esc(anchor)}">Ask Boltz</button></div>
      <div class="ta-btns"><button class="btn co" type="button" data-ta="draft" data-to="claude" data-a="${esc(anchor)}">Prompt Claude</button>
      <button class="btn co" type="button" data-ta="draft" data-to="antigravity" data-a="${esc(anchor)}">Prompt Antigravity</button>
      <button class="btn co" type="button" data-ta="draft" data-to="chatgpt" data-a="${esc(anchor)}">Prompt ChatGPT</button></div></div>`;
  }

  function stHTML(it){
    const th = (T.data.threads || {})[it.anchor] || [];
    const open = !!T.open[it.anchor];
    const pend = (T.data.pending || []).some(p => p.anchor === it.anchor);
    T.quotes[it.anchor] = it.text;
    const tags = [];
    if (it.tag) tags.push(`<span class="ta-tag">${esc(it.tag)}</span>`);
    if (it.status && it.status !== 'open') tags.push(`<span class="ta-tag ${esc(it.status)}">${esc(it.status)}</span>`);
    if (it.due) tags.push(`<span class="ta-tag due">${esc(new Date(it.due).toLocaleString([], { weekday: 'short', hour: 'numeric', minute: '2-digit' }))}</span>`);
    return `<div class="ta-st ${esc(it.level || '')}"><div class="ta-row" data-ta="toggle" data-a="${esc(it.anchor)}"><div class="ta-txt"><p>${esc(it.text)}</p>${it.sub ? `<small>${esc(it.sub)}</small>` : ''}${tags.length ? `<div class="ta-tags">${tags.join('')}</div>` : ''}</div>
      <button class="ta-cm${th.length || pend ? ' has' : ''}" type="button" aria-expanded="${open}" data-ta="toggle" data-a="${esc(it.anchor)}" title="Comments on this">${BUBBLE}${th.length || ''}</button></div>${open ? threadHTML(it.anchor) : ''}</div>`;
  }

  /* comments on any other part of the page (pick mode) show up here too, so none is lost */
  function pageThreads(){
    const th = T.data.threads || {};
    return Object.keys(th).filter(k => k.startsWith('pg:')).map(k => {
      const q = (th[k].find(m => m.quote) || {}).quote || k;
      return { anchor: k, text: q, sub: 'on the ' + (k.split(':')[1] || '').split('-')[0] + ' page', tag: 'page' };
    });
  }

  function bodyHTML(){
    if (T.err && !T.data) return `<p class="muted" style="margin:0">${esc(T.err)}</p>`;
    if (!T.data) return '<p class="muted" style="margin:0">Reading the projects…</p>';
    const secs = (T.data.sections || []).slice();
    const pg = pageThreads(); if (pg.length) secs.push({ id: 'pages', title: 'Comments elsewhere on the page', items: pg });
    return `${T.err ? `<p class="muted" style="font-size:12px">${esc(T.err)}</p>` : ''}<div class="ta">
      <div class="ta-hero"><div class="ta-pills">${pills(secs)}<button class="ta-pill info" type="button" data-ta="pair" style="background:none;cursor:pointer">${T.pair ? 'code ' + esc(T.pair.code) + ' · 10 min' : 'Pair a browser or phone'}</button></div><span class="ta-live${T.on ? ' on' : ''}">${T.on ? 'live' : 'reconnecting'}</span></div>
      ${secs.map(s => `<section class="ta-sec"><header><b>${esc(s.title)}</b>${s.note ? `<i>${esc(s.note)}</i>` : ''}</header>${s.items.map(stHTML).join('')}</section>`).join('')}</div>`;
  }

  window.artifactHTML = function(){
    if (!live) return `<section class="panel"><div class="in"><h2>Team Brief</h2><p class="muted" style="margin:0">Connect to your PC to see the team Brief.</p></div></section>`;
    if (!T.data && !T.busy) load();
    startStream();
    return `<section class="panel"><div class="in"><h2>Team Brief<small>comment on anything · Lucille and Boltz answer</small></h2><div id="ta-body">${bodyHTML()}</div></div></section>`;
  };

  function ensureSheet(){
    let el = document.getElementById('ta-sheet');
    if (!el) { el = document.createElement('div'); el.id = 'ta-sheet'; document.body.appendChild(el); }
    return el;
  }
  function paintSheet(){
    const el = ensureSheet();
    if (!T.sheet || !T.data) { el.classList.remove('on'); el.innerHTML = ''; return; }
    const keep = document.activeElement && el.contains(document.activeElement) && document.activeElement.dataset.draft ? { s: document.activeElement.selectionStart, e: document.activeElement.selectionEnd } : null;
    el.classList.add('on');
    el.innerHTML = `<div class="box"><header><b>Comment on this</b><button class="btn" type="button" data-ta="sheetclose">Close</button></header>
      <p class="q">${esc(T.sheet.quote)}</p>${threadHTML(T.sheet.anchor)}</div>`;
    if (keep) { const n = el.querySelector('[data-draft]'); if (n) { n.focus(); try { n.setSelectionRange(keep.s, keep.e); } catch (_) {} } }
  }

  function ensurePickBtn(){
    if (document.getElementById('ta-pickbtn') || !live) return;
    const b = document.createElement('button');
    b.id = 'ta-pickbtn'; b.type = 'button'; b.className = 'ta-pickbtn'; b.setAttribute('aria-pressed', 'false');
    b.innerHTML = BUBBLE + '<span>Comment anywhere</span>';
    b.addEventListener('click', () => {
      T.pick = !T.pick; b.setAttribute('aria-pressed', String(T.pick));
      document.body.classList.toggle('ta-pick', T.pick);
      toast(T.pick ? 'Tap any text on the page to comment on it.' : 'Comment mode off.');
    });
    document.body.appendChild(b);
  }
  setInterval(ensurePickBtn, 2000);
  window.__taPick = () => T.pick;
  window.openThread = function(anchor, quote){
    if (!live) return;
    if (quote) T.quotes[anchor] = quote;
    T.sheet = { anchor, quote: quote || T.quotes[anchor] || anchor };
    if (!T.data && !T.busy) load();
    else paintSheet();
  };

  const slug = s => String(s).toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40);
  document.addEventListener('click', e => {
    if (!T.pick || !T.data) return;
    const t = e.target;
    if (!t.closest || t.closest('#ta-sheet, #gc-sheet, #ta-pickbtn, #ta-body, nav, .tabs')) return;
    const blk = t.closest('li,p,tr,.item,.sg,.nudge,h2,h3,label,small,button,section.panel') || t;
    const text = (blk.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 200);
    if (text.length < 4) return;
    e.preventDefault(); e.stopPropagation();
    const anchor = 'pg:' + tab + '-' + (slug(text) || 'x');
    T.quotes[anchor] = text;
    T.sheet = { anchor, quote: text };
    paintSheet();
  }, true);

  async function send(a, quiet){
    const box = document.querySelector(`[data-draft="${CSS.escape(a)}"]`);
    const text = (box ? box.value : T.drafts[a] || '').trim();
    if (!text) return !!quiet;
    const quote = T.quotes[a] || '';
    const r = await api('/api/threads/post', { anchor: a, text, quote, project: '', title: quote });
    if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
    T.drafts[a] = ''; if (box) box.value = '';
    if (r.change && r.change !== 'noted') toast('Lucille: ' + r.change);
    return true;
  }

  document.addEventListener('input', e => {
    const t = e.target; if (!t || !t.dataset) return;
    if (t.dataset.draft) T.drafts[t.dataset.draft] = t.value;
    if (t.dataset.jobprompt && T.draft) T.draft.prompt = t.value;
  });
  document.addEventListener('click', async e => {
    const b = e.target.closest && e.target.closest('[data-ta]'); if (!b) return;
    if (T.pick && !b.closest('#ta-sheet, #ta-body')) return;
    const a = b.dataset.a, act = b.dataset.ta;
    if (act === 'toggle') {
      if (String(window.getSelection && window.getSelection()).trim()) return;   // selecting text is not a click
      T.open[a] = !T.open[a]; paint(); if (T.open[a]) { const n = document.querySelector(`#ta-body [data-draft="${CSS.escape(a)}"]`); if (n) n.focus(); } return;
    }
    if (act === 'pair') {
      try { const r = await api('/api/pair/new'); if (!r || !r.ok) throw new Error((r && r.error) || 'Only works on the PC itself'); T.pair = { code: r.code }; paint(); setTimeout(() => { T.pair = null; paint(); }, 600000); }
      catch (err) { toast('Could not make a code: ' + (err.message || err)); }
      return;
    }
    if (act === 'sheetclose') { T.sheet = null; paintSheet(); return; }
    if (act === 'jobdrop') { T.draft = null; paint(); return; }
    if (act === 'jobcopy') {
      const j = Object.values(T.data.jobs || {}).flat().find(x => x.id === b.dataset.j);
      if (j) copyText(j.prompt).then(ok => toast(ok ? 'Prompt copied. Paste it into ChatGPT.' : 'Could not copy: select it manually.'));
      return;
    }
    b.disabled = true;
    try {
      if (act === 'send') await send(a);
      if (act === 'deal') {
        const box = document.querySelector(`[data-draft="${CSS.escape(a)}"]`);
        const text = (box ? box.value : T.drafts[a] || '').trim();
        const quote = T.quotes[a] || '';
        const r = await api('/api/threads/deal', { anchor: a, text, quote, to: 'auto', launch: true });
        if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
        T.drafts[a] = ''; if (box) box.value = '';
        toast('Lucille: Handed off to ' + (r.target || 'coder') + (r.launched ? ' (started on PC)' : ' (queued)'));
      }
      if (act === 'ask') {
        await send(a, true);
        const r = await api('/api/threads/ask', { anchor: a, who: b.dataset.who });
        if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
      }
      if (act === 'draft') {
        await send(a, true);
        const r = await api('/api/jobs/draft', { anchor: a, to: b.dataset.to });
        if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
        T.draft = { anchor: a, to: r.to, prompt: r.prompt };
      }
      if ((act === 'jobsend' || act === 'jobstart') && T.draft) {
        const d = T.draft;
        const r = await api('/api/jobs', { anchor: d.anchor, to: d.to, prompt: d.prompt, launch: act === 'jobstart' });
        if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
        if (d.to === 'chatgpt') copyText(r.job.prompt).then(ok => toast(ok ? 'Prompt copied. Paste it into ChatGPT.' : 'Saved. Use "copy prompt" below.'));
        else toast(act === 'jobstart' ? 'Starting ' + d.to + '. I will post what it says here.' : 'Queued for ' + d.to + '.');
        T.draft = null;
      }
      if (act === 'jobcancel') { const r = await api('/api/jobs/' + encodeURIComponent(b.dataset.j) + '/cancel', {}); if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no'); }
      await load();
      paint();
    } catch (err) { toast('Could not do that: ' + (err.message || err)); }
    finally { b.disabled = false; }
  });
  document.addEventListener('keydown', e => {
    const t = e.target; if (e.key !== 'Enter' || !(e.ctrlKey || e.metaKey) || !t || !t.dataset || !t.dataset.draft) return;
    e.preventDefault(); const s = document.querySelector(`[data-ta="send"][data-a="${CSS.escape(t.dataset.draft)}"]`); if (s) s.click();
  });
})();
