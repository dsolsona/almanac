# ADR-0025: Serve fast operational journeys from retained time series

- Status: accepted
- Created: 2026-10-31
- Accepted: 2026-10-31
- Approval: maintainer approved the performance/tracing intervention in Crit session `ede9414e6ca5` after its retention, pagination, latency and historical-series amendments
- Related specs: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0007](../specs/0007-storage-and-retention.md), [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: partially supersedes [ADR-0024](0024-precomputed-operational-reads.md)'s foreground preparation/cache policy; replaces provisional 400-day retention with one year as specified below

## Context

Exact-response caches keyed by moving page-request times can miss during normal
navigation. Scheduling graph preparation and polling after two seconds makes an
already-prepared response fast without making the page fast. Short-lived graph
payloads also do not provide a retained historical SLO time series.

The accepted requirement is accurate calculations with 10/100/1,000 SLOs and one
year of data. UI lists display 10 or 20 rows, not every installed objective.
Lists must become usable in < 1,000 ms, preferably <= 500 ms. Detail and graph
range/burn changes must complete in < 500 ms, including rendered graphs.

The paragraph above records the original acceptance context. On 2026-11-01 the
maintainer accepted complete browser journeys under one second, with 500 ms as
the optimization target, on normal hardware in normal/added-100-ms network
conditions. Current thresholds are recorded in SPEC-0012 and SPEC-0019. This
clarification changes neither this tracing/read-path decision nor the API budgets,
accuracy, year-history and fleet-size requirements.

## Decision

Treat SLI observations and calculated SLO values as time series. Retain exact
canonical observations and calculated attainment, remaining budget and supported
burn-window histories for 365 days. Advance affected current summaries/points as
observations arrive and window/freshness boundaries expire. Keep revisions,
coverage, source/calculation provenance and coherent publication. Never calculate
ratios from averaged percentages or silently replace exact extrema/gaps.

Maintain stable resolution summaries independent of page-request boundaries.
Foreground reads assemble bounded graph data and exact-value pages from available
records and bounded edges. Normal navigation cannot require a preparation job or
polling. A missing whole-response cache is not a reason to delay the page. Heavy
imports, repair and recovery remain explicit background operations, with visible
failure and separate admission from live collection and interactive work.

Paginate UI lists at 20 rows by default, with a 10-row option and maximum 20.
Global counts/urgency apply to the full filtered set. Do not calculate every
objective's full history or issue per-row detail requests before showing a page.

Instrument browser journeys, backend HTTP/services, database acquisition/queries,
calculation, background work, agents and CLI using OpenTelemetry. Propagate W3C
Trace Context and send bounded OTLP/HTTP batches to VictoriaTraces. Browser export
uses a fixed authenticated, bounded same-origin intake; exporter credentials stay
server-side. Exclude secrets and raw query parameters from telemetry. Exporter
failure must not block application work; incomplete traces fail acceptance evidence.

Acceptance measures actual navigation through usable rows and rendered graphs,
including first visits, cold response caches, time boundaries and restart. Use
trace evidence to locate cost and deterministic independent expected values to
establish correctness. Neither API-only latency nor passing suite counts establish
page performance. Preserve failures and run broad suites at completed-candidate
boundaries, using focused checks and affected journey inspection while editing.

ADR-0024's owned pools, exact aggregates, durability, generation fencing and
retained authorization remain accepted. Its absent-cache foreground preparation
policy is replaced by this decision. Reconcile old retention constants and
cutoffs with an impact preview/audit before operator cleanup; this ADR deletes
no existing data by itself.

## Alternatives considered

- Continue exact-response job/polling: fails first-visit and moving-range latency.
- Recompute all retained raw observations in each request: unbounded with retention.
- Reduce precision or lose short incidents through hourly averages: violates accuracy.
- Introduce another database immediately: no measured justification yet. Evaluate
  indexed partitioned PostgreSQL and stable time series first; a storage change
  requires its costs, migration and workload evidence.

## Consequences

Calculated histories become retained data with migration, size and write costs,
separate from expendable HTTP caches. Tracing adds pinned dependencies and bounded
export overhead. Measure all costs, enabled/disabled tracing and the full 365-day
scale fixture; the decision is not a capacity claim. Preserve immutable exports
and honest gaps if expired source inputs prevent historical recalculation.

## Revisit conditions

Revisit storage/resolution choices if traced bounded reads cannot meet the accepted
latency/accuracy limits on the documented fixture. Do not raise limits, enlarge
pools or hide slow samples to preserve an implementation.
