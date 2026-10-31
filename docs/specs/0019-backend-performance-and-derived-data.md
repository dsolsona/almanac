# SPEC-0019: Backend performance and derived data

- Status: accepted
- Created: 2026-10-30
- Accepted: 2026-10-30
- Approval: maintainer explicitly replied "approved" after the combined Crit review of this spec, ADR-0024 and the stabilization plan
- Implementation: in progress
- Verification: acceptance evidence remains outstanding, tracked in [F3-T08](../tasks/F3-T08.md)
- Related ADRs: [ADR-0024](../adr/0024-precomputed-operational-reads.md), [ADR-0005](../adr/0005-measurement-semantics.md), [ADR-0007](../adr/0007-postgresql-and-storage-boundaries.md), [ADR-0023](../adr/0023-slo-operational-urgency.md)
- Plan: [Backend stabilization](../plans/0022-backend-stabilization.md)
- Supersedes / superseded by: amends the existing contracts in the table below

## Purpose

Keep SLI/SLO navigation fast as history grows, and make computed results trustworthy
under concurrent ingestion, late data, edits, failures and restart. Feature delivery
is paused while this stabilization is designed and completed. Existing operator
data, credentials and preview availability must be preserved.

## Scope and non-goals

Includes pooled PostgreSQL access, bounded SLI lists, exact aggregate storage,
durable recomputation, current projections, cached history, list pagination,
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
| SPEC-0006 | Define exact aggregate equivalence, generation metadata, bounded asynchronous preparation, time-driven recomputation and stale-cache behavior. Replace any implication that an interactive read must calculate from raw facts with calculation provenance and exact equivalence. |
| SPEC-0007 | Make hourly count/coverage aggregates and derived status/history required caches; retain 400-day canonical data. Specify dirty ranges, generation fencing, cache retention, pools and admission budgets. |
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

Under the controlled workload, p95 time from accepted commit to its affected current
summary becoming visible is at most 10 seconds. Clock-driven status must advance
within 10 seconds after its required cadence boundary. Large historical rebuilding
has explicit progress and a separate budget; it cannot delay current summaries.

For an absent/invalidated generation, return HTTP 503 with a stable problem code
`derived_data_pending`, `Retry-After: 2`, and bounded preparation metadata. Preparation
is idempotently queued; repeated reads do not duplicate jobs. This is distinct from
source no-data, zero traffic, not-applicable eligibility, expired history and a
database failure. Browser/CLI show that distinction. Do not synchronously rebuild
months of history as a fallback. Preserve unrelated controls and any independently
available section. A previously confirmed value may remain visible only with an
explicit stale/preparing label and its evaluation time.

Once preparation completes, ordinary reads use published projections. Global
filters, urgency counts, ordering and pagination pin one coherent generation or
immutable set of generation references. A cursor cannot mix updates between pages;
expired generations return an explicit expired-cursor error. Background publication
cannot change an already pinned response/export. Invalidated Healthy values cannot
be presented as fresh or used to infer alert recovery. Authorization is checked on
every request and exact-value page, including retained snapshots.

Current summary and history can be requested independently; each identifies its
generation. When presented as one coherent result, they must agree on the pinned
generation. Existing composite response compatibility must be explicitly retained
or versioned during the API contract review; do not silently alter generated types.

### Browser behavior

Share identical in-flight reads; cache definitions and confirmed history for up to
30 seconds with at most 100 retained query entries and five minutes of inactivity.
Mutations invalidate affected entries immediately; sign-out/authorization failure
clears protected entries. Cancel obsolete navigation/range requests and ignore late
responses. Show status without waiting for history. Lists use bounded backend
projections, not per-row detail requests. Cache hits retain their disclosed evaluation
time; they do not extend freshness or invalidate the mutation fencing above.

### Work and resource budgets

