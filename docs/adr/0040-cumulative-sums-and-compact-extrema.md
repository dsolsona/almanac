# ADR-0040: Serve SLO history from cumulative sums and compact extrema

- Status: proposed
- Created: 2026-11-09
- Accepted: pending
- Approval: pending
- Related specs: [SPEC-0020](../specs/0020-cumulative-slo-calculation.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0007](../specs/0007-storage-and-retention.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: proposes to supersede the retained point/tile series of [ADR-0025](0025-fast-operational-journeys-and-tracing.md), the hourly aggregates, history jobs and display blocks of [ADR-0024](0024-precomputed-operational-reads.md), and to reduce the queues governed by [ADR-0034](0034-self-healing-derived-data.md) to two cursors. Depends on [ADR-0041](0041-one-observation-table-with-cumulative-sums.md).

## Context

The 2026-11-09 review of the SLI/SLO flows found the arithmetic exact and the
derived-data layer expensive. One retained series point measures 3,633 bytes of
JSON (`internal/storage/retained_series.go`, measured with a throwaway test at
`259f7c1`). Per SLO at a 60 s cadence this is about 1.9 GB of points per year,
plus hourly tiles that store every sample of the hour twice (payload and `fine_wire`),
about 3 GB more. The raw observations those values derive from are about 170 MB.
The UI reads eight of the roughly sixty fields in each sample.

Keeping those copies coherent needs five queues (dirty hours, current projections,
series repair, tile work, history jobs), three one-second loops and the recovery
rules of ADR-0034. In steady state every ingested observation rewinds the repair
cursor, republishes the point, rebuilds the open hour's tile (about 340 KB) and
queues a seven-day sparkline job. That is roughly 20 MB of rewritten JSON per SLO
per hour, before any user opens a page. These figures are derived from the measured
point and the schema; the local Postgres was not running and the dev database is
empty, so they were not confirmed on a populated installation.

What the reads need is small: for any cadence-aligned window, its exact sums and
interval counts; for any display bucket, the first, last, minimum and maximum
endpoint and the quality states present. ADR-0024 chose hourly aggregates with
bounded raw edges for the first and tiles for the second. Both are correct; both
are larger and more churn-prone than necessary.

## Decision

Proposal. Replace the retained point and tile series with two structures:

1. **Cumulative sums on the observation rows** (ADR-0041). Each observation row
   carries the running sums of good, total, observed, known-empty and zero-traffic
   counts since the start of its measurement revision. Any cadence-aligned window
   is the difference of two rows, found by two index lookups. A late or corrected
   observation invalidates the sums from its interval forward; a worker recomputes
   them forward in bounded chunks, tracked by one cursor per SLI revision.
2. **Compact extrema rows** per SLO, per UTC hour and per UTC day. A row stores,
   for each lane (rolling window, 1 h burn, 6 h burn, attainment), the endpoint
   offset and exact counts of the first, last, minimum and maximum evaluation, the
   quality states and freshness present, and the quality-transition offsets. A
   daily row also stores the day-end compliance evaluation. Rows are about 1 KB;
   a year is about 9 MB per SLO. One cursor per SLO tracks the hours to rebuild.

Reads compose a graph from extrema rows for closed hours or days and evaluate open
hours and non-aligned edges live with cumulative lookups. Values shown to users
are recomputed from stored exact counts with the canonical evaluator; nothing is
averaged, rounded or reconstructed from plotted values. Current status, both burn
windows and the attainment interval are eight lookups per SLO per cadence tick.

Removed when this is in place: `almanac_slo_series_points`, `almanac_slo_series_tiles`,
`almanac_slo_series_tile_work`, `almanac_slo_series_repair`, `almanac_sli_hourly_aggregates`,
`almanac_derived_dirty_ranges`, `almanac_slo_display_blocks`, `almanac_slo_history_jobs`
and the coverage triggers, with their Go code. Kept: current projections and their
generations, current failures, the pinned detail payload used by exact-value pages
(five-minute lifetime) and the overview snapshot tables, unless
[ADR-0042](0042-keyset-overview-pagination.md) is accepted.

Also decided here, because they fall out of the same change:

- The open hour is never materialized; it is always evaluated live. Until this
  ADR is implemented, hourly tile work is deferred to the end of its hour, as daily
  tile work already is, which removes the per-minute tile rebuilds at no risk.
- The seven-day list sparkline is composed from hourly extrema when an hour closes
  and only its last bucket is refreshed per cadence tick.
- Stored payloads are no longer re-hashed on read. Generation keys and PostgreSQL
  checksums are the integrity mechanism; a digest is kept only on the pinned
  exact-value payload.
- Workers wake on the next due time rather than polling every second when idle.
- Code reachable only when no runtime is configured, the raw-fact sweep builders,
  display blocks and detail-kind history jobs are deleted.

## Alternatives considered

- Keep the series but shrink the samples to the eight fields the UI uses: about a
  five-fold saving, but the per-minute churn, the repair cursor and the tile queue
  remain. Rejected as a half measure.
- Hourly aggregates with prefix sums at hour resolution only: edges still scan up
  to 120 raw rows per window and extrema need the raw rows anyway. Rejected; the
  per-row sums cost about 60 bytes per observation and remove the edge scans.
- Recompute every cadence endpoint on read: a year graph needs 525,600 evaluations
  per lane. Rejected; extrema rows exist precisely to avoid this.
- Another storage engine: no measured justification; ADR-0025's reasoning holds.

## Consequences

- Derived storage per SLO-year falls from gigabytes to about 9 MB; canonical rows
  grow by about 60 bytes. Background writes between observations fall to one
  cumulative extension and, once per hour, one extrema row per SLO.
- Any range, burn window or cold read is a bounded number of index lookups; no
  cache miss or preparation phase exists for normal navigation.
- A late observation costs a forward recomputation of cumulative sums from its
  interval, chunked per partition. Rare very late data is proportionally more
  expensive than today's one-hour rebuild; it remains bounded and resumable.
- `latest_receipt_at` in window quality becomes the receipt time of the window's
  last observed interval rather than the maximum receipt time in the window. It is
  receipt metadata and does not enter any calculation; SPEC-0020 records it.
- SPEC-0019 AC-12 and the SPEC-0007 amendment that require persisting a year of
  calculated points are replaced by the extrema and cumulative contracts; the
  observable guarantees (year of history, exact drill-down, extrema and gaps,
  survival across restart) are unchanged.
- The migration is preserving: new structures are built from existing observations
  before old ones are dropped; no operator data is deleted by this decision.

## Revisit conditions

Revisit if the extrema worker cannot close an hour for every SLO within the
currentness budget at the 1,000-SLO fixture, if late-data recomputation measurably
competes with live ingestion, or if a method other than occurrences needs a
sufficient statistic that cumulative sums cannot provide.
