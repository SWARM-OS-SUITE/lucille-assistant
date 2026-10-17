/* lucille-hud.js — 2026-10-16, dev-claude (user: "on the gaming tab only, swipe from the left for a gaming hud: telemetry from
   the PC, game info, it doesn't always have to be the same info; the pop-up windows toggleable: suggestions, hints,
   recommendations; she should speak some too").

   A drawer that slides in from the left edge, ONLY while the Games tab is open. Inside: a rotating info card (telemetry,
   session, temperatures, latest finding), four switches (Suggestions / Hints / Recommendations / Speak) and a preview button.
   While the Games tab is open the switches decide which pop-up cards appear and whether Lucille reads them aloud.
   Data: GET /api/rig (the same gaming-device-only numbers as the Gaming rig panel), POST /api/rig/advice, POST /api/tts.
   Page globals used: api, esc, live, tab. Settings live in localStorage ('la.hud'); nothing is sent anywhere but this server. */
(function(){
  'use strict';
  const css = `
  #hud-scrim{position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:900;opacity:0;pointer-events:none;transition:opacity .2s}
  #hud-scrim.open{opacity:1;pointer-events:auto}
  #hud{position:fixed;top:0;bottom:0;left:0;width:min(88vw,340px);z-index:901;transform:translateX(-102%);transition:transform .22s ease;
       background:rgba(10,14,22,.985);backdrop-filter:blur(8px);border-right:1px solid rgba(255,255,255,.10);padding:calc(env(safe-area-inset-top,0px) + 14px) 14px 16px;
       overflow-y:auto;color:var(--filament,#e6e9f0);font-family:var(--body,system-ui,sans-serif)}
  #hud.open{transform:none}
  #hud h3{margin:0 0 4px;font:700 13px/1.2 var(--display,system-ui);letter-spacing:.14em;text-transform:uppercase;color:var(--plasma-hi,#a8b3ff)}
  #hud .hud-sub{font-size:12px;color:var(--smoke,#9aa4b2);margin-bottom:12px}
  #hud-card{min-height:170px;border:1px solid rgba(255,255,255,.09);border-radius:12px;padding:12px;background:rgba(255,255,255,.03);cursor:pointer}
  #hud-card .k{font:600 10px/1.4 var(--mono,monospace);letter-spacing:.12em;text-transform:uppercase;color:var(--smoke,#9aa4b2)}
  #hud-card .big{font:700 28px/1.2 var(--display,system-ui);margin:6px 0}
  #hud .gz-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:10px 6px;margin-top:10px}
  #hud-card .row{display:flex;justify-content:space-between;font-size:13px;margin:5px 0}
  #hud-card .row span:last-child{font-family:var(--mono,monospace);font-variant-numeric:tabular-nums}
  #hud-card p{margin:6px 0 0;font-size:13.5px;line-height:1.5}
  #hud-dots{display:flex;gap:6px;justify-content:center;margin:8px 0 14px}
  #hud-dots i{width:6px;height:6px;border-radius:50%;background:rgba(255,255,255,.2)}#hud-dots i.on{background:var(--plasma,#7c8cff)}
  .hud-sw{display:flex;align-items:center;justify-content:space-between;gap:10px;padding:11px 2px;border-top:1px solid rgba(255,255,255,.07);font-size:14px;min-height:44px}
  .hud-sw small{display:block;color:var(--smoke,#9aa4b2);font-size:11.5px}
  .hud-sw input{appearance:none;width:44px;height:26px;border-radius:13px;background:rgba(255,255,255,.18);position:relative;flex:none;outline:0}
  .hud-sw input::after{content:"";position:absolute;top:3px;left:3px;width:20px;height:20px;border-radius:50%;background:#fff;transition:left .15s}
  .hud-sw input:checked{background:var(--plasma,#7c8cff)}.hud-sw input:checked::after{left:21px}
  #hud button.hud-btn{width:100%;margin-top:12px;min-height:44px}
  #hud-pops{position:fixed;left:0;right:0;bottom:calc(env(safe-area-inset-bottom,0px) + 14px);z-index:902;display:flex;flex-direction:column;align-items:center;gap:8px;pointer-events:none;padding:0 12px}
  .hud-pop{pointer-events:auto;max-width:440px;width:100%;box-sizing:border-box;background:rgba(14,18,26,.96);border:1px solid rgba(124,140,255,.45);border-left-width:4px;
           border-radius:12px;padding:10px 12px;font-size:14px;line-height:1.45;box-shadow:0 8px 24px rgba(0,0,0,.45);animation:hudin .25s ease}
  .hud-pop b{display:block;font:600 10px/1.5 var(--mono,monospace);letter-spacing:.12em;text-transform:uppercase;color:var(--plasma-hi,#a8b3ff)}
  .hud-pop.suggestion{border-color:var(--warn,#fbbf24)}.hud-pop.hint{border-color:var(--arc,#5eead4)}
  .hud-pop .x{float:right;background:none;border:0;color:var(--smoke,#9aa4b2);font-size:18px;line-height:1;padding:0 2px;cursor:pointer}
  @keyframes hudin{from{opacity:0;transform:translateY(10px)}to{opacity:1;transform:none}}`;
  const st = document.createElement('style'); st.textContent = css; document.head.appendChild(st);

  const KEY = 'la.hud';
  const defaults = { suggestion: true, hint: true, recommendation: false, speak: false };
  let cfg = Object.assign({}, defaults);
  try { Object.assign(cfg, JSON.parse(localStorage.getItem(KEY) || '{}')); } catch (e) {}
  const save = () => { try { localStorage.setItem(KEY, JSON.stringify(cfg)); } catch (e) {} };

  const H = { data: null, card: 0, open: false, seen: new Map(), lastAdvice: 0, lastHint: 0, lastSuggest: 0, hintN: 0, speakQ: [], speaking: false };

  const root = document.createElement('div');
  root.innerHTML = `<div id="hud-scrim"></div>
    <aside id="hud" aria-label="Gaming HUD" aria-hidden="true">
      <h3>Gaming HUD</h3><div class="hud-sub" id="hud-sub">Swipe from the left edge on the Games tab</div>
      <div id="hud-card" role="button" tabindex="0" aria-label="Next info card"></div><div id="hud-dots"></div>
      <label class="hud-sw"><span>Suggestions<small>Warnings from your numbers</small></span><input type="checkbox" data-k="suggestion"></label>
      <label class="hud-sw"><span>Hints<small>Breaks, water, posture</small></span><input type="checkbox" data-k="hint"></label>
      <label class="hud-sw"><span>Recommendations<small>Settings advice while you play</small></span><input type="checkbox" data-k="recommendation"></label>
      <label class="hud-sw"><span>Speak<small>Lucille reads pop-ups aloud</small></span><input type="checkbox" data-k="speak"></label>
      <button class="btn hot hud-btn" id="hud-preview" type="button">Preview a pop-up</button>
      <button class="btn hud-btn" id="hud-close" type="button">Close</button>
    </aside><div id="hud-pops" aria-live="polite"></div>`;
  document.body.appendChild(root);
  const $ = id => document.getElementById(id);
  root.querySelectorAll('input[data-k]').forEach(i => {
    i.checked = !!cfg[i.dataset.k];
    i.addEventListener('change', () => { cfg[i.dataset.k] = i.checked; save(); });
  });

  /* ---- rotating info cards ---- */
  function cards(){
    const d = H.data;
    if (!live) return [{ k: 'Not connected', html: '<p>Connect to your PC to see the rig.</p>' }];
    if (!d) return [{ k: 'Reading the rig', html: '<p>One moment…</p>' }];
    if (!d.ok) return [{ k: 'No gaming device', html: `<p>${esc(d.error || 'No gaming device is set.')}</p>` }];
    const t = d.telemetry || {}, g = t.gpu, gm = d.gaming || {}, out = [];
    const row = (a, b) => `<div class="row"><span>${esc(a)}</span><span>${esc(b)}</span></div>`;
    out.push({ k: 'Right now', html: `<div class="big">${gm.active ? esc(gm.game) : 'Not gaming'}</div>` +
      (gm.active ? row('Session', Math.round(gm.minutes) + ' min') : '') + row('Device', d.device || 'this PC') });
    const G = window.rigGauge || (() => '');
    out.push({ k: 'Load', html: '<div class="gz-grid">' + (g ? G('GPU', g.util_percent, Math.round(g.util_percent) + '%', 90, 99, '🎮') : '') +
      G('CPU', t.cpu_percent, Math.round(t.cpu_percent) + '%', 85, 95, '⚙') + G('RAM', t.ram_percent, Math.round(t.ram_percent) + '%', 85, 92, '▤') + '</div>' });
    const heat = (g ? G('GPU heat', g.temp_c / 95 * 100, Math.round(g.temp_c) + '°', 80 / 95 * 100, 88 / 95 * 100, '🌡') : '') +
      (t.temp_c != null ? G('Heat', t.temp_c, Math.round(t.temp_c) + '°', 80, 90, '🌡') : '') +
      (g && g.vram_total_mb > 0 ? G('VRAM', g.vram_used_mb / g.vram_total_mb * 100, Math.round(g.vram_used_mb / g.vram_total_mb * 100) + '%', 85, 92, '▦') : '');
    if (heat) out.push({ k: 'Heat & memory', html: '<div class="gz-grid">' + heat + '</div>' });
    (d.findings || []).slice(0, 2).forEach(f => out.push({ k: f.area || 'Note', html: `<p>${esc(f.text)}</p>` }));
    return out;
  }
  function paintCard(){
    const cs = cards(), c = cs[H.card % cs.length];
    $('hud-card').innerHTML = `<div class="k">${esc(c.k)}</div>${c.html}`;
    $('hud-dots').innerHTML = cs.map((_, i) => `<i class="${i === H.card % cs.length ? 'on' : ''}"></i>`).join('');
    const gm = (H.data && H.data.gaming) || {};
    $('hud-sub').textContent = gm.active ? `Playing ${gm.game} · ${Math.round(gm.minutes)} min` : 'Swipe from the left edge on the Games tab';
  }
  $('hud-card').addEventListener('click', () => { H.card++; paintCard(); });
  setInterval(() => { if (H.open) { H.card++; paintCard(); } }, 8000);

  /* ---- open / close; only on the Games tab ---- */
  function setOpen(v){
    if (v && tab !== 'games') return;
    H.open = v;
    $('hud').classList.toggle('open', v); $('hud-scrim').classList.toggle('open', v);
    $('hud').setAttribute('aria-hidden', String(!v));
    if (v) { poll(); paintCard(); }
  }
  $('hud-scrim').addEventListener('click', () => setOpen(false));
  $('hud-close').addEventListener('click', () => setOpen(false));
  let sx = 0, sy = 0, tracking = null;
  document.addEventListener('touchstart', e => {
    const t = e.touches[0]; sx = t.clientX; sy = t.clientY;
    tracking = (!H.open && tab === 'games' && sx <= 70) ? 'open' : (H.open ? 'close' : null);
  }, { passive: true });
  document.addEventListener('touchend', e => {
    if (!tracking) return;
    const t = e.changedTouches[0], dx = t.clientX - sx, dy = t.clientY - sy;
    if (Math.abs(dx) > 55 && Math.abs(dx) > Math.abs(dy) * 1.5) {
      if (tracking === 'open' && dx > 0) setOpen(true);
      if (tracking === 'close' && dx < 0) setOpen(false);
    }
    tracking = null;
  }, { passive: true });

  /* ---- data ---- */
  async function poll(){
    if (tab !== 'games') { if (H.open) setOpen(false); return; }
    if (!live) { paintCard(); return; }
    try { H.data = await api('/api/rig'); } catch (e) { H.data = null; }
    if (H.open) paintCard();
    tick();
  }
  setInterval(poll, 4000);

  /* ---- pop-ups ---- */
  const HINTS = ['Take a sip of water.', 'Roll your shoulders and unclench your jaw.', 'Look at something far away for twenty seconds.',
    'Sit back: is your screen at eye height?', 'Stretch your wrists and fingers.', 'Quick breath: in for four, out for six.'];
  function pop(kind, text){
    const el = document.createElement('div');
    el.className = 'hud-pop ' + kind;
    el.innerHTML = `<button class="x" aria-label="Dismiss">×</button><b>${esc(kind)}</b>${esc(text)}`;
    el.querySelector('.x').addEventListener('click', () => el.remove());
    const box = $('hud-pops'); box.appendChild(el);
    while (box.children.length > 3) box.firstChild.remove();
    setTimeout(() => el.remove(), 10000);
    if (cfg.speak) say(text);
  }
  function fresh(key, minutes){
    const now = Date.now(), last = H.seen.get(key) || 0;
    if (now - last < minutes * 60000) return false;
    H.seen.set(key, now); return true;
  }
  async function tick(){
    if (tab !== 'games' || !live || !H.data || !H.data.ok) return;
    const gm = H.data.gaming || {}, now = Date.now();
    if (cfg.suggestion) (H.data.findings || []).filter(f => f.level === 'warn' || f.level === 'bad').slice(0, 1)
      .forEach(f => { if (now - H.lastSuggest > 3 * 60000 && fresh('s:' + f.text.replace(/\d+/g, '#'), 15)) { H.lastSuggest = now; pop('suggestion', f.text); } });
    if (cfg.hint && gm.active && now - H.lastHint > 20 * 60000) {
      H.lastHint = now;
      pop('hint', gm.minutes >= 60 && H.hintN % 3 === 0 ? `You've been playing ${Math.round(gm.minutes)} minutes. Time for a proper break soon.` : HINTS[H.hintN % HINTS.length]);
      H.hintN++;
    }
    if (cfg.recommendation && gm.active && now - H.lastAdvice > 20 * 60000) {
      H.lastAdvice = now;
      try {
        const r = await api('/api/rig/advice', { game: '' }, 'POST');
        const t = String(r.advice || '').replace(/\s+/g, ' ').trim();
        if (t) pop('recommendation', t.length > 220 ? t.slice(0, 217).replace(/\s\S*$/, '') + '…' : t);
      } catch (e) {}
    }
  }
  $('hud-preview').addEventListener('click', () => {
    setOpen(false);
    pop('hint', HINTS[Math.floor(Math.random() * HINTS.length)]);
    const f = ((H.data && H.data.findings) || [])[0];
    setTimeout(() => pop('suggestion', f ? f.text : 'This is how a suggestion looks.'), 600);
  });

  /* ---- speech: Lucille's voice from the server, the phone's own voice if that fails ---- */
  async function say(text){
    H.speakQ.push(text); if (H.speaking) return;
    H.speaking = true;
    while (H.speakQ.length) {
      const t = H.speakQ.shift();
      try {
        const r = await api('/api/tts', { text: t }, 'POST');
        if (!r || !r.audio_b64) throw new Error('no audio');
        const a = new Audio('data:audio/mp3;base64,' + r.audio_b64);
        await new Promise((ok, no) => { a.onended = ok; a.onerror = no; a.play().catch(no); });
      } catch (e) {
        try {
          if ('speechSynthesis' in window) await new Promise(ok => { const u = new SpeechSynthesisUtterance(t); u.onend = u.onerror = ok; speechSynthesis.speak(u); });
        } catch (e2) {}
      }
    }
    H.speaking = false;
  }
  window.lucilleHud = { open: () => setOpen(true), close: () => setOpen(false), pop };
})();
