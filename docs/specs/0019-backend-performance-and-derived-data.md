# SPEC-0019: Backend performance and derived data

> Accepted amendment, 2026-11-09: [ADR-0042](../adr/0042-keyset-overview-pagination.md)
> supersedes the overview's coherent multi-page snapshot and admission rules.
> Overview pages read live rows with per-row generation stamps, filter-bound
> keyset cursors and per-request group counts; rows can move between pages.
> The deprecated v1 `snapshot_id` stays a valid nonzero response UUID for CLI
> compatibility. Detail exact-value payloads remain generation-pinned.

- Status: accepted
- Created: 2026-10-30
- Accepted: 2026-10-30; amended 2026-10-31 after maintainer approval of the performance/tracing intervention in Crit
- Approval: maintainer explicitly replied "approved" after the combined Crit review of this spec, ADR-0024 and the stabilization plan
- Implementation: in progress
- Verification: bounded ten-SLO/year runtime evidence and dependency-selected integration coverage are recorded in [F3-T08](../tasks/F3-T08.md); larger/endurance validation is deferred to [F3-T10](../tasks/F3-T10.md)
- Related ADRs: [ADR-0025](../adr/0025-fast-operational-journeys-and-tracing.md), [ADR-0024](../adr/0024-precomputed-operational-reads.md), [ADR-0005](../adr/0005-measurement-semantics.md), [ADR-0007](../adr/0007-postgresql-and-storage-boundaries.md), [ADR-0023](../adr/0023-slo-operational-urgency.md)
- Plan: [Backend stabilization](../plans/0022-backend-stabilization.md)
- Supersedes / superseded by: amends the existing contracts in the table below

## Purpose

Keep SLI/SLO navigation fast as history grows, and make computed results trustworthy
under concurrent ingestion, late data, edits, failures and restart. Feature delivery
is paused while this stabilization is designed and completed. Existing operator
data, credentials and preview availability must be preserved.

## Scope and non-goals

Includes pooled PostgreSQL access, bounded SLI lists, exact aggregate storage,
durable recomputation, current projections, retained SLO time series, list pagination,
independent browser loading, diagnostics and production-stack acceptance.
The first implementation supports the currently executable single-SLI occurrence
objectives. Later methods must use their own proven sufficient aggregates or remain
on their explicitly supported calculation path; this spec does not enable them.

No visual redesign, later milestone features, new database/broker, retention
reduction, float conversion, altered urgency policy or backend datasource collection.

## Required amendments to accepted contracts

The following amendments were accepted on 2026-10-30. They supplement the named
specs without rewriting historical ADR rationale. Existing acceptance identifiers
remain stable; AC-01 through AC-09 below govern the added stabilization requirements.

| Contract | Required change |
| --- | --- |
| ADR-0007 | ADR-0024 replaces optional acceleration with required pooled access and precomputed operational reads. Retained canonical facts, SQL/pgx/sqlc and migration discipline stay intact. |
| ADR-0005 | No mathematical amendment: exact counts, complete intervals, unknown coverage, revisions and immutable exports remain required. |
| ADR-0023 | No urgency-threshold amendment. Clarify that bounded list/history projections are served from coherent published derived generations; currentness and stale results follow this spec. |
| SPEC-0003 | Canonical acceptance and its dirty-generation marker commit atomically; duplicates do not advance generations; derived failures cannot reverse accepted ingestion. |
| SPEC-0006 | Define exact aggregate equivalence, generation metadata, incremental maintenance, bounded foreground reads, time-driven recomputation and stale-cache behavior. Replace any implication that an interactive read must calculate from raw facts with calculation provenance and exact equivalence. |
| SPEC-0007 | Maintain exact source summaries and one year of retained SLO attainment/budget/burn time series; retain 365-day canonical data. Specify dirty ranges, generation fencing, cache retention, pools and admission budgets. |
| SPEC-0010 | Describe coherent generation fields, preparation/unavailable responses, pinned exact-value pagination and UI/CLI parity. Final endpoint/schema diffs are reviewed before implementation. |
| SPEC-0012 | Add shared bounded request caching and independent status/history loading. Keep page composition, 1,500-point cap, extrema/gaps, exact tables and existing frontend budgets. |
| SPEC-0013 | Own pools/workers in backend lifecycle; validate schema at startup/readiness; expose lag, queue, query and pool metrics; test restart/shutdown. |
| SPEC-0014 | Add the real database/API/browser workload and latency/currentness/isolation gates below. Frontend fixture timings alone cannot establish backend performance. |

