import {beforeEach,afterEach,expect,it,vi} from 'vitest';
import {fireEvent,render,screen,waitFor,cleanup,within} from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import {ServicesPage} from '../src/services/ServicesPage.tsx';
import type {AuthContext} from '../src/auth/client.ts';
import {serviceClient} from '../src/services/client.ts';
import {clearQueryCache} from '../src/queryCache.ts';
import {serviceFleet} from '../browser/support/service-fleet.ts';

const id='00000000-0000-4000-8000-000000000001',other='00000000-0000-4000-8000-000000000002';
const context:AuthContext={csrf_token:'csrf',session:{user_id:id,login:'editor@example.test',role:'editor',restricted:false}};
const service={id,name:'checkout',display_name:'Checkout',version:'1',created_at:'2026-11-06T13:42:00Z',updated_at:'2026-11-06T13:42:00Z',description:'Processes orders',owner:'Store experience',contact:'store@example.test',labels:{team:['store']},links:[{name:'Runbook',url:'https://docs.example.test/checkout'}],dependencies:[],dependency_services:[],used_by:[],used_by_count:0,slo_summary:{state:'no_slos',total:0,on_track:0,breached:0,unknown:0}};
const reply=(body:unknown,status=200,etag?:string)=>new Response(JSON.stringify(body),{status,headers:{'Content-Type':'application/json',...(etag?{'ETag':etag}:{})}});
const page=(items:unknown[])=>({items,next_cursor:'',total:items.length,all_count:items.length,with_slos_count:0,owners:['Store experience']});
const originalShowModal=Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype,'showModal'),originalClose=Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype,'close');
beforeEach(()=>{context.session!.user_id=crypto.randomUUID();Object.defineProperty(HTMLDialogElement.prototype,'showModal',{configurable:true,value:function(this:HTMLDialogElement){this.setAttribute('open','');}});Object.defineProperty(HTMLDialogElement.prototype,'close',{configurable:true,value:function(this:HTMLDialogElement){this.removeAttribute('open');}});});
afterEach(()=>{cleanup();if(originalShowModal)Object.defineProperty(HTMLDialogElement.prototype,'showModal',originalShowModal);else delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).showModal;if(originalClose)Object.defineProperty(HTMLDialogElement.prototype,'close',originalClose);else delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).close;vi.unstubAllGlobals();clearQueryCache(false);history.replaceState({},'','/');});

it('shows truthful service SLO marks and opens details by clicking a status cell',async()=>{
 history.replaceState({},'','/services');
 const breached={...service,slo_summary:{state:'breached',total:3,on_track:1,breached:1,unknown:1}};
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL)=>String(input).startsWith('/api/v1/services?')?reply(page([breached])):String(input).includes('/slos/overview')?reply({snapshot_id:id,items:[],next_cursor:'',previous_cursor:'',group_counts:{needs_attention:0,watch:0,healthy:0}}):reply(breached,200,`"service:${id}:1"`)));
 render(<ServicesPage context={context}/>);
 expect(await screen.findByRole('link',{name:'Checkout'})).toHaveAttribute('href',`/services/${id}`);
 fireEvent.click(screen.getByText('Breached',{exact:true}));
 await waitFor(()=>expect(location.pathname).toBe(`/services/${id}`));
 expect(await screen.findByRole('region',{name:'Service information'})).toHaveTextContent('Store experience');
 expect(screen.queryByText('Record details')).not.toBeInTheDocument();
});

it('uses removable key/value labels and validates links before creating',async()=>{
 history.replaceState({},'','/services/new');
 const writes:RequestInit[]=[];
 vi.stubGlobal('fetch',vi.fn(async(_input:RequestInfo|URL,init?:RequestInit)=>{if(init?.method==='POST'){writes.push(init);return reply(service,201,`"service:${id}:1"`)}return reply(page([]));}));
 render(<ServicesPage context={context}/>);
 await userEvent.type(await screen.findByLabelText('Display name',{exact:true}),'Checkout');
 await userEvent.type(screen.getByRole('textbox',{name:'Labels'}),'team:store{Enter}');
 expect(screen.getByRole('button',{name:'Remove label team:store'})).toBeVisible();
 await userEvent.type(screen.getByLabelText('Name',{exact:true}),'Runbook');
 await userEvent.type(screen.getByLabelText('URL',{exact:true}),'javascript:alert(1){Enter}');
 await userEvent.click(screen.getByRole('button',{name:'Create service'}));
 expect(writes).toHaveLength(0);
 expect(screen.getByLabelText('URL',{exact:true})).toHaveAttribute('aria-invalid','true');
});

