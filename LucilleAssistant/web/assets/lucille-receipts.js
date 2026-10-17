/* lucille-receipts.js — 2026-10-16, dev-claude (user: "you don't know if your comments get through or not").

   Every send now leaves a visible receipt: a pill that says "Sending…", then a green "✓ Sent", or a red "Not sent" that stays
   until dismissed (with Retry where repeating the call is safe). Chat bubbles get a small tick. It wraps the page's own api()
   once, so chat, thread comments, per-item comments and the care taps (meds, water, meals, sleep) are all covered without
   touching their code. Reads and everything else pass straight through untouched. */
(function(){
  'use strict';
  if (typeof window.api !== 'function' || window.api.__receipts) return;
  const css = `
  #rcpt{position:fixed;left:12px;right:12px;bottom:calc(env(safe-area-inset-bottom,0px) + 74px);z-index:903;display:flex;justify-content:center;pointer-events:none}
  #rcpt .p{pointer-events:auto;display:flex;align-items:center;gap:10px;max-width:440px;width:100%;box-sizing:border-box;padding:10px 14px;border-radius:14px;font-size:14px;line-height:1.35;
           background:rgba(14,18,26,.97);border:1px solid rgba(255,255,255,.14);box-shadow:0 8px 24px rgba(0,0,0,.45);animation:rin .2s ease}
  #rcpt .p.ok{border-color:rgba(74,222,128,.6)}#rcpt .p.fail{border-color:rgba(248,113,113,.7)}
  #rcpt .ic{flex:none;width:22px;height:22px;border-radius:50%;display:grid;place-items:center;font-size:13px;font-weight:700}
  #rcpt .sending .ic{border:2px solid rgba(255,255,255,.25);border-top-color:var(--plasma,#7c8cff);animation:rspin .8s linear infinite}
  #rcpt .ok .ic{background:rgba(74,222,128,.22);color:var(--ok,#4ade80)}#rcpt .fail .ic{background:rgba(248,113,113,.25);color:var(--crit,#f87171)}
  #rcpt .t{flex:1;min-width:0;overflow-wrap:anywhere}#rcpt .t small{display:block;opacity:.6;font-size:11.5px}
  #rcpt button{all:unset;cursor:pointer;padding:8px 10px;border-radius:9px;font-weight:600;font-size:13px;background:rgba(255,255,255,.1)}
  .bubble .tick{font-size:11px;opacity:.75;margin-left:8px;white-space:nowrap}.bubble .tick.fail{color:var(--crit,#f87171);opacity:1}.bubble .tick.ok{color:var(--ok,#4ade80)}
  @keyframes rin{from{opacity:0;transform:translateY(8px)}to{opacity:1;transform:none}}@keyframes rspin{to{transform:rotate(360deg)}}`;
  const st = document.createElement('style'); st.textContent = css; document.head.appendChild(st);
  const box = document.createElement('div'); box.id = 'rcpt'; box.setAttribute('aria-live', 'polite'); document.body.appendChild(box);

  // [pattern, what it's called, retry is safe]
  const KINDS = [
    [/^\/api\/chat(\/|$)/, 'Message', false],
    [/^\/api\/bridge\/say/, 'Message', false],
    [/^\/api\/comments\//, 'Comment', true],
    [/^\/api\/threads\/(post|ask|deal)/, 'Comment', true],
    [/^\/api\/care\/(med|water|meal|sleep)/, 'Logged', true],
  ];
  let hideTimer = 0;
  function show(state, text, sub, retry){
    clearTimeout(hideTimer);
    const ic = state === 'ok' ? '✓' : state === 'fail' ? '!' : '';
    box.innerHTML = `<div class="p ${state} ${state}" role="status"><span class="ic">${ic}</span><span class="t">${esc(text)}${sub ? `<small>${esc(sub)}</small>` : ''}</span>` +
      (state === 'fail' ? `${retry ? '<button data-r="retry">Retry</button>' : ''}<button data-r="x" aria-label="Dismiss">✕</button>` : '') + `</div>`;
    const p = box.firstChild;
    p.querySelector('[data-r="x"]') && p.querySelector('[data-r="x"]').addEventListener('click', () => { box.innerHTML = ''; });
    const rb = p.querySelector('[data-r="retry"]');
    if (rb && retry) rb.addEventListener('click', () => { box.innerHTML = ''; retry().catch(() => {}); });
    if (state === 'ok') hideTimer = setTimeout(() => { box.innerHTML = ''; }, 2800);
  }
  const tick = (cls, txt) => {
    const b = document.querySelectorAll('#chat-msgs .b-user'); const last = b[b.length - 1]; if (!last) return;
    let t = last.querySelector('.tick'); if (!t) { t = document.createElement('span'); last.querySelector('p').appendChild(t); }
    t.className = 'tick ' + cls; t.textContent = txt;
  };

  const orig = window.api;
  window.api = async function(path, body, method){
    const m = method || (body ? 'POST' : 'GET');
    const kind = m === 'POST' ? KINDS.find(k => k[0].test(path)) : null;
    if (!kind) return orig.apply(this, arguments);
    const args = arguments, chat = /^\/api\/chat/.test(path);
    const retry = kind[2] ? () => window.api.apply(window, args) : null;
    show('sending', kind[1] === 'Logged' ? 'Saving…' : 'Sending…');
    if (chat) setTimeout(() => tick('', '…'), 0);
    try {
      const r = await orig.apply(this, args);
      if (r && r.ok === false) {
        show('fail', kind[1] === 'Logged' ? 'Not saved' : 'Not sent', String(r.error || 'The PC said no'), retry);
        if (chat) tick('fail', '⚠ not sent');
      } else {
        show('ok', kind[1] === 'Logged' ? 'Saved' : 'Sent', new Date().toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }));
        if (chat) tick('ok', '✓');
      }
      return r;
    } catch (e) {
      show('fail', kind[1] === 'Logged' ? 'Not saved' : 'Not sent', (e && e.message) || 'Could not reach the PC', retry);
      if (chat) tick('fail', '⚠ not sent');
      throw e;
    }
  };
  window.api.__receipts = true;
})();
