# Trace and log datasources

Almanac measures **good events / total events** per datasource interval. The same
SLI can feed an occurrence SLO, error budget and burn rate whether its events come
from metrics, ingested spans or logs. An availability SLI filters successful events;
a latency SLI filters individual durations. A percentile is not an event count.

## Local test services

After the usual `make setup` / `make bootstrap`, start the providers you need:

```sh
make loki
make vt
make tempo
```

Each named target starts only that source and its required dependencies, then waits
for HTTP readiness. `make vt` uses the existing VictoriaTraces service, shared with
application tracing. `make sources-start` remains a convenience for starting all
three sources. `make sources-stop` preserves their volumes and leaves
VictoriaTraces available for application tracing. Normal application startup does not require
Loki or Tempo. These fixtures bind host ports to loopback and are for local testing.

| Provider | Endpoint from the local agent | Host access | Ingest |
| --- | --- | --- | --- |
| VictoriaTraces | `http://victoriatraces:10428` | `http://localhost:19428` | OTLP HTTP `/insert/opentelemetry/v1/traces` |
| Tempo | `http://tempo:3200` | `http://localhost:13200` | OTLP HTTP `http://localhost:14318/v1/traces` |
| Loki | `http://loki:3100` | `http://localhost:13100` | HTTP `/loki/api/v1/push` |

Use those agent endpoints with the local container agent. An agent on another
machine needs an endpoint it can reach; its `localhost` means that agent's machine.
The optional services retain seven days. Ports can be changed with
`ALMANAC_LOKI_PORT`, `ALMANAC_TEMPO_PORT`, `ALMANAC_TEMPO_OTLP_PORT` and the existing
`ALMANAC_TRACES_PORT`. Existing source data is not automatically registered in Almanac.

After testing, run `make sources-stop` to stop the optional services and their
readiness monitor while preserving source data.

## Add a datasource and an SLI

An administrator opens Datasources → New datasource, chooses its provider, enters
its base endpoint, configures authentication/TLS and selects an agent. Test the
connection, save, then preview the intended query on the datasource detail page.
An editor or administrator can create an SLI from that saved datasource.

VictoriaTraces and Tempo are separate choices. VictoriaTraces has experimental
Tempo search compatibility, but Almanac uses its native LogsQL statistics API.
Grafana Tempo uses TraceQL metrics. Search result limits never define SLI counts.

Examples below assume one event per request and a consistent `checkout` population.
Replace the field names and predicates with those present in your source.

| Source | Total query | Good query |
| --- | --- | --- |
| VictoriaTraces | `resource_attr:service.name:="checkout" \| stats count() as events` | `resource_attr:service.name:="checkout" AND name:="successful-request" \| stats count() as events` |
| Tempo | `{ resource.service.name = "checkout" && kind = server } \| count_over_time()` | `{ resource.service.name = "checkout" && kind = server && status != error } \| count_over_time()` |
| Loki | `sum(count_over_time({service_name="checkout"}[$interval]))` | `sum(count_over_time({service_name="checkout"} != "error" [$interval])) or vector(0)` |

For Loki, `$interval` expands literally to the datasource cadence, for example
`15s`, including occurrences inside quoted text. Avoid the token in quoted values
unless that replacement is intended.
The good query above uses `or vector(0)` so an interval containing only error logs
returns zero good events. Use this fallback only when the paired total query
confirms traffic and source completeness justifies zero; keep the total query
without a fallback. A missing total series remains unknown. Empty/missing results do not become
successful requests. Each component must return exactly one integral count;
aggregate unwanted label combinations explicitly. Good cannot exceed total.

The agent supplies interval bounds to VictoriaTraces and Tempo, and evaluates Loki
at the interval end shifted by one nanosecond to preserve `[start,end)` membership.
LogsQL `options(...)` clauses, including nested clauses, are rejected in this
initial count profile: they can override interval bounds or partial-response policy.
Use datasource settings for transport options and the native query for filters and statistics.
Cadence sets VictoriaTraces/Tempo interval bounds and Loki’s `$interval` value;
Prometheus ranges remain part of the user’s PromQL. Changing cadence does not
rewrite PromQL or fixed LogQL ranges, so review those queries when changing it.
Both components use the same settled interval. Diagnostics receive an explicit
interval end computed from the agent’s backend-adjusted clock by the shared
scheduler helper; the query client does not read the clock. Previews use these same semantics,
are bounded and do not write observations. The existing 60-second settling delay
is retained; this does not guarantee that every upstream ingest pipeline has finished.

Tracing SLOs describe the **ingested span population**. Select one server/root span
per request, understand head/tail sampling and dropped spans, and do not treat a
biased sample as proof of whole-service availability. Logs likewise need one event
per request and a known ingestion policy. Almanac does not extrapolate samples,
deduplicate application events or infer source retention. Historical backfill is
not enabled by registering a source.

Providers support none/basic/bearer authentication and the existing certificate
controls. Prefer HTTPS for credentials; the UI warns when authenticated HTTP
sends credentials without encryption and explains that certificate settings only
apply to HTTPS. Multi-tenant installations that require routing headers currently need
an authenticated gateway endpoint configured for their tenant.

The decisions and discussion points are in
[ADR-0047](adr/0047-trace-and-log-event-count-datasources.md);
verification is recorded in [F2-T34](tasks/F2-T34.md).