it('keeps the draft after a version conflict and does not silently retry a write',async()=>{
 history.replaceState({},'',`/services/${id}/edit`);
 let patches=0;
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>init?.method==='PATCH'?(patches++,reply({code:'conflict'},409)):String(input).includes('?')?reply(page([service])):reply(service,200,`"service:${id}:1"`)));
 render(<ServicesPage context={context}/>);
 const name=await screen.findByLabelText('Display name',{exact:true});
 await userEvent.clear(name);await userEvent.type(name,'Checkout API');
 await userEvent.click(screen.getByRole('button',{name:'Save changes'}));
 expect(await screen.findByRole('alert')).toHaveTextContent(/changed|conflict/i);
 expect(name).toHaveValue('Checkout API');expect(patches).toBe(1);
});

it('keeps dependency selection as removable chips using the existing combobox pattern',async()=>{
 history.replaceState({},'','/services/new');
 const payments={...service,id:other,name:'payments',display_name:'Payments'};
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page([payments]))));
 render(<ServicesPage context={context}/>);
 await userEvent.type(await screen.findByRole('combobox',{name:'Search dependencies'}),'pay');
 await userEvent.click(await screen.findByRole('option',{name:/Payments/}));
 const remove=screen.getByRole('button',{name:'Remove dependency Payments'});
 expect(remove).toBeVisible();await userEvent.click(remove);
 expect(screen.queryByRole('button',{name:'Remove dependency Payments'})).not.toBeInTheDocument();
});

it('does not expose writes to a viewer',async()=>{
 history.replaceState({},'',`/services/${id}`);
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL)=>String(input).includes('/slos/overview')?reply({snapshot_id:id,items:[],next_cursor:'',previous_cursor:'',group_counts:{needs_attention:0,watch:0,healthy:0}}):reply(service,200,`"service:${id}:1"`)));
 render(<ServicesPage context={{...context,session:{...context.session!,role:'viewer'}}}/>);
 await screen.findByRole('region',{name:'Service information'});
 expect(screen.queryByRole('link',{name:'Edit service'})).not.toBeInTheDocument();
 expect(screen.queryByRole('button',{name:'Delete service…'})).not.toBeInTheDocument();
});

it('normalizes empty optional metadata omitted by JSON v2',async()=>{
 history.replaceState({},'','/services');
 const {description,contact,labels,links,dependencies,dependency_services,used_by,used_by_count,...omitted}=service;
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page([omitted]))));
 render(<ServicesPage context={context}/>);
 expect(await screen.findByRole('link',{name:'Checkout'})).toBeVisible();
 expect(screen.queryByRole('alert')).not.toBeInTheDocument();
 expect(screen.getAllByText('No SLOs',{exact:true}).length).toBeGreaterThan(0);
});

it('does not redirect a later page when a service save finishes after leaving the form',async()=>{
 history.replaceState({},'','/services/new');
 let finish:(response:Response)=>void=()=>{};
 vi.stubGlobal('fetch',vi.fn(async(_input:RequestInfo|URL,init?:RequestInit)=>init?.method==='POST'?new Promise<Response>(resolve=>{finish=resolve;}):reply(page([]))));
 const mounted=render(<ServicesPage context={context}/>);
 await userEvent.type(screen.getByLabelText('Display name',{exact:true}),'Checkout');
 await userEvent.click(screen.getByRole('button',{name:'Create service'}));
 mounted.unmount();history.pushState({},'','/agents');
 finish(reply(service,201,`"service:${id}:1"`));
 await new Promise(resolve=>setTimeout(resolve,20));
 expect(location.pathname).toBe('/agents');
});

it('reconciles a lost PATCH reply after an explicit retry conflicts',async()=>{
 history.replaceState({},'',`/services/${id}/edit`);let patches=0;
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  if(init?.method==='PATCH'){patches++;if(patches===1)throw new TypeError('offline');return reply({code:'conflict'},409);}
  return String(input).includes('?')?reply(page([service])):reply(patches?{...service,display_name:'Changed checkout',version:'2'}:service,200,`"service:${id}:${patches?'2':'1'}"`);
 }));
 render(<ServicesPage context={context}/>);
 const name=await screen.findByLabelText('Display name',{exact:true});await userEvent.clear(name);await userEvent.type(name,'Changed checkout');
 await userEvent.click(screen.getByRole('button',{name:'Save changes'}));
 await userEvent.click(await screen.findByRole('button',{name:'Retry the same save'}));
 await userEvent.click(await screen.findByRole('button',{name:'Check saved service'}));
 await waitFor(()=>expect(location.pathname).toBe(`/services/${id}`));expect(patches).toBe(2);
});