## Behavior and contracts

### Exact aggregates and invalidation

An hourly aggregate is keyed by SLI, measurement revision and half-open UTC hour.
It retains exact summed good/total, interval outcome counts and sufficient temporal
coverage/freshness evidence to reproduce the supported result. A sum may exceed the
numeric precision of one canonical observation; it must not overflow or be rounded.
Irregular cadence, partial hours, validity/retention edges and exclusion boundaries
use exact aligned contributions; never prorate or assume a full bucket is complete.

Published points must equal the canonical evaluator at the same endpoint and bounds.
Hourly source sums alone cannot establish minute-level extrema. Budget/burn point
caches are computed over the accepted endpoint grid before display reduction;
first/last/min/max and missing-region transitions remain available. Exact tables
return the actual selected point and its source counts, not a value reconstructed
from a plotted pixel or rounded percentage.

Under [ADR-0027](../adr/0027-bounded-history-quality-display.md), dense quality
transitions cannot reject an otherwise supported graph. Preserve individual
transitions when they fit; otherwise attach the distinct sample, quality and
freshness states present in each display bucket as `display_coverage`. These
presence sets do not change selected-point values or imply exact interval counts.
Render incomplete buckets conservatively, retain qualified numeric markers and
use narrower ranges for finer gap locations. Compose presence from retained
transitions/summaries and exact bounded edges, without a new preparation job.

Late data dirties all endpoints in which that interval contributes. For an interval
ending at t and a rolling window W, schedule the enclosing affected endpoint range
through t+W, clipped to retained/evaluable time; include relevant burn windows,
completed UTC days and display blocks. Clock ticks advance windows and freshness
without ingestion. Definition, historical eligibility, exclusion and retention
changes invalidate the appropriate generations; annotation changes do not.

### Currentness, preparation and consistency

Identify source generation, measurement/definition/exclusion revisions, calculation
version, evaluated-through boundary and publication time. Report source-observation
freshness separately from calculation lag. A new generation does not make old
observations fresh. A current revision mismatch is invalidation, not mere cache age.

On 2026-11-02 the maintainer approved showing last known values with explicit
staleness instead of blanking them during updates. Operational list rows report
`current_state`: `ready`, `stale`, `failed` or `unavailable`. Retain compatible,
intact last-published values and their generation while newer source inputs or
clock boundaries await publication; show their evaluation time and mark them
stale, or failed when the update has exhausted its budget. This calculation state
is separate from the original observations' freshness. Never show an unconfirmed
Healthy badge/group: retain prior Needs attention, otherwise use Watch. Definition,
measurement or calculation-version mismatches, corrupt payloads and values older
than the retained horizon remain unavailable. Pinned exact-value pages preserve
their selected values/state. Currentness and all latency gates still apply.

Under the controlled workload, p95 time from accepted commit to its affected current
summary becoming visible is at most 10 seconds. Clock-driven status must advance
within 10 seconds after its required cadence boundary. Large historical rebuilding
has explicit progress and a separate budget; it cannot delay current summaries.

Normal list/detail/range navigation returns bounded results from maintained time
series and exact bounded edges; it cannot enqueue graph work and poll before
showing useful results. Response-cache misses, minute boundaries, new observations
and restart with retained data are ordinary reads, not exempt preparation phases.
Explicit import/repair/disaster recovery may expose preparation/failure metadata;
it is distinct from source no-data, zero traffic, not-applicable eligibility,
expired history and database failures. Never fabricate current success.

Backfill never gates an unaffected selected range behind a whole-history repair
cursor. Read availability follows source changes within each calculation's input
window. Preserve ready graph intervals and current status while other intervals
are unfinished; uncalculated intervals have unavailable quality and null values,
not missing source observations or zero values. Initial preparation reports selected
graph coverage (ready/total seconds) and failures without inventing an ETA or
claiming paused processing is running. Partial responses do not become reusable
ready caches. Exact-value pages remain pinned to their response generation.

Current attainment, remaining budget and supported burn windows are maintained on
affected ingestion and due window/freshness boundaries. Persist their historical
points for one year independently of ephemeral response caches. Historical
resolution summaries retain extrema/gaps and are keyed independently of moving
page ranges. A graph's display period is separate from its burn window: a year of
1-hour or 6-hour burn is a historical range, not a year-long burn calculation.

