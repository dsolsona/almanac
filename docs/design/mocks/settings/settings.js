/* Throwaway interaction mock. All mutations are in memory; passwords are never retained. */
(() => {
  const params = new URLSearchParams(location.search);
  const state = params.get('state');
  const roles = { viewer: 'Viewer', editor: 'Editor', administrator: 'Admin' };
  const examples = [
    ['owner', 'Alex Morgan', 'alex@example.com', 'administrator', 'active', { label:'5 min ago', at:'2026-11-06T13:55:00Z' }],
    ['sam', 'Sam Rivera', 'sam@example.com', 'editor', 'active', { label:'2 hours ago', at:'2026-11-06T12:00:00Z' }],
    ['jo', 'Jo Chen', 'jo@example.com', 'viewer', 'active', { label:'Yesterday', at:'2026-11-05T17:30:00Z' }],
    ['pending', 'Priya Shah', 'priya@example.com', 'editor', 'pending', null],
    ['lee', 'Lee Park', 'lee@example.com', 'viewer', 'disabled', { label:'17 days ago', at:'2026-10-20T10:18:00Z' }],
    ['robin', 'Robin Ellis', 'robin@example.com', 'editor', 'active', { label:'30 min ago', at:'2026-11-06T13:30:00Z' }],
    ['noor', 'Noor Ali', 'noor@example.com', 'viewer', 'active', { label:'3 days ago', at:'2026-11-03T09:45:00Z' }],
    ['jules', 'Jules Martin', 'jules@example.com', 'editor', 'active', { label:'1 hour ago', at:'2026-11-06T13:00:00Z' }],
    ['mika', 'Mika Ito', 'mika@example.com', 'viewer', 'pending', null],
    ['drew', 'Drew Taylor', 'drew@example.com', 'viewer', 'active', { label:'7 days ago', at:'2026-10-30T11:20:00Z' }],
    ['remy', 'Remy Laurent', 'remy@example.com', 'editor', 'active', { label:'4 hours ago', at:'2026-11-06T10:00:00Z' }],
    ['kim', 'Kim Santos', 'kim@example.com', 'viewer', 'disabled', undefined],
  ];
  let users = state === 'empty' ? [] : examples.map(([id, name, email, role, status, lastSignIn]) => ({ id, name, email, role, status, lastSignIn, hadPassword:status !== 'pending' }));
  const me = users.find(user => user.id === 'owner') || { id: 'owner', name: 'Alex Morgan', email: 'alex@example.com', role: 'administrator', status: 'active' };
  let previewRole = params.get('role') in roles ? params.get('role') : 'administrator';
  let filter = 'all', search = '', roleFilter = '', page = 1, pageSize = 10;
  let notice = '', failSave = false, listFailed = state === 'error';
  let dialogAction = null, dialogTrigger = null;
  const screen = document.querySelector('#screen');
  const dialog = document.querySelector('#confirmation');
  const mobile = document.querySelector('#mobile-navigation');
  for (const modal of [dialog, mobile]) modal.addEventListener('keydown', event => {
    if (event.key !== 'Tab') return;
    const controls = [...modal.querySelectorAll('button:not(:disabled), a[href], input:not(:disabled), select:not(:disabled)')];
    const first = controls[0], last = controls.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  });
  const escape = value => String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]);
  const button = (label, action, variant = '', disabled = false) => `<button type="button" class="btn ${variant}" data-action="${action}" ${disabled ? 'disabled' : ''}>${label}</button>`;
  const link = (label, href, variant = '') => `<a class="btn ${variant}" href="${href}">${label}</a>`;
  const crumb = label => `<nav class="p-crumbs" aria-label="Breadcrumb"><a href="#settings">Settings</a><span aria-hidden="true">/</span>${label === 'Users' ? '<span aria-current="page">Users</span>' : `<a href="#users">Users</a><span aria-hidden="true">/</span><span aria-current="page">${escape(label)}</span>`}</nav>`;
  const head = (title, action = '') => `<div class="p-head"><h1 tabindex="-1">${escape(title)}</h1><span class="spacer"></span>${action}</div>`;
  const section = (title, body, step = '', extra = '') => `<section class="section ${extra}"><header>${step ? `<span class="step">${step}</span>` : ''}<h2>${title}</h2></header><div class="body">${body}</div></section>`;
  const field = (id, label, value = '', type = 'text', hint = '') => `<div class="field"><label for="${id}">${label}</label><input id="${id}" name="${id}" type="${type}" value="${escape(value)}" required ${type === 'password' ? 'autocomplete="new-password"' : `autocomplete="${type === 'email' ? 'email' : 'name'}" maxlength="160"`} aria-describedby="${hint ? `${id}-hint ` : ''}${id}-error">${hint ? `<div class="hint" id="${id}-hint">${hint}</div>` : ''}<div class="err" id="${id}-error"></div></div>`;
  const roleDescriptions = {
    viewer: 'Read services, SLIs, SLOs and measurements.',
    editor: 'Viewer access, plus manage services, SLIs and SLOs.',
    administrator: 'Editor access, plus manage users, agents, datasources and credentials.',
  };
  const rolePicker = value => `<div class="field role-picker"><input type="hidden" id="role" name="role" value="${value}"><div class="role-entry" ${value ? 'hidden' : ''}><label for="role-search">Role</label><div class="combo"><input id="role-search" role="combobox" aria-expanded="false" aria-controls="role-options" aria-autocomplete="list" aria-describedby="role-search-error" autocomplete="off" placeholder="Search roles…"><ul id="role-options" class="combo-list" role="listbox" aria-label="Roles" hidden></ul></div></div><div class="role-selection" ${value ? '' : 'hidden'}><span class="role-label">Role</span><div class="dscard role-summary"><span class="nm">${roles[value] || ''}</span><button class="change" type="button" data-action="change-role">Change</button><span class="sm">${roleDescriptions[value] || ''}</span></div></div><div class="err" id="role-search-error"></div></div>`;
  const badge = status => `<span class="hc ${status === 'active' ? 'good' : status}"><i aria-hidden="true"></i>${({ active: 'Active', pending: 'Pending setup', disabled: 'Disabled', resetting: 'Password reset' })[status] || status}</span>`;
  const lastAdmin = user => user.role === 'administrator' && user.status === 'active' && users.filter(item => item.role === 'administrator' && item.status === 'active').length === 1;
  const formActions = (save, cancel = '#users', note = '') => `<div class="form-actions">${note ? `<p>${note}</p>` : ''}<div class="actions">${link('Cancel', cancel)}<button class="btn primary" type="submit">${save}</button></div></div>`;
  const route = () => location.hash.slice(1) || 'settings';
  function go(destination, message = '') {
    notice = message;
    if (route() === destination) render();
    else location.hash = destination;
  }

  // Extend the existing mock shell locally; approved reference pages remain unchanged.
  const administration = document.querySelector('.sidebar .navgroup:last-child');
  administration.insertAdjacentHTML('beforeend', '<a class="current" href="#settings" aria-current="page"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 7h16M4 17h16"/><circle cx="9" cy="7" r="3"/><circle cx="15" cy="17" r="3"/></svg>Settings</a>');
  document.querySelectorAll('.sidebar nav a[href="#"]').forEach(anchor => {
    anchor.removeAttribute('href');
    anchor.setAttribute('aria-disabled', 'true');
    anchor.title = 'Outside this settings proposal';
  });
  document.querySelector('.account .id').innerHTML = `<span class="avatar" aria-hidden="true">A</span><span><strong>Alex Morgan</strong><small id="shell-role">Admin</small></span>`;
  document.querySelector('.account button').remove();
  function setTheme(theme) {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    screen.querySelectorAll('[data-theme]').forEach(control => control.setAttribute('aria-pressed', String(control.dataset.theme === theme)));
  }
  const signOut = document.querySelector('.account button:last-child');
  signOut.onclick = () => go('sign-in');
  const menuTrigger = document.querySelector('.mobile-head button');
  menuTrigger.setAttribute('aria-label', 'Open navigation');
  menuTrigger.onclick = () => {
    mobile.innerHTML = `<h2>Almanac</h2><nav><a href="#settings">Settings</a><a href="#account">Your account</a>${previewRole === 'administrator' ? '<a href="#users">Users</a>' : ''}</nav><div class="actions">${button('Close', 'close-menu')}</div>`;
    mobile.showModal();
  };
  mobile.addEventListener('click', event => {
    if (event.target.closest('a, [data-action="close-menu"]')) mobile.close();
  });
  mobile.addEventListener('close', () => menuTrigger.focus());

  // Same type-ahead → selection summary interaction as the existing service picker.
  function setupRolePicker() {
    const picker = screen.querySelector('.role-picker');
    if (!picker) return;
    const input = picker.querySelector('#role-search'), list = picker.querySelector('#role-options');
    const entry = picker.querySelector('.role-entry'), selection = picker.querySelector('.role-selection');
    const value = picker.querySelector('#role'), change = picker.querySelector('[data-action="change-role"]');
    let matches = [], active = -1;
    function close() {
      list.hidden = true; input.setAttribute('aria-expanded', 'false');
      input.removeAttribute('aria-activedescendant'); active = -1;
    }
    function options() {
      const term = input.value.trim().toLowerCase();
      matches = Object.keys(roles).filter(key => roles[key].toLowerCase().includes(term));
      list.innerHTML = matches.map((key, index) => `<li role="option" id="role-option-${key}" data-role="${key}" aria-selected="${index === active}"><span class="nm">${roles[key]}</span><span class="sm">${roleDescriptions[key]}</span></li>`).join('') || '<li class="none" role="presentation">No matching role. Try Viewer, Editor or Admin.</li>';
      list.hidden = false; input.setAttribute('aria-expanded', 'true');
      if (active >= 0 && matches[active]) input.setAttribute('aria-activedescendant', `role-option-${matches[active]}`);
      else input.removeAttribute('aria-activedescendant');
    }
    function choose(key) {
      value.value = key;
      selection.querySelector('.nm').textContent = roles[key];
      selection.querySelector('.sm').textContent = roleDescriptions[key];
      picker.querySelector('.err').textContent = '';
      input.removeAttribute('aria-invalid');
      close(); entry.hidden = true; selection.hidden = false; change.focus();
    }
    change.onclick = () => {
      value.value = ''; selection.hidden = true; entry.hidden = false;
      input.value = ''; input.focus();
    };
    input.onfocus = options;
    input.oninput = () => { active = -1; options(); };
    input.onkeydown = event => {
      if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
        event.preventDefault();
        if (list.hidden) options();
        if (!matches.length) return;
        if (event.key === 'Home') active = 0;
        else if (event.key === 'End') active = matches.length - 1;
        else active = event.key === 'ArrowDown' ? (active + 1) % matches.length : (active <= 0 ? matches.length : active) - 1;
        options();
      } else if (event.key === 'Enter') {
        event.preventDefault();
        const key = matches[active] || (matches.length === 1 ? matches[0] : null);
        if (!list.hidden && key) choose(key);
      } else if (event.key === 'Escape') { event.preventDefault(); close(); }
    };
    list.onmousedown = event => { if (event.target.closest('[data-role]')) event.preventDefault(); };
    list.onclick = event => { const option = event.target.closest('[data-role]'); if (option) choose(option.dataset.role); };
    picker.addEventListener('focusout', event => { if (!picker.contains(event.relatedTarget)) close(); });
  }

  function userList() {
    return crumb('Users') + head('Users', link('Add user', '#users/new', 'primary')) + `<div class="p-toolbar"><label class="search"><svg viewBox="0 0 20 20" aria-hidden="true"><circle cx="8" cy="8" r="5.5"/><path d="m12 12 5 5"/></svg><input id="search" type="search" aria-label="Search users" placeholder="Search name or email" value="${escape(search)}"><kbd aria-hidden="true">/</kbd></label><div class="seg" role="group" aria-label="User status">${['all', 'active', 'pending', 'disabled'].map(key => `<button type="button" data-filter="${key}" aria-pressed="${filter === key}">${({all:'All', active:'Active', pending:'Pending', disabled:'Disabled'})[key]}</button>`).join('')}</div><select class="p-filter" id="role-filter" aria-label="Filter by role"><option value="">All roles</option>${Object.entries(roles).map(([key,label]) => `<option value="${key}" ${roleFilter === key ? 'selected' : ''}>${label}</option>`).join('')}</select><span class="spacer"></span><button class="p-refresh" data-action="refresh" aria-label="Refresh users" title="Refresh sample users"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0-2.3 5.7"/><path d="M20 4v7h-7"/></svg></button></div><div id="list-results"></div>`;
  }
  function renderRows() {
    const target = document.querySelector('#list-results');
    if (!target) return;
    if (listFailed) {
      target.innerHTML = '<div class="notice error" role="alert"><strong>Users could not be loaded.</strong><p>Try again. Your account and other settings are still available.</p></div>' + button('Try again', 'refresh');
      return;
    }
    const matches = users.filter(user => `${user.name} ${user.email}`.toLowerCase().includes(search.toLowerCase()) && (!roleFilter || user.role === roleFilter) && (filter === 'all' || user.status === filter));
    page = Math.min(page, Math.max(1, Math.ceil(matches.length / pageSize)));
    const start = (page - 1) * pageSize;
    if (!matches.length) {
      target.innerHTML = `<div class="list-frame empty-state"><h2>${users.length ? 'No matching users' : 'Add your first teammate'}</h2><p>${users.length ? 'Try another name, email or filter.' : 'Create an account, choose a role and share a one-time password setup link.'}</p>${users.length ? button('Clear filters', 'clear-filters') : '<p>Use Add user above to get started.</p>'}</div>`;
      return;
    }
    target.innerHTML = `<div class="list-frame"><div class="table-scroll" role="region" aria-label="Users table" tabindex="0"><table class="tbl rows-top"><thead><tr><th scope="col">Name</th><th scope="col">Email</th><th scope="col">Status</th><th scope="col">Role</th><th scope="col">Last sign-in</th></tr></thead><tbody>${matches.slice(start, start + pageSize).map(user => `<tr><td><a class="rowname" href="#users/${user.id}">${escape(user.name)}${user.id === 'owner' ? '<span class="user-you">You</span>' : ''}</a></td><td class="user-email">${escape(user.email)}</td><td>${badge(user.status)}</td><td><span class="hc role-chip">${roles[user.role]}</span></td><td class="last-sign-in">${user.lastSignIn ? `<time datetime="${user.lastSignIn.at}" title="${user.lastSignIn.at.replace('T', ' ').replace('Z', ' UTC')}" aria-label="Last sign-in: ${user.lastSignIn.at.replace('T', ' ').replace('Z', ' UTC')}">${user.lastSignIn.label}</time>` : `<span class="muted" ${user.lastSignIn === undefined ? 'title="Sign-in history unavailable" aria-label="Sign-in history unavailable"' : ''}>${user.lastSignIn === null ? 'Never signed in' : '—'}</span>`}</td></tr>`).join('')}</tbody></table></div><nav class="pager" aria-label="User pages"><span aria-live="polite">${start + 1}–${Math.min(start + pageSize, matches.length)} of ${matches.length} users</span><label for="page-size">Per page</label><select id="page-size"><option ${pageSize === 10 ? 'selected' : ''}>10</option><option ${pageSize === 20 ? 'selected' : ''}>20</option></select>${button('‹ Previous', 'previous', 'sm', page === 1)}${button('Next ›', 'next', 'sm', start + pageSize >= matches.length)}</nav></div>`;
  }

  function userForm(user) {
    const creating = !user;
    const protectedAdmin = user && lastAdmin(user);
    const password = creating ? '' : `<div class="danger-row"><div><h3>${user.status === 'pending' ? 'Password setup' : 'Reset password'}</h3><p>${user.status === 'pending' ? 'Setup pending. Create a replacement link if needed.' : user.status === 'resetting' ? 'Reset pending. Password sign-in is suspended until completed or cancelled.' : 'Create a private reset link, valid for 15 minutes.'}${protectedAdmin ? ' Another active admin is required before resetting this password.' : user.status === 'disabled' ? ' Enable this user before resetting their password.' : ''}</p></div>${user.status === 'resetting' ? button('Cancel password reset', 'cancel-reset', 'link') : button(user.status === 'pending' ? 'Replace setup link' : 'Reset password', 'reset-password', 'link', user.status === 'disabled' || protectedAdmin)}</div>`;
    return crumb(creating ? 'Add user' : user.name) + head(creating ? 'Add user' : user.name, creating ? '' : badge(user.status)) + `<form id="user-form" data-id="${user?.id || ''}">` +
      section('Profile', `<div class="grid2">${field('name', 'Full name', user?.name || '')}${field('email', 'Email', user?.email || '', 'email')}</div>${rolePicker(user?.role || '')}${protectedAdmin ? '<div class="notice warning">This is the last active admin with a local password. Activate another admin before changing this role, disabling or deleting this account.</div>' : ''}`) +
      (creating ? '' : section('Account access', password + `<div class="danger-row"><div><h3>${user.status === 'disabled' ? 'Enable user' : 'Disable user'}</h3><p>${user.status === 'disabled' ? 'Restore access with the same profile and role. Revoked sessions and links stay revoked.' : 'Block sign-in and revoke their sessions, tokens and pending links. You can enable them again.'}</p></div>${button(user.status === 'disabled' ? 'Enable user' : 'Disable user', 'toggle-user', user.status === 'disabled' ? '' : 'danger-outline', protectedAdmin)}</div><div class="danger-row"><div><h3>Delete user</h3><p>Remove their account and access permanently. Past activity remains attributed in audit history.</p></div>${button('Delete user', 'delete-user', 'danger-outline', protectedAdmin)}</div>${protectedAdmin ? '<p>The last active local admin cannot be disabled or deleted.</p>' : ''}`)) +
      '<div id="form-error" role="alert"></div>' + formActions(creating ? 'Add user' : 'Save changes', '#users', creating ? 'Next: share a 24-hour password setup link.' : '') + '</form>';
  }

  function account() {
    const dark = document.documentElement.classList.contains('dark');
    return '<nav class="p-crumbs" aria-label="Breadcrumb"><a href="#settings">Settings</a><span aria-hidden="true">/</span><span aria-current="page">Your account</span></nav>' +
      `<div class="p-head"><h1 tabindex="-1">Your account</h1><span class="hc role-chip" aria-label="Role: ${roles[previewRole]}">${roles[previewRole]}</span></div>` +
      `<form id="account-form">${section('Profile', `<div class="grid2">${field('name', 'Full name', me.name)}<div class="field"><label for="account-email">Sign-in email</label><input id="account-email" type="email" value="${escape(me.email)}" readonly aria-describedby="email-help"><div class="hint" id="email-help">An admin can change your email in Users.</div></div></div><div class="field"><span class="role-label" id="theme-label">Theme</span><div class="seg" role="group" aria-labelledby="theme-label"><button type="button" data-theme="light" aria-pressed="${!dark}">Light</button><button type="button" data-theme="dark" aria-pressed="${dark}">Dark</button></div></div>`)}<div id="form-error" role="alert"></div><div class="form-actions">${link('Change password', '#password')}<div class="actions">${link('Cancel', '#settings')}<button class="btn primary" type="submit">Save changes</button></div></div></form>`;
  }
  function passwordForm(publicFlow, id) {
    const user = users.find(item => item.id === id);
    if (publicFlow && (state === 'expired' || !user || !['pending', 'resetting'].includes(user.status))) {
      return '<div class="auth-wordmark">Almanac</div>' + head('This link is unavailable') + '<div class="notice warning"><p>The link has expired, has already been used or is no longer valid. Ask your Almanac admin for a new link.</p></div>' + link('Back to sign in', '#sign-in');
    }
    return (publicFlow ? '<div class="auth-wordmark">Almanac</div>' : '<nav class="p-crumbs" aria-label="Breadcrumb"><a href="#account">Your account</a><span aria-hidden="true">/</span><span aria-current="page">Change password</span></nav>') + head(publicFlow ? (user.status === 'resetting' ? 'Reset your password' : 'Set your password') : 'Change password') +
      `<p class="form-intro">${publicFlow ? `Choose a password for ${escape(user.email)}.` : 'Changing your password signs out other sessions and revokes personal tokens and pending sign-in links. You stay signed in here.'}</p><form id="password-form" data-user="${publicFlow ? user.id : ''}">${section('New password', field('password', 'Password', '', 'password', '15–128 characters. Spaces are welcome. Avoid common passwords.') + field('confirm-password', 'Confirm password', '', 'password', 'Enter the same password again.') + '<label class="check"><input type="checkbox" id="show-password">Show passwords</label>')}<div id="form-error" role="alert"></div><div class="form-actions">${publicFlow ? '<p>Keep this password private.</p>' : link('Cancel', '#account')}<button class="btn primary" type="submit">${publicFlow ? 'Set password' : 'Change password'}</button></div></form>`;
  }

  function render() {
    dialog.close();
    const current = route() === 'settings' ? 'account' : route();
    const publicFlow = /^(activate\/|sign-in|ready)/.test(current);
    document.body.classList.toggle('public', publicFlow);
    document.querySelector('#shell-role').textContent = roles[previewRole];
    const parts = current.split('/');
    let content;
    if (parts[0] === 'users' && previewRole !== 'administrator') content = crumb('Users') + head('User administration') + '<div class="notice warning"><strong>Admin access required.</strong><p>Your role can manage your own account, but cannot view or change other users.</p></div>' + link('Your account', '#account');
    else if (current === 'users') content = userList();
    else if (current === 'users/new') content = userForm(null);
    else if (parts[0] === 'users') {
      const user = users.find(item => item.id === parts[1]);
      content = user ? userForm(user) : crumb('User unavailable') + head('User unavailable') + '<p>This account was deleted or is no longer available.</p>' + link('Back to users', '#users');
    } else if (current === 'account') content = account();
    else if (current === 'password') content = passwordForm(false);
    else if (parts[0] === 'activate') content = passwordForm(true, parts[1]);
    else if (current === 'ready') content = '<div class="auth-wordmark">Almanac</div>' + head('Your account is ready') + '<div class="notice" role="status">Your password is set. You can now sign in with your email.</div>' + link('Continue to sign in', '#sign-in', 'primary');
    else if (current === 'sign-in') content = '<div class="auth-wordmark">Almanac</div>' + head('Sign in') + `<form id="sign-in-form">${section('Your account', field('email', 'Email', '', 'email') + field('password', 'Password', '', 'password'))}<div id="form-error" role="alert"></div><p class="role-help">Design preview: this form does not authenticate.</p><div class="actions"><button class="btn primary" type="submit">Sign in</button></div></form>`;
    else content = account();
    const tabs = !publicFlow ? `<nav class="settings-nav" aria-label="Settings"><a href="#account" ${['account', 'password'].includes(current) ? 'aria-current="page"' : ''}>Your account</a>${previewRole === 'administrator' ? `<a href="#users" ${parts[0] === 'users' ? 'aria-current="page"' : ''}>Users</a>` : ''}</nav>` : '';
    screen.innerHTML = tabs + (notice ? `<div class="notice" role="status">${escape(notice)}</div>` : '') + content;
    notice = '';
    renderRows();
    setupRolePicker();
    if (current === 'sign-in') document.querySelector('#password').autocomplete = 'current-password';
    document.title = `${screen.querySelector('h1')?.textContent || 'Settings'} · Almanac mock`;
    screen.querySelector('h1')?.focus({ preventScroll:true });
    window.scrollTo(0, 0);
  }

  function showDialog(title, description, label, action, destructive = false, extra = '') {
    dialogTrigger = document.activeElement;
    dialogAction = action;
    dialog.innerHTML = `<h2 id="dialog-title">${escape(title)}</h2><p id="dialog-description">${escape(description)}</p>${extra}<div class="actions">${button('Cancel', 'cancel-dialog')}<button type="button" class="btn ${destructive ? 'danger-fill' : 'primary'}" id="confirm-action">${label}</button></div>`;
    dialog.showModal();
    dialog.querySelector('[data-action="cancel-dialog"]').focus();
  }
  function showOneTime(user, reset) {
    const demoLink = `https://almanac.example/${reset ? 'reset-password' : 'activate'}#demo-link`;
    showDialog(reset ? 'Password reset link ready' : 'Password setup link ready', `${user.name} · ${user.email}. Share this link privately. It expires in ${reset ? '15 minutes' : '24 hours'} and can be used once. No email has been sent.`, 'Done', () => dialog.close(), false,
      `<div class="one-time"><code>${demoLink}</code></div><p class="role-help">This is a non-working example link. It will not be available after closing this dialog.</p><div class="actions">${button('Copy link', 'copy-link')}${link('Preview recipient journey', `#activate/${user.id}`)}</div><p id="copy-result" role="status"></p>`);
    dialog.querySelector('[data-action="cancel-dialog"]').remove();
    dialog.querySelector('#confirm-action').focus();
    dialog.dataset.demoLink = demoLink;
  }
  dialog.addEventListener('close', () => {
    if (dialog.open) return;
    dialogAction = null; delete dialog.dataset.demoLink;
    if (dialogTrigger?.isConnected) dialogTrigger.focus();
  });
  dialog.addEventListener('click', async event => {
    if (event.target.closest('#confirm-action')) dialogAction?.();
    if (event.target.closest('[data-action="cancel-dialog"]')) dialog.close();
    if (event.target.closest('a')) dialog.close();
    if (event.target.closest('[data-action="copy-link"]')) {
      try { await navigator.clipboard.writeText(dialog.dataset.demoLink); dialog.querySelector('#copy-result').textContent = 'Example link copied.'; }
      catch { dialog.querySelector('#copy-result').textContent = 'Copy is unavailable. Select the example link above to copy it.'; }
    }
  });

  function formError(message, fieldID) {
    const fieldInput = fieldID && document.getElementById(fieldID);
    if (fieldInput) {
      fieldInput.setAttribute('aria-invalid', 'true');
      document.getElementById(`${fieldID}-error`).textContent = message;
      fieldInput.focus();
    } else {
      const error = document.querySelector('#form-error');
      error.className = 'notice error'; error.textContent = message;
    }
  }
  screen.addEventListener('input', event => {
    if (event.target.id === 'search') { search = event.target.value; page = 1; renderRows(); }
    if (event.target.hasAttribute('aria-invalid')) {
      event.target.removeAttribute('aria-invalid');
      document.getElementById(`${event.target.id}-error`).textContent = '';
    }
  });
  screen.addEventListener('change', event => {
    if (event.target.id === 'role-filter') { roleFilter = event.target.value; page = 1; renderRows(); }
    if (event.target.id === 'page-size') { pageSize = Number(event.target.value); page = 1; renderRows(); }
    if (event.target.id === 'show-password') screen.querySelectorAll('[autocomplete="new-password"]').forEach(input => { input.type = event.target.checked ? 'text' : 'password'; });
  });
  screen.addEventListener('click', event => {
    const themeControl = event.target.closest('[data-theme]');
    if (themeControl) { setTheme(themeControl.dataset.theme); return; }
    const row = event.target.closest('#list-results tbody tr');
    if (row && event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey && !event.target.closest('a, button, input, select, textarea') && !window.getSelection()?.toString()) {
      row.querySelector('a.rowname').click();
      return;
    }
    const filterButton = event.target.closest('[data-filter]');
    if (filterButton) {
      filter = filterButton.dataset.filter; page = 1;
      screen.querySelectorAll('[data-filter]').forEach(item => item.setAttribute('aria-pressed', String(item === filterButton)));
      renderRows(); return;
    }
    const action = event.target.closest('[data-action]')?.dataset.action;
    const user = users.find(item => item.id === route().split('/')[1]);
    if (action === 'previous' || action === 'next') { page += action === 'next' ? 1 : -1; renderRows(); }
    if (action === 'refresh') { listFailed = false; renderRows(); }
    if (action === 'clear-filters') { search = ''; filter = 'all'; roleFilter = ''; page = 1; render(); }
    if (!user) return;
    if (action === 'toggle-user') {
      const enabling = user.status === 'disabled';
      showDialog(`${enabling ? 'Enable' : 'Disable'} ${user.name}?`, enabling ? 'Restore this account’s access. They will need to sign in again; previous sessions and links stay revoked.' : 'They will be signed out immediately. Their sessions, personal tokens and pending setup or reset links will be revoked. Their profile and role are kept.', enabling ? 'Enable user' : 'Disable user', () => {
        user.status = enabling ? (user.hadPassword ? 'active' : 'pending') : 'disabled';
        go(`users/${user.id}`, `${user.name} ${enabling ? 'enabled' : 'disabled'}.`);
      }, !enabling);
    }
    if (action === 'delete-user') showDialog(`Delete ${user.name}?`, 'This permanently removes the account and revokes its sessions, tokens and pending links. Past audit activity keeps its attribution. This cannot be undone.', 'Delete user', () => {
      users = users.filter(item => item.id !== user.id);
      go('users', `${user.name} deleted.`);
    }, true);
    if (action === 'reset-password') showDialog(user.status === 'pending' ? 'Replace password setup link?' : `Reset ${user.name}’s password?`, user.status === 'pending' ? 'The original setup link will stop working. Share the replacement privately; it expires in 24 hours.' : 'This signs the user out, revokes their tokens and pending links, and suspends password sign-in until the new password is set. Share the new link privately; it expires in 15 minutes.', 'Generate link', () => {
      const reset = user.status !== 'pending';
      if (reset) user.status = 'resetting';
      dialog.close(); render(); showOneTime(user, reset);
    });
    if (action === 'cancel-reset') showDialog('Cancel password reset?', 'The reset link will stop working. The user can sign in again with their previous password. Revoked sessions and tokens stay revoked.', 'Cancel reset', () => { user.status = 'active'; go(`users/${user.id}`, 'Password reset cancelled.'); });
  });
  screen.addEventListener('submit', event => {
    event.preventDefault();
    const form = event.target;
    const data = new FormData(form);
    if (failSave) {
      failSave = false;
      document.querySelector('#fail-save').textContent = 'Fail next save';
      formError('Changes could not be saved. Your entries are still here. Try again.'); return;
    }
    if (form.id === 'user-form') {
      const name = data.get('name').trim(), email = data.get('email').trim().toLowerCase(), role = data.get('role');
      const existing = users.find(item => item.id === form.dataset.id);
      if (!name) return formError('Enter a name.', 'name');
      if (users.some(user => user.email.toLowerCase() === email && user !== existing)) return formError('A user with this email already exists.', 'email');
      if (!Object.hasOwn(roles, role)) return formError('Choose a role from the list.', 'role-search');
      if (existing && lastAdmin(existing) && role !== 'administrator') return formError('Keep at least one active admin with a local password. Activate another admin before changing this role.');
      if (existing) { Object.assign(existing, { name, email, role }); go(`users/${existing.id}`, 'Changes saved.'); }
      else {
        const user = { id: crypto.randomUUID(), name, email, role, status:'pending', hadPassword:false, lastSignIn:null };
        users.push(user);
        // Render before opening a modal so hash navigation cannot close its one-time output.
        history.pushState(null, '', `${location.pathname}${location.search}#users/${user.id}`);
        render(); showOneTime(user, false);
      }
    } else if (form.id === 'account-form') {
      const name = data.get('name').trim();
      if (!name) return formError('Enter a name.', 'name');
      me.name = name;
      document.querySelector('.account .id strong').textContent = name;
      go('account', 'Your name has been updated.');
    } else if (form.id === 'password-form') {
      const password = data.get('password'), length = [...password].length;
      if (length < 15 || length > 128) return formError('Use between 15 and 128 characters.', 'password');
      if (password !== data.get('confirm-password')) return formError('Passwords do not match.', 'confirm-password');
      form.reset();
      const user = users.find(item => item.id === form.dataset.user);
      if (user) { user.status = 'active'; user.hadPassword = true; go('ready'); }
      else go('account', 'Password changed. You remain signed in; other sessions and personal tokens have been revoked.');
    } else if (form.id === 'sign-in-form') {
      form.reset(); go('settings', 'Design preview only. No authentication was performed.');
    }
  });
  document.querySelector('#review-role').value = previewRole;
  document.querySelector('#review-role').onchange = event => { previewRole = event.target.value; render(); };
  document.querySelector('#fail-save').onclick = () => {
    failSave = true;
    document.querySelector('#fail-save').textContent = 'Next save will fail';
  };
  document.querySelector('.skip').onclick = event => { event.preventDefault(); screen.querySelector('h1')?.focus(); };
  document.addEventListener('keydown', event => {
    if (event.key === '/' && !event.ctrlKey && !event.metaKey && !event.altKey && !event.target.closest('input, textarea, select, [contenteditable="true"]') && !dialog.open && !mobile.open) {
      const input = document.querySelector('#search');
      if (input) { event.preventDefault(); input.focus(); }
    }
  });
  window.addEventListener('hashchange', render);
  render();
})();
