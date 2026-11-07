/* Throwaway visual prototype. Sample records and edits exist only in this tab. */
(() => {
  const examples = [
    ['checkout','Checkout','Accepts and processes customer orders.','Store experience',['payments','inventory','notifications']],
    ['payments','Payments','Authorizes payments and handles refunds.','Commerce',['identity']],
    ['inventory','Inventory','Tracks stock and reserves items for checkout.','Commerce',[]],
    ['storefront','Storefront','The customer-facing web store.','Store experience',['checkout','identity']],
    ['identity','Identity','Authentication and account access.','Platform',[]],
    ['notifications','Notifications','Delivers order confirmations and updates.','Platform',[]],
    ['search','Search','Finds products across the catalog.','Discovery',['inventory']],
    ['orders','Orders','Manages customer orders from placement to delivery.','Store experience',['checkout','notifications']],
    ['billing','Billing','Invoices and recurring subscriptions.','Commerce',['payments']],
    ['recommendations','Recommendations','Suggests products from recent activity.','',[]],
    ['fulfilment','Fulfilment','Coordinates picking, packing and delivery.','Operations',['orders','inventory']],
    ['legacy-reports','Legacy reports','Previous reporting service, being retired.','',[]],
  ];
  const objectives = {
    checkout:[['Order completion','healthy','On track','99.9% · 30 d','62.4%'],['Checkout latency < 500 ms','breached','Breached','99% · 30 d','−8.2%'],['Confirmation delivery','unknown','Partial data','99.5% · 7 d','—']],
    payments:[['Payment authorization','healthy','On track','99.95% · 30 d','84.1%'],['Refund processing','healthy','On track','99.9% · 30 d','72.0%']],
    inventory:[['Stock reservation','healthy','On track','99.9% · 30 d','91.0%']],
    storefront:[['Store availability','healthy','On track','99.9% · 30 d','78.4%']],
    identity:[['Sign-in availability','healthy','On track','99.95% · 30 d','88.3%']],
    search:[['Search latency','unknown','Stale data','99% · 7 d','—']],
    orders:[['Order retrieval','healthy','On track','99.9% · 30 d','96.0%']],
    billing:[['Invoice generation','healthy','On track','99.5% · 30 d','75.8%']],
    fulfilment:[['Shipment creation','healthy','On track','99.5% · 30 d','61.4%']],
  };
  // Readable fixture routes are aliases; service references use stable UUIDs.
  let services = examples.map(([key,name,description,owner,deps],index) => ({id:`00000000-0000-4000-8000-${String(index+1).padStart(12,'0')}`,routeKey:key,slug:key,name,description,owner,deps:[...deps],contact:owner ? `${owner.toLowerCase().replaceAll(' ','-')}@example.com` : '',labels:[['team',owner ? owner.toLowerCase().replaceAll(' ','-') : 'unassigned'],['env','production']],links:[['Runbook',`https://docs.example.com/${key}/runbook`],['Git',`https://git.example.com/services/${key}`]],created:'26 Oct 2026, 09:14 UTC',changed:'6 Nov 2026, 13:42 UTC'}));
  for(const service of services)service.deps=service.deps.map(key=>services.find(candidate=>candidate.routeKey===key).id);
  for(const [key,rows] of Object.entries(objectives)){objectives[services.find(service=>service.routeKey===key).id]=rows;delete objectives[key];}
  const params = new URLSearchParams(location.search);
  let state = params.get('state') || 'normal', role = params.get('role') === 'viewer' ? 'viewer' : 'editor';
  let search = '', coverage = 'all', ownerFilter = '', page = 1, pageSize = 10;
  let draft = null, draftKey = '', dirty = false, dialogOrigin = null, selectedNode = 'checkout';
  let dependencyStyle=params.get('dependencies')==='cards'?'cards':'connected';
  let dependencyResize=null;
  const screen = document.querySelector('#screen'), modal = document.querySelector('#modal');
  const esc = value => String(value ?? '').replace(/[&<>"']/g, char => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'})[char]);
  const get = id => services.find(service => service.id === id || service.routeKey === id);
  const canWrite = () => role !== 'viewer';
  const count = service => (objectives[service.id] || []).length;
  const uses = service => services.filter(other => other.deps.includes(service.id));
  const svg = paths => `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true">${paths}</svg>`;
  const icons = {refresh:svg('<path d="M20 11a8 8 0 1 0-2.3 5.7M20 4v7h-7"/>'),close:svg('<path d="m6 6 12 12M18 6 6 18"/>'),plus:svg('<path d="M12 5v14M5 12h14"/>'),search:svg('<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>'),service:svg('<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="14" width="7" height="7" rx="1"/><path d="M17 10V6h-3M7 14v4h3M10 6h7M7 10v8"/>')};
  const btn = (label,action,variant='',attrs='') => `<button type="button" class="btn ${variant}" data-action="${action}" ${attrs}>${label}</button>`;
  const route = (label,hash,variant='') => `<a class="${variant}" href="#${hash}">${label}</a>`;
  const refresh = () => `<button class="p-refresh" data-action="refresh" title="Refresh · last refreshed 13:42 UTC" aria-label="Refresh services">${icons.refresh}</button>`;
  const chip = (tone,label,title='') => `<span class="hc ${tone}" ${title ? `title="${esc(title)}" role="img" aria-label="${esc(`${label}. ${title}`)}"` : ''}><i aria-hidden="true"></i>${esc(label)}</span>`;
  const labelParts = (key,value) => `<span class="label-key">${esc(key)}<span class="label-colon">:</span></span><span class="label-value">${esc(value)}</span>`;
  const tags = service => `<div class="label-chips">${service.labels.map(([key,value])=>`<span class="label-chip service-label">${labelParts(key,value)}</span>`).join('')}</div>`;
  const section = (title,body,action='') => `<section class="panel"><header><h2>${title}</h2>${action}</header><div class="panel-body">${body}</div></section>`;
  const message = (title,body,kind='',action='') => `<div class="notice ${kind}" ${kind==='error'?'role="alert"':''}><div><strong>${title}</strong>${body ? `<p>${body}</p>` : ''}</div>${action}</div>`;
  const empty = (title,copy,action='') => `<div class="empty"><div class="empty-symbol">${icons.service}</div><h2>${title}</h2><p>${copy}</p>${action}</div>`;
  function notify(text) { document.querySelector('#notice').innerHTML = text ? message(esc(text),'') : ''; }
  function heading(title,action='',crumbs='') { return `${crumbs ? `<nav class="p-crumbs" aria-label="Breadcrumb">${route('Services','list')}<span>/</span>${crumbs}</nav>` : ''}<div class="p-head"><h1 tabindex="-1">${esc(title)}</h1><span class="spacer"></span><div class="actions">${action}</div></div>`; }
  function readNotice() {
    return state==='stale' ? message('Couldn’t refresh services.','Showing the last available records from 13:42 UTC.','warning',btn('Try again','retry')) : '';
  }
  function table(headers,rows,targets=[]) { return `<table class="tbl fit"><thead><tr>${headers.map(name=>`<th scope="col">${name}</th>`).join('')}</tr></thead><tbody>${rows.map((row,index)=>`<tr ${targets[index]?`data-row-href="${esc(targets[index])}"`:''}>${row.map((cell,index)=>`<td data-label="${esc(headers[index])}">${cell}</td>`).join('')}</tr>`).join('')}</tbody></table>`; }
  const catalogViews = graph => `<nav class="service-view-tabs" aria-label="Service views"><a href="#list" ${graph?'':'aria-current="page"'}>Table</a><a href="#map/a" ${graph?'aria-current="page"':''}>Graph</a></nav>`;
  function relationList(records,withStatus=false) {
    if(!records.length) return '<p class="muted">No dependencies recorded.</p>';
    return `<ul class="relationships">${records.map(service=>`<li><span class="service-symbol" aria-hidden="true">${esc(service.name[0])}</span><span class="service-caption">${route(esc(service.name),`service/${service.id}`)}${service.owner?`<small>${esc(service.owner)}</small>`:''}</span>${withStatus?serviceSloStatus(service):''}<span class="relation-count">${count(service)} SLO${count(service)===1?'':'s'}</span></li>`).join('')}</ul>`;
  }
  function serviceSloStatus(service) {
    const slos=objectives[service.id]||[];
    if(!slos.length)return chip('none','No SLOs','No objectives are defined for this service.');
    const breached=slos.filter(slo=>slo[1]==='breached').length;
    const unknown=slos.filter(slo=>!['healthy','breached'].includes(slo[1])).length;
    return chip(breached?'crit':unknown?'stale':'good',breached?'Breached':unknown?'Unknown':'On track',`${breached} breached · ${unknown} unknown · ${slos.length-breached-unknown} on track`);
  }
  function list() {
    const all = state==='empty' ? [] : services;
    screen.innerHTML = heading('Services',canWrite()?route(`${icons.plus} New service`,'new','btn primary'):'<span class="read-only">View only</span>') + catalogViews(false) + readNotice() + `<div class="p-toolbar"><label class="search">${icons.search}<input id="service-search" aria-label="Search services" placeholder="Search name, owner or label" value="${esc(search)}"><kbd>/</kbd></label><span class="seg" aria-label="SLO coverage filter">${[['all','All',all.length],['with','With SLOs',all.filter(s=>count(s)).length],['without','No SLOs',all.filter(s=>!count(s)).length]].map(([value,label,total])=>`<button type="button" data-action="coverage" data-value="${value}" aria-pressed="${coverage===value}">${label} <span class="n">${total}</span></button>`).join('')}</span><label class="p-filter">Owner <select id="owner-filter" aria-label="Filter by owner"><option value="">Any</option>${[...new Set(services.map(s=>s.owner||'Unassigned'))].sort().map(owner=>`<option ${owner===ownerFilter?'selected':''}>${esc(owner)}</option>`).join('')}</select></label><span class="spacer"></span>${refresh()}</div><div id="list-results"></div>`;
    listResults();
  }
  function listResults() {
    const target = document.querySelector('#list-results');
    if(state==='error') { target.innerHTML=section('Services',empty('Services couldn’t load','The service catalog is unavailable. Try again to load your services.',btn('Try again','retry'))); return; }
    const all = state==='empty'?[]:services;
    const filtered = all.filter(s=>(!search || [s.name,s.slug,s.id,s.description,s.owner,...s.labels.flat()].join(' ').toLowerCase().includes(search.toLowerCase()))&&(!ownerFilter||(s.owner||'Unassigned')===ownerFilter)&&(coverage==='all'||(coverage==='with'?count(s)>0:count(s)===0)));
    page=Math.min(page,Math.max(1,Math.ceil(filtered.length/pageSize)));
    if(!filtered.length) { target.innerHTML=`<div class="panel">${!all.length?empty('Your services start here','Create a service to group its SLOs, share useful links and record dependencies.',canWrite()?route('New service','new','btn primary'):'<p>An editor can create the first service.</p>'):empty('No services match','Try another name, owner or label, or clear your filters.',btn('Clear filters','clear-filters'))}</div>`;return; }
    const start=(page-1)*pageSize;
    const visible=filtered.slice(start,start+pageSize);
    target.innerHTML=`<div class="panel">${table(['Service','SLO status','Owner','SLOs','Depends on'],visible.map(s=>[`${route(esc(s.name),`service/${s.id}`,'rowname')}<span class="desc">${esc(s.description)}</span>`,serviceSloStatus(s),esc(s.owner||'Unassigned'),count(s)?route(`${count(s)} SLO${count(s)===1?'':'s'}`,`service/${s.id}`,'inline-link'):'<span class="muted">No SLOs</span>',route(String(s.deps.length),`dependencies/${s.id}`,'inline-link')]),visible.map(s=>`#service/${s.id}`))}<div class="pager"><span>Showing ${start+1}–${Math.min(start+pageSize,filtered.length)} of ${filtered.length}</span><span class="spacer"></span><label>Per page <select id="page-size"><option ${pageSize===10?'selected':''}>10</option><option ${pageSize===20?'selected':''}>20</option></select></label>${btn('‹ Previous','previous','',page===1?'disabled':'')}${btn('Next ›','next','',start+pageSize>=filtered.length?'disabled':'')}</div></div>`;
  }
  function sloTable(service) {
    const rank={breached:0,unknown:1,healthy:2};
    const slos = [...(objectives[service.id] || [])].sort((a,b)=>rank[a[1]]-rank[b[1]]);
    return slos.length ? table(['SLO','Status','Objective','Budget left'],slos.map(([name,status,label,target,budget])=>[`<a href="../slo-detail.html" class="rowname" title="Open the retained SLO detail mock">${esc(name)}</a>`,chip(status==='healthy'?'good':status==='breached'?'crit':'stale',label,status==='unknown'?'A complete result is unavailable; missing measurements do not count as success.':''),`<span class="mono">${target}</span>`,`<span class="mono ${status==='unknown'?'muted':''}" ${status==='breached'?'style="color:var(--status-breached)"':''}>${budget}</span>`])):empty('No SLOs yet',`Add an objective to measure ${esc(service.name)} against a reliability target.`,canWrite()?route('Create SLO',`slo-component/${service.id}`,'btn'):'');
  }
  function detail(service) {
    const slos=objectives[service.id]||[];
    const contact=service.contact?`<p class="service-contact">${/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(service.contact)?`<a href="mailto:${esc(service.contact)}">${esc(service.contact)}</a>`:esc(service.contact)}</p>`:'';
    const results=[['breached','crit','breached'],['unknown','stale','unknown'],['healthy','good','on track']].map(([state,tone,label])=>{const total=slos.filter(slo=>slo[1]===state).length;return total?chip(tone,`${total} ${label}`):'';}).join('');
    screen.innerHTML=`<article class="service-overview">`+
      heading(service.name,`${refresh()}${canWrite()?route('Edit service',`edit/${service.id}`,'btn primary'):'<span class="read-only">View only</span>'}`,`<span aria-current="page">${esc(service.name)}</span>`)+readNotice()+
      `<section class="panel service-info" aria-label="Service information"><div><h2>Description</h2><p class="service-description">${esc(service.description||'No description added.')}</p></div><div><h2>Owner</h2><strong class="service-owner">${esc(service.owner||'Unassigned')}</strong>${contact}</div><div><h2>Labels</h2>${service.labels.length?tags(service):'<p class="muted">No labels added.</p>'}</div><div><h2>Links</h2>${service.links.length?`<nav class="service-quick-links" aria-label="Service links">${service.links.map(([name,url])=>`<a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(name)} <span aria-hidden="true">↗</span></a>`).join('')}</nav>`:'<p class="muted">No links added.</p>'}</div></section>`+
      `<section class="service-section service-objectives"><header><div><h2>SLOs</h2>${slos.length?`<div class="service-result-summary" aria-label="SLO results">${results}</div>`:''}</div>${canWrite()&&slos.length?route('Create SLO',`slo-component/${service.id}`,'btn'):''}</header><div class="panel service-slo-table">${sloTable(service)}</div></section>`+
      `<section class="service-section service-dependencies"><header><h2>Dependencies</h2></header>${serviceRelationships(service)}</section>`+
      (canWrite()?`<section class="danger-zone"><div><h2>Delete service</h2><p>${count(service)?`${count(service)} SLOs reference this service.`:'No SLOs reference this service.'}</p></div>${btn('Delete service…','delete','danger-outline',`data-id="${service.id}"`)}</section>`:'')+'</article>';
  }
  function serviceRelationships(service) {
    const consumers=uses(service),providers=service.deps.map(get).filter(Boolean);
    if(!consumers.length&&!providers.length)return '<p class="dependency-empty">No dependencies recorded.</p>';
    const branch=(label,records,direction)=>`<div class="dependency-branch ${direction}"><h3>${label}<span>${records.length}</span></h3><div class="dependency-node-stack">${records.length?`<ul class="dependency-nodes">${records.map(other=>`<li><a class="dependency-node" href="#service/${other.id}" aria-label="Open ${esc(other.name)}. ${esc(direction==='inbound'?`${other.name} depends on ${service.name}`:`${service.name} depends on ${other.name}`)}"><span class="dependency-node-icon" aria-hidden="true">${icons.service}</span><span><strong>${esc(other.name)}</strong>${other.owner?`<small>${esc(other.owner)}</small>`:''}</span><span class="dependency-open" aria-hidden="true">↗</span></a></li>`).join('')}</ul>`:`<p class="dependency-empty">${direction==='inbound'?'No services recorded.':'No dependencies recorded.'}</p>`}</div></div>`;
    return `<div class="dependency-scene" data-presentation="${dependencyStyle}" data-has-consumers="${consumers.length>0}" data-has-providers="${providers.length>0}">${branch('Used by',consumers,'inbound')}<div class="dependency-current"><div class="dependency-current-node"><span class="dependency-node-icon" aria-hidden="true">${icons.service}</span><strong>${esc(service.name)}</strong><small>This service</small></div></div>${branch('Depends on',providers,'outbound')}</div>`;
  }
  function connectServiceDependencies() {
    const scene=screen.querySelector('.dependency-scene[data-presentation="connected"]');
    if(!scene)return;
    scene.insertAdjacentHTML('afterbegin','<svg class="dependency-connections" aria-hidden="true"></svg>');
    const connections=scene.querySelector('.dependency-connections');
    function draw(){
      const current=scene.querySelector('.dependency-current .dependency-node-icon').getBoundingClientRect();
      if(!current.width){connections.innerHTML='';return;}
      const bounds=scene.getBoundingClientRect(),left=bounds.left+scene.clientLeft,top=bounds.top+scene.clientTop;
      connections.setAttribute('viewBox',`0 0 ${scene.clientWidth} ${scene.clientHeight}`);
      const paths=[...scene.querySelectorAll('.dependency-node')].map(node=>{
        const other=node.querySelector('.dependency-node-icon').getBoundingClientRect(),inbound=!!node.closest('.inbound');
        const from=inbound?other:current,to=inbound?current:other;
        const sx=from.right-left+(inbound?8:10),sy=from.top-top+from.height/2;
        const ex=to.left-left-(inbound?10:8),ey=to.top-top+to.height/2,bend=(sx+ex)/2;
        return `<path class="map-edge active" d="M${sx},${sy} C${bend},${sy} ${bend},${ey} ${ex},${ey}" marker-end="url(#detail-dependency-arrow)"/>`;
      }).join('');
      connections.innerHTML=`<defs><marker id="detail-dependency-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M1 1 9 5 1 9" fill="none" stroke="var(--selection)" stroke-width="1.5"/></marker></defs>${paths}`;
    }
    draw();
    dependencyResize=new ResizeObserver(draw);dependencyResize.observe(scene);
  }
  const input = (id,label,value='',hint='',attrs='') => `<div class="field"><label for="${id}">${label}</label><input id="${id}" name="${id}" value="${esc(value)}" ${attrs} aria-describedby="${id}-hint ${id}-error"><div id="${id}-hint" class="hint">${hint}</div><div id="${id}-error" class="err"></div></div>`;
  function edit(service) {
    const editing=!!service, key=service?.id||'new';
    if(draftKey!==key||!draft) { draftKey=key; draft=service?structuredClone(service):{id:crypto.randomUUID(),slug:'',name:'',description:'',owner:'',contact:'',labels:[],links:[],deps:[]};dirty=false; }
    const formSection=(step,title,body)=>`<section class="section"><header><span class="step">${step}</span><h2>${title}</h2></header><div class="body">${body}</div></section>`;
    screen.innerHTML=heading(editing?`Edit ${service.name}`:'New service','',editing?`${route(esc(service.name),`service/${service.id}`)}<span>/</span><span>Edit</span>`:'<span>New service</span>')+`<div id="form-notice">${state==='conflict'?message('This service changed while you were editing.','Your draft is still here. Reload the saved service before applying your changes.','warning',btn('Reload saved service','reload-draft')):''}</div><form class="form" id="service-form" novalidate><fieldset ${!canWrite()?'disabled':''} style="border:0;padding:0;margin:0">`+
      formSection(1,'Identity',`<div class="grid2">${input('display-name','Display name',draft.name,'','required autocomplete="off"')}${input('identifier','Identifier',draft.slug,'Lowercase letters, numbers and hyphens.','required autocomplete="off"')}</div><div class="field"><label for="description">Description <span class="optional">optional</span></label><textarea id="description" name="description" rows="3" placeholder="What does this service do?">${esc(draft.description)}</textarea></div>`)+
      formSection(2,'Ownership',`<div class="grid2">${input('owner','Owner <span class="optional">optional</span>',draft.owner,'','placeholder="e.g. Store experience"')}${input('contact','Contact <span class="optional">optional</span>',draft.contact,'','placeholder="Team email or support channel"')}</div>`)+
      formSection(3,'Labels',labelEditor())+
      formSection(4,'Links',linkEditor())+
      formSection(5,'Dependencies',dependencyField('service-dependency'))+
      `</fieldset><div class="form-actions">${btn('Cancel','cancel-edit')}${canWrite()?`<button class="btn primary" type="submit">${editing?'Save changes':'Create service'}</button>`:'<span class="read-only">Editor access is required to save.</span>'}</div></form>`;
    paintLabels();paintLinks();
    setupDependencyPicker(screen.querySelector('.dependency-picker'),draft.deps,draft.id,values=>{draft.deps=values;dirty=true;});
  }
  function labelEditor() {
    return `<div class="field"><label for="label-entry">Labels</label><div class="token-field"><ul class="token-list" id="label-chips" aria-label="Added labels"></ul><input id="label-entry" placeholder="team:store" autocomplete="off" aria-describedby="label-entry-hint label-entry-error"><button type="button" class="token-add" data-action="add-label">Add</button></div><p class="hint" id="label-entry-hint">Type key:value and press Enter. Use a key again to add another value.</p><div class="err" id="label-entry-error" role="alert"></div></div>`;
  }
  function paintLabels() {
    screen.querySelector('#label-chips').innerHTML=draft.labels.map(([key,value],index)=>`<li class="input-token service-label">${labelParts(key,value)}<button type="button" data-action="remove-label" data-index="${index}" aria-label="Remove label ${esc(key)}:${esc(value)}">${icons.close}</button></li>`).join('');
  }
  function addLabel() {
    const entry=screen.querySelector('#label-entry'), error=screen.querySelector('#label-entry-error'), text=entry.value.trim();
    error.textContent='';entry.removeAttribute('aria-invalid');if(!text)return true;
    const colon=text.indexOf(':'),key=text.slice(0,colon).trim(),value=text.slice(colon+1).trim();
    if(colon<1||!value||!/^[a-zA-Z0-9]([-._a-zA-Z0-9]{0,61}[a-zA-Z0-9])?$/.test(key)) error.textContent='Use key:value, for example team:store. Keys can contain letters, numbers, dots, underscores and hyphens.';
    else if(draft.labels.some(pair=>pair[0]===key&&pair[1]===value))error.textContent='This label is already added.';
    if(error.textContent){entry.setAttribute('aria-invalid','true');entry.focus();return false;}
    draft.labels.push([key,value]);entry.value='';dirty=true;paintLabels();entry.focus();return true;
  }
  function linkEditor() {
    return `<ul class="added-links" id="added-links" aria-label="Added links"></ul><div class="link-composer">${input('link-name','Name','','','placeholder="e.g. Runbook"')}${input('link-url','URL','','','placeholder="https://…" inputmode="url"')}${btn(`${icons.plus} Add link`,'add-link','')}</div><p class="hint" style="margin:0">Press Enter to add the link.</p>`;
  }
  function paintLinks() {
    screen.querySelector('#added-links').innerHTML=draft.links.map(([name,url],index)=>`<li><span class="saved-link"><a href="${esc(url)}" target="_blank" rel="noopener noreferrer">${esc(name)} ↗</a><span>${esc(url)}</span></span><button type="button" class="remove-saved" data-action="remove-link" data-index="${index}" aria-label="Remove link ${esc(name)}">${icons.close}</button></li>`).join('');
  }
  function addLink() {
    const name=screen.querySelector('#link-name'),url=screen.querySelector('#link-url');
    for(const field of [name,url]){field.removeAttribute('aria-invalid');screen.querySelector(`#${field.id}-error`).textContent='';}
    if(!name.value.trim()&&!url.value.trim())return true;
    let badField=null,reason='';
    if(!name.value.trim()){badField=name;reason='Give this link a name.';}
    else try{const parsed=new URL(url.value.trim());if(!['http:','https:'].includes(parsed.protocol)||parsed.username||parsed.password)throw new Error();}catch{badField=url;reason='Enter an http:// or https:// URL without credentials.';}
    if(!badField&&draft.links.some(pair=>pair[0]===name.value.trim()&&pair[1]===url.value.trim())){badField=url;reason='This link is already added.';}
    if(badField){badField.setAttribute('aria-invalid','true');screen.querySelector(`#${badField.id}-error`).textContent=reason;badField.focus();return false;}
    draft.links.push([name.value.trim(),url.value.trim()]);name.value='';url.value='';dirty=true;paintLinks();name.focus();return true;
  }
  function showModal(title,body,footer='') {
    dialogOrigin=document.activeElement;
    modal.innerHTML=`<header class="modal-head"><h2 id="modal-title">${title}</h2>${btn(icons.close,'close-modal','ghost icon','aria-label="Close dialog"')}</header><div class="modal-body">${body}</div>${footer?`<footer class="modal-foot">${footer}</footer>`:''}`;
    modal.showModal();
  }
  modal.addEventListener('close',()=>dialogOrigin?.isConnected&&dialogOrigin.focus());
  function dependencyField(prefix) {
    return `<div class="field dependency-picker"><label for="${prefix}-search">Search dependencies</label><div class="combo"><div class="token-field"><ul class="token-list" data-selected aria-label="Selected dependencies"></ul><input id="${prefix}-search" role="combobox" aria-expanded="false" aria-controls="${prefix}-options" aria-autocomplete="list" autocomplete="off" placeholder="Search services or owners…"></div><ul id="${prefix}-options" class="combo-list" role="listbox" aria-label="Services" hidden></ul></div></div>`;
  }
  // Extends the existing role picker's type-ahead and keyboard behavior to multiple selections.
  function setupDependencyPicker(picker,initial,subjectID,onChange) {
    const entry=picker.querySelector('input'),list=picker.querySelector('[role="listbox"]'),tokens=picker.querySelector('[data-selected]');
    let selected=[...initial],matches=[],active=-1;
    function close(){list.hidden=true;entry.setAttribute('aria-expanded','false');entry.removeAttribute('aria-activedescendant');active=-1;}
    function paint(){tokens.innerHTML=selected.map(id=>`<li class="input-token"><span>${esc(get(id).name)}</span><button type="button" data-remove-id="${id}" aria-label="Remove dependency ${esc(get(id).name)}">${icons.close}</button></li>`).join('');}
    function options(){
      const term=entry.value.trim().toLowerCase();
      matches=services.filter(service=>service.id!==subjectID&&!selected.includes(service.id)&&`${service.name} ${service.owner}`.toLowerCase().includes(term));
      list.innerHTML=matches.map((service,index)=>`<li role="option" id="${entry.id}-option-${service.id}" data-option="${service.id}" aria-selected="${index===active}"><span class="nm">${esc(service.name)}</span><span class="sm">${esc(service.owner||'Unassigned')} · ${esc(service.description)}</span></li>`).join('')||'<li class="none" role="presentation">No matching services. Try another name or owner.</li>';
      list.hidden=false;entry.setAttribute('aria-expanded','true');
      if(active>=0&&matches[active]){entry.setAttribute('aria-activedescendant',`${entry.id}-option-${matches[active].id}`);document.getElementById(entry.getAttribute('aria-activedescendant'))?.scrollIntoView({block:'nearest'});}else entry.removeAttribute('aria-activedescendant');
    }
    function choose(id){if(!canWrite()||selected.includes(id))return;selected.push(id);paint();onChange([...selected]);entry.value='';entry.focus();close();}
    paint();entry.addEventListener('focus',options);entry.addEventListener('click',options);
    entry.addEventListener('input',()=>{active=-1;options();});
    entry.addEventListener('keydown',event=>{
      if(['ArrowDown','ArrowUp','Home','End'].includes(event.key)){
        event.preventDefault();if(list.hidden)options();if(!matches.length)return;
        if(event.key==='Home')active=0;else if(event.key==='End')active=matches.length-1;else active=event.key==='ArrowDown'?(active+1)%matches.length:(active<=0?matches.length:active)-1;
        options();
      }else if(event.key==='Enter'){event.preventDefault();const choice=matches[active]||(matches.length===1?matches[0]:null);if(!list.hidden&&choice)choose(choice.id);}
      else if(event.key==='Escape'){event.preventDefault();event.stopPropagation();close();}
    });
    list.addEventListener('mousedown',event=>{if(event.target.closest('[data-option]'))event.preventDefault();});
    picker.addEventListener('click',event=>{
      const option=event.target.closest('[data-option]'),remove=event.target.closest('[data-remove-id]');
      if(option)choose(option.dataset.option);
      if(remove&&canWrite()){selected=selected.filter(id=>id!==remove.dataset.removeId);paint();onChange([...selected]);entry.focus();close();}
    });
    picker.addEventListener('focusout',event=>{if(!picker.contains(event.relatedTarget))close();});
  }
  function deleteService(service) {
    const blockers=count(service), dependents=uses(service);
    if(blockers||dependents.length) {
      showModal('This service is still in use',`<p><b>${esc(service.name)}</b> can’t be deleted while other records reference it.</p>${blockers?`<h3>SLOs · ${blockers}</h3><ul>${(objectives[service.id]||[]).map(slo=>`<li>${esc(slo[0])}</li>`).join('')}</ul><p class="muted">Move or remove these SLOs before deleting the service.</p>`:''}${dependents.length?`<h3>Used by · ${dependents.length}</h3>${relationList(dependents)}<p class="muted">Remove these dependency references first.</p>`:''}`,btn('Close','close-modal'));
      return;
    }
    showModal(`Delete ${esc(service.name)}?`,`<p>This removes <b>${esc(service.name)}</b> from the service catalog. It has no SLOs and no services depend on it.</p>`,`${btn('Cancel','close-modal')}${btn('Delete service','confirm-delete','danger-fill',`data-id="${service.id}"`)}`);
  }
  const graphIDs=['storefront','orders','checkout','payments','inventory','notifications','identity'];
  function graph(design='a',small=false) {
    const positions=design==='b'?{storefront:[175,125],checkout:[175,295],orders:[175,465],payments:[520,190],inventory:[520,400],identity:[865,190],notifications:[865,400]}:{storefront:[160,160],orders:[175,410],checkout:[510,280],payments:[805,110],inventory:[870,315],notifications:[730,485],identity:[505,70]};
    const marker=`arrow-${design}-${small?'small':'full'}`;
    const edges=graphIDs.flatMap(id=>get(id).deps.map(dep=>get(dep).routeKey).filter(dep=>graphIDs.includes(dep)).map(dep=>[id,dep]));
    const nearby=new Set(edges.filter(pair=>pair.includes(selectedNode)).flat());
    const glyph=icons.service.replace('<svg ','<svg x="-12" y="-12" width="24" height="24" ');
    const nodes=graphIDs.map(id=>{
      const s=get(id),[x,y]=positions[id],selected=selectedNode===id,r=selected?42:32,slos=objectives[s.id]||[],status=slos.some(slo=>slo[1]==='breached')?'breached':slos.some(slo=>slo[1]!=='healthy')?'unknown':slos.length?'on_track':'no_slos';
      return `<g data-slo-state="${status}" class="map-node ${selected?'selected':nearby.has(id)?'nearby':'quiet'}" ${small?'':`data-action="select-node" data-id="${id}" role="button" tabindex="0" aria-pressed="${selected}" aria-label="Select ${esc(s.name)}, ${count(s)} SLO${count(s)===1?'':'s'}"`} transform="translate(${x} ${y})"><circle class="map-node-halo" r="${r+22}"/><circle class="map-node-orbit" r="${r+11}"/><circle class="map-node-core" r="${r}"/>${glyph}${status==='breached'?`<rect class="map-node-port" x="${r+7}" y="-4" width="8" height="8"/>`:`<circle class="map-node-port" cx="${r+11}" cy="0" r="4"/>`}<text text-anchor="middle" y="${r+36}">${esc(s.name)}</text><text class="subtitle" text-anchor="middle" y="${r+58}">${s.owner?esc(s.owner)+' · ':''}${count(s)} SLO${count(s)===1?'':'s'}</text></g>`;
    }).join('');
    const paths=edges.map(([from,to])=>{
      const [x,y]=positions[from],[tx,ty]=positions[to];
      const direction=tx>=x?1:-1,fromR=from===selectedNode?54:44,toR=to===selectedNode?54:44;
      const sx=x+direction*fromR,ex=tx-direction*toR,bend=Math.max(70,Math.abs(ex-sx)/2),active=from===selectedNode||to===selectedNode;
      const d=tx===x?`M${x+fromR},${y} C${x+fromR+100},${y} ${tx+toR+100},${ty} ${tx+toR},${ty}`:`M${sx},${y} C${sx+direction*bend},${y} ${ex-direction*bend},${ty} ${ex},${ty}`;
      return `<path class="map-edge ${active?'active':''}" d="${d}" marker-end="url(#${marker}${active?'-active':''})"/>`;
    }).join('');
    const groups=design==='b'?['Store experience','Commerce','Platform'].map((label,index)=>`<ellipse class="map-group" cx="${175+index*345}" cy="300" rx="155" ry="270"/><text class="map-caption" text-anchor="middle" x="${175+index*345}" y="50">${label}</text>`).join(''):'';
    const arrows=['','-active'].map(suffix=>`<marker id="${marker}${suffix}" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="6" markerHeight="6" orient="auto"><path d="M1 1 9 5 1 9" fill="none" stroke="var(--${suffix?'selection':'border-strong'})" stroke-width="1.5"/></marker>`).join('');
    return `<svg class="service-network" viewBox="0 0 1040 600" ${small?'aria-hidden="true"':`role="group" aria-label="Service dependency map. Arrows point from a service to the service it depends on."`}><defs>${arrows}</defs>${groups}${paths}${nodes}</svg>`;
  }
  function graphDetails() {
    const s=get(selectedNode);
    return `<span class="service-symbol" aria-hidden="true">${s.name[0]}</span><div class="service-caption"><strong>${esc(s.name)}</strong><small>${esc(s.description)}</small></div>${serviceSloStatus(s)}<span class="label-chip">${uses(s).length} used by</span>${route('View service',`service/${s.id}`,'btn')}`;
  }
  function map(design) {
    const s=get(selectedNode), name={a:'Service focus',b:'Grouped by owner',c:'List & map'}[design]||'Service focus';
    screen.innerHTML=heading('Services',canWrite()?route(`${icons.plus} New service`,'new','btn primary'):'<span class="read-only">View only</span>')+catalogViews(true)+`<div class="variant" aria-label="Dependency design">Design ${['a','b','c'].map(d=>route(`${d.toUpperCase()} · ${{a:'Service focus',b:'Owner groups',c:'List & map'}[d]}`,`map/${d}`,d===design?'on':'')).join('')}</div><div class="service-graph-legend" role="group" aria-label="Service SLO status">${chip('good','On track')}${chip('crit','Breached')}${chip('stale','Unknown')}${chip('none','No SLOs')}</div><section class="panel"><div class="graph-toolbar"><label>Find a service <select id="graph-service">${graphIDs.map(id=>`<option value="${id}" ${id===selectedNode?'selected':''}>${get(id).name}</option>`).join('')}</select></label><span class="map-legend">A → B means A depends on B</span></div><div class="map-desktop ${design==='c'?'map-split':''}">${design==='c'?`<div class="map-service-list">${graphIDs.map(id=>`<button data-action="select-node" data-id="${id}" aria-pressed="${id===selectedNode}"><strong>${get(id).name}</strong><small>${get(id).owner?esc(get(id).owner)+' · ':''}${count(get(id))} SLOs</small></button>`).join('')}</div>`:''}<div class="map-stage">${graph(design)}</div></div><div class="graph-mobile"><h3>Used by</h3>${relationList(uses(s),true)}<div class="selected-focus"><strong>${esc(s.name)}</strong><p style="margin:4px 0 0;font-size:13px">${esc(s.description)}</p></div><h3>Depends on</h3>${relationList(s.deps.map(get).filter(Boolean),true)}</div><div class="graph-details">${graphDetails()}</div></section><p class="compare-note">${{a:'Nearby relationships lead the view. Select a service to inspect it without losing your place.',b:'Owner groups show where dependencies cross team boundaries. Ownership does not change access or SLO results.',c:'A persistent service list gives a predictable way to find a service while the map supplies context.'}[design]||name}</p>${section('Relationships',table(['Service','Depends on'],graphIDs.flatMap(id=>get(id).deps.filter(dep=>graphIDs.includes(get(dep).routeKey)).map(dep=>[route(esc(get(id).name),`service/${id}`,'rowname'),route(esc(get(dep).name),`service/${dep}`,'inline-link')]))))}`;
  }
  function compare() {
    screen.innerHTML=heading('Dependency designs',route('Back to services','list','btn'))+`<p class="lead">Three ways to explore the same seven services and their dependencies. Open each design to try selection, service details and its mobile layout.</p><div class="design-options">${[['a','Service focus','Start with a service and follow its relationships. The simplest view for a specific question.'],['b','Owner groups','See how services connect across teams, with each team kept together.'],['c','List & map','Find services in a familiar list, with the dependency map alongside it.']].map(([d,title,description])=>`<section class="design-option"><div class="map-stage ${d==='c'?'small-split':''}">${d==='c'?`<ul aria-hidden="true">${graphIDs.map(id=>`<li>${get(id).name}</li>`).join('')}</ul>`:''}${graph(d,true)}</div><div class="option-copy"><span class="option-letter">DESIGN ${d.toUpperCase()}</span><h2>${title}</h2><p>${description}</p>${route(`Explore design ${d.toUpperCase()} →`,`map/${d}`,'btn')}</div></section>`).join('')}</div><p class="compare-note">All options use the same catalog data. No dependency changes an SLO’s result, and none is automatically treated as confirmed customer impact.</p>`;
  }
  function components(service=get('checkout')) {
    screen.innerHTML=heading('Catalog components',route('Back to services','list','btn'))+`<p class="lead">Review individual controls and edge states. Use the State and Role controls above to try complete screens in those states.</p><div class="state-grid">`+
      section('Ownership & labels',`<h3>Assigned owner</h3><p>Store experience <span class="muted">· store-experience@example.com</span></p><h3>Unassigned owner</h3><p class="muted">Unassigned</p><h3>Labels with multiple values</h3>${tags({labels:[['team','store'],['team','commerce'],['env','production']]})}`)+
      section('SLO states',`<p>${chip('good','On track')} ${chip('crit','Breached')}</p><p>${chip('stale','Partial data')} ${chip('stale','Stale data')}</p><p>${chip('none','No SLOs')}</p><p class="muted">Status belongs to each SLO. A service without objectives is not marked healthy.</p>`)+
      section('Freely named links',`<ul class="link-list"><li><a href="https://docs.example.com/checkout/runbook" target="_blank" rel="noopener noreferrer">Runbook ↗</a></li><li><a href="https://git.example.com/store/checkout" target="_blank" rel="noopener noreferrer">Git ↗</a></li><li><a href="https://docs.example.com/checkout/launch" target="_blank" rel="noopener noreferrer">Launch checklist ↗</a></li></ul>`)+
      section('Field validation',`${input('example-identifier','Identifier','Checkout_API','Use lowercase letters, numbers and hyphens.','aria-invalid="true"')}<p class="err" style="color:var(--status-breached)">Use a lowercase identifier, for example checkout-api.</p>${input('example-link','URL','docs.example.com','Include https:// in the URL.','aria-invalid="true"')}`)+
      section('Blocked deletion',`<p>Checkout has three SLOs and is referenced by other services.</p>${btn('Review deletion safeguards','delete','danger-outline','data-id="checkout"')}`)+
      section('Unreferenced service',`<p>Recommendations has no SLOs or dependent services. Deletion asks for confirmation.</p>${canWrite()?btn('Delete Recommendations…','delete','danger-outline','data-id="recommendations"'):'<span class="read-only">Only editors can delete a service.</span>'}`)+`</div>`+section('Service selection in New SLO',`<div class="field"><label>Service</label><div class="selection"><span class="service-symbol">C</span><span><strong>${esc(service.name)}</strong><small>${esc(service.description)}</small></span>${btn('Change','choose-slo-service','link')}</div><p class="hint">Pick an existing service, or create one without leaving this form.</p></div><div class="slo-pick">${canWrite()?btn('Create a new service…','inline-create',''):''}</div>`);
  }
  function sloForm(service) {
    screen.innerHTML=heading('New SLO','',`${route(esc(service.name),`service/${service.id}`)}<span>/</span><span>New SLO</span>`)+`<p class="form-intro">Name the objective, pick an indicator, set a target.</p><form id="slo-form" class="form" data-service="${service.id}"><section class="section"><header><span class="step">1</span><h2>Identity</h2><span class="hint">How it appears in lists and who owns it</span></header><div class="body">${input('slo-name','Display name','','For example, Checkout availability.','required')}<div class="field"><label>Service</label><div class="selection"><span class="service-symbol">${esc(service.name[0])}</span><span><strong>${esc(service.name)}</strong><small>${esc(service.description)}</small></span>${btn('Change','choose-slo-service','link')}</div><p class="hint">Pick an existing service, or create one without leaving this form.</p></div><div>${btn('Create a new service…','inline-create','link')}</div></div></section><section class="section"><header><span class="step">2</span><h2>Indicator</h2><span class="hint">What are we measuring?</span></header><div class="body"><div class="selection"><span class="st good"><i aria-hidden="true"></i></span><span><strong>Request success</strong><small>Prometheus · 30 s cadence · up to date</small></span><button type="button" class="btn link" disabled title="Indicator selection is shown in the existing SLO mocks">Change</button></div></div></section><section class="section"><header><span class="step">3</span><h2>Objective</h2><span class="hint">Target over a rolling window</span></header><div class="body"><div class="grid2">${input('slo-target','Target (%)','99.9','Share of events that must be good.','type="number" min="0.0001" max="99.9999" step="any" required')}<div class="field"><label for="slo-window">Rolling window</label><select id="slo-window"><option value="7">7 days</option><option value="28">28 days</option><option value="30" selected>30 days</option><option value="90">90 days</option></select></div></div></div></section><div class="form-actions"><p>Evaluation starts from collected measurements. Missing intervals remain unknown.</p>${route('Cancel',`service/${service.id}`,'btn')}<button type="submit" class="btn primary">Create SLO</button></div></form>`;
  }
  function selectSloService(service) {
    const selection=screen.querySelector('.selection');
    selection.querySelector('.service-symbol').textContent=service.name[0];
    selection.querySelector('strong').textContent=service.name;
    selection.querySelector('small').textContent=service.description;
    const form=document.querySelector('#slo-form');if(form)form.dataset.service=service.id;
  }
  function captureDraft() {
    const form=document.querySelector('#service-form');if(!form||!draft)return;
    Object.assign(draft,{name:form.querySelector('#display-name').value,slug:form.querySelector('#identifier').value,description:form.querySelector('#description').value,owner:form.querySelector('#owner').value,contact:form.querySelector('#contact').value});
  }
  function saveForm() {
    captureDraft();
    const form=document.querySelector('#service-form');let valid=true;
    form.querySelectorAll('[aria-invalid]').forEach(input=>input.removeAttribute('aria-invalid'));
    form.querySelectorAll('.err').forEach(error=>error.textContent='');
    if(!addLabel()||!addLink())return;
    const invalid=(input,message)=>{valid=false;input.setAttribute('aria-invalid','true');document.querySelector(`#${input.id}-error`).textContent=message;};
    if(!draft.name.trim())invalid(form.querySelector('#display-name'),'Enter a display name.');
    if(!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(draft.slug))invalid(form.querySelector('#identifier'),'Use 1–63 lowercase letters, numbers or hyphens; start and end with a letter or number.');
    if(services.some(service=>service.slug===draft.slug&&service.id!==draft.id))invalid(form.querySelector('#identifier'),'This identifier is already in use. Choose another.');
    if(!valid){form.querySelector('[aria-invalid="true"]').focus();return;}
    const notice=document.querySelector('#form-notice');
    if(state==='conflict'){notice.innerHTML=message('This service changed while you were editing.','Your draft is preserved. Reload the saved service before trying again.','warning',btn('Reload saved service','reload-draft'));notice.scrollIntoView({block:'center'});return;}
    if(state==='save-error'){notice.innerHTML=message('Your changes couldn’t be saved.','Your draft is still here. Try again when the service is available.','error',btn('Try again','retry-save'));notice.scrollIntoView({block:'center'});return;}
    const editing=draftKey!=='new';const record={...structuredClone(draft),name:draft.name.trim(),created:get(draftKey)?.created||'6 Nov 2026, 13:42 UTC',changed:'6 Nov 2026, 13:42 UTC'};
    if(editing)services[services.findIndex(s=>s.id===draftKey)]=record;else services.push(record);
    dirty=false;draft=null;draftKey='';location.hash=`service/${record.id}`;notify(editing?'Service changes saved.':'Service created. Add an SLO whenever you’re ready.');
  }
  function render() {
    dependencyResize?.disconnect();
    const [view='list',id]=location.hash.slice(1).split('/');
    const service=get(id||'checkout');
    document.title=`${view==='list'?'Services':view==='new'?'New service':service?.name||'Services'} · Almanac design proposal`;
    document.querySelectorAll('.review-bar nav a').forEach(a=>{const match=a.hash===location.hash||(!location.hash&&a.hash==='#list');if(match)a.setAttribute('aria-current','page');else a.removeAttribute('aria-current');});
    if(view==='list'||!view)list();else if(view==='new')edit(null);else if(view==='compare')compare();else if(view==='map')map(['a','b','c'].includes(id)?id:'a');else if(view==='components')components(service||get('checkout'));else if(!service)screen.innerHTML=heading('Service not found',route('Back to services','list','btn'))+empty('This service is unavailable','It may have been deleted. Return to the catalog to find another service.');else if(view==='slo-component')sloForm(service);else if(view==='edit')edit(service);else detail(service);
    if(view==='dependencies')requestAnimationFrame(()=>screen.querySelector('.service-dependencies')?.scrollIntoView({block:'start'}));
    const h1=screen.querySelector('h1');if(h1){h1.tabIndex=-1;h1.focus({preventScroll:true});}
    document.querySelector('#preview-state').value=state;document.querySelector('#preview-role').value=role;
    document.querySelector('#dependency-style-control').hidden=!['service','dependencies'].includes(view);
    document.querySelector('#preview-dependencies').value=dependencyStyle;
    document.querySelector('.account strong').textContent='Alex Morgan';
    document.querySelector('.account small').textContent=role==='viewer'?'Viewer':'Editor';
    connectServiceDependencies();
  }
  document.addEventListener('input',event=>{
    if(event.target.id==='service-search'){search=event.target.value;page=1;listResults();}
    if(event.target.closest('#service-form')){dirty=true;if(event.target.id==='display-name'&&draftKey==='new'&&!document.querySelector('#identifier').dataset.edited)document.querySelector('#identifier').value=event.target.value.toLowerCase().replace(/[^a-z0-9]+/g,'-').replace(/^-|-$/g,'').slice(0,63);if(event.target.id==='identifier')event.target.dataset.edited='true';}
  });
  document.addEventListener('change',event=>{
    const {id,value}=event.target;
    if(id==='owner-filter'){ownerFilter=value;page=1;listResults();}
    if(id==='page-size'){pageSize=Number(value);page=1;listResults();}
    if(id==='preview-state'){captureDraft();state=value;render();}
    if(id==='preview-role'){captureDraft();role=value;render();}
    if(id==='preview-dependencies'){dependencyStyle=value;render();}
    if(id==='graph-service'){selectedNode=value;render();}
  });
  document.addEventListener('submit',event=>{
    if(event.target.id==='service-form'){event.preventDefault();if(canWrite())saveForm();}
    if(event.target.id==='slo-form'){
      event.preventDefault();if(!canWrite())return;
      const form=event.target, id=form.dataset.service;
      (objectives[id]||=[]).push([form.elements['slo-name'].value,'unknown','Waiting for data',`${form.elements['slo-target'].value}% · ${form.querySelector('#slo-window').value} d`,'—']);
      location.hash=`service/${id}`;notify('SLO created. It will show a result when measurements are available.');
    }
    if(event.target.id==='inline-service-form'){
      event.preventDefault();if(!canWrite())return;
      const name=event.target.elements['inline-name'].value.trim(),slug=event.target.elements['inline-id'].value.trim();
      if(!name||!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(slug)||services.some(service=>service.slug===slug)){document.querySelector('#inline-error').textContent='Enter a name and a unique lowercase identifier.';return;}
      const id=crypto.randomUUID();
      services.push({id,slug,name,description:'',owner:'',contact:'',labels:[],links:[],deps:[],created:'6 Nov 2026, 13:42 UTC',changed:'6 Nov 2026, 13:42 UTC'});
      modal.close();selectSloService(get(id));notify(`${name} created and selected. Your SLO form stays open.`);
    }
  });
  document.addEventListener('click',event=>{
    if(event.target.closest('.skip')){event.preventDefault();screen.focus();return;}
    const row=event.target.closest('[data-row-href]');
    if(row&&!event.target.closest('a,button,input,select,textarea')&&!event.ctrlKey&&!event.metaKey&&!event.shiftKey&&!event.altKey&&!window.getSelection()?.toString()){
      location.hash=row.dataset.rowHref;return;
    }
    const target=event.target.closest('[data-action]');if(!target)return;
    const action=target.dataset.action,id=target.dataset.id;
    if(action==='coverage'){coverage=target.dataset.value;page=1;list();}
    if(action==='clear-filters'){search='';coverage='all';ownerFilter='';page=1;list();}
    if(action==='previous'||action==='next'){page+=action==='next'?1:-1;listResults();}
    if(action==='refresh'||action==='retry'){state='normal';render();notify('Services refreshed.');}
    if(action==='close-modal'){modal.close();}
    if(action==='add-label'&&canWrite())addLabel();
    if(action==='add-link'&&canWrite())addLink();
    if(action==='remove-label'&&canWrite()){draft.labels.splice(Number(target.dataset.index),1);dirty=true;paintLabels();screen.querySelector('#label-entry').focus();}
    if(action==='remove-link'&&canWrite()){draft.links.splice(Number(target.dataset.index),1);dirty=true;paintLinks();screen.querySelector('#link-name').focus();}
    if(action==='delete'&&canWrite())deleteService(get(id));
    if(action==='confirm-delete'&&canWrite()){if(count(get(id))||uses(get(id)).length)return;services=services.filter(s=>s.id!==id);modal.close();location.hash='list';notify('Service deleted.');}
    if(action==='cancel-edit'){const destination=draftKey==='new'?'list':`service/${draftKey}`;if(dirty)showModal('Discard service changes?','<p>Your changes have not been saved.</p>',`${btn('Keep editing','close-modal')}${btn('Discard changes','discard','danger-fill',`data-destination="${destination}"`)}`);else location.hash=destination;}
    if(action==='discard'){dirty=false;draft=null;draftKey='';modal.close();location.hash=target.dataset.destination;}
    if(action==='reload-draft'){draft=null;draftKey='';dirty=false;state='normal';render();notify('Saved service reloaded.');}
    if(action==='retry-save'){state='normal';saveForm();}
    if(action==='select-node'){selectedNode=id;render();const control=screen.querySelector(`.map-node[data-id="${id}"]`);control?.focus({preventScroll:true});}
    if(action==='choose-slo-service'){showModal('Choose a service',`<div class="dependency-options">${services.map(s=>`<div class="relationships"><li><span class="service-caption"><strong>${esc(s.name)}</strong><small>${esc(s.description)}</small></span>${btn('Select','select-slo-service','',`data-id="${s.id}"`)}</li></div>`).join('')}</div>`,btn('Cancel','close-modal'));}
    if(action==='select-slo-service'){modal.close();selectSloService(get(id));}
    if(action==='inline-create'&&canWrite()){showModal('New service',`<p>Create a service and keep working on your SLO.</p><form id="inline-service-form">${input('inline-name','Display name','','','required')}${input('inline-id','Identifier','','Lowercase letters, numbers and hyphens.','required')}<p id="inline-error" role="alert" style="color:var(--status-breached)"></p></form>`,`${btn('Cancel','close-modal')}<button class="btn primary" type="submit" form="inline-service-form">Create and select</button>`);}
  });
  document.addEventListener('keydown',event=>{
    if(event.key==='Enter'&&event.target.id==='label-entry'){event.preventDefault();if(canWrite())addLabel();}
    if(event.key==='Enter'&&['link-name','link-url'].includes(event.target.id)){
      event.preventDefault();if(!canWrite())return;
      if(event.target.id==='link-name'&&!screen.querySelector('#link-url').value)screen.querySelector('#link-url').focus();else addLink();
    }
    if(event.target.matches('.map-node')&&['Enter',' '].includes(event.key)){event.preventDefault();event.target.dispatchEvent(new MouseEvent('click',{bubbles:true}));}
    if(event.key==='/'&&!event.target.closest('input,textarea,select,[contenteditable]')&&!modal.open){const input=document.querySelector('#service-search');if(input){event.preventDefault();input.focus();}}
  });
  window.addEventListener('hashchange',()=>{notify('');render();window.scrollTo(0,0);});
  const productNav=document.querySelector('.navgroup');
  productNav.querySelector('h2').insertAdjacentHTML('afterend',`<a data-services href="#list" class="current" aria-current="page">${icons.service}Services</a>`);
  productNav.querySelectorAll('a:not([data-services])').forEach(a=>{a.href=a.textContent.trim()==='SLOs'?'../slo-list.html':'../sli-list.html';});
  const themeButton=document.querySelector('.account button');
  function updateTheme(){themeButton.textContent=document.documentElement.classList.contains('dark')?'☾ Dark theme':'☼ Light theme';themeButton.title='Switch color theme';}
  themeButton.addEventListener('click',()=>{document.documentElement.classList.toggle('dark');updateTheme();});updateTheme();
  const menu=document.querySelector('#mobile-menu');
  document.querySelector('.mobile-head button').addEventListener('click',()=>{menu.innerHTML=`<header class="modal-head"><h2>Almanac</h2><button type="button" class="btn ghost" id="close-mobile">Close</button></header><nav class="mobile-menu-nav">${route('Services','list')}${route('Dependency designs','compare')}${route('Catalog components','components')}<button class="btn" id="mobile-theme">Switch theme</button></nav>`;menu.showModal();menu.querySelector('#close-mobile').onclick=()=>menu.close();menu.querySelector('#mobile-theme').onclick=()=>{document.documentElement.classList.toggle('dark');updateTheme();};menu.querySelectorAll('a').forEach(a=>a.addEventListener('click',()=>menu.close()));});
  document.querySelector('.account button:last-child').addEventListener('click',()=>notify('This is a design prototype; no signed-in account is connected.'));
  render();
})();
