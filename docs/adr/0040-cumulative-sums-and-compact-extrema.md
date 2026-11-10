# ADR-0040: Serve SLO history from compact extrema over exact hourly aggregates

- Status: proposed
- Created: 2026-11-09
- Accepted: pending
- Approval: pending; on 2026-11-09 the maintainer delegated the design decision to the primary agent and asked for the resulting documents in PR #72
- Related specs: [SPEC-0020](../specs/0020-cumulative-slo-calculation.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0007](../specs/0007-storage-and-retention.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: proposes to supersede the retained point/tile series of [ADR-0025](0025-fast-operational-journeys-and-tracing.md) and the history jobs and display blocks of [ADR-0024](0024-precomputed-operational-reads.md). ADR-0024's hourly aggregates, dirty ranges and generation fencing and [ADR-0034](0034-self-healing-derived-data.md)'s retry rules remain. Independent of [ADR-0041](0041-one-observation-table-with-cumulative-sums.md).

## Context

The 2026-11-09 review of the SLI/SLO flows found the arithmetic exact and the
derived-data layer expensive. One retained series point measures 3,633 bytes of
JSON (`internal/storage/retained_series.go`, measured with a throwaway test at
`259f7c1`). Per SLO at a 60 s cadence this is about 1.9 GB of points per year,
plus hourly tiles that store every sample of the hour twice (payload and `fine_wire`),
about 3 GB more. The raw observations those values derive from are about 170 MB.
The UI reads eight of the roughly sixty fields in each sample. These figures are
derived from the measured point and the schema; they were not confirmed on a
populated installation and F9-T06 measures them.

Keeping those copies coherent needs five queues (dirty hours, current projections,
series repair, tile work, history jobs), three one-second loops and the recovery
rules of ADR-0034. In steady state every ingested observation rewinds the repair
cursor, republishes the point, rebuilds the open hour's tile (about 340 KB) and
queues a seven-day sparkline job.

The first draft of this record proposed cumulative sums on the observation rows.
A branch review on 2026-11-09 showed three consequences of that choice: a window
boundary on an unprocessed row reads a stale sum, a single retained floor cannot
reproduce windows that start before the retention cutoff, and one very late
observation makes every later window wait for a revision-wide recomputation.
The review's proposed remedy kept the hourly aggregates as a fallback and added
checkpoint and baseline tables. That remedy also shows the simpler path: the
hourly aggregates with cadence-aligned raw edges, read by the existing batched
window query (`retained_window_batch.sql`), already evaluate a whole hour of
endpoints with one interior sum per lane and prefix-summed edges. They build
every retained point of every SLO in production today, and readiness is checked
per dirty hour, so a late observation only affects windows that contain it. The
cumulative sums were never needed for speed.

What is actually missing is small: a compact, durable record of which endpoints
in each hour and day are the first, last, minimum, maximum and quality
transitions, with the exact inputs to re-evaluate them.

## Decision

Proposal.

1. **Keep the exact source path.** Hourly aggregates, dirty-hour marking, source
   generations, the batched window query and its per-hour readiness rules remain
   the only way window totals are produced, for current status, graphs and the
   extrema worker alike.
2. **Replace points, tiles, summaries, the repair cursor and history jobs with
   extrema rows.** One row per SLO per UTC hour and per UTC day holds, for each
   lane (rolling window, 1 h burn, 6 h burn, attainment), the selected endpoints
   the existing reducer would keep: first, last, minimum, maximum and both sides
   of every state, quality or freshness transition. Each selected endpoint stores
   its absolute time, labels, exact good/total sums, interval counts, first and
   last observation times, receipt watermark and sample state. The row stores the
   retention boundary used when it was built; a mark whose hour was rebuilt under
   a different boundary carries its own, so a daily row composed from hours built
   at different times still re-evaluates each mark exactly. A daily row also stores the
   day-end compliance evaluation at its actual cadence endpoint. One cursor per
   SLO tracks the hours to build or rebuild. SPEC-0020 defines the shape.
3. **Reads compose from rows plus bounded live edges.** Closed hours and days
   come from extrema rows and are re-evaluated with the canonical evaluator from
   the stored exact inputs; the open hour and non-aligned edges are evaluated
   live with the batched query, at most 7,200 endpoints per read. Nothing is
   averaged, rounded or reconstructed from plotted values.
4. **Late data and retention.** An observation accepted for interval `t` dirties
   its hour (as today) and the extrema hours in `[t, t + max(window, 6 h) + 1 h)`.
   A rebuilt hour is evaluated against the retention cutoff in force at rebuild
   time; intervals before it are counted as expired, the explicit expired outcome
   SPEC-0019 allows. Rows that are not rebuilt keep their original values and
   boundary. No checkpoint, floor or baseline table exists. On upgrade, extrema
   are built from the inputs that exist; endpoints whose windows reach before the
   raw cutoff show `expired_history` for that part, once, visibly. SPEC-0019
   AC-12's "coherent late corrections" is read as: a correction is coherent with
   the facts that exist when it is applied. It does not require reproducing
   totals from facts that retention has already removed.
5. **Triggers stay** until ADR-0041 replaces them. During the cutover the
   series-invalidation trigger does both jobs: it keeps rewinding the repair
   cursor for the old point pipeline and widens the extrema cursor's dirty range.
   The repair update is removed together with the old pipeline.

Removed when this is in place: `almanac_slo_series_points`, `almanac_slo_series_tiles`,
`almanac_slo_series_tile_work`, `almanac_slo_series_repair`, `almanac_slo_display_blocks`,
`almanac_slo_history_jobs`, `almanac_slo_history_admission`, the display-block
branch of the cache-usage trigger, and their Go code and queries. Kept: hourly
aggregates and dirty ranges, current projections, generations and failures, the
pinned detail payload for exact-value pages (five-minute lifetime, digest
checked on read) and the overview snapshot tables unless
[ADR-0042](0042-keyset-overview-pagination.md) is accepted.

Also decided here:

- Until this ADR is implemented, hourly tile work is deferred to the end of its
  hour, as daily work already is. This removes the per-minute tile rebuilds and
  changes no value: the reader already serves the open hour from points.
- The seven-day list sparkline is composed from hourly extrema rows when an hour
  closes or is rebuilt; only its last bucket is refreshed per cadence tick.
- No read recomputes a payload digest except the pinned exact-value payload.
  SPEC-0019's rule that corrupt payloads remain unavailable is met by JSON
  validation and generation-key checks on every read; SPEC-0020 amends that
  sentence accordingly.
- Workers wake on the next due time instead of polling every second when idle.
- Code reachable only when no runtime is configured, the raw-fact sweep builders,
  display blocks and detail-kind history jobs are deleted.

## Alternatives considered

- Keep the series but shrink the samples to the fields the UI uses: a five-fold
  saving, but the per-minute churn, the repair cursor and the tile queue remain.
- Cumulative sums on observation rows, with or without hourly fallback: either a
  global readiness fence on unaffected windows or two parallel exact paths plus
  checkpoint and baseline tables to survive retention. Rejected after the branch
  review; the aggregates already give bounded exact windows.
- Keep previously known totals when late data arrives in a window that now
  predates the cutoff, as `preserveRetainedPrefixes` does: needs per-endpoint
  evidence beyond retention. Rejected; the explicit expired outcome is already
  the contract for cold evaluations and costs nothing.
- Recompute every cadence endpoint on read: a year graph needs 525,600
  evaluations per lane. Rejected; extrema rows exist to avoid this.
- Another storage engine: no measured justification; ADR-0025's reasoning holds.

## Consequences

- Derived storage per SLO-year at 60 s becomes about 8,760 hourly rows plus 365
  daily rows. A typical hour keeps four marks per lane at about 350 bytes each,
  roughly 6 KB per row, about 50 MB per year including hourly aggregates; an hour
  with a transition at every endpoint stays under 90 KB. These are estimates from
  the mark shape, not measurements; F9-T06 reports actual totals.
- Background writes between observations fall to the dirty-hour rebuild and, once
  per hour, one extrema row per SLO. The extrema worker's cost is at most today's
  repair worker's cost, since it issues the same batched query per hour.
- Normal navigation issues a bounded number of row reads and one live batch for
  the open hour; there is no preparation phase for closed history.
- A rebuilt hour whose window reaches before the cutoff changes from complete
  to partly expired. Operators see the expired portion rather than a total that
  can no longer be reproduced.
- SPEC-0019 AC-12 and SPEC-0007's persisted-point wording are satisfied by
  extrema rows; a year of exact history, extrema, gaps, pinned values, restart
  survival and late correction remain requirements.
- The migration adds tables, builds extrema from existing aggregates and raw
  rows, switches readers after equivalence is proven, and only then drops the
  old tables. No operator data is deleted by this decision.

## Revisit conditions

Revisit if the extrema worker cannot close an hour for every SLO within the
currentness budget at the 1,000-SLO fixture, or if a method other than
occurrences needs inputs the hourly aggregates cannot supply.