Use separate live, interactive and derived/historical connection admission as in
ADR-0024. Initial interactive pool acquisition timeout is 250 ms, statement timeout
2 s and lock timeout 250 ms; background statements are bounded to 5 s and batches
to 100 dirty keys, at most two database workers. Tune batch size down if isolation
fails; changing acceptance targets requires explicit review. No sleeping worker
holds a database transaction or connection. Bound queue scans and retain dirty work
durably across outages. Expose exhausted errors rather than retrying forever.

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
Record CPU/RAM, storage, PostgreSQL settings, dataset, image/source revision,
query plans, connection counts, memory and derived disk cost. These are acceptance
fixtures, not an unmeasured capacity promise.

| Fixture | Data and purpose |
| --- | --- |
| Focused regression | One SLI at 15 s for 14 days (80,640 intervals), plus one at 60 s for 56 days; 28-day occurrence windows. Reproduce dense reads without a broad matrix. |
| Integrated workload | 100 SLIs at 60 s for 56 days (8.064 million canonical intervals), 100 SLOs, 10 agents and 10 interactive users. Include shared-SLI objectives in the 100 total, fractional/large counts, incidents, gaps, known-empty and zero traffic. |
| Retention boundary | The same 100 SLIs over 400 days (57.6 million intervals), including month partitions and expiry. Run once on the final candidate; record required resources and setup separately. |

Evaluate 7/28/90/365-day graph ranges with truthful unavailable regions. Use a
50-row list page and at most 1,500 points per numerical series. For API statistics
collect at least 100 reads per operation at ten-user concurrency after derived
preparation; measure cold application/cache startup separately with persisted
derived data. Include a blank-derived rebuild scenario and report its duration,
without charging preparation as an ordinary ready-data latency or hiding it.

| Operation, ready derived data | Required p95 server latency |
| --- | --- |
| SLI list; SLO list with global groups/counts; current SLO summary | <= 200 ms each |
| SLO four-lane history; bounded exact-value page | <= 500 ms each |
| Accepted ingestion under simultaneous reads/rebuild | <= 250 ms and <= 1.25 times its measured ingestion-only p95 |

For the production browser fixture, record 20 cold loads and 50 navigations or
filter/range interactions. SLI/SLO lists must show primary rows at p75 <= 1 s;
SLO details must show primary content and chart at p75 <= 2.5 s under SPEC-0012's
network/CPU settings. Existing 200 ms interaction/chart-update budgets remain.
Measure backend, transport, parsing and rendering separately.

During a 15-minute integrated live run, each agent must remain within its existing
scheduling/resource contract; backend-induced collection lateness p99 may increase
by at most one second over the same ingestion-only fixture. Inject historical
saturation and verify background admission pauses before reserved live capacity is
used. Report missing source samples separately from backend-induced lateness.
Do not increase pool sizes, timeouts or retry counts to hide unbounded query work.

## Acceptance criteria

- AC-01: Ready list/status/history reads meet the stated latency budgets in the
  integrated and retention fixtures; cold/rebuild results are reported separately.
- AC-02: Canonical versus aggregate/cached output matches exactly for fractional
  and maximum counts, equality thresholds, negative budgets, partial/empty/gapped
  intervals, non-hour windows, validity, exclusion and retention boundaries.
- AC-03: Every dirty/publication crash point and concurrent newer arrival recovers
  without lost work, duplicate contributions or mixed generations.
- AC-04: Currentness advances within its budget after ingestion and clock ticks;
  preparation, invalidation and worker failures cannot become apparent success.
- AC-05: Group counts/order/filtering and exact-value pages remain coherent across
  concurrent publication, cursor expiry and permission revocation.
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

## Dependencies and open questions

The architecture, numeric budgets and additive API direction were accepted on
2026-10-30. Existing precision/retention/authorization remain constraints. The plan's
first checkpoint produces the exact schema/API diff and resource estimates before
coding; the detailed wire contract remains subject to that checkpoint's review.

## Verification evidence

Implementation is in progress in [F3-T08](../tasks/F3-T08.md). Its fast unit and
compile checks do not establish these workload budgets. Integrated performance
acceptance remains outstanding, and known page latency failures remain open.
This document does not claim measured capacity.

## Change notes

- 2026-10-30: Drafted after the maintainer paused feature delivery to prioritize
  backend correctness, predictable latency and reuse of proven processing patterns.
- 2026-10-30: Accepted by explicit maintainer approval following Crit review.
