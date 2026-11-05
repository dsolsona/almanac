// Preset chips with a Custom… reveal, for target and window on the SLO form.
(function () {
  function wire(groupId, customId, hintId, format, describe) {
    const group = document.getElementById(groupId), custom = document.getElementById(customId), hint = document.getElementById(hintId);
    const input = custom.querySelector('input'), customChip = group.querySelector('[data-custom]');
    let customValue = null;
    function select(btn) { group.querySelectorAll('[role=radio]').forEach((b) => b.setAttribute('aria-checked', String(b === btn))); }
    function setHint(value) { if (hint && value !== null) hint.innerHTML = describe(value); }
    group.addEventListener('click', (e) => {
      const btn = e.target.closest('[role=radio]'); if (!btn) return;
      select(btn);
      if (btn.dataset.custom !== undefined) { custom.hidden = false; input.focus(); if (customValue !== null) input.value = customValue; }
      else { custom.hidden = true; setHint(btn.dataset.v); }
    });
    function apply(collapse) {
      const v = input.value.trim();
      customValue = v || null; customChip.textContent = v ? `Custom · ${format(v)}` : 'Custom…'; if (v) setHint(v);
      if (collapse && v) custom.hidden = true;
    }
    input.addEventListener('input', () => apply(false));
    input.addEventListener('blur', () => apply(true));
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { e.preventDefault(); apply(true); input.blur(); } });
  }
  wire('target-chips', 'target-custom', 'target-hint', (v) => `${v}%`, (v) => `Share of events that must be good over the window. Error budget is the rest: <b>${(100 - Number(v)).toFixed(Math.max(1, (String(v).split('.')[1] || '').length))}%</b>.`);
  wire('window-chips', 'window-custom', 'window-hint', (v) => `${v} days`, (v) => `The window rolls forward continuously; every evaluation covers the last <b>${v} days</b>.`);
  const p = new URLSearchParams(location.search), st = p.get('chips');
  if (st === 'custom-open') { document.querySelector('#target-chips [data-custom]').click(); }
  if (st === 'custom-set') { document.querySelector('#target-chips [data-custom]').click(); const i = document.querySelector('#target-custom input'); i.value = '99.7'; i.dispatchEvent(new Event('input')); i.dispatchEvent(new Event('blur')); document.querySelector('#window-chips [data-custom]').click(); const w = document.querySelector('#window-custom input'); w.value = '14'; w.dispatchEvent(new Event('input')); w.dispatchEvent(new Event('blur')); }
})();
