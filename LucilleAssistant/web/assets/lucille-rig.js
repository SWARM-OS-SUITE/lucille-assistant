/* lucille-rig.js — 2026-10-16, dev-claude (user: "she is still the gaming assistant, she needs to track the PC while
   gaming, show telemetry just from the gaming device and give settings, evals and other advice on gaming").

   A "Gaming rig" panel at the top of the Games tab: live numbers from the GAMING device only (this PC, or the telemetry
   node named "gaming_node" in config.json), what they mean in plain words, and a "Review my settings" button that asks
   Lucille for a short settings review of the game being played (rules only when no model answers).
   Server: rig.go (GET /api/rig, POST /api/rig/advice). Uses the page's globals: api, esc, live, tab. */
(function(){
  'use strict';
  const css = `
  .rig-head{display:flex;justify-content:space-between;align-items:baseline;gap:10px;flex-wrap:wrap;margin-bottom:10px}
  .rig-dev{font:600 13px/1.2 var(--mono,monospace)}
  .rig-chip{font:600 10px/1 var(--mono,monospace);letter-spacing:.08em;text-transform:uppercase;border:1px solid currentColor;border-radius:999px;padding:5px 9px}
  .rig-chip.on{color:var(--ok,#4ade80)}.rig-chip.off{color:var(--smoke,#9aa3a0)}
  .rig-rows{display:flex;flex-direction:column;gap:9px;margin-bottom:12px}
  .rig-row .lbl{display:flex;justify-content:space-between;gap:8px;font-size:12px;margin-bottom:3px}
  .rig-row .lbl span:last-child{font-family:var(--mono,monospace);color:var(--smoke,#9aa3a0);font-variant-numeric:tabular-nums}
  .rig-track{height:6px;background:rgba(255,255,255,.08);border-radius:3px;overflow:hidden}
  .rig-fill{height:100%;border-radius:3px;background:var(--plasma,#7c8cff);transition:width .4s}
  .rig-fill.warn{background:var(--warn,#fbbf24)}.rig-fill.bad{background:var(--crit,#f87171)}
  .rig-find{display:flex;flex-direction:column;gap:6px;margin:0 0 12px;padding:0;list-style:none}
  .rig-find li{padding:8px 10px;border-left:3px solid var(--smoke-dim,#4b5563);background:rgba(255,255,255,.03);font-size:13px;line-height:1.45}
  .rig-find li.warn{border-color:var(--warn,#fbbf24)}.rig-find li.bad{border-color:var(--crit,#f87171);background:rgba(248,113,113,.08)}
  .rig-find li.ok{border-color:var(--ok,#4ade80)}.rig-find li.info{border-color:var(--arc,#5eead4)}
  .rig-find b{display:block;font:600 9px/1.6 var(--mono,monospace);letter-spacing:.1em;text-transform:uppercase;color:var(--smoke,#9aa3a0)}
  .rig-ask{display:flex;gap:7px}.rig-ask input{flex:1;min-width:0}
  #rig-advice{margin-top:10px;font-size:13.5px;line-height:1.55;white-space:pre-wrap;overflow-wrap:anywhere}
  #rig-advice small{display:block;opacity:.55;margin-top:6px}
  .gz-grid{display:grid;grid-template-columns:repeat(3,1fr);gap:12px 8px;margin-bottom:14px}
  .gz{position:relative;text-align:center}
  .gz svg{width:100%;max-width:84px;display:block;margin:0 auto}
  .gz .bgc{fill:none;stroke:rgba(255,255,255,.09);stroke-width:7}
  .gz .fgc{fill:none;stroke:var(--ok,#4ade80);stroke-width:7;stroke-linecap:round;transition:stroke-dasharray .5s,stroke .3s}
  .gz.warn .fgc{stroke:var(--warn,#fbbf24)}.gz.bad .fgc{stroke:var(--crit,#f87171)}
  .gz .gv{position:absolute;left:0;right:0;top:calc(min(84px,100%) * .5 - 11px);font:700 15px/22px var(--mono,monospace);font-variant-numeric:tabular-nums}
  .gz .gl{margin-top:4px;font:600 10px/1.3 var(--mono,monospace);letter-spacing:.08em;text-transform:uppercase;color:var(--smoke,#9aa3a0)}
  .fchip{all:unset;box-sizing:border-box;display:flex;gap:10px;align-items:flex-start;width:100%;padding:10px 12px;border-radius:12px;background:rgba(255,255,255,.04);
         border:1px solid rgba(255,255,255,.08);cursor:pointer;font-size:13px;line-height:1.4}
  .fchip i{font-style:normal;flex:none;width:22px;height:22px;border-radius:50%;display:grid;place-items:center;font-size:12px;background:rgba(255,255,255,.08)}
  .fchip.ok i{background:rgba(74,222,128,.2);color:var(--ok,#4ade80)}.fchip.info i{background:rgba(94,234,212,.18)}
  .fchip.warn i{background:rgba(251,191,36,.2)}.fchip.bad i{background:rgba(248,113,113,.22)}
  .fchip .ft{min-width:0;flex:1}
  .fchip em{font-style:normal;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}
  .fchip.open em{-webkit-line-clamp:unset}
  .fchip b{display:block;font:600 10px/1.5 var(--mono,monospace);letter-spacing:.1em;text-transform:uppercase;color:var(--smoke,#9aa3a0)}
  .rig-find{gap:8px}.rig-find li{padding:0;border:0;background:none}`;
  const st = document.createElement('style'); st.textContent = css; document.head.appendChild(st);

  const R = { data: null, err: '', timer: 0, advice: '', busy: false };

  function bar(label, pct, text, warnAt, badAt){
    if (pct == null || pct < 0) return '';
    const p = Math.max(0, Math.min(100, pct));
    const cls = p >= badAt ? 'bad' : p >= warnAt ? 'warn' : '';
    return `<div class="rig-row"><div class="lbl"><span>${esc(label)}</span><span>${esc(text)}</span></div>
      <div class="rig-track"><div class="rig-fill ${cls}" style="width:${p}%"></div></div></div>`;
  }

  /* Ring gauge: green while fine, amber then red as it climbs. Also used by the Gaming HUD drawer. */
  function gauge(label, pct, text, warnAt, badAt, icon){
    if (pct == null || pct < 0) return '';
    const p = Math.max(0, Math.min(100, pct)), cls = p >= badAt ? 'bad' : p >= warnAt ? 'warn' : 'ok', C = 2 * Math.PI * 26;
    return `<div class="gz ${cls}"><svg viewBox="0 0 64 64" aria-hidden="true"><circle class="bgc" cx="32" cy="32" r="26"/><circle class="fgc" cx="32" cy="32" r="26" stroke-dasharray="${(p / 100 * C).toFixed(1)} ${C.toFixed(1)}" transform="rotate(-90 32 32)"/></svg>
      <div class="gv">${esc(text)}</div><div class="gl">${icon || ''} ${esc(label)}</div></div>`;
  }
  window.rigGauge = gauge;

  function bodyHTML(){
    const d = R.data;
    if (!live) return `<p class="muted" style="font-size:13px">Connect to your PC to see the gaming rig.</p>`;
    if (R.err) return `<p class="muted" style="font-size:13px">${esc(R.err)}</p>`;
    if (!d) return `<p class="muted" style="font-size:13px">Reading the rig…</p>`;
    if (!d.ok) return `<p class="muted" style="font-size:13px">${esc(d.error || 'No gaming device.')}</p>`;
    const t = d.telemetry || {}, g = t.gpu, gm = d.gaming || {};
    const chip = gm.active ? `<span class="rig-chip on">Playing ${esc(gm.game)} · ${Math.round(gm.minutes)} min</span>`
                           : `<span class="rig-chip off">Not gaming</span>`;
    const gz = [];
    if (g) {
      gz.push(gauge('GPU', g.util_percent, Math.round(g.util_percent) + '%', 90, 99, '🎮'));
      gz.push(gauge('GPU heat', g.temp_c / 95 * 100, Math.round(g.temp_c) + '°', 80/95*100, 88/95*100, '🌡'));
      if (g.vram_total_mb > 0) gz.push(gauge('VRAM', g.vram_used_mb / g.vram_total_mb * 100, Math.round(g.vram_used_mb / g.vram_total_mb * 100) + '%', 85, 92, '▦'));
    }
    gz.push(gauge('CPU', t.cpu_percent, Math.round(t.cpu_percent) + '%', 85, 95, '⚙'));
    gz.push(gauge('RAM', t.ram_percent, Math.round(t.ram_percent) + '%', 85, 92, '▤'));
    if (t.temp_c != null) gz.push(gauge('Heat', t.temp_c, Math.round(t.temp_c) + '°', 80, 90, '🌡'));
    const ICON = { ok: '✓', info: 'ℹ', warn: '⚠', bad: '⛔' };
    const finds = (d.findings || []).map(f => `<li><button type="button" class="fchip ${esc(f.level)}" data-full="1"><i>${ICON[f.level] || 'ℹ'}</i><span class="ft"><b>${esc(f.area)}</b><em>${esc(f.text)}</em></span></button></li>`).join('');
    return `<div class="rig-head"><span class="rig-dev">${esc(d.device)}${g && g.name ? ' · ' + esc(g.name) : ''}</span>${chip}</div>
      <div class="gz-grid">${gz.join('')}</div><ul class="rig-find">${finds}</ul>`;
  }

  function paint(){
    const el = document.getElementById('rig-body');
    if (el) el.innerHTML = bodyHTML();
  }

  async function poll(){
    // stop when the panel is no longer on screen (another tab was opened)
    if (!document.getElementById('rig-body') || tab !== 'games') { clearInterval(R.timer); R.timer = 0; return; }
    if (!live) { paint(); return; }
    try { R.data = await api('/api/rig'); R.err = ''; } catch (e) { R.err = 'Could not read the rig: ' + (e.message || e); }
    paint();
  }

  window.rigHTML = function(){
    if (!R.timer) { R.timer = setInterval(poll, 3000); setTimeout(poll, 50); }
    return `<section class="panel"><div class="in"><h2>Gaming rig<small>live · this device only</small></h2>
      <div id="rig-body">${bodyHTML()}</div>
      <form id="rig-form" class="rig-ask"><input class="field" name="game" placeholder="Game to review (blank = what you're playing)" maxlength="80"><button class="btn hot" type="submit">Review my settings</button></form>
      <div id="rig-advice">${R.advice}</div></div></section>`;
  };

  document.addEventListener('click', e => { const c = e.target.closest('.fchip'); if (c) c.classList.toggle('open'); });

  document.addEventListener('submit', async e => {
    if (e.target.id !== 'rig-form') return;
    e.preventDefault();
    if (R.busy) return;
    R.busy = true;
    const out = document.getElementById('rig-advice');
    if (out) out.textContent = 'Looking at your numbers…';
    try {
      const r = await api('/api/rig/advice', { game: String(new FormData(e.target).get('game') || '') }, 'POST');
      R.advice = `${esc(r.advice || r.error || 'No answer.')}<small>${r.source === 'model' ? 'Lucille · ' + esc(r.model || 'model') : esc(r.note || 'from the rules')}</small>`;
    } catch (err) { R.advice = esc('Could not get a review: ' + (err.message || err)); }
    R.busy = false;
    const o2 = document.getElementById('rig-advice'); if (o2) o2.innerHTML = R.advice;
  });
})();