Under accepted [ADR-0042](../adr/0042-keyset-overview-pagination.md), overview
filters, urgency counts and ordering are evaluated per request. Opaque keyset
cursors bind the filters and page limit and seek by urgency rank, lowercase name
and UUID, in either direction. Rows can repeat or be skipped when urgency changes
between pages; each row retains its own generation evidence. The overview keeps
the required deprecated nonzero `snapshot_id` response UUID without snapshot
admission. Exact-value pages and exports stay pinned;
background publication cannot change those results. Invalidated Healthy values
cannot be presented as fresh or used to infer alert recovery. Authorization is
checked on every request and exact-value page, including retained snapshots.

Current summary and history can be requested independently; each identifies its
generation. When presented as one coherent result, they must agree on the pinned
generation. Existing composite response compatibility must be explicitly retained
or versioned during the API contract review; do not silently alter generated types.

### Browser behavior

ADR-0042 replaces the 2026-11-01 list snapshot admission clarification. Overview
pages use the existing 10/20-row controls, default 50/max 200 API limit and
filter-bound cursors of at most 512 bytes. There are no snapshot slots, byte
reservations, fingerprints or full-result materializations. Lists must not
automatically poll preparing history or refresh without an explicit user action.

On 2026-11-01 the maintainer clarified that the acceptance device is reasonably
capable, without artificial CPU slowdown. Record real hardware and measure the
complete journey with separate normal-network and added-100-ms-latency cohorts,
without an artificial bandwidth cap. In the same conversation the maintainer
accepted complete journeys under 1,000 ms in both conditions, retaining <=500 ms
as the optimization target. This replaces the earlier 500-ms detail/graph hard
limit; API budgets, accuracy and the 10/100/1,000-objective year-history
requirements remain unchanged. Keep the existing renderer while investigating
smaller improvements; no rendering-architecture change was approved.

Share identical in-flight reads; cache definitions and confirmed history for up to
30 seconds with at most 100 retained query entries and five minutes of inactivity.
Mutations invalidate affected entries immediately; sign-out/authorization failure
clears protected entries. Cancel obsolete navigation/range requests and ignore late
responses. Show status without waiting for history. Lists use bounded backend
projections, not per-row detail requests. Cache hits retain their disclosed evaluation
time; they do not extend freshness or invalidate the mutation fencing above.

The maintainer approved server-selected `period=window|7d|90d|1y` bounds on
2026-11-01 under [the parallel-read plan](../plans/0024-parallel-operational-detail-reads.md).
Definition, status and history start independently after authentication; history
does not wait for a definition or browser clock. The additive selector preserves
explicit timestamp callers and the existing seven-day API default. Its selected
period, server alignment and unchanged calculation/response contracts are defined
in the API source and approved plan.

### Work and resource budgets

Write bounded graph summaries alongside retained fine tiles, preserving exact
numeric extrema and coverage information. Validate each summary against the tile's
source generation and payload digest before use. Preserve finer points whenever
they fit the response limit; missing or stale summaries use the bounded legacy
read path. Representation upgrades reuse low-priority, bounded background work;
they neither invalidate ready measurements nor block normal data publication.
Encode coarse series during publication rather than reconstructing their JSON on
each read. Reuse completed daily compliance from the same protected graph read,
fetching only days absent from that read. Include every transferred payload in the
shared wire budget; retain source, readiness, null and expiry checks during reuse.
Hourly tiles also retain encoded finer lanes; daily tiles do not duplicate those
larger fine representations. Recent edges first use source-validated maintained
points, with bounded exact calculation when those points are missing or outdated.
An immutable response derives its digest and canonical byte count together at
insertion. Cache accounting reuses that exact count, including legacy-row fallback;
payload edits retain the previous digest so corruption remains detectable.

Use separate live, interactive and derived/historical connection admission as in
ADR-0024. Initial interactive pool acquisition timeout is 250 ms, statement timeout
2 s and lock timeout 250 ms; background statements are bounded to 5 s and batches
to 100 dirty keys, at most two database workers. Tune batch size down if isolation
fails; changing acceptance targets requires explicit review. No sleeping worker
holds a database transaction or connection. Bound queue scans and retain dirty work
durably across outages. Expose exhausted errors rather than retrying forever.

