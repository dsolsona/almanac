// Fictional, fixed one-hour fixture shared by all proposals. No application API calls.
document.documentElement.classList.toggle('dark', new URLSearchParams(location.search).get('theme') === 'dark');
const directions = {
  overview: ['A', 'At a glance', 'A compact answer to “what am I looking at, and does anything need attention?” Balanced for everyday use.'],
  trend: ['B', 'Trend first', 'Give the measurements the most space. Best for spotting a change; the collection trail stays one click away.'],
  objectives: ['C', 'SLO impact first', 'Lead with the objectives that rely on this indicator. Best for a service owner deciding where to go next.'],
  investigate: ['D', 'Guided investigation', 'Read the page as three questions: what is measured, what changed, and can I trust the data? Best for occasional users.'],
  refined: ['E', 'Trend + collection', 'B’s chart-led layout with D’s interval states, an integrated latest result, and no separate result card. Compare directly with B.'],
};
const rows = Array.from({ length:60 }, (_, i) => {
  const outcome = i === 52 ? 'missing' : i === 43 || i === 44 ? 'failed' : 'observed';
  const good = i === 59 ? 1998 : i >= 27 && i <= 31 ? [1940, 1880, 1850, 1910, 1960][i - 27] : 1998 - [0, 1, 0, 2, 0, 1][i % 6];
  return { start:`09:${String(i).padStart(2, '0')}`, end:i === 59 ? '10:00' : `09:${String(i + 1).padStart(2, '0')}`, outcome, good:outcome === 'observed' ? good : null, total:outcome === 'observed' ? 2000 : null };
});
const observed = rows.filter(row => row.outcome === 'observed');
const goodCount = observed.reduce((sum, row) => sum + row.good, 0);
const totalCount = observed.length * 2000;
const format = value => value.toLocaleString('en-US');
const ratio = (good, total) => `${(100 * good / total).toFixed(2)}%`;
const hourRatio = ratio(goodCount, totalCount);
const latestRatio = ratio(rows[59].good, rows[59].total);
const status = (tone, label) => `<span class="st ${tone}"><i aria-hidden="true"></i>${label}</span>`;
const openButton = (target, text) => `<button class="btn quiet" data-open="${target}">${text}</button>`;
const review = key => `<aside class="review" aria-label="Mockup review controls"><div class="review-top"><span class="eyebrow">SLI detail / design proposals / sample data</span><label>Theme <select id="theme"><option value="light">Light</option><option value="dark">Dark</option></select></label></div><nav class="directions" aria-label="Design directions">${Object.entries(directions).map(([id, [letter, name]]) => `<a href="#${id}" ${key === id ? 'aria-current="page"' : ''}>${letter} · ${name}</a>`).join('')}</nav><p class="review-note">${directions[key][2]}</p></aside>`;
const heading = () => `<nav class="crumbs" aria-label="Breadcrumb"><a href="../sli-list.html">SLIs</a><span aria-hidden="true">/</span>Checkout request success</nav><header class="detail-head"><div><h1>Checkout request success</h1><div class="status-line">${status('good', 'Up to date')}<span>Latest interval ended 10:00 UTC · 1 min ago</span></div></div><div class="actions">${openButton('definition', 'View queries')}<a class="btn" href="../slo-create.html">New SLO</a></div></header>`;
const meaning = () => `<p class="definition-line"><strong>Successful checkout requests ÷ all checkout requests.</strong> HTTP 5xx responses count as unsuccessful. Measured every minute.</p>`;
const coverage = () => `<div class="coverage" role="img" aria-label="60 expected intervals: 57 observed, two query errors, one missing">${rows.map(row => `<i class="${row.outcome}" title="${row.start}–${row.end} UTC · ${row.outcome}"></i>`).join('')}</div><div class="coverage-legend"><span>57 observed</span><span class="failed">2 query errors</span><span class="missing">1 missing</span></div>`;
function graph(large = false, volume = false, states = false) {
  const mobile = window.matchMedia('(max-width:700px)').matches;
  const width = mobile ? 320 : 980;
  const left = mobile ? 44 : 48, right = width - (mobile ? 8 : 30), top = 18, bottom = large ? 222 : 150;
  const markWidth = Math.max(2, (right - left) / 60 - 2);
  const x = i => left + i * (right - left) / 59;
  const y = value => top + (100 - value) / 8 * (bottom - top);
  const segments = [];
  let segment = [];
  rows.forEach((row, i) => {
    if (row.outcome !== 'observed') { if (segment.length) segments.push(segment); segment = []; return; }
    segment.push([x(i), y(100 * row.good / row.total)]);
  });
  if (segment.length) segments.push(segment);
  const volumeBottom = bottom + (volume ? 66 : states ? 48 : 0);
  return `<svg class="chart" viewBox="0 0 ${width} ${volumeBottom + 42}" role="img" aria-label="One-minute good-event ratio from 09:00 to 10:00 UTC, 11 November 2026. Lowest observed value 92.50% at 09:29–09:30. Query errors at 09:43 and 09:44, and no observation at 09:52, appear as gaps. Exact values are in Recent intervals.">
    ${[100, 98, 96, 94, 92].map(value => `<line class="grid" x1="${left}" x2="${right}" y1="${y(value)}" y2="${y(value)}"/><text x="35" y="${y(value) + 4}" text-anchor="end">${value}%</text>`).join('')}
    ${rows.map((row, i) => row.outcome === 'observed' ? '' : `<rect class="gap" x="${x(i) - markWidth / 2}" y="${top}" width="${markWidth}" height="${bottom - top}"/>`).join('')}
    ${segments.map(points => `<path class="area" d="M${points[0][0]},${bottom} ${points.map(([px, py]) => `L${px},${py}`).join(' ')} L${points.at(-1)[0]},${bottom} Z"/><path class="line" d="${points.map(([px, py], i) => `${i ? 'L' : 'M'}${px},${py}`).join(' ')}"/>`).join('')}
    ${states ? rows.map((row, i) => row.good === null ? '' : `<circle class="chart-point" data-point="${i}" cx="${x(i)}" cy="${y(100 * row.good / row.total)}" r="2.5"/>`).join('') : ''}
    ${volume ? `<text x="${left}" y="${bottom + 23}">${mobile ? 'Requests · 2,000 / minute' : 'Total requests · 2,000 per observed minute'}</text>${rows.map((row, i) => row.outcome === 'observed' ? `<rect class="volume" x="${x(i) - markWidth / 2}" y="${bottom + 34}" width="${markWidth}" height="26"/>` : '').join('')}` : ''}
    ${states ? `<g class="interval-states"><text x="${left}" y="${bottom + 22}">${mobile ? 'Collection · one square / min' : 'Collection · one square per minute'}</text>${rows.map((row, i) => { const size = Math.min(12, markWidth); return `<rect class="interval-state ${row.outcome}" x="${x(i) - size / 2}" y="${bottom + 31}" width="${size}" height="${size}"><title>${row.start}–${row.end} UTC · ${row.outcome === 'failed' ? 'Query error' : row.outcome === 'missing' ? 'Missing' : 'Observed'}${row.good === null ? ' · no ratio' : ` · ${ratio(row.good, row.total)}`}</title></rect>`; }).join('')}</g>` : ''}
    ${states ? `<g class="inspection-cursor" data-left="${left}" data-right="${right}" hidden><line class="inspection-line" y1="${top}" y2="${bottom + 43}"/><circle class="active-dot" r="5"/></g>` : ''}
    ${(mobile ? [0, 30, 59] : [0, 15, 30, 45, 59]).map(i => `<text x="${x(i)}" y="${volumeBottom + 27}" text-anchor="${i === 0 ? 'start' : i === 59 ? 'end' : 'middle'}">${i === 59 ? '10:00 UTC' : rows[i].start}</text>`).join('')}
  </svg>`;
}
const chart = (large = false, volume = false) => `<section class="card"><header><h2>Measurements</h2><span class="hint">11 Nov 2026 · 09:00–10:00 UTC</span><div class="tools">${openButton('intervals', 'Exact values ↓')}</div></header><div class="chart-body">${large ? `<div class="chart-meta"><div><strong>${hourRatio}</strong> <span>of observed requests successful</span></div><div><strong>92.50%</strong> <span>lowest minute</span></div><span>57 of 60 intervals observed</span></div>` : ''}${graph(large, volume)}</div><div class="chart-caption"><span><i class="swatch"></i>Good ÷ total per minute</span><span><i class="swatch gap"></i>No ratio available</span><span>Axis 92–100% · no SLO threshold</span></div></section>`;
const objectivesTable = () => `<div class="table-wrap"><table class="objective-table"><thead><tr><th scope="col">SLO</th><th scope="col">Target · rolling window</th><th scope="col">Evaluation</th></tr></thead><tbody><tr><td><a class="text-link" href="../slo-detail.html">Checkout availability</a><span class="desc">Checkout · 99.9% over 30 days</span></td><td class="mono">99.9% · 30 d</td><td>${status('warn', 'Partial data')}</td></tr><tr><td><a class="text-link" href="../slo-detail.html">Checkout weekly success</a><span class="desc">Checkout · 99.5% over 7 days</span></td><td class="mono">99.5% · 7 d</td><td>${status('warn', 'Partial data')}</td></tr></tbody></table></div>`;
const objectives = () => `<section class="card" id="linked-objectives"><header><h2>Used by 2 SLOs</h2><span class="hint">Targets belong to each SLO</span></header>${objectivesTable()}<p class="inline-note">Both windows include the three unobserved intervals. Open an SLO for its budget and burn rate.</p></section>`;
const issues = () => `<div class="issue-note"><span><strong>Three intervals have no ratio.</strong> This hour’s result uses observed requests only.</span>${openButton('intervals', 'Inspect gaps ↓')}</div>`;
const definition = () => `<details class="disclosure" id="definition"><summary>Measurement definition<span>Source, queries and collection timing</span></summary><div class="source-line"><span>Datasource: <a class="text-link" href="../ds-detail.html">Production Prometheus</a></span><span>Agent: <a class="text-link" href="../agent-detail.html">EU collector</a></span><span>60 s intervals · 60 s settling delay</span></div><div class="queries"><div><h3>Good events</h3><p class="small">Checkout requests without an HTTP 5xx response</p><pre>sum(increase(http_requests_total{service="checkout",status!~"5.."}[1m]))</pre></div><div><h3>Total events</h3><p class="small">All checkout requests</p><pre>sum(increase(http_requests_total{service="checkout"}[1m]))</pre></div><p class="small">Queries return event counts for each completed minute. A 60-second delay allows source data to settle.</p></div></details>`;
const collection = () => `<section class="card" id="collection"><header><h2>Collection</h2><span class="hint">Current state and this hour’s gaps are separate</span></header><dl class="collection-facts"><div><dt>Assigned agent</dt><dd><a class="text-link" href="../agent-detail.html">EU collector</a> · connected</dd></div><div><dt>Configuration</dt><dd>Active</dd></div><div><dt>Collection progress</dt><dd>Running</dd></div><div><dt>Observation freshness</dt><dd>Fresh · ended 10:00</dd></div></dl><div class="body">${coverage()}</div><ul class="event-list"><li><time>09:43–09:45</time><div><strong>Two queries timed out</strong><p>No counts recorded for these minutes. Collection resumed at 09:45. Check the <a class="text-link" href="../ds-detail.html">datasource</a> if this recurs.</p></div></li><li><time>09:52–09:53</time><div><strong>One interval is missing</strong><p>No observation was received. The cause is unknown; an online agent alone does not prove collection succeeded.</p></div></li></ul><p class="inline-note">Last attempt 10:01:20 UTC · next query 10:02:20 UTC</p></section>`;
const intervals = () => `<details class="disclosure" id="intervals"><summary>Recent intervals<span>60 expected · 57 observed · 3 without a ratio · exact values</span></summary><div class="interval-filter" aria-label="Filter intervals"><button class="btn sm" data-filter="all" aria-pressed="true">All 60</button><button class="btn sm" data-filter="problems" aria-pressed="false">Errors & missing 3</button><span class="small">Newest first · event counts per minute</span></div><div class="table-wrap" tabindex="0" role="region" aria-label="Scrollable interval values"><table class="intervals"><caption>11 Nov 2026 · UTC · Observed means counts are available, not that an SLO target was met.</caption><thead><tr><th scope="col">Interval (UTC)</th><th scope="col">Outcome</th><th scope="col">Good</th><th scope="col">Total</th><th scope="col">Good ÷ total</th><th scope="col">Details</th></tr></thead><tbody>${[...rows].reverse().map(row => `<tr data-outcome="${row.outcome}"><td class="mono">${row.start}–${row.end}</td><td>${status(row.outcome === 'observed' ? 'good' : row.outcome === 'failed' ? 'warn' : 'none', row.outcome === 'observed' ? 'Observed' : row.outcome === 'failed' ? 'Query error' : 'Missing')}</td><td class="mono">${row.good === null ? '—' : format(row.good)}</td><td class="mono">${row.total === null ? '—' : format(row.total)}</td><td class="mono">${row.good === null ? '—' : ratio(row.good, row.total)}</td><td>${row.outcome === 'failed' ? 'Good query timed out' : row.outcome === 'missing' ? 'No observation received' : '—'}</td></tr>`).join('')}</tbody></table></div></details>`;
const summary = () => `<dl class="summary-strip"><div class="stat"><dt>Latest minute</dt><dd>${latestRatio}</dd><p>${format(rows[59].good)} / 2,000 good · 09:59–10:00 UTC</p></div><div class="stat"><dt>Observed requests · this hour</dt><dd>${hourRatio}</dd><p>${format(totalCount - goodCount)} unsuccessful / ${format(totalCount)} total</p></div><div class="stat"><dt>Data coverage · this hour</dt><dd class="warm">57 <small>/ 60 min</small></dd><p>2 query errors · 1 missing</p></div><div class="stat"><dt>Objectives using this SLI</dt><dd>2 <small>SLOs</small></dd><p><a class="text-link" href="#" data-scroll="linked-objectives">Both have partial data ↓</a></p></div></dl>`;
function overview() { return `${meaning()}<div class="stack">${summary()}${issues()}${chart()}${objectives()}${intervals()}${definition()}<details class="disclosure"><summary>Collection diagnostics<span>Agent connected · running · fresh</span></summary>${collection()}</details></div>`; }
function trend() { return `${meaning()}<div class="stack">${chart(true, true)}${issues()}<section class="card"><header><h2>Latest measurement</h2><span class="hint">09:59–10:00 UTC</span></header><div class="lead-result"><div><div class="number">${latestRatio}</div><p class="small">${format(rows[59].good)} of 2,000 requests successful</p></div><div class="explanation"><strong>The ratio returned to 99.90% in the latest minute.</strong><p>The lowest minute was 92.50% at 09:29–09:30. This compares observations; SLOs evaluate their own rolling windows.</p></div></div></section>${objectives()}${intervals()}${definition()}<details class="disclosure"><summary>Collection diagnostics<span>57 of 60 intervals observed</span></summary>${collection()}</details></div>`; }
function objectivesFirst() { return `${meaning()}<div class="stack"><section class="card" id="linked-objectives"><header><h2>Objectives relying on this measurement</h2><span class="hint">Checkout</span></header><div class="objective-intro"><strong class="warm">2</strong><div><p><strong>SLOs have incomplete measurement coverage.</strong></p><p>Three minutes are unobserved. Both objectives need this indicator, so neither can report complete-window performance.</p></div></div>${objectivesTable()}<p class="inline-note">Open an SLO for observed attainment, remaining budget and burn. Those values belong to its rolling window.</p></section><section class="card"><div class="lead-result"><div><p class="small">Latest minute · 09:59–10:00 UTC</p><div class="number">${latestRatio}</div></div><div class="explanation"><strong>${format(rows[59].good)} of 2,000 requests successful</strong><p>The agent is connected and collection is running. Recent delivery does not fill the earlier gaps.</p></div></div></section>${chart()}${intervals()}${definition()}<details class="disclosure"><summary>Collection diagnostics<span>2 query errors · 1 missing interval</span></summary>${collection()}</details></div>`; }
function investigate() { return `<div class="stack"><section class="reading-section"><div class="reading-heading"><span class="step">01</span><h2>What does this measure?</h2></div><p class="reading-copy">The share of <strong>checkout requests that avoid an HTTP 5xx response</strong>. Each result covers one minute. In the latest minute, <strong>${format(rows[59].good)} of 2,000 requests were successful (${latestRatio})</strong>.</p><p class="small" style="margin-top:8px">09:59–10:00 UTC · Production Prometheus · ${openButton('definition', 'Read the queries ↓')}</p></section><section class="reading-section"><div class="reading-heading"><span class="step">02</span><h2>What happened this hour?</h2></div><div class="investigation-tabs" role="tablist" aria-label="Investigation"><button id="tab-measurements" role="tab" aria-selected="true" aria-controls="panel-measurements" data-tab="measurements">Measurements</button><button id="tab-collection" role="tab" aria-selected="false" aria-controls="panel-collection" tabindex="-1" data-tab="collection">Collection · 3 gaps</button></div><div id="panel-measurements" role="tabpanel" aria-labelledby="tab-measurements"><p class="reading-copy" style="margin:16px 0"><strong>${hourRatio} of observed requests succeeded.</strong> The lowest minute was 92.50%. Three intervals have no ratio and are excluded from this figure.</p>${chart()}</div><div id="panel-collection" role="tabpanel" aria-labelledby="tab-collection" hidden style="margin-top:16px">${collection()}</div></section><section class="reading-section"><div class="reading-heading"><span class="step">03</span><h2>Can I judge reliability from this?</h2></div><p class="reading-copy"><strong>The measurements alone do not set a target.</strong> Both linked SLOs include the gaps in their windows and show Partial data. Open an objective to see its observed performance and budget.</p><div style="margin-top:16px">${objectives()}</div></section>${intervals()}${definition()}</div>`; }
function refined() {
  return `${meaning()}<div class="stack">
    <section class="card refined-chart">
      <header><h2>Measurements</h2><span class="hint">11 Nov 2026 · 09:00–10:00 UTC</span><div class="tools">${openButton('intervals', 'Exact values ↓')}</div></header>
      <div class="chart-body">
        <dl class="refined-summary">
          <div><dt>Latest minute</dt><dd>${latestRatio}</dd><p>${format(rows[59].good)} / 2,000 good · 09:59–10:00 UTC</p></div>
          <div><dt>Observed requests · this hour</dt><dd>${hourRatio}</dd><p>${format(totalCount - goodCount)} unsuccessful / ${format(totalCount)} total</p></div>
          <div><dt>Data coverage · this hour</dt><dd class="warm">57 <small>/ 60 min</small></dd><p>2 query errors · 1 missing</p></div>
        </dl>
        <div class="inspectable-chart" role="group" tabindex="0" aria-label="Interactive measurements. Hover or tap to inspect. Use left and right arrow keys to move between minutes, Home and End for the first and last, Escape to dismiss." aria-describedby="chart-selection">
          ${graph(true, false, true)}
          <div class="chart-tooltip" role="tooltip" hidden></div>
          <span class="chart-selection" id="chart-selection" aria-live="polite"></span>
        </div>
        <p class="chart-help small">Hover or tap to inspect · keyboard: ← → between minutes</p>
      </div>
      <div class="chart-caption interval-legend"><span><i class="state-key observed"></i>57 observed</span><span><i class="state-key failed"></i>2 query errors</span><span><i class="state-key missing"></i>1 missing</span><span>Observed means counts received, not an SLO verdict.</span></div>
      <div class="inline-note"><span>Lowest minute <strong class="mono">92.50%</strong> · 09:29–09:30 UTC. Ratios use observed requests only.</span>${openButton('intervals', 'Inspect gaps ↓')}</div>
    </section>
    ${objectives()}${intervals()}${definition()}
    <details class="disclosure"><summary>Collection diagnostics<span>Agent connected · running · fresh</span></summary>${collection()}</details>
  </div>`;
}
function render() {
  const key = Object.hasOwn(directions, location.hash.slice(1)) ? location.hash.slice(1) : document.body.dataset.direction || 'overview';
  document.title = `SLI detail · ${directions[key][0]} · ${directions[key][1]}`;
  document.getElementById('content').innerHTML = `${review(key)}${heading()}${({ overview, trend, objectives:objectivesFirst, investigate, refined })[key]()}<footer class="footer-note">Proposal ${directions[key][0]} · Fictional data at 10:01 UTC, 11 Nov 2026 · <button class="btn quiet sm" data-support>Support identifiers</button></footer><dialog class="support-dialog"><h2>Support identifiers</h2><p class="small">For API, CLI and support use. These do not describe measurement quality.</p><p>Identifier: <code>checkout-request-success</code></p><p>Measurement revision: <code>1</code></p><p>SLI ID: <code>00000000-0000-4000-8000-000000000001</code></p><form method="dialog"><button class="btn">Close</button></form></dialog>`;
  document.getElementById('theme').value = document.documentElement.classList.contains('dark') ? 'dark' : 'light';
}
render();
window.addEventListener('hashchange', () => { if (Object.hasOwn(directions, location.hash.slice(1))) { render(); window.scrollTo(0, 0); } });
window.matchMedia('(max-width:700px)').addEventListener('change', render);
document.addEventListener('change', event => {
  if (event.target.id !== 'theme') return;
  const dark = event.target.value === 'dark';
  document.documentElement.classList.toggle('dark', dark);
  const url = new URL(location.href);
  if (dark) url.searchParams.set('theme', 'dark'); else url.searchParams.delete('theme');
  history.replaceState(null, '', url);
});
function selectTab(tab) {
  document.querySelectorAll('[data-tab]').forEach(button => { const active = button === tab; button.setAttribute('aria-selected', String(active)); button.tabIndex = active ? 0 : -1; document.getElementById(`panel-${button.dataset.tab}`).hidden = !active; });
}
document.addEventListener('click', event => {
  const open = event.target.closest('[data-open],[data-scroll]');
  if (open) {
    event.preventDefault();
    const target = document.getElementById(open.dataset.open || open.dataset.scroll);
    if (target instanceof HTMLDetailsElement) target.open = true;
    target.scrollIntoView({ block:'start' });
    const focus = target.querySelector('summary,h2');
    focus.tabIndex = -1;
    focus.focus({ preventScroll:true });
  }
  const filter = event.target.closest('[data-filter]');
  if (filter) { document.querySelectorAll('[data-filter]').forEach(button => button.setAttribute('aria-pressed', String(button === filter))); document.querySelectorAll('[data-outcome]').forEach(row => { row.hidden = filter.dataset.filter === 'problems' && row.dataset.outcome === 'observed'; }); }
  const tab = event.target.closest('[data-tab]');
  if (tab) selectTab(tab);
  if (event.target.closest('[data-support]')) document.querySelector('dialog').showModal();
});
document.addEventListener('keydown', event => {
  if (!event.target.matches('[data-tab]') || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  const tabs = [...document.querySelectorAll('[data-tab]')];
  const next = event.key === 'Home' ? tabs[0] : event.key === 'End' ? tabs.at(-1) : tabs[(tabs.indexOf(event.target) + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length];
  selectTab(next); next.focus();
});
// The review's theme control replaces the old mock shell's theme button.
document.addEventListener('DOMContentLoaded', () => {
  const themeButton = document.querySelector('.account button');
  themeButton?.remove();
  const routes = ['../slo-list.html', '../sli-list.html', '../services/index.html', '../ds-list.html', '../agents-list.html'];
  document.querySelectorAll('.sidebar nav a').forEach((link, i) => { link.href = routes[i]; });
  document.querySelector('.account button')?.remove();
  const menu = document.querySelector('.mobile-head button');
  menu.setAttribute('aria-expanded', 'false');
  menu.addEventListener('click', () => {
    const open = menu.getAttribute('aria-expanded') !== 'true';
    menu.setAttribute('aria-expanded', String(open));
    document.querySelector('.sidebar').classList.toggle('mock-menu-open', open);
  });
});

function dismissInspection(frame) {
  frame.querySelector('.chart-tooltip').hidden = true;
  frame.querySelector('.inspection-cursor').setAttribute('hidden', '');
}
function inspectInterval(frame, index, clientY) {
  const row = rows[index];
  frame.dataset.index = index;
  const cursor = frame.querySelector('.inspection-cursor');
  const point = frame.querySelector(`[data-point="${index}"]`);
  const left = Number(cursor.dataset.left), right = Number(cursor.dataset.right);
  const x = left + index * (right - left) / (rows.length - 1);
  cursor.removeAttribute('hidden');
  cursor.querySelector('line').setAttribute('x1', x);
  cursor.querySelector('line').setAttribute('x2', x);
  const dot = cursor.querySelector('circle');
  dot.toggleAttribute('hidden', !point);
  if (point) { dot.setAttribute('cx', point.getAttribute('cx')); dot.setAttribute('cy', point.getAttribute('cy')); }
  const label = row.outcome === 'observed' ? 'Observed' : row.outcome === 'failed' ? 'Query error' : 'Missing';
  const tooltip = frame.querySelector('.chart-tooltip');
  tooltip.innerHTML = `<p class="small">11 Nov 2026 · ${row.start}–${row.end} UTC</p><p class="tooltip-result">${row.good === null ? 'No ratio available' : ratio(row.good, row.total)}</p><p>${status(row.outcome === 'observed' ? 'good' : row.outcome === 'failed' ? 'warn' : 'none', label)}</p>${row.good === null ? `<p class="tooltip-reason">${row.outcome === 'failed' ? 'Good query timed out' : 'No observation received'}. Counts are unknown.</p>` : `<dl><div><dt>Good</dt><dd>${format(row.good)}</dd></div><div><dt>Total</dt><dd>${format(row.total)}</dd></div><div><dt>Unsuccessful</dt><dd>${format(row.total - row.good)}</dd></div></dl>`}`;
  tooltip.hidden = false;
  const bounds = frame.getBoundingClientRect();
  const svg = frame.querySelector('svg');
  const position = svg.createSVGPoint(); position.x = x; position.y = point ? Number(point.getAttribute('cy')) : 100;
  const screen = position.matrixTransform(svg.getScreenCTM());
  tooltip.style.left = `${Math.max(0, Math.min(screen.x - bounds.left + 16, bounds.width - tooltip.offsetWidth))}px`;
  const anchorY = (clientY ?? screen.y) - bounds.top;
  const tooltipY = bounds.top + anchorY + 16 + tooltip.offsetHeight <= innerHeight - 8 ? anchorY + 16 : anchorY - tooltip.offsetHeight - 16;
  tooltip.style.top = `${Math.max(8 - bounds.top, tooltipY)}px`;
  if (document.activeElement === frame) frame.querySelector('.chart-selection').textContent = tooltip.textContent;
}
function inspectPointer(event, frame) {
  if (event.target.closest('.chart-tooltip')) return;
  const svg = frame.querySelector('svg');
  const position = svg.createSVGPoint(); position.x = event.clientX; position.y = event.clientY;
  const local = position.matrixTransform(svg.getScreenCTM().inverse());
  const cursor = frame.querySelector('.inspection-cursor');
  const left = Number(cursor.dataset.left), right = Number(cursor.dataset.right);
  if (local.x < left - 8 || local.x > right + 8) return;
  inspectInterval(frame, Math.max(0, Math.min(rows.length - 1, Math.round((local.x - left) / (right - left) * (rows.length - 1)))), event.clientY);
}
document.addEventListener('pointermove', event => {
  const frame = event.target.closest('.inspectable-chart');
  if (frame && event.pointerType !== 'touch') inspectPointer(event, frame);
});
document.addEventListener('pointerdown', event => {
  const frame = event.target.closest('.inspectable-chart');
  if (frame && event.pointerType === 'touch') inspectPointer(event, frame);
  if (!frame) document.querySelectorAll('.inspectable-chart').forEach(dismissInspection);
});
document.addEventListener('pointerleave', event => {
  if (event.target instanceof Element && event.target.matches('.inspectable-chart') && event.pointerType !== 'touch') dismissInspection(event.target);
}, true);
document.addEventListener('focusin', event => {
  if (event.target.matches('.inspectable-chart')) inspectInterval(event.target, Number(event.target.dataset.index ?? rows.length - 1));
});
document.addEventListener('focusout', event => {
  if (event.target.matches('.inspectable-chart')) dismissInspection(event.target);
});
document.addEventListener('keydown', event => {
  if (event.key === 'Escape') { document.querySelectorAll('.inspectable-chart').forEach(dismissInspection); return; }
  if (!event.target.matches('.inspectable-chart') || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  event.preventDefault();
  const current = Number(event.target.dataset.index ?? rows.length - 1);
  const index = event.key === 'Home' ? 0 : event.key === 'End' ? rows.length - 1 : current + (event.key === 'ArrowRight' ? 1 : -1);
  inspectInterval(event.target, Math.max(0, Math.min(rows.length - 1, index)));
});