it('preserves service fields and unfinished composers across SPA navigation',async()=>{
 history.replaceState({},'','/services/new');
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page([]))));
 const first=render(<ServicesPage context={context}/>);
 await userEvent.type(screen.getByLabelText('Display name',{exact:true}),'Unsaved checkout');
 await userEvent.type(screen.getByRole('textbox',{name:'Labels'}),'team:unfinished');
 await userEvent.type(screen.getByLabelText('URL',{exact:true}),'https://unfinished.example.test');
 first.unmount();history.pushState({},'','/agents');history.pushState({},'','/services/new');render(<ServicesPage context={context}/>);
 expect(screen.getByLabelText('Display name',{exact:true})).toHaveValue('Unsaved checkout');
 expect(screen.getByRole('textbox',{name:'Labels'})).toHaveValue('team:unfinished');expect(screen.getByLabelText('URL',{exact:true})).toHaveValue('https://unfinished.example.test');
});

it('does not redirect another page when an archive response arrives late',async()=>{
 history.replaceState({},'',`/services/${id}`);let finish:(response:Response)=>void=()=>{};
 vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  if(init?.method==='DELETE')return new Promise<Response>(resolve=>{finish=resolve;});
  if(String(input).includes('/slos/overview'))return reply({snapshot_id:id,items:[],next_cursor:'',previous_cursor:'',group_counts:{needs_attention:0,watch:0,healthy:0}});
  return reply(service,200,`"service:${id}:1"`);
 }));
 const mounted=render(<ServicesPage context={context}/>);await userEvent.click(await screen.findByRole('button',{name:'Delete service…'}));
 await userEvent.click(screen.getByRole('button',{name:'Delete service'}));
 mounted.unmount();history.pushState({},'','/services/new');finish(new Response(null,{status:204}));
 await new Promise(resolve=>setTimeout(resolve,20));expect(location.pathname).toBe('/services/new');
});

it('rereads service summaries on a new visit instead of reusing a cached success',async()=>{
 let reads=0;vi.stubGlobal('fetch',vi.fn(async()=>{reads++;return reply(page([{...service,slo_summary:reads===1?{state:'on_track',total:1,on_track:1,breached:0,unknown:0}:{state:'unknown',total:1,on_track:0,breached:0,unknown:1}}]));}));
 expect((await serviceClient.list()).items[0]!.slo_summary!.state).toBe('on_track');
 expect((await serviceClient.list()).items[0]!.slo_summary!.state).toBe('unknown');expect(reads).toBe(2);
});

it('opens the full graph from Services and exposes each service SLO status without empty-owner placeholders',async()=>{
 history.replaceState({},'','/services');
 const records=[
  {...service,owner:'',slo_summary:{state:'on_track',total:1,on_track:1,breached:0,unknown:0}},
  {...service,id:other,name:'payments',display_name:'Payments',owner:'',slo_summary:{state:'breached',total:2,on_track:0,breached:1,unknown:1}},
  {...service,id:'00000000-0000-4000-8000-000000000003',name:'inventory',display_name:'Inventory',owner:'Commerce',slo_summary:{state:'unknown',total:1,on_track:0,breached:0,unknown:1}},
  {...service,id:'00000000-0000-4000-8000-000000000004',name:'notifications',display_name:'Notifications',owner:''},
 ];
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page(records))));
 const rendered=render(<ServicesPage context={context}/>);
 await screen.findByRole('link',{name:'Checkout'});
 await userEvent.click(screen.getByRole('link',{name:'Graph'}));
 await waitFor(()=>expect(rendered.container.querySelectorAll('.map-node')).toHaveLength(4));
 expect(screen.getByRole('link',{name:'Graph'})).toHaveAttribute('aria-current','page');
 expect(screen.getByRole('group',{name:'Service SLO status'})).toHaveTextContent('On track');
 for(const record of records)expect(rendered.container.querySelector(`.map-node[data-id="${record.id}"]`)).toHaveAttribute('data-slo-state',record.slo_summary.state);
 expect(rendered.container.querySelector(`.map-node[data-id="${id}"] title`)).toHaveTextContent(/^Checkout · On track · 1 SLO$/);
 expect(screen.queryByText('Unassigned',{exact:true})).not.toBeInTheDocument();
 await userEvent.click(screen.getByRole('button',{name:'Owner groups'}));
 expect(screen.queryByText('Unassigned',{exact:true})).not.toBeInTheDocument();
 await userEvent.click(screen.getByRole('link',{name:'Table'}));
 expect(await screen.findByRole('link',{name:'Checkout'})).toBeVisible();
});