Retained detail transactions set local PostgreSQL `work_mem` to 32 MiB so each
bounded 20-MiB payload can remain in memory during materialization and sorting.
The retained query has four payload workspaces, approximately 128 MiB per active
read and 1 GiB across the eight interactive connections, in addition to database
and application overhead. This is transaction-local, not a global database setting;
wire limits, pool admission, timeouts and acceptance budgets remain unchanged.

## Failure and recovery

Test termination before/after dirty-marker commit, claim, derived writes, publication
and acknowledgment. Recovery must neither lose accepted work nor publish mixed
generations. A new dirty generation survives an older acknowledgment. Revision
conflicts discard the candidate and schedule bounded retry. Retention cannot delete
facts still needed by an admitted rebuild without an explicit expired outcome.

Cache corruption or missing derived blocks causes rebuilding from retained canonical
facts, with preparation/error state exposed; it cannot fabricate a successful graph.
Rebuild current status ahead of old history. Exhausted rebuilds show stable operator
diagnostics and support an audited bounded retry. Backend shutdown preserves durable
work and completes within the existing 30-second limit. Revoked sessions cannot
read previously authorized derived responses.

## Workloads and acceptance budgets

Use production builds, real PostgreSQL 18 and ordinary authenticated HTTP paths.
The 2026-10-31 amendment requires 10/100/1,000-SLO installation fixtures, with
10/20-row UI pages and one year of shared/independent SLI history. The original
100-SLI fixture below remains an intermediate checkpoint, not full scale acceptance.
Record CPU/RAM, storage, PostgreSQL settings, dataset, image/source revision,
query plans, connection counts, memory and derived disk cost. These are acceptance
fixtures, not an unmeasured capacity promise.

| Fixture | Data and purpose |
| --- | --- |
| Focused regression | One SLI at 15 s for 14 days (80,640 intervals), plus one at 60 s for 56 days; 28-day occurrence windows. Reproduce dense reads without a broad matrix. |
| Integrated workload | 100 SLIs at 60 s for 56 days (8.064 million canonical intervals), 100 SLOs, 10 agents and 10 interactive users. Include shared-SLI objectives in the 100 total, fractional/large counts, incidents, gaps, known-empty and zero traffic. |
| Retention boundary | The same 100 SLIs over 365 days (52.56 million intervals), including month partitions and expiry. Run once on the final candidate; record required resources and setup separately. |

Evaluate 7/28/90/365-day graph ranges with truthful unavailable regions. Use a
10- or 20-row list page and at most 1,500 points per numerical series. For API statistics
collect at least 100 reads per operation at ten-user concurrency without excluding normal cache-miss/first-visit waits. Measure cold application/cache
startup separately with persisted derived data. Include a blank-derived rebuild scenario and report its duration,
without charging preparation as an ordinary ready-data latency or hiding it.

| Operation, ready derived data | Required p95 server latency |
| --- | --- |
| SLI list; SLO list with global groups/counts; current SLO summary | <= 200 ms each |
| SLO four-lane history; bounded exact-value page | <= 500 ms each |
| Accepted ingestion under simultaneous reads/rebuild | <= 250 ms and <= 1.25 times its measured ingestion-only p95 |

For the production browser fixture, record at least 20 first/cold and repeat
loads per critical page and 50 interactions. Every SLI/SLO list sample must become
usable in < 1,000 ms, preferably <= 500 ms; every SLO-detail and range/burn change
must complete in < 1,000 ms, preferably <=500 ms. Include rendered graphs, not just API completion or a
loading shell. Keep the existing <= 200 ms feedback/chart-update budgets and
SPEC-0012's CPU/network settings. Report p50/p95/max and retain every failure;
percentiles do not exempt samples exceeding the action-specific limit.

Trace browser navigation through fetch, HTTP/auth/services, pool acquisition,
named SQL, calculations, serialization and chart paint with OpenTelemetry. Send
bounded OTLP/HTTP exports to VictoriaTraces; include agent, CLI and background
paths and W3C propagation. Protect secrets, bound authenticated browser intake,
and isolate exporter failures from application work. Full local acceptance capture
is required; missing/incomplete traces fail evidence. Tracing establishes duration,
while independent exact expected values establish numerical correctness.

