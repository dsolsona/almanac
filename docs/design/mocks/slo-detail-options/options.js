// Design exploration only. Fixed fictional evaluations, never live service data.
const params = new URLSearchParams(location.search);
let state = params.get('state') || 'burning';
let theme = params.get('theme') || 'light';
let burnWindow = '1 h';
const directions = {
  a: ['A · Essential', 'Evolution', 'The familiar page, edited down: one verdict, three useful numbers, and four calm, aligned lanes.'],
  b: ['B · Budget in context', 'Evolution', 'Integrate the summary into the budget graph, as the new SLI page does with measurements. The graph becomes the status card.'],
  c: ['C · Signal & detail', 'Evolution', 'Two generous primary charts, with attainment and completed-day compliance lower down. More room to inspect each signal.'],
  d: ['D · Reliability briefing', 'Radical', 'A readable briefing: where we stand, what changed, and what to inspect. A narrative instead of a dashboard.'],
  e: ['E · Budget ledger', 'Radical', 'Make the error allowance tangible. A visual balance sheet leads; time-series evidence explains how it changed.'],
  f: ['F · Investigation desk', 'Radical', 'An analysis workspace: persistent current context beside large linked charts. Designed for sustained investigation.'],
};
const scenarios = {
  burning: {label:'Burning fast',tone:'warn',budget:18,attainment:'99.918',burn:'2.40',six:'1.20',good:9991800,bad:8200,remaining:1800,coverage:'Complete · fresh',copy:'The objective is met, but recent errors are spending budget quickly.',detail:'18% remains over 30 days. The last hour is burning at 2.4×; the 6-hour rate is 1.2×.',met:'Met',days:27},
  healthy: {label:'Healthy',tone:'good',budget:72,attainment:'99.972',burn:'0.30',six:'0.45',good:9997200,bad:2800,remaining:7200,coverage:'Complete · fresh',copy:'The objective is met and recent burn is below 1×.',detail:'72% of the error budget remains. Both recent burn windows are below 1×.',met:'Met',days:30},
  breached: {label:'Breached',tone:'crit',budget:-20,attainment:'99.880',burn:'0.44',six:'0.70',good:9988000,bad:12000,remaining:-2000,coverage:'Complete · fresh',copy:'Recent burn has slowed, but the 30-day objective is still breached.',detail:'The allowance is overspent by 2,000 events. A quieter last hour does not restore the rolling budget.',met:'Violated',days:24},
  partial: {label:'Partial data',tone:'none',budget:18,attainment:'99.918',burn:'—',six:'—',good:9991800,bad:8200,remaining:1800,coverage:'41,760 / 43,200 intervals',copy:'The full-window result is unknown because measurements are missing.',detail:'The observed budget is 18%. Missing intervals can change that result; inspect collection before judging performance.',met:'Unknown',days:25},
};
const fmt = n => n.toLocaleString('en-US');
const s = () => scenarios[state] || scenarios.burning;
const observed = () => state === 'partial' ? 'Observed ' : '';
const mark = () => `<span class="st ${s().tone}"><i aria-hidden="true"></i>${s().label}</span>`;
const metric = (label,value,note,cls='') => `<div class="metric"><span class="label">${label}</span><strong class="${cls}">${value}</strong><small>${note}</small></div>`;
const verdict = () => `<div class="verdict ${state==='healthy'?'status-good':state==='breached'?'status-breach':state==='partial'?'status-missing':''}"><div>${mark()}<p>${s().copy}</p></div><a class="btn sm" href="${state==='partial'?'#definition':'#burn'}">${state==='partial'?'Inspect data ↓':'Inspect burn ↓'}</a></div>`;
const metrics = () => `<div class="metrics">${metric(`${observed()}budget remaining`,`${s().budget}%`,`${fmt(s().remaining)} events ${s().remaining<0?'overspent':'left'}`,state==='breached'?'bad':state==='healthy'?'good-text':'warm')}${metric(`${observed()}attainment`,`${s().attainment}%`,'99.9% target · rolling 30 days')}${metric('Recent burn',`${s().burn}${s().burn==='—'?'':'×'}`,`1 h · ${s().six}${s().six==='—'?'':'×'} over 6 h`)}</div>`;
// Explicit independent historical snapshots for visual composition, not derived from a current interval ratio.
function samples(kind) {
  const budget = state==='healthy'?[91,89,88,85,84,81,79,77,75,74,73,72]:state==='breached'?[70,67,65,62,60,57,48,15,-15,-21,-20,-20]:[85,82,78,76,71,66,60,53,35,24,21,18];
  const values = kind==='budget'?budget:kind==='burn'?(burnWindow==='1 h'?[.4,.3,.5,.4,.7,.6,.8,1,5.4,3.8,2.8,Number(s().burn)||0]:[.4,.4,.4,.5,.5,.6,.7,.8,1,1.3,1.4,Number(s().six)||0]):[99.98,99.97,99.98,99.96,99.95,99.94,99.96,99.92,99.52,99.81,99.91,99.94];
  return values.map((value,i)=>({i,timestamp:new Date(Date.UTC(2026,10,6,10)+i*6*86400000/11).toISOString(),value:state==='partial'&&(i===7||i===8)?null:value}));
}
function chart(kind,compact=false) {
  const data=samples(kind), label=kind==='budget'?'Rolling budget remaining':kind==='burn'?`Burn rate · ${burnWindow}`:'Interval attainment';
  const ticks=kind==='budget'?[100,50,0,-25]:kind==='burn'?[6,4,2,0]:[100,99.8,99.6,99.4];
  const direction=location.hash.slice(1);
  let available=innerWidth-(innerWidth>860?264:0)-(innerWidth>700?104:44);
  if(innerWidth>700&&(direction==='c'||direction==='d'&&kind!=='burn'||direction==='e'&&kind!=='budget'))available=(available-40)/2;
  if(innerWidth>1100&&direction==='f')available-=310;
  const h=compact?160:225, w=Math.max(280,available), left=65,right=w-20, top=16,bottom=h-28;
  const max=ticks[0],min=ticks.at(-1), x=i=>left+i*(right-left)/11,y=v=>top+(max-v)/(max-min)*(bottom-top);
  const segments=[];let seg=[];
  data.forEach(p=>{if(p.value===null){if(seg.length)segments.push(seg);seg=[];}else seg.push(p);});if(seg.length)segments.push(seg);
  const ref=kind==='budget'?0:kind==='burn'?1:99.9;
  const unit=kind==='burn'?'×':'%';
  return `<svg viewBox="0 0 ${w} ${h}" role="img" aria-label="${label}. Fictional history from 6 to 12 November UTC. ${state==='partial'?'Missing periods are gaps.':''}">${ticks.map(t=>`<line class="gridline" x1="${left}" x2="${right}" y1="${y(t)}" y2="${y(t)}"/><text class="axis" x="55" y="${y(t)+4}" text-anchor="end">${t}${unit}</text>`).join('')}<line class="ref" x1="${left}" x2="${right}" y1="${y(ref)}" y2="${y(ref)}"/>${segments.map(seg=>`${kind==='budget'?`<path class="fill" d="M${x(seg[0].i)},${y(0)} ${seg.map(p=>`L${x(p.i)},${y(p.value)}`).join(' ')} L${x(seg.at(-1).i)},${y(0)} Z"/>`:''}<path class="curve ${kind==='burn'?'burn':''}" d="${seg.map((p,i)=>`${i?'L':'M'}${x(p.i)},${y(p.value)}`).join(' ')}"/>`).join('')}${(innerWidth<=700?[0,6,11]:[0,4,8,11]).map(i=>`<text class="axis" x="${x(i)}" y="${h-7}" text-anchor="${i===0?'start':i===11?'end':'middle'}">${({0:'06 Nov',4:'08 Nov',6:'09 Nov',8:'10 Nov',11:'12 Nov UTC'})[i]}</text>`).join('')}<circle class="inspection-dot" r="4.5" hidden/><line class="cursor" x1="${left}" x2="${left}" y1="${top}" y2="${bottom}" hidden/></svg>`;
}
const burnControls = () => `<span class="inline" aria-label="Burn window">${['1 h','6 h'].map(v=>`<button class="mini" data-burn="${v}" aria-pressed="${burnWindow===v}">${v}</button>`).join('')}</span>`;
function plot(kind,compact=false) {
  const label=kind==='budget'?'Error budget':kind==='burn'?'Recent burn':'Attainment';
  return `<section class="panel" id="${kind}"><div class="plot-head"><h2>${label}</h2>${kind==='burn'?burnControls():`<small>${kind==='budget'?'Rolling 30-day evaluation':'Good ÷ total per plotted interval'}</small>`}</div><div class="plot-body inspect" data-kind="${kind}" tabindex="0" role="group" aria-label="${label} inspection: use left and right arrows, Home, End and Escape">${chart(kind,compact)}</div><div class="inspect-note sr-only" aria-live="polite"></div><div class="plot-foot"><p>${kind==='budget'?'0% exhausted':kind==='burn'?'1× reference':'99.9% target'}</p></div></section>`;
}
function compliance() {
 return `<section class="panel" id="compliance"><div class="plot-head"><h2>Completed days</h2><small>${s().days} / 30 met · ${state==='partial'?'3 unknown':'0 unknown'}</small></div><div class="inset"><div class="day-strip" role="img" aria-label="30 completed UTC days: ${s().days} met, ${state==='partial'?'2 violated, 3 unknown':`${30-s().days} violated`}">${Array.from({length:30},(_,i)=>`<i class="${i<s().days?'':state==='partial'&&i>=27?'unknown':'violated'}" title="${i+1}: ${i<s().days?'Met':state==='partial'&&i>=27?'Unknown':'Violated'}"></i>`).join('')}</div><div class="plot-foot" style="padding:0"><span class="day-legend"><span><i class="day-key met" aria-hidden="true"></i>Met</span><span><i class="day-key violated" aria-hidden="true"></i>Violated</span><span><i class="day-key unknown" aria-hidden="true"></i>Unknown</span></span></div><p class="small">Rolling result at UTC day end</p></div></section>`;
}
const definition = () => `<details class="disclosure" id="definition"><summary>Objective & data quality <span>99.9% · rolling 30 days · ${s().coverage}</span></summary><div class="objective-body">
<div class="calculation-inputs"><div><span>Target</span><strong>99.9%</strong><small>Successful requests</small></div><div><span>Window</span><strong>30 days</strong><small>Rolling evaluation</small></div><div><span>Method</span><strong>Occurrence</strong><small>Good events ÷ total events</small></div></div>
<section class="objective-measurement"><h3>Measurement</h3><a href="../sli-detail-options/refined.html">Checkout request success →</a><p>Successful checkout requests ÷ all checkout requests</p><span class="small">HTTP 5xx responses count as unsuccessful · collected every minute</span></section>
<section class="objective-quality"><div class="objective-section-head"><h3>Data quality</h3><span class="st ${state==='partial'?'warn':'good'}"><i aria-hidden="true"></i>${state==='partial'?'Partial data':'Complete'}</span></div><dl class="quality-facts"><div><dt>Collected intervals</dt><dd>${state==='partial'?'41,760':'43,200'} <small>/ 43,200</small></dd></div><div><dt>Missing intervals</dt><dd class="${state==='partial'?'warm':''}">${state==='partial'?'1,440':'0'}</dd></div><div><dt>Observation freshness</dt><dd>Fresh</dd></div></dl>${state==='partial'?'<p class="calculation-warning">Full-window performance is unknown. Budget and attainment use observed requests only.</p>':''}</section>
<dl class="objective-timing"><div><dt>Evaluation period · UTC</dt><dd>13 Oct–12 Nov 2026 · 10:00</dd></div><div><dt>Measurements through · UTC</dt><dd>12 Nov 2026 · 10:00</dd></div></dl>
</div></details><details class="disclosure" id="calculation"><summary>Calculation details <span>How the budget is calculated</span></summary><div class="calculation-body">
${state==='partial'?'<p class="calculation-warning">Observed requests only · missing measurements make the full-window result unknown.</p>':''}
<div class="calculation-inputs"><div><span>Target</span><strong>99.9%</strong><small>0.1% allowed errors</small></div><div><span>${observed()}requests</span><strong>10,000,000</strong><small>${fmt(s().good)} successful</small></div><div><span>Evaluation window</span><strong>30 days</strong><small>13 Oct–12 Nov · 10:00 UTC</small></div></div>
<ol class="calculation-steps">
<li><div><h3>Error allowance</h3><span>Total requests × allowed error rate</span></div><div class="calculation-equation"><span>10,000,000 × 0.1%</span><strong>= 10,000 <small>events</small></strong></div></li>
<li><div><h3>Remaining allowance</h3><span>Allowed errors − unsuccessful requests</span></div><div class="calculation-equation"><span>10,000 − ${fmt(s().bad)}</span><strong class="${s().remaining<0?'bad':''}">= ${fmt(s().remaining)} <small>events</small></strong></div></li>
<li class="calculation-result"><div><h3>${observed()}budget remaining</h3><span>Remaining allowance ÷ error allowance</span></div><div class="calculation-equation"><span>${fmt(s().remaining)} ÷ 10,000</span><strong class="${s().remaining<0?'bad':''}">= ${s().budget}%</strong></div></li>
</ol><div class="calculation-meta"><span>Evaluated 12 Nov 2026 · 10:00 UTC</span><span>${s().coverage}</span></div>
</div></details>`;
const rangeNote = () => `<div class="rowline small" style="justify-content:space-between"><span>History · 06–12 Nov 2026 · UTC</span><span>Current summary · rolling 30 days ending 12 Nov, 10:00 UTC</span></div>`;
function a(){return `<div class="stack"><section class="panel">${verdict()}${metrics()}</section>${rangeNote()}${plot('budget',true)}${plot('burn',true)}${plot('attainment',true)}${compliance()}${definition()}</div>`;}
function b(){return `<div class="stack">${verdict()}<section class="panel"><div class="plot-head"><h2>Your error budget</h2><small>7-day history · UTC</small></div>${metrics()}<div class="plot-body inspect" data-kind="budget" tabindex="0" role="group" aria-label="Budget history. Use arrow keys to inspect.">${chart('budget')}</div><div class="inspect-note sr-only" aria-live="polite"></div><div class="plot-foot"><span>${s().coverage}</span></div></section>${plot('burn')}${plot('attainment',true)}${compliance()}${definition()}</div>`;}
function c(){return `<div class="stack">${verdict()}<section class="panel">${metrics()}</section>${rangeNote()}<div class="paired">${plot('budget')}${plot('burn')}</div><div class="paired">${plot('attainment')}${compliance()}</div>${definition()}</div>`;}
function d(){return `<div class="stack"><section class="panel"><div class="brief editorial"><div><span class="eyebrow">Your reliability briefing · 12 Nov</span><h2>${state==='breached'?'Burn is slowing.<br>The budget is still overspent.':state==='partial'?'Missing data.<br>An incomplete answer.':state==='healthy'?'Within target.<br>Room to spare.':'Within target.<br>Less room for error.'}</h2>${mark()}<p>${s().detail}</p></div><div>${metric(`${observed()}budget remaining`,`${s().budget}%`,`${fmt(s().remaining)} events ${s().remaining<0?'overspent':'left'}`)}${chart('budget',true)}</div></div><div class="brief"><span class="step">01 / WHERE WE STAND</span><h2>${observed()}${s().attainment}% against a 99.9% target</h2><p>${s().copy}</p><p class="small">${s().coverage} · rolling 30-day objective · evaluated 10:00 UTC</p></div><div class="brief"><span class="step">02 / WHAT CHANGED</span><h2>${state==='partial'?'There are gaps in the evidence.':'The recent rate matters too.'}</h2><p>${s().burn==='—'?'Recent burn is unavailable. Missing measurements are not successful requests.':`Last hour: ${s().burn}× burn. Last six hours: ${s().six}×. These rates describe recent spending, independently of the 30-day result.`}</p>${plot('burn')}</div><div class="brief"><span class="step">03 / FOLLOW THE EVIDENCE</span><h2>Inspect the measurements and daily outcomes.</h2><div class="paired">${plot('attainment',true)}${compliance()}</div></div></section>${definition()}</div>`;}
function e(){return `<div class="stack">${verdict()}<section class="panel inset"><span class="eyebrow">The 30-day error allowance</span><div class="balance"><div><h2 class="section-title" style="margin:16px 0">${observed()}budget balance</h2><div class="budget-number ${state==='breached'?'bad':''}">${s().budget}%</div><p>${fmt(s().remaining)} unsuccessful requests ${s().remaining<0?'beyond the allowance':'still within the allowance'}</p><p class="small">${s().coverage} · ${s().met}</p></div><div><div class="ledger" role="img" aria-label="${Math.max(0,s().budget)} percent remains of the allowed errors. ${s().budget<0?'Additional overspend is shown numerically.':''}">${Array.from({length:50},(_,i)=>`<i class="${i<50-Math.max(0,s().budget)/2?'spent':''}"></i>`).join('')}</div><div class="ledger-legend"><span>■ Remaining</span><span>▧ Used · 2% per block</span></div><p class="small">The grid stops at zero; negative balances stay visible above.</p></div></div><div class="metrics" style="border-top:1px solid var(--border);margin-top:24px">${metric('Allowed errors','10,000','0.1% of 10 million requests')}${metric('Unsuccessful',fmt(s().bad),`${observed()}${s().attainment}% attainment`)}${metric('Recent burn',`${s().burn}${s().burn==='—'?'':'×'}`,`1 h · ${s().six}${s().six==='—'?'':'×'} over 6 h`)}</div></section>${rangeNote()}${plot('budget')}<div class="paired">${plot('burn')}${plot('attainment')}</div>${compliance()}${definition()}</div>`;}
function f(){return `<div class="stack">${verdict()}<nav class="focus-links" aria-label="Jump to evidence"><a href="#budget">Budget</a><a href="#burn">Burn</a><a href="#attainment">Measurements</a><a href="#compliance">Completed days</a></nav><div class="workspace"><div class="stack">${rangeNote()}${plot('budget')}${plot('burn')}${plot('attainment')}${compliance()}</div><aside class="panel context"><span class="eyebrow">Current context</span><h2 style="margin:10px 0">${s().label}</h2><p class="small">${s().copy}</p><dl><dt>${observed()}budget left</dt><dd>${s().budget}%</dd><dt>${observed()}attainment</dt><dd>${s().attainment}%</dd><dt>Target</dt><dd>99.9% / 30 d</dd><dt>1 h / 6 h burn</dt><dd>${s().burn} / ${s().six} ×</dd></dl><p class="small">${s().coverage}</p><p class="small">Evaluated 12 Nov, 10:00 UTC.<br>Current context stays fixed while inspecting historical points.</p><a href="#definition" class="btn sm">Objective & data ↓</a></aside></div>${definition()}</div>`;}
function render(){
 const key=location.hash.slice(1) in directions?location.hash.slice(1):'a';
 document.documentElement.classList.toggle('dark',theme==='dark');
 document.title=`SLO detail · ${directions[key][0]}`;
 document.getElementById('content').innerHTML=`<aside class="review" aria-label="Design review controls"><div class="review-top"><span class="eyebrow">Six proposals · fictional sample data</span><div class="switches"><label>Theme <select id="theme"><option value="light">Light</option><option value="dark">Dark</option></select></label><label>Scenario <select id="scenario">${Object.keys(scenarios).map(v=>`<option value="${v}">${scenarios[v].label}</option>`).join('')}</select></label></div></div><nav class="directions" aria-label="Design directions">${Object.entries(directions).map(([k,v])=>`<a href="#${k}" ${key===k?'aria-current="page"':''}>${v[0]}</a>`).join('')}</nav></aside><nav class="crumbs" aria-label="Breadcrumb"><a href="../slo-list.html">SLOs</a><span>/</span>Checkout availability</nav><header class="heading"><div><h1>Checkout availability</h1><p>Successful checkout requests · 99.9% over a rolling 30 days</p></div><a class="btn" href="#definition">View definition ↓</a></header>${({a,b,c,d,e,f})[key]()}`;
 document.getElementById('theme').value=theme;document.getElementById('scenario').value=state;
 document.getElementById('theme').onchange=event=>{theme=event.target.value;persist();render();};
 document.getElementById('scenario').onchange=event=>{state=event.target.value;persist();render();};
 document.querySelectorAll('[data-burn]').forEach(button=>button.onclick=()=>{const y=scrollY;burnWindow=button.dataset.burn;render();window.scrollTo(0,y);});
 document.querySelectorAll('a[href^="#"]').forEach(link=>{if(link.hash.slice(1) in directions)return;link.onclick=event=>{event.preventDefault();const target=document.getElementById(link.hash.slice(1));if(target){if(target.tagName==='DETAILS')target.open=true;target.scrollIntoView({behavior:'instant',block:'start'});const focus=target.querySelector('summary')||target;if(focus.tagName!=='SUMMARY')focus.setAttribute('tabindex','-1');focus.focus({preventScroll:true});}};});
 document.querySelectorAll('.inspect').forEach(el=>{
   let i=11;
   const tooltip=document.createElement('div');tooltip.className='point-tooltip';tooltip.hidden=true;el.appendChild(tooltip);
   const clear=()=>{el.querySelector('.cursor').setAttribute('hidden','');el.querySelector('.inspection-dot').setAttribute('hidden','');tooltip.hidden=true;el.nextElementSibling.textContent='';};
   const inspect=value=>{
     i=Math.max(0,Math.min(11,value));const kind=el.dataset.kind,sample=samples(kind)[i];
     const svg=el.querySelector('svg'),width=svg.viewBox.baseVal.width,height=svg.viewBox.baseVal.height;
     const px=65+i*(width-85)/11;
     const line=el.querySelector('.cursor');line.removeAttribute('hidden');line.setAttribute('x1',px);line.setAttribute('x2',px);
     const dot=el.querySelector('.inspection-dot');
     if(sample.value===null)dot.setAttribute('hidden','');else{
       const max=kind==='budget'?100:kind==='burn'?6:100,min=kind==='budget'?-25:kind==='burn'?0:99.4;
       dot.setAttribute('cx',px);dot.setAttribute('cy',16+(max-sample.value)/(max-min)*(height-44));dot.removeAttribute('hidden');
     }
     const time=new Date(sample.timestamp).toLocaleString('en-GB',{day:'2-digit',month:'short',year:'numeric',hour:'2-digit',minute:'2-digit',timeZone:'UTC',hour12:false})+' UTC';
     const label=kind==='budget'?'Rolling budget remaining':kind==='burn'?`Burn rate · ${burnWindow} window`:'Interval attainment';
     const valueText=sample.value===null?'Missing · no value':`${sample.value}${kind==='burn'?'×':'%'}`;
     tooltip.innerHTML=`<span>${time}</span><strong>${valueText}</strong><span>${label}</span><small>${sample.value===null?'No measurement available':'Complete evaluation'}</small>`;
     tooltip.hidden=false;
     const bounds=el.getBoundingClientRect(),chartBounds=svg.getBoundingClientRect();
     const pointX=chartBounds.left-bounds.left+px/width*chartBounds.width;
     tooltip.style.left=`${Math.max(8,Math.min(bounds.width-tooltip.offsetWidth-8,pointX+14))}px`;
     el.nextElementSibling.textContent=`${time} · ${valueText} · ${label} · ${i+1} / 12`;
   };
   el.onpointermove=event=>{const svg=el.querySelector('svg'),r=svg.getBoundingClientRect(),width=svg.viewBox.baseVal.width;inspect(Math.round(((event.clientX-r.left)/r.width*width-65)/(width-85)*11));};
   el.onpointerdown=el.onpointermove;
   el.onpointerleave=event=>{if(event.pointerType!=='touch'&&document.activeElement!==el)clear();};
   el.onblur=clear;
   el.onkeydown=event=>{if(['ArrowLeft','ArrowRight','Home','End','Escape'].includes(event.key)){event.preventDefault();if(event.key==='Escape')clear();else inspect(event.key==='Home'?0:event.key==='End'?11:i+(event.key==='ArrowRight'?1:-1));}};
 });
}
function persist(){const url=new URL(location.href);url.searchParams.set('theme',theme);url.searchParams.set('state',state);history.replaceState(null,'',url);}
window.addEventListener('hashchange',()=>{if(location.hash.slice(1) in directions)render();});
render();
