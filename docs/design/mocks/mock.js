// Injects the shared application shell around the mock page and honours ?theme=dark.
(function () {
  const params = new URLSearchParams(location.search);
  if (params.get('theme') === 'dark') document.documentElement.classList.add('dark');
  const current = document.body.dataset.current || '';
  const icon = {
    slos: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="8.5"/><path d="M12 7v5l3.5 2"/></svg>',
    slis: '<svg viewBox="0 0 24 24"><path d="M3 12h3l2-6 4 12 2-6h7"/></svg>',
    alerts: '<svg viewBox="0 0 24 24"><path d="M18 9a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9"/><path d="M10 21h4"/></svg>',
    datasources: '<svg viewBox="0 0 24 24"><ellipse cx="12" cy="5" rx="8.5" ry="3"/><path d="M3.5 5v7c0 1.7 3.8 3 8.5 3s8.5-1.3 8.5-3V5M3.5 12v7c0 1.7 3.8 3 8.5 3s8.5-1.3 8.5-3v-7"/></svg>',
    agents: '<svg viewBox="0 0 24 24"><circle cx="8" cy="8" r="3"/><circle cx="17" cy="9" r="2.5"/><path d="M2.5 20c.4-3.4 2.3-5 5.5-5s5.1 1.6 5.5 5M14 15c3.8-.8 6.8.7 7.5 4"/></svg>',
  };
  const link = (key, label) => `<a href="#" class="${current === key ? 'current' : ''}">${icon[key]}${label}</a>`;
  const sidebar = `<aside class="sidebar">
    <div class="wordmark">Almanac</div><div class="tag">Service reliability</div>
    <nav>
      <div class="navgroup"><h2>Product</h2>${link('slos', 'SLOs')}${link('slis', 'SLIs')}</div>
      <div class="navgroup"><h2>Operations</h2><div class="planned"><span style="display:flex;gap:10px;align-items:center">${icon.alerts}Alerts</span><small>Not available yet</small></div></div>
      <div class="navgroup"><h2>Administration</h2>${link('datasources', 'Datasources')}${link('agents', 'Agents')}</div>
    </nav>
    <footer class="account">
      <div class="id"><span class="avatar">A</span><span><strong>admin</strong><small>Administrator</small></span></div>
      <button class="btn">☼ Light theme</button><button class="btn">Sign out</button>
    </footer>
  </aside>`;
  const main = document.querySelector('main');
  const wrap = document.createElement('div');
  wrap.className = 'shell';
  wrap.innerHTML = sidebar + '<div><div class="mobile-head"><span class="wordmark" style="font-size:22px">Almanac</span><button class="btn">☰ Menu</button></div></div>';
  main.classList.add('main');
  main.parentNode.insertBefore(wrap, main);
  wrap.lastElementChild.appendChild(main);
})();
