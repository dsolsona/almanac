// Capture only delivery status and trace IDs, never cookies, CSRF values,
// resource attributes, span attributes or arbitrary request/response bodies.
export async function captureTelemetryDelivery(page) {
  const deliveries=[];
  await page.exposeFunction('recordAlmanacDelivery',value=>{
    const results=['queued','unsampled','queue_full','flush_requested','flush_done','flush_failed','exported','failed','serialization_failed','disabled','unauthenticated'];
    if(deliveries.length>=256||!results.includes(value?.result)||!Array.isArray(value.trace_ids))return;
    const entry={sdk_result:value.result,trace_ids:value.trace_ids.filter(id=>typeof id==='string'&&/^[0-9a-f]{32}$/.test(id)).slice(0,64)};
    for(const key of ['queueSpans','queueCapacity','exportFailures','droppedSpans','droppedBatches','outboxDropped','outboxFailures'])if(Number.isInteger(value[key])&&value[key]>=0)entry[key]=value[key];
    for(const key of ['authenticated','enabled'])if(typeof value[key]==='boolean')entry[key]=value[key];
    if(['Error','TypeError','RangeError','SyntaxError'].includes(value.error_name))entry.error_name=value.error_name;
    if(typeof value.error_asset==='string'&&/^[A-Za-z0-9_-]+\.js$/.test(value.error_asset))entry.error_asset=value.error_asset;
    if(Number.isInteger(value.error_line))entry.error_line=value.error_line;
    if(Number.isInteger(value.error_column))entry.error_column=value.error_column;
    deliveries.push(entry);
  });
  await page.addInitScript(()=>addEventListener('almanac:telemetry-delivery',event=>void window.recordAlmanacDelivery(event.detail)));
  const identify=request=>{
    if(new URL(request.url()).pathname!=='/api/v1/telemetry/traces')return null;
    try{
      const batch=JSON.parse(request.postData()??'{}');
      const spans=(batch.resourceSpans??[]).flatMap(resource=>(resource.scopeSpans??[]).flatMap(scope=>scope.spans??[]));
      // Fixed SDK structure only: never retain arbitrary attributes, resource
      // labels or payload text when diagnosing a rejected batch.
      return {trace_ids:[...new Set(spans.map(span=>span.traceId).filter(id=>typeof id==='string'&&/^[0-9a-f]{32}$/.test(id)))].slice(0,64),span_count:spans.length,
        shape:{resources:batch.resourceSpans?.length,scopes:(batch.resourceSpans??[]).map(resource=>resource.scopeSpans?.length),spans:spans.slice(0,64).map(span=>({
          name:/^(ui\.(?:sli\.(?:list|detail)|slo\.(?:list|detail)|management|chart\.render|fonts\.wait|json\.parse)|http\.response\.read)$/.test(span.name)?span.name:'http-or-unknown',
          kind:span.kind,start:/^\d{1,20}$/.test(span.startTimeUnixNano)?span.startTimeUnixNano:'invalid',end:/^\d{1,20}$/.test(span.endTimeUnixNano)?span.endTimeUnixNano:'invalid',
          attributes:span.attributes?.length??0,status:span.status?.code??0
        }))}};
    }catch{return {trace_ids:[],unreadable:true};}
  };
  page.on('response',response=>{const batch=identify(response.request());if(batch&&deliveries.length<256){if(response.status()<400)delete batch.shape;const reason=response.headers()['x-almanac-telemetry-rejection'];deliveries.push({...batch,status:response.status(),...(['batch','span_count','span_id','span_name','span_kind','span_time','span_attributes','span_status'].includes(reason)?{rejection:reason}:{})});}});
  page.on('requestfailed',request=>{const batch=identify(request);if(batch&&deliveries.length<256)deliveries.push({...batch,failed:true});});
  return deliveries;
}
