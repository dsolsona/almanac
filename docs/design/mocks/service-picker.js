// Type-ahead service picker with inline creation, for the New SLO mock.
(function () {
  const services = [
    { id: 's1', name: 'Core', slug: 'core', slos: 3, attention: 2, owner: 'platform team' },
    { id: 's2', name: 'Payments', slug: 'payments', slos: 2, attention: 0, owner: 'payments team' },
    { id: 's3', name: 'Search', slug: 'search', slos: 2, attention: 1, owner: 'discovery team' },
    { id: 's4', name: 'CI', slug: 'ci', slos: 1, attention: 1, owner: 'platform team' },
    { id: 's5', name: 'Batch jobs', slug: 'batch-jobs', slos: 2, attention: 0, owner: 'data team' },
  ];
  const q = (s) => document.querySelector(s);
  const esc = (t) => t.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const slug = (t) => t.normalize('NFKD').replace(/[̀-ͯ]/g, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  const mark = (name, term) => { if (!term) return esc(name); const i = name.toLowerCase().indexOf(term.toLowerCase()); return i < 0 ? esc(name) : `${esc(name.slice(0, i))}<mark>${esc(name.slice(i, i + term.length))}</mark>${esc(name.slice(i + term.length))}`; };
  const input = q('#svc-input'), list = q('#svc-list'), selected = q('#svc-selected'), field = q('#svc-field');
  let active = -1, matches = [];
  const attention = (s) => s.attention ? `<span class="st warn" style="font-weight:500;color:var(--text-3)"><i></i>${s.attention} need attention</span>` : `<span class="st good" style="font-weight:500;color:var(--text-3)"><i></i>all healthy</span>`;
  function render(term) {
    matches = services.filter((s) => !term || `${s.name} ${s.slug} ${s.owner}`.toLowerCase().includes(term.toLowerCase()));
    const exact = services.some((s) => s.name.toLowerCase() === (term || '').toLowerCase());
    const rows = matches.map((s, i) => `<li role="option" aria-selected="${i === active}" data-id="${s.id}">
        <span class="st good" style="color:var(--text-3)"><i style="border:2px solid currentColor;background:none;border-radius:2px"></i></span><span class="nm">${mark(s.name, term)} <span class="pill">${s.slug}</span></span>${attention(s)}
        <span class="sm">${s.slos} SLO${s.slos === 1 ? '' : 's'} · ${esc(s.owner)}</span></li>`);
    if (term && !exact) rows.push(`<li role="option" aria-selected="${active === matches.length}" data-create="${esc(term)}" style="border-top:1px solid var(--border);margin-top:4px">
        <span class="st" style="color:var(--accent-ink)"><i style="border:0;background:none;width:11px;text-align:center;line-height:11px">+</i></span><span class="nm" style="color:var(--accent-ink)">Create service “${esc(term)}”</span><span></span>
        <span class="sm">identifier ${esc(slug(term))} · created when you save this SLO</span></li>`);
    list.innerHTML = rows.join('') || '<li class="none">No services yet. Type a name to create one.</li>';
    list.hidden = false; input.setAttribute('aria-expanded', 'true');
  }
  function close() { list.hidden = true; input.setAttribute('aria-expanded', 'false'); active = -1; }
  function card(s, isNew) {
    return `<div class="dscard ${isNew ? '' : ''}">
      <span class="st ${isNew ? 'none' : 'good'}" style="color:var(--text-2)"><i style="${isNew ? '' : 'border:2px solid currentColor;background:none;border-radius:2px'}"></i>${isNew ? 'New service' : 'Service'}</span>
      <span class="nm">${esc(s.name)} <span class="pill">${esc(s.slug)}</span></span>
      <button type="button" class="change" data-change>Change</button>
      <span class="sm">${isNew ? 'Will be created with this SLO · you can rename it later from the service page' : `${s.slos} SLO${s.slos === 1 ? '' : 's'} · ${s.attention ? `${s.attention} need attention` : 'all healthy'} · ${esc(s.owner)}`}</span>
    </div>`;
  }
  function choose(idOrName, isNew) {
    const s = isNew ? { name: idOrName, slug: slug(idOrName) } : services.find((x) => x.id === idOrName);
    if (!s) return;
    selected.innerHTML = card(s, isNew); selected.hidden = false; field.hidden = true; close();
    selected.querySelector('[data-change]').addEventListener('click', () => { field.hidden = false; selected.hidden = true; input.value = ''; input.focus(); render(''); });
  }
  input.addEventListener('focus', () => render(input.value));
  input.addEventListener('input', () => { active = -1; render(input.value); });
  input.addEventListener('keydown', (e) => {
    const total = list.querySelectorAll('[role=option]').length;
    if (e.key === 'ArrowDown') { e.preventDefault(); active = Math.min(total - 1, active + 1); render(input.value); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); active = Math.max(0, active - 1); render(input.value); }
    else if (e.key === 'Enter' && active >= 0) { e.preventDefault(); const li = list.querySelectorAll('[role=option]')[active]; li.dataset.create ? choose(li.dataset.create, true) : choose(li.dataset.id); }
    else if (e.key === 'Escape') close();
  });
  list.addEventListener('mousedown', (e) => { const li = e.target.closest('li[role=option]'); if (!li) return; e.preventDefault(); li.dataset.create ? choose(li.dataset.create, true) : choose(li.dataset.id); });
  document.addEventListener('click', (e) => { if (!e.target.closest('.combo')) close(); });
  const p = new URLSearchParams(location.search), st = p.get('service');
  if (st === 'typing') { input.value = p.get('term') ?? 'pay'; input.focus(); render(input.value); }
  else if (st === 'new') { input.value = p.get('term') ?? 'Fulfilment'; input.focus(); render(input.value); }
  else if (st === 'selected') choose(p.get('id') ?? 's2');
  else if (st === 'created') choose(p.get('term') ?? 'Fulfilment', true);
})();
