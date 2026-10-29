import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';

export type Service = components['schemas']['Service'];
export type Slo = components['schemas']['Slo'];
export type Evaluation = components['schemas']['SloEvaluation'];
export type CreateSlo = components['schemas']['SloCreateRequest'];
export type OperationalOverview = components['schemas']['SloOperationalOverviewPage'];
export type OperationalDetail = components['schemas']['SloOperationalDetail'];
export class SloFailure extends Error {
  constructor(message: string, readonly uncertain = false, readonly status = 0) { super(message); }
}
const record = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x);
const decimal = (x: unknown) => x === null || typeof x === 'string' && /^-?\d+(?:\.\d+)?$/.test(x) && x.length <= 128;
function validEvaluation(x: Record<string, unknown>): boolean {
  if (!record(x.window) || !record(x.quality) || !record(x.performance) || !record(x.objective) || !record(x.timeline)) return false;
  const w=x.window, q=x.quality, p=x.performance, t=x.timeline;
  const timestamp=(v:unknown) => typeof v === 'string' && /Z$/.test(v) && Number.isFinite(Date.parse(v));
  const integer=(v:unknown) => typeof v === 'number' && Number.isSafeInteger(v) && v >= 0;
  return x.snapshot_kind === 'current_evaluation' && timestamp(x.requested_at) && timestamp(x.effective_end) &&
    timestamp(w.effective_start) && timestamp(w.end) && timestamp(t.start) && timestamp(t.end) && integer(t.cadence_seconds) && typeof t.cadence_seconds === 'number' && t.cadence_seconds > 0 && t.max_intervals === 1500 &&
    ['complete','partial','no_data','zero_traffic','expired_history','not_applicable'].includes(String(q.state)) && ['unknown','fresh','stale'].includes(String(q.freshness)) &&
    ['expected_intervals','observed_intervals','known_empty_intervals','missing_intervals','zero_traffic_intervals','expired_intervals'].every(k=>integer(q[k])) && decimal(q.coverage) &&
    ['met','violated','unknown','not_applicable'].includes(String(p.state)) && (p.observed_state===null || p.observed_state==='met' || p.observed_state==='violated') && typeof p.provisional==='boolean' &&
    ['good','total','bad','attainment','allowed_budget','remaining_budget','consumed_fraction','remaining_fraction','burn'].every(k=>decimal(p[k])) && p.budget_unit==='events' &&
    Array.isArray(t.items) && t.items.length<=1500 && t.items.every(item=>record(item) && timestamp(item.start) && timestamp(item.end) &&
      ['observed','zero_traffic','known_empty','missing','expired','not_applicable'].includes(String(item.state)) && ['good','total','attainment','burn'].every(k=>decimal(item[k])) &&
      (item.state==='observed' ? typeof item.attainment==='string' : item.attainment===null));
}
const utcTimestamp=(v:unknown) => typeof v==='string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z$/.test(v) && Number.isFinite(Date.parse(v));
const nonnegativeInteger=(v:unknown) => typeof v==='number' && Number.isSafeInteger(v) && v>=0;
const urgencyCodes=['on_track','stale_observations','no_data','expired_history','complete_window_violation','negative_observed_budget','low_observed_budget','high_1h_burn','evaluation_unavailable','not_yet_eligible','zero_traffic','incomplete_coverage','short_burn_unavailable','budget_below_half'];
function validSlo(value:unknown):value is Record<string,unknown> {
  return record(value) && typeof value.id==='string' && /^[0-9a-f-]{36}$/.test(value.id) && typeof value.name==='string' && value.name.length>0 &&
    typeof value.display_name==='string' && value.display_name.length>0 && typeof value.version==='string' && /^[1-9][0-9]*$/.test(value.version) &&
    typeof value.definition_revision==='string' && typeof value.service_id==='string' && typeof value.service_display_name==='string' &&
    typeof value.sli_id==='string' && typeof value.sli_display_name==='string' && typeof value.target==='string' && /^0(?:\.\d{1,12})?$/.test(value.target) &&
    typeof value.rolling_window_seconds==='number' && Number.isSafeInteger(value.rolling_window_seconds) && value.rolling_window_seconds>0 &&
    typeof value.cadence_seconds==='number' && Number.isSafeInteger(value.cadence_seconds) && value.cadence_seconds>0 &&
    utcTimestamp(value.activated_at) && utcTimestamp(value.evaluation_valid_from);
}
function validOperationalQuality(value:unknown):boolean {
  if(!record(value)) return false;
  return ['complete','partial','no_data','zero_traffic','expired_history','not_yet_eligible','not_applicable','unavailable'].includes(String(value.state)) &&
    ['unknown','fresh','stale'].includes(String(value.freshness)) && ['expected_intervals','observed_intervals','known_empty_intervals','missing_intervals','expired_intervals','zero_traffic_intervals'].every(k=>nonnegativeInteger(value[k])) &&
    decimal(value.coverage) && [value.first_observation_at,value.last_observation_at,value.latest_receipt_at].every(v=>v===null||utcTimestamp(v)) && (value.reason===undefined||value.reason===null||typeof value.reason==='string');
}
function validOperationalPerformance(value:unknown):boolean {
  if(!record(value)) return false;
  return ['met','violated','unknown','not_applicable'].includes(String(value.state)) && (value.observed_state===null||value.observed_state==='met'||value.observed_state==='violated') &&
    typeof value.provisional==='boolean' && (value.reason===null||typeof value.reason==='string') &&
    ['good','total','bad','attainment','allowed_budget','remaining_budget','consumed_fraction','remaining_fraction','burn'].every(k=>decimal(value[k])) && value.budget_unit==='events';
}
function validOperationalValue(value:unknown):boolean {
  if(!record(value)||!record(value.window)) return false;
  const w=value.window;
  return utcTimestamp(value.evaluation_at) && ['requested_start','effective_start','end','eligible_start','retention_start','activated_at','evaluation_valid_from'].every(k=>utcTimestamp(w[k])) &&
    (w.historical_requested_by===null||typeof w.historical_requested_by==='string') && (w.historical_requested_at===null||utcTimestamp(w.historical_requested_at)) &&
    validOperationalQuality(value.quality) && validOperationalPerformance(value.performance);
}
function validOperationalSample(value:unknown):boolean {
  if(!record(value)||!utcTimestamp(value.bucket_start)||!utcTimestamp(value.bucket_end)||!utcTimestamp(value.evaluation_at)||!validOperationalQuality(value.quality)||typeof value.revision!=='string' || !Array.isArray(value.retained_extrema)) return false;
  if(!['observed','zero_traffic','known_empty','missing','expired','not_applicable','partial','stale'].includes(String(value.state))) return false;
  return ['good','total','attainment','allowed_budget','remaining_budget','remaining_fraction','burn'].every(k=>decimal(value[k])) &&
    value.retained_extrema.every(v=>['first','last','minimum','maximum'].includes(String(v)));
}
function validUrgency(value:unknown):boolean {
  return record(value) && urgencyCodes.includes(String(value.code)) && typeof value.provisional==='boolean';
}
function validOperationalSeries(value:unknown,kind:string):boolean {
  return record(value) && value.kind===kind && (value.burn_window_seconds===null||value.burn_window_seconds===3600||value.burn_window_seconds===21600) &&
    Array.isArray(value.samples) && value.samples.length<=1500 && value.samples.every(validOperationalSample);
}
function validDailyCompliance(value:unknown,summary:unknown,objective:Record<string,unknown>,start:string,end:string,effectiveAt:string,cadenceSeconds:number):boolean {
  if(!record(value)||value.kind!=='daily_compliance'||!Array.isArray(value.days)||value.days.length>365||!record(summary))return false;
  const counts=['met_days','violated_days','evaluable_days','unknown_days','not_applicable_days'];
  if(!counts.every(key=>nonnegativeInteger(summary[key]))||summary.evaluable_days!==(summary.met_days as number)+(summary.violated_days as number)||
    (summary.met_days as number)+(summary.violated_days as number)+(summary.unknown_days as number)+(summary.not_applicable_days as number)!==value.days.length)return false;
  const floorUTCDate=(instant:string)=>{const d=new Date(instant);return Date.UTC(d.getUTCFullYear(),d.getUTCMonth(),d.getUTCDate());};
  const first=Date.parse(start),rangeEnd=Date.parse(end),completedThrough=floorUTCDate(effectiveAt);
  const final=Math.min(rangeEnd,completedThrough),dayEnd=Date.UTC(new Date(first).getUTCFullYear(),new Date(first).getUTCMonth(),new Date(first).getUTCDate())+86_400_000;
  const expected:string[]=[];for(let current=dayEnd;current<=final;current+=86_400_000)expected.push(new Date(current).toISOString());
  if(expected.length!==value.days.length)return false;
  const seen={met:0,violated:0,unknown:0,not_applicable:0};
  for(let index=0;index<value.days.length;index++){
    const day=value.days[index];if(!record(day)||typeof day.utc_date!=='string'||!/^(\d{4})-(\d\d)-(\d\d)$/.test(day.utc_date)||
      typeof day.day_start!=='string'||!utcTimestamp(day.day_start)||typeof day.day_end!=='string'||!utcTimestamp(day.day_end)||typeof day.requested_at!=='string'||!utcTimestamp(day.requested_at)||typeof day.effective_at!=='string'||!utcTimestamp(day.effective_at)||Date.parse(day.day_end)!==Date.parse(expected[index]!)||Date.parse(day.requested_at)!==Date.parse(expected[index]!)||
      Date.parse(day.effective_at)!==Math.floor(Date.parse(expected[index]!)/(cadenceSeconds*1000))*cadenceSeconds*1000||
      Date.parse(day.day_start)!==Date.parse(expected[index]!)-86_400_000||Date.parse(day.utc_date+'T00:00:00Z')!==Date.parse(day.day_start)||
      day.slo_id!==objective.id||day.definition_revision!==objective.definition_revision||day.target!==objective.target||typeof day.explanation!=='string'||!day.explanation.trim()||
      !record(day.quality)||!record(day.performance)||!validOperationalQuality(day.quality)||!validOperationalPerformance(day.performance))return false;
    const quality=day.quality,performance=day.performance;
    if(day.status==='met'||day.status==='violated'){
      seen[day.status]++;
      if(quality.state!=='complete'||performance.state!==day.status||performance.provisional)return false;
    }else if(day.status==='unknown'){
      seen.unknown++;if(performance.state!=='unknown'||!performance.provisional)return false;
    }else if(day.status==='not_applicable')seen.not_applicable++;
    else return false;
  }
  return summary.met_days===seen.met&&summary.violated_days===seen.violated&&summary.unknown_days===seen.unknown&&summary.not_applicable_days===seen.not_applicable;
}
function validOperationalOverview(value:unknown):boolean {
  if(!record(value)||typeof value.snapshot_id!=='string'||!/^[0-9a-f-]{36}$/.test(value.snapshot_id)||!Array.isArray(value.items)||value.items.length>200||!record(value.group_counts)||
    typeof value.next_cursor!=='string'||value.next_cursor.length>512||typeof value.previous_cursor!=='string'||value.previous_cursor.length>512) return false;
  const counts=value.group_counts;
  if(!['needs_attention','watch','healthy'].every(k=>nonnegativeInteger(counts[k]))) return false;
  return value.items.every(item=>{
    if(!record(item)||!validSlo(item.slo)||!['needs_attention','watch','healthy'].includes(String(item.urgency))||!validUrgency(item.leading_reason)||!Array.isArray(item.reasons)||item.reasons.length>14||!item.reasons.every(validUrgency)||!validOperationalValue(item.current)||
      ![item.burn_1h,item.burn_6h].every(v=>record(v)&&(v.window_seconds===3600||v.window_seconds===21600)&&utcTimestamp(v.start)&&utcTimestamp(v.end)&&validOperationalQuality(v.quality)&&validOperationalPerformance(v.performance))||
      !record(item.exhaustion)||!Array.isArray(item.budget_sparkline)||item.budget_sparkline.length>120||!item.budget_sparkline.every(validOperationalSample)) return false;
    const exhaustion=item.exhaustion;
    return ['estimated','exhausted','unavailable'].includes(String(exhaustion.state))&&['remaining_events','bad_events_per_hour','hours_until_exhaustion'].every(k=>decimal(exhaustion[k]))&&validOperationalQuality(exhaustion.current_quality)&&validOperationalQuality(exhaustion.one_hour_quality)&&Array.isArray(exhaustion.assumptions)&&exhaustion.assumptions.every(v=>typeof v==='string');
  });
}
function validOperationalDetail(value:unknown,pathID:string):boolean {
  if(!record(value)||typeof value.evaluation_id!=='string'||!/^[0-9a-f-]{36}$/.test(value.evaluation_id)||!utcTimestamp(value.requested_at)||!utcTimestamp(value.effective_at)||typeof value.calculation_version!=='string'||value.snapshot_kind!=='current_operational_read'||
    !validSlo(value.objective)||value.objective.id!==pathID||!record(value.context)||!validOperationalValue(value.current)||!['needs_attention','watch','healthy'].includes(String(value.urgency))||!validUrgency(value.leading_reason)||!Array.isArray(value.reasons)||!value.reasons.every(validUrgency)||
    !record(value.range)) return false;
  const c=value.context,range=value.range,exhaustion=value.exhaustion;
  if(!['requested_start','requested_end','effective_start','effective_end'].every(k=>utcTimestamp(range[k]))||!nonnegativeInteger(range.source_cadence_seconds)||!nonnegativeInteger(range.resolution_seconds)||typeof range.reduced!=='boolean'||range.max_samples_per_series!==1500||range.bucket_bounds!=='half_open') return false;
  if(typeof c.sli_id!=='string'||typeof c.sli_name!=='string'||typeof c.sli_display_name!=='string'||typeof c.datasource_id!=='string'||typeof c.datasource_display_name!=='string'||typeof c.measurement_revision!=='string'||!nonnegativeInteger(c.polling_interval_seconds)||!nonnegativeInteger(c.settling_delay_seconds)||typeof c.good_query!=='string'||typeof c.total_query!=='string') return false;
  return [value.burn_1h,value.burn_6h].every(v=>record(v)&&(v.window_seconds===3600||v.window_seconds===21600)&&utcTimestamp(v.start)&&utcTimestamp(v.end)&&validOperationalQuality(v.quality)&&validOperationalPerformance(v.performance))&&
    record(exhaustion)&&['estimated','exhausted','unavailable'].includes(String(exhaustion.state))&&['remaining_events','bad_events_per_hour','hours_until_exhaustion'].every(k=>decimal(exhaustion[k]))&&validOperationalQuality(exhaustion.current_quality)&&validOperationalQuality(exhaustion.one_hour_quality)&&Array.isArray(exhaustion.assumptions)&&
    validOperationalSeries(value.attainment_series,'attainment')&&validOperationalSeries(value.budget_series,'rolling_budget')&&validOperationalSeries(value.burn_series,'burn')&&
    validDailyCompliance(value.compliance_series,value.compliance_summary,value.objective as Record<string,unknown>,range.effective_start as string,range.effective_end as string,value.effective_at as string,range.source_cadence_seconds as number);
}
const messages: Record<string, string> = {
  conflict: 'This name or version conflicts with an existing record. Review it and try again.',
  snapshot_expired: 'This SLO list snapshot expired. Restart from the first page.',
  resource_limit: 'The operational result exceeds the server resource limit. Narrow the requested range.',
  range_too_dense: 'This operational range is too dense to return. Choose a shorter range.',
  invalid_request: 'Review the fields, target and window alignment, then try again.',
  not_found: 'The requested record was not found.', forbidden: 'Your role does not allow this operation.',
  unauthorized: 'Your session has expired. Sign in again.', unauthenticated: 'Your session has expired. Sign in again.',
};
const fieldMessages: Record<string, string> = {
  'target:target_invalid': 'Target must be from 0% up to but not including 100%.',
  'rolling_window_seconds:window_invalid': 'Enter a positive window within the supported limit and aligned to the SLI collection interval.',
  'evaluation_valid_from:historical_start_invalid': 'The selected history is no longer available. Turn off earlier measurements, or reload the page and choose history again.',
  'timeline_start:timeline_range_invalid': 'Choose both aligned timeline bounds within the objective window, with at most 1,500 intervals.',
  'at:evaluation_time_invalid': 'Choose a valid evaluation time that is not in the future.',
};
async function request<T>(path: string, init: RequestInit = {}, mutation = false, responseLimit = 4 * 1024 * 1024): Promise<{ value: T; etag: string }> {
  let response: Response;
  try { response = await fetch(path, { ...init, credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error' }); }
  catch { throw new SloFailure(mutation ? 'The request may have succeeded. Check the list before deliberately retrying the same request.' : 'Almanac could not be reached.', mutation); }
  let data: unknown;
  try {
    if (!response.body || Number(response.headers.get('Content-Length')) > responseLimit) throw new Error('large response');
    const reader = response.body.getReader(); const chunks: Uint8Array[] = []; let size = 0;
    try { while (true) { const item = await reader.read(); if (item.done) break; size += item.value.length; if (size > responseLimit) { await reader.cancel(); throw new Error('large response'); } chunks.push(item.value); } }
    finally { reader.releaseLock(); }
    const bytes = new Uint8Array(size); let offset = 0; for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
    data = JSON.parse(new TextDecoder().decode(bytes));
  } catch { throw new SloFailure(mutation ? 'The request may have succeeded, but its response could not be confirmed. Check the list before retrying.' : 'The server returned an invalid response.', mutation, response.status); }
  if (!response.ok) {
    const code = typeof data === 'object' && data !== null && 'code' in data && typeof data.code === 'string' ? data.code : '';
    const errors = record(data) && Array.isArray(data.field_errors) && data.field_errors.length <= 16 ? data.field_errors : [];
    const guidance = errors.filter(record).map(item => fieldMessages[`${item.field}:${item.code}`]).filter(Boolean).join(' ');
    throw new SloFailure(guidance || messages[code] || 'The request could not be completed. Try again.', mutation && response.status >= 500, response.status);
  }
  const expected = init.method === 'POST' ? 201 : 200;
  if (response.status !== expected) throw new SloFailure('The server returned an unexpected response.', mutation, response.status);
  if (!data || typeof data !== 'object') throw new SloFailure('The server returned an invalid response.', mutation, response.status);
  const value = data as Record<string, unknown>;
  const isService = path.startsWith('/api/v1/services');
  const isEvaluation = path.includes('/evaluation');
  const pathname = path.split('?')[0]!;
  const isOverview=pathname==='/api/v1/slos/overview';
  const operationalID=/^\/api\/v1\/slos\/([0-9a-f-]{36})\/operational$/.exec(pathname)?.[1];
  const pathID = /^\/api\/v1\/(?:services|slos)\/([0-9a-f-]{36})(?:\/evaluation)?$/.exec(pathname)?.[1];
  const isList = !pathID && !operationalID && init.method !== 'POST';
  const uuid = (v: unknown) => typeof v === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
  const version = (v: unknown) => typeof v === 'string' && /^[1-9][0-9]{0,18}$/.test(v) && (v.length < 19 || v <= '9223372036854775807');
  const resource = (item: unknown): item is Record<string, unknown> => typeof item === 'object' && item !== null &&
    uuid((item as Record<string, unknown>).id) && typeof (item as Record<string, unknown>).name === 'string' && (item as Record<string, string>).name.length > 0 &&
    typeof (item as Record<string, unknown>).display_name === 'string' && (item as Record<string, string>).display_name.length > 0 && version((item as Record<string, unknown>).version);
  const slo = (item: unknown) => resource(item) && typeof item.service_display_name === 'string' && typeof item.sli_display_name === 'string' &&
    typeof item.target === 'string' && /^(?:0(?:\.\d{1,12})?)$/.test(item.target) && version(item.definition_revision) &&
    typeof item.rolling_window_seconds === 'number' && Number.isSafeInteger(item.rolling_window_seconds) && item.rolling_window_seconds > 0 &&
    typeof item.cadence_seconds === 'number' && Number.isSafeInteger(item.cadence_seconds) && item.cadence_seconds > 0 &&
    item.rolling_window_seconds % item.cadence_seconds === 0 && uuid(item.service_id) && uuid(item.sli_id);
  const valid = isOverview ? validOperationalOverview(value)
    : operationalID ? validOperationalDetail(value,operationalID)
    : isEvaluation ? validEvaluation(value) && record(value.objective) && value.objective.slo_id === pathID
    : isList ? Array.isArray(value.items) && value.items.length <= 200 && typeof value.next_cursor === 'string' && value.next_cursor.length <= 512 && value.items.every(isService ? resource : slo)
      : (isService ? resource(value) : slo(value)) && (!pathID || value.id === pathID);
  if (!valid) throw new SloFailure('The server returned an invalid response.', mutation, response.status);
  if (!isEvaluation && !isList && !operationalID) {
    const tag = response.headers.get('ETag');
    if (tag !== `"${isService ? 'service' : 'slo'}:${value.id}:${value.version}"`)
      throw new SloFailure(mutation ? 'The request may have succeeded, but its version could not be confirmed. Check the record before retrying.' : 'The record version could not be confirmed.', mutation, response.status);
  }
  return { value: data as T, etag: response.headers.get('ETag') ?? '' };
}
function write(context: AuthContext, body: unknown, key?: string, etag?: string): RequestInit {
  return { method: etag ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token, ...(key ? { 'Idempotency-Key': key } : {}), ...(etag ? { 'If-Match': etag } : {}) }, body: JSON.stringify(body) };
}
export const sloClient = {
  services: (cursor = '', signal?: AbortSignal) => request<{items: Service[]; next_cursor: string}>(`/api/v1/services?${new URLSearchParams({limit:'50', ...(cursor ? {cursor} : {})})}`, {signal}).then(x => x.value),
  service: (id: string, signal?: AbortSignal) => request<Service>(`/api/v1/services/${id}`, {signal}),
  createService: (body: {name:string; display_name:string}, context: AuthContext, key: string) => request<Service>('/api/v1/services', write(context, body, key), true).then(x => x.value),
  updateService: (id: string, name: string, etag: string, context: AuthContext) => request<Service>(`/api/v1/services/${id}`, write(context, {display_name:name}, undefined, etag), true),
  list: (cursor = '', signal?: AbortSignal) => request<{items:Slo[]; next_cursor:string}>(`/api/v1/slos?${new URLSearchParams({limit:'50', ...(cursor ? {cursor} : {})})}`, {signal}).then(x => x.value),
  get: (id: string, signal?: AbortSignal) => request<Slo>(`/api/v1/slos/${id}`, {signal}),
  create: (body: CreateSlo, context: AuthContext, key: string) => request<Slo>('/api/v1/slos', write(context, body, key), true).then(x => x.value),
  update: (id: string, name: string, etag: string, context: AuthContext) => request<Slo>(`/api/v1/slos/${id}`, write(context, {display_name:name}, undefined, etag), true),
  evaluation: (id: string, signal?: AbortSignal) => request<Evaluation>(`/api/v1/slos/${id}/evaluation`, {signal}).then(x => x.value),
  overview: (filters: {q?:string;service_id?:string;urgency?:components['schemas']['SloOperationalOverviewItem']['urgency'];limit?:number;cursor?:string} = {}, signal?:AbortSignal) => {
    const params=new URLSearchParams();
    if(filters.q) params.set('q',filters.q);
    if(filters.service_id) params.set('service_id',filters.service_id);
    if(filters.urgency) params.set('urgency',filters.urgency);
    if(filters.limit!==undefined) params.set('limit',String(filters.limit));
    if(filters.cursor) params.set('cursor',filters.cursor);
    return request<OperationalOverview>(`/api/v1/slos/overview?${params}`,{signal},false,20*1024*1024).then(x=>x.value);
  },
  operational: (id:string, options:{at?:string;start?:string;end?:string;burn_window_seconds?:3600|21600}={}, signal?:AbortSignal) => {
    const params=new URLSearchParams();
    if(options.at) params.set('at',options.at);
    if(options.start) params.set('start',options.start);
    if(options.end) params.set('end',options.end);
    if(options.burn_window_seconds) params.set('burn_window_seconds',String(options.burn_window_seconds));
    return request<OperationalDetail>(`/api/v1/slos/${id}/operational?${params}`,{signal},false,20*1024*1024).then(x=>x.value);
  },
};
