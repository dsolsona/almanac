// Interactive datasource picker for the New SLI mock. Two variants: A type-ahead combobox, B selectable cards.
(function () {
  const sources = [
    { id: 'ds1', name: 'local-victoriametrics', provider: 'Prometheus', cadence: '60 s', agent: 'Local', agentState: 'connected', health: 'good', healthLabel: 'Healthy', note: '9 SLIs collect here · last query 40 s ago' },
    { id: 'ds2', name: 'prod-prometheus-eu', provider: 'Prometheus', cadence: '30 s', agent: 'eu-west-1', agentState: 'connected', health: 'good', healthLabel: 'Healthy', note: '14 SLIs collect here · last query 12 s ago' },
    { id: 'ds3', name: 'prod-prometheus-us', provider: 'Prometheus', cadence: '30 s', agent: 'us-east-1', agentState: 'unreachable for 2 h', health: 'warn', healthLabel: 'Agent unreachable', note: 'New SLIs will queue until the agent reconnects' },
    { id: 'ds4', name: 'staging-mimir', provider: 'Prometheus', cadence: '60 s', agent: null, agentState: 'no agent assigned', health: 'none', healthLabel: 'No agent', note: 'Assign an agent on the datasource page before collecting', disabled: true },
  ];
  const q = (s) => document.querySelector(s);
  const esc = (t) => t.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c]));
  const mark = (name, term) => { if (!term) return esc(name); const i = name.toLowerCase().indexOf(term.toLowerCase()); return i < 0 ? esc(name) : `${esc(name.slice(0, i))}<mark>${esc(name.slice(i, i + term.length))}</mark>${esc(name.slice(i + term.length))}`; };
  const stMark = (h) => `<span class="st ${h}"><i></i></span>`;
  const selectedCard = (d) => `<div class="dscard ${d.health === 'warn' ? 'warn' : ''}">
      <span class="st ${d.health}"><i></i>${d.healthLabel}</span>
      <span class="nm">${esc(d.name)} <span class="pill">${d.provider}</span></span>
      <button type="button" class="change" data-change>Change</button>
      <span class="sm">${d.cadence} intervals · settles 60 s · agent ${d.agent ? `<b>${esc(d.agent)}</b> ${d.agentState}` : d.agentState} · ${esc(d.note)}</span>
      ${d.health === 'warn' ? '<p class="msg">You can still create the SLI. The first result arrives when the agent is back; nothing is lost in between.</p>' : ''}
    </div>`;

  // ---- Variant A
  const input = q('#ds-input'), list = q('#ds-list'), selected = q('#ds-selected'), field = q('#ds-field');
  let active = -1, matches = [];
  function render(term) {
    matches = sources.filter((d) => !term || `${d.name} ${d.provider} ${d.agent ?? ''}`.toLowerCase().includes(term.toLowerCase()));
    list.innerHTML = matches.length ? matches.map((d, i) => `<li role="option" id="opt-${d.id}" aria-selected="${i === active}" class="${d.disabled ? 'off' : ''}" data-id="${d.id}">
        ${stMark(d.health)}<span class="nm">${mark(d.name, term)}</span><span class="st ${d.health}" style="color:var(--text-3);font-weight:500">${d.healthLabel}</span>
        <span class="sm">${d.provider} · ${d.cadence} cadence · ${d.agent ? `agent ${esc(d.agent)} · ${d.agentState}` : d.agentState}</span></li>`).join('')
      : `<li class="none">No datasource matches “${esc(term)}”. <a href="#" style="color:var(--accent-ink);text-decoration:underline">Add a datasource</a></li>`;
    list.hidden = false; input.setAttribute('aria-expanded', 'true');
  }
  function close() { list.hidden = true; input.setAttribute('aria-expanded', 'false'); active = -1; }
  function choose(id) {
    const d = sources.find((x) => x.id === id); if (!d) return;
    selected.innerHTML = selectedCard(d); selected.hidden = false; field.hidden = true; close();
    selected.querySelector('[data-change]').addEventListener('click', () => { field.hidden = false; selected.hidden = true; input.value = ''; input.focus(); render(''); });
  }
  input.addEventListener('focus', () => render(input.value));
  input.addEventListener('input', () => { active = -1; render(input.value); });
  input.addEventListener('keydown', (e) => {
    if (list.hidden && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) { render(input.value); return; }
    if (e.key === 'ArrowDown') { e.preventDefault(); active = Math.min(matches.length - 1, active + 1); render(input.value); }
    else if (e.key === 'ArrowUp') { e.preventDefault(); active = Math.max(0, active - 1); render(input.value); }
    else if (e.key === 'Enter' && active >= 0) { e.preventDefault(); choose(matches[active].id); }
    else if (e.key === 'Escape') close();
  });
  list.addEventListener('mousedown', (e) => { const li = e.target.closest('li[data-id]'); if (li) { e.preventDefault(); choose(li.dataset.id); } });
  document.addEventListener('click', (e) => { if (!e.target.closest('.combo')) close(); });

  // ---- Variant B
  const cards = q('#ds-cards'), selectedB = q('#ds-selected-b');
  function renderCards(current) {
    cards.innerHTML = sources.map((d) => `<label class="choice ${current === d.id ? 'sel' : ''} ${d.disabled ? 'off' : ''}" data-id="${d.id}">
        <span class="t"><span class="rad"></span>${esc(d.name)}<span class="st ${d.health}"><i></i>${d.healthLabel}</span></span>
        <span class="meta">${d.provider} · ${d.cadence} cadence · ${d.agent ? `agent ${esc(d.agent)} · ${d.agentState}` : d.agentState}</span></label>`).join('');
    const d = sources.find((x) => x.id === current);
    selectedB.hidden = !d; if (d) { selectedB.innerHTML = `<p class="hint" style="margin:10px 0 0;font-size:12px;color:var(--text-2)">${esc(d.note)}.${d.health === 'warn' ? ' You can still create the SLI; the first result arrives when the agent is back.' : ''}</p>`; }
  }
  cards.addEventListener('click', (e) => { e.preventDefault(); const c = e.target.closest('[data-id]'); if (c && !c.classList.contains('off')) renderCards(c.dataset.id); });
  renderCards(null);

  // ---- variant toggle and URL-driven state for screenshots
  const params = new URLSearchParams(location.search);
  function setVariant(v) { q('#variant-a').hidden = v !== 'a'; q('#variant-b').hidden = v !== 'b'; document.querySelectorAll('[data-variant]').forEach((b) => b.setAttribute('aria-pressed', String(b.dataset.variant === v))); }
  document.querySelectorAll('[data-variant]').forEach((b) => { b.type = 'button'; b.addEventListener('click', (e) => { e.preventDefault(); setVariant(b.dataset.variant); }); });
  setVariant(params.get('variant') === 'b' ? 'b' : 'a');
  const state = params.get('picker');
  if (state === 'typing') { input.value = params.get('term') ?? 'prod'; input.focus(); render(input.value); }
  else if (state === 'selected') choose(params.get('id') ?? 'ds1');
  else if (state === 'warn') choose('ds3');
  else if (state === 'cards') renderCards(params.get('id') ?? 'ds3');
})();
