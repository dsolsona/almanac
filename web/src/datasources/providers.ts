export const PROVIDERS = [
  { id: 'prometheus', label: 'Prometheus', language: 'PromQL', description: 'Metrics from Prometheus, VictoriaMetrics, Mimir and Thanos.', endpoint: 'https://prometheus.internal:9090', example: 'sum(requests_in_interval)' },
  { id: 'victoriatraces', label: 'VictoriaTraces', language: 'LogsQL', description: 'Count ingested spans with native LogsQL statistics.', endpoint: 'http://victoriatraces:10428', example: 'resource_attr:service.name:="checkout" | stats count() as events' },
  { id: 'tempo', label: 'Tempo', language: 'TraceQL', description: 'Count ingested spans with Grafana Tempo metrics queries.', endpoint: 'http://tempo:3200', example: '{ resource.service.name = "checkout" && kind = server } | count_over_time()' },
  { id: 'loki', label: 'Loki', language: 'LogQL', description: 'Count log events with LogQL metric queries.', endpoint: 'http://loki:3100', example: 'sum(count_over_time({service_name="checkout"}[$interval]))' },
] as const;
export type ProviderID = typeof PROVIDERS[number]['id'];
/** Look up presentation metadata, using the default metrics profile for unknown IDs. */
export const providerInfo = (id: string) => PROVIDERS.find((provider) => provider.id === id) ?? PROVIDERS[0];
/** Admit only the canonical versioned configuration format for a supported provider. */
export const validProviderPair = (id: unknown, format: unknown) => PROVIDERS.some((provider) => provider.id === id && format === `${provider.id}-http-v1`);