During a 15-minute integrated live run, each agent must remain within its existing
scheduling/resource contract; backend-induced collection lateness p99 may increase
by at most one second over the same ingestion-only fixture. Inject historical
saturation and verify background admission pauses before reserved live capacity is
used. Report missing source samples separately from backend-induced lateness.
Do not increase pool sizes, timeouts or retry counts to hide unbounded query work.

## Acceptance criteria

- AC-01: Actual SLI/SLO lists load in < 1,000 ms and detail/range/burn actions in
  < 1,000 ms, preferably <=500 ms, at 10/100/1,000 SLOs with one year of history; first visits, response-cache
  misses and ordinary restart/time-boundary loads are included. API budgets also apply.
- AC-02: Canonical versus aggregate/cached output matches exactly for fractional
  and maximum counts, equality thresholds, negative budgets, partial/empty/gapped
  intervals, non-hour windows, validity, exclusion and retention boundaries.
- AC-03: Every dirty/publication crash point and concurrent newer arrival recovers
  without lost work, duplicate contributions or mixed generations.
- AC-04: Currentness advances within its budget after ingestion and clock ticks;
  preparation, invalidation and worker failures cannot become apparent success.
- AC-05: Overview group counts/order/filtering match each request's rows under
  ADR-0042 keyset semantics; exact-value pages remain pinned across concurrent
  publication and cursor expiry. Every page rechecks permission revocation.
- AC-06: Live ingestion/scheduling remains within the isolation budgets while
  historical rebuilding, concurrent users and partition maintenance run.
- AC-07: Shared browser requests, independent sections, cancellation and bounded
  cache retention pass real production-stack journeys; both themes/mobile retain
  the existing presentation and exact values.
- AC-08: Pool exhaustion, schema incompatibility, database outage, rebuild exhaustion,
  backend restart and shutdown fail visibly and preserve operator data and work.
- AC-09: Evidence includes row/query counts and plans proving ready list/detail
  reads do not sweep canonical retention or rebuild every objective; report derived
  storage bytes and worker backlog/drain rates as well as HTTP percentiles.

- AC-10: OpenTelemetry traces in VictoriaTraces cover complete rendered journeys,
  backend/database/calculation work and agent/CLI/background paths; missing spans,
  export failures or incorrect source identity cannot produce passing evidence.
- AC-11: Normal navigation does not wait for queued preparation; current summaries
  and stable historical inputs are maintained independently of page visits.
- AC-12: A year of historical attainment, remaining budget and both supported burn
  windows survives cache expiry/restart and supports bounded month/year graphs
  with exact drill-down, coherent late corrections and preserved extrema/gaps.

## Dependencies and open questions

The original architecture, numeric budgets and additive API direction were accepted
on 2026-10-30. ADR-0025 and the 2026-10-31 intervention amendment supersede the
foreground preparation policy, retention, page scale and end-to-end latency limits. Existing precision/retention/authorization remain constraints. The plan's
first checkpoint produces the exact schema/API diff and resource estimates before
coding; the detailed wire contract remains subject to that checkpoint's review.

## Verification evidence

Bounded ten-SLO/year runtime acceptance is recorded in [F3-T08](../tasks/F3-T08.md):
normal and added-100-ms rendered journeys pass all 342 connected trace checks,
alongside independent arithmetic, repair/restart, pagination, logical expiry and
shared-source checks. The integration follow-up covers all 173 cases through
thirteen corrected/previously unexecuted cases and 160 source-matched reused
passes, with fresh and reused execution reported separately. Earlier failed runs
remain recorded in F3-T08. This does not establish the deferred larger-installation
or endurance criteria.
On 2026-11-04 the maintainer deferred further deliberate endurance observation
and larger installations to [F3-T10](../tasks/F3-T10.md). The product criteria above
remain unchanged; this is not a larger-installation or fresh endurance capacity claim.

## Change notes

- 2026-10-30: Drafted after the maintainer paused feature delivery to prioritize
  backend correctness, predictable latency and reuse of proven processing patterns.
- 2026-10-30: Accepted by explicit maintainer approval following Crit review.

- 2026-10-31: Accepted the Crit-reviewed intervention: one-year retained SLO history,
  10/20-row lists at 10/100/1,000 scale, < 500 ms detail/graph actions, no foreground
  job wait, and OpenTelemetry/VictoriaTraces journey evidence. Implementation remains unverified.