it('provides graph camera controls without a duplicate Relationships table',async()=>{
 history.replaceState({},'','/services/dependencies');
 const records=[{...service,dependencies:[other]}, {...service,id:other,name:'payments',display_name:'Payments',slo_summary:{state:'breached',total:1,on_track:0,breached:1,unknown:0}}];
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page(records))));
 render(<ServicesPage context={context}/>);
 await screen.findByRole('button',{name:'Select Checkout, No SLOs, 0 SLOs'});
 expect(screen.queryByRole('heading',{name:'Relationships'})).not.toBeInTheDocument();
 const zoom=screen.getByLabelText('Graph zoom');const before=zoom.textContent;
 await userEvent.click(screen.getByRole('button',{name:'Zoom in'}));
 expect(zoom.textContent).not.toBe(before);
 await userEvent.click(screen.getByRole('button',{name:'Reset view'}));
 expect(zoom.textContent).toBe(before);
 await userEvent.click(within(screen.getByRole('group',{name:'Service SLO status'})).getByRole('button',{name:'Breached'}));
 expect(screen.getByRole('button',{name:'Select Payments, Breached, 1 SLO'})).toHaveAttribute('aria-pressed','true');
 expect(screen.queryByRole('button',{name:'Focus selected'})).not.toBeInTheDocument();
});


it('lays out a fifty-service dependency network compactly instead of in three columns',async()=>{
 history.replaceState({},'','/services/dependencies');const records=serviceFleet();
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page(records))));
 const mounted=render(<ServicesPage context={context}/>);
 await waitFor(()=>expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(50));
 const points=[...mounted.container.querySelectorAll('.map-node')].map(node=>node.getAttribute('transform')!.match(/-?[0-9.]+/g)!.map(Number));
 const xs=points.map(point=>point[0]!),ys=points.map(point=>point[1]!);
 expect((Math.max(...xs)-Math.min(...xs))/(Math.max(...ys)-Math.min(...ys))).toBeGreaterThan(.8);
 expect(new Set(xs.map(x=>Math.round(x))).size).toBeGreaterThan(20);
 const before=points.map(point=>point.join(','));
 await userEvent.type(screen.getByRole('combobox',{name:'Find a service'}),records[20]!.display_name);
 await userEvent.click(screen.getByRole('option',{name:new RegExp(records[20]!.display_name)}));
 const after=[...mounted.container.querySelectorAll('.map-node')].map(node=>node.getAttribute('transform')!.match(/-?[0-9.]+/g)!.map(Number).join(','));
 expect(after).toEqual(before);
});

it('finds services by typing their owner and shows their status before selection',async()=>{
 history.replaceState({},'','/services/dependencies');
 const records=[service,{...service,id:other,name:'payments',display_name:'Payments',owner:'Money movement',slo_summary:{state:'on_track',total:2,on_track:2,breached:0,unknown:0}}];
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page(records))));
 render(<ServicesPage context={context}/>);
 await screen.findByRole('button',{name:'Select Payments, On track, 2 SLOs'});
 const finder=screen.getByRole('combobox',{name:'Find a service'});
 await userEvent.type(finder,'Money');
 const option=screen.getByRole('option',{name:/Payments/});expect(option).toHaveTextContent('On track');expect(option).toHaveTextContent('Money movement');
 await userEvent.keyboard('{ArrowDown}{Enter}');
 expect(screen.getByRole('button',{name:'Select Payments, On track, 2 SLOs'})).toHaveAttribute('aria-pressed','true');
 expect(screen.getByRole('combobox',{name:'Find a service'})).toHaveValue('');
 expect(screen.queryByRole('button',{name:'List & map'})).not.toBeInTheDocument();
});

it('combines SLO status filters and resets an empty graph back to the complete catalog',async()=>{
 history.replaceState({},'','/services/dependencies');
 const records=[service,{...service,id:other,name:'payments',display_name:'Payments',slo_summary:{state:'breached',total:1,on_track:0,breached:1,unknown:0}},{...service,id:'00000000-0000-4000-8000-000000000003',display_name:'Search',slo_summary:{state:'unknown',total:1,on_track:0,breached:0,unknown:1}}];
 vi.stubGlobal('fetch',vi.fn(async()=>reply(page(records))));
 const mounted=render(<ServicesPage context={context}/>);
 await waitFor(()=>expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(3));
 const filters=screen.getByRole('group',{name:'Service SLO status'});
 await userEvent.click(within(filters).getByRole('button',{name:'Breached'}));
 expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(1);
 expect(screen.getByRole('button',{name:'Select Payments, Breached, 1 SLO'})).toHaveAttribute('aria-pressed','true');
 await userEvent.click(within(filters).getByRole('button',{name:'Unknown'}));
 expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(2);
 await userEvent.click(within(filters).getByRole('button',{name:'Breached'}));
 expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(1);
 await userEvent.click(within(filters).getByRole('button',{name:'Unknown'}));
 expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(3);
 await userEvent.click(within(filters).getByRole('button',{name:'On track'}));
 expect(screen.getByText('No services match these statuses.')).toBeVisible();
 expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(0);
 await userEvent.click(screen.getByRole('button',{name:'Reset view'}));
 expect(mounted.container.querySelectorAll('.map-node')).toHaveLength(3);
 expect(within(filters).getByRole('button',{name:'On track'})).toHaveAttribute('aria-pressed','false');
});
