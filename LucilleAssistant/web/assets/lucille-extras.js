/* lucille-extras.js — 2026-10-14, swarm-claude (user: "touch Lucille Assistant").
   Kept in its own file so it does not collide with other AIs editing index.html; index.html only loads it
   and calls three hooks: suggestionsHTML() on the Brief tab, i30ltzActive()/sendI30ltz() in sendChatMessage.

   1. Dr. I30LTZ in the chat panel. A switch in the chat header picks who you talk to. His replies come from
      POST /api/chat/i30ltz (persona.go): his own prompt and rules and his OWN offline model (qwen3:4b by default),
      never Lucille's models, never the cloud. If his model does not answer, he says so (no canned fallback).
   2. Suggestions & schedule (Brief tab). Every suggestion from /api/projectbrief gets a comment box. What you
      write is kept on the PC (comments.go) and plain words move it on the schedule: "done", "skip", "later",
      "tomorrow 3pm", "friday", "in 2 days", "undo". Anything else is just a note on that suggestion.

   Uses the page's own globals: api, panel, offline, esc, toast, render, store, addChatMsg, live, tab. */
(function(){
  'use strict';

  /* ── styles ─────────────────────────────────────────────────────── */
  const css = `
  .persona-sw{display:flex;gap:4px}
  .persona-sw button{font:600 10px/1 var(--mono);letter-spacing:.1em;text-transform:uppercase;padding:6px 9px;border-radius:999px;
    border:1px solid var(--line,rgba(255,255,255,.18));background:transparent;color:inherit;opacity:.65;cursor:pointer}
  .persona-sw button[aria-pressed="true"]{opacity:1;border-color:var(--plasma-hi,#f80);box-shadow:0 0 0 1px var(--plasma-hi,#f80) inset}
  .persona-sw button[data-persona="i30ltz"][aria-pressed="true"]{border-color:#e33;box-shadow:0 0 0 1px #e33 inset;color:#ff8a7a}
  .b-i30ltz{background:rgba(120,10,10,.35);border:1px solid rgba(230,60,50,.55);border-radius:12px 12px 12px 4px;
    font-family:var(--mono,monospace);font-size:12.5px}
  .b-i30ltz .who{display:block;font:600 9px/1 var(--mono,monospace);letter-spacing:.14em;color:#ff8a7a;margin-bottom:5px;text-transform:uppercase}
  .b-i30ltz.err{opacity:.85;border-style:dashed}
  .sg-proj{margin:12px 0 4px;font:600 10px/1 var(--mono,monospace);letter-spacing:.14em;text-transform:uppercase;opacity:.7}
  .sg{border:1px solid var(--line,rgba(255,255,255,.12));border-radius:12px;padding:10px 12px;margin:6px 0}
  .sg-head{display:flex;justify-content:space-between;gap:8px;align-items:baseline}
  .sg-head b{font-size:13.5px}
  .sg-why{margin:4px 0 6px;font-size:12px;opacity:.75}
  .sg-chip{font:600 9.5px/1 var(--mono,monospace);letter-spacing:.08em;text-transform:uppercase;padding:4px 7px;border-radius:999px;
    border:1px solid currentColor;white-space:nowrap;opacity:.85}
  .sg-chip.done{color:#5c5}.sg-chip.skipped{color:#999}.sg-chip.later{color:#c9c}.sg-chip.open{color:var(--plasma-hi,#f80)}
  .sg-notes{margin:6px 0;padding:0;list-style:none;font-size:12px}
  .sg-notes li{margin:3px 0;opacity:.85}.sg-notes li i{opacity:.6;font-style:normal;font-size:11px}
  .sg-row{display:flex;gap:6px;margin-top:6px}
  .sg-row input{flex:1;min-width:0}
  .sched{list-style:none;margin:0;padding:0}
  .sched li{display:grid;grid-template-columns:118px 1fr;gap:8px;padding:5px 0;border-bottom:1px dashed var(--line,rgba(255,255,255,.1));font-size:12.5px}
  .sched li .when{font:600 11px/1.4 var(--mono,monospace);opacity:.85}
  `;
  const st = document.createElement('style'); st.textContent = css; document.head.appendChild(st);

  /* ── 1. Dr. I30LTZ in the chat panel ─────────────────────────────── */
  let persona = store.get('la.persona') === 'i30ltz' ? 'i30ltz' : 'lucille';
  const i30Hist = [];

  function paintPersona(){
    const title = document.querySelector('#chatpanel .chatpanel-title');
    if (title && !title.querySelector('.persona-sw')) {
      title.innerHTML = `<span class="persona-sw" role="group" aria-label="Who you are talking to">
        <button type="button" data-persona="lucille">Lucille</button>
        <button type="button" data-persona="i30ltz">Dr. I30LTZ</button></span>`;
    }
    document.querySelectorAll('.persona-sw button').forEach(b => b.setAttribute('aria-pressed', String(b.dataset.persona === persona)));
    const inp = document.getElementById('chat-in');
    if (inp) inp.placeholder = persona === 'i30ltz' ? 'Ask Dr. I30LTZ…' : 'Message Lucille…';
  }

  function addI30(text, model, isErr){
    const log = document.getElementById('chat-msgs'); if (!log) return;
    const d = document.createElement('div');
    d.className = 'bubble b-i30ltz' + (isErr ? ' err' : '');
    d.innerHTML = `<span class="who">Dr. I30LTZ${model ? ' · ' + esc(model) : ''}</span><p>${esc(text)}</p>`;
    log.appendChild(d); log.scrollTop = log.scrollHeight;
  }

  window.i30ltzActive = () => persona === 'i30ltz';

  window.sendI30ltz = async function(msg){
    if (!msg || !msg.trim()) return;
    addChatMsg(msg, true);
    if (!live) { addI30('I only answer from this PC. Connect to it first (Settings).', '', true); return; }
    const wait = document.createElement('div');
    wait.className = 'bubble b-i30ltz err';
    wait.innerHTML = '<span class="who">Dr. I30LTZ</span><p>Consulting recovery diagnostics\u2026</p>';
    const log = document.getElementById('chat-msgs'); if (log) { log.appendChild(wait); log.scrollTop = log.scrollHeight; }
    try {
      const r = await api('/api/chat/i30ltz', { message: msg, history: i30Hist.slice(-12), voice: true });
      wait.remove();
      if (r && r.ok) {
        addI30(r.text, r.model);
        i30Hist.push({ role: 'user', content: msg }, { role: 'assistant', content: r.text });
        if (r.audio_b64) {
          try {
            const audio = new Audio("data:audio/mp3;base64," + r.audio_b64);
            audio.play().catch(e => console.log('Audio autoplay:', e));
          } catch(ae) {
            console.warn('Audio play error:', ae);
          }
        }
      } else {
        addI30((r && r.error) || 'No answer.', '', true);
      }
    } catch (e) {
      wait.remove();
      addI30('Could not reach the PC: ' + (e.message || e), '', true);
    }
  };

  document.addEventListener('click', e => {
    const b = e.target.closest('.persona-sw button'); if (!b) return;
    persona = b.dataset.persona === 'i30ltz' ? 'i30ltz' : 'lucille';
    store.set('la.persona', persona);
    paintPersona();
    if (persona === 'i30ltz' && !document.querySelector('#chat-msgs .b-i30ltz'))
      addI30('Dr. I30LTZ here. My own offline model, nothing leaves this PC, and I cannot run anything from chat. Answers can take a couple of minutes. Ask.', '');
  });
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', paintPersona); else paintPersona();

  /* ── 2. Suggestions & schedule ───────────────────────────────────── */
  let PB = null, CM = { items: {}, schedule: [] }, sgErr = '', sgBusy = false, sgAt = 0;

  async function loadSuggestions(){
    if (sgBusy) return;
    sgBusy = true;
    try {
      const [pb, cm] = await Promise.all([api('/api/projectbrief'), api('/api/comments')]);
      PB = pb && pb.ok !== false ? pb : null;
      if (cm && cm.ok) { CM = cm; sgErr = ''; } else { sgErr = (cm && cm.error) || 'Could not read the comments.'; }
    } catch (e) { sgErr = e.message || String(e); }
    sgBusy = false; sgAt = Date.now();
    if (tab === 'brief') render();
  }

  const fmtDue = due => {
    const d = new Date(due);
    if (isNaN(d)) return due;
    return d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' }) + ' · ' +
           d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  };

  function scheduleHTML(){
    const s = CM.schedule || [];
    if (!s.length) return `<p class="muted" style="margin:0 0 6px;font-size:12px">Nothing scheduled yet. Write "tomorrow 3pm", "friday" or "in 2 days" under a suggestion and it lands here; "done" or "skip" takes it off.</p>`;
    return `<ul class="sched">${s.map(it => `<li><span class="when">${it.status === 'later' ? 'later' : it.due ? esc(fmtDue(it.due)) : 'no date'}</span>
      <span>${it.project ? `<b>[${esc(it.project)}]</b> ` : ''}${esc(it.title || it.id)}</span></li>`).join('')}</ul>`;
  }

  function suggestionHTML(p, sg){
    const it = (CM.items || {})[sg.id];
    const status = it ? it.status : 'open';
    const chip = it ? (status === 'open' && it.due ? fmtDue(it.due) : status) : 'new';
    const notes = it && it.comments ? it.comments.slice(-3) : [];
    return `<div class="sg">
      <div class="sg-head"><b>${esc(sg.title)}</b><span class="sg-chip ${esc(status)}">${esc(chip)}</span></div>
      ${sg.why ? `<p class="sg-why">${esc(sg.why)}</p>` : ''}
      ${notes.length ? `<ul class="sg-notes">${notes.map(c => `<li>${esc(c.text)} <i>— ${esc(c.by)}, ${esc(new Date(c.at).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }))}</i></li>`).join('')}</ul>` : ''}
      <div class="sg-row"><input class="field" data-sgin="${esc(sg.id)}" placeholder="Your comment: done, skip, later, tomorrow 3pm, or a note" maxlength="1000">
        <button class="btn hot" type="button" data-sgsend="${esc(sg.id)}" data-proj="${esc(p.code || p.project || '')}" data-title="${esc(sg.title)}">Send</button>
        <button class="btn hot" type="button" data-sgdeal="${esc(sg.id)}" data-proj="${esc(p.code || p.project || '')}" data-title="${esc(sg.title)}" title="Lucille tells Claude or next available model to deal with it now">⚡ Deal</button></div>
    </div>`;
  }

  window.suggestionsHTML = function(){
    if (!live) return panel('Suggestions & schedule', offline('Connect to your PC to comment on Lucille\'s suggestions.'));
    if ((!PB && !sgBusy) || Date.now() - sgAt > 5 * 60 * 1000) loadSuggestions();
    if (!PB) return panel('Suggestions & schedule', `<p class="muted" style="margin:0">${sgErr ? esc(sgErr) : 'Reading the projects…'}</p>`);
    const projects = (PB.projects || []).filter(p => (p.suggestions || []).length);
    const body = `<h3 class="kicker" style="margin:0 0 6px">Schedule</h3>${scheduleHTML()}
      ${sgErr ? `<p class="muted" style="font-size:12px">${esc(sgErr)}</p>` : ''}
      ${projects.map(p => `<div class="sg-proj">${esc(p.code || '')} · ${esc(p.title || p.id)}</div>${p.suggestions.map(sg => suggestionHTML(p, sg)).join('')}`).join('')}`;
    return panel('Suggestions & schedule', body, 'your words move them');
  };

  async function sendComment(btn){
    const id = btn.dataset.sgsend;
    const inp = document.querySelector(`[data-sgin="${CSS.escape(id)}"]`);
    const text = inp ? inp.value.trim() : '';
    if (!text) { toast('Write something first.'); return; }
    btn.disabled = true;
    try {
      const r = await api('/api/comments/' + encodeURIComponent(id), { text, project: btn.dataset.proj, title: btn.dataset.title });
      if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
      CM.items = CM.items || {}; CM.items[id] = r.item; CM.schedule = r.schedule || CM.schedule;
      if (inp) inp.value = '';
      toast('Lucille: ' + r.change);
      render();
    } catch (e) {
      toast('Could not save: ' + (e.message || e));
    } finally { btn.disabled = false; }
  }

  async function dealComment(btn){
    const id = btn.dataset.sgdeal;
    const inp = document.querySelector(`[data-sgin="${CSS.escape(id)}"]`);
    const text = inp ? inp.value.trim() : '';
    btn.disabled = true;
    try {
      const r = await api('/api/threads/deal', { anchor: id, text, quote: btn.dataset.title, to: 'auto', launch: true });
      if (!r || !r.ok) throw new Error((r && r.error) || 'The PC said no');
      if (inp) inp.value = '';
      toast('Lucille: Handed off to ' + (r.target || 'coder') + (r.launched ? ' (started on PC)' : ' (queued)'));
      loadSuggestions();
    } catch (e) {
      toast('Could not deal with that: ' + (e.message || e));
    } finally { btn.disabled = false; }
  }

  document.addEventListener('click', e => {
    const b = e.target.closest('[data-sgsend]'); if (b) sendComment(b);
    const d = e.target.closest('[data-sgdeal]'); if (d) dealComment(d);
  });
  document.addEventListener('keydown', e => {
    if (e.key !== 'Enter') return;
    const inp = e.target.closest && e.target.closest('[data-sgin]'); if (!inp) return;
    const b = document.querySelector(`[data-sgsend="${CSS.escape(inp.dataset.sgin)}"]`); if (b) { e.preventDefault(); sendComment(b); }
  });
})();
