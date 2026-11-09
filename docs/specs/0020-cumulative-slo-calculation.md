# SPEC-0020: Cumulative SLO calculation and retained extrema

- Status: draft
- Created: 2026-11-09
- Accepted: pending
- Approval: pending
- Implementation: not started
- Related ADRs: [ADR-0040](../adr/0040-cumulative-sums-and-compact-extrema.md), [ADR-0041](../adr/0041-one-observation-table-with-cumulative-sums.md), [ADR-0042](../adr/0042-keyset-overview-pagination.md) (optional)
- Plan: [Lean SLO derived data](../plans/0030-lean-slo-derived-data.md)
- Supersedes / superseded by: amends SPEC-0006, SPEC-0007, SPEC-0010 and SPEC-0019 as listed below; supersedes no spec entirely

## Purpose

Keep every accepted SLO behavior, latency and accuracy requirement while storing
derived data in roughly a thousandth of today's space and removing most background
churn. Operators get the same pages, exact values and self-healing with fewer
moving parts to fail.

## Scope and non-goals

Includes the observation table layout, cumulative sums, extrema rows, the two
maintenance cursors, the read paths for current status, list sparkline, detail
graphs, daily compliance and exact-value pages, diagnostics, retention and the
preserving migration. Occurrence objectives on a single SLI only, as today.

Excludes new calculation methods, exclusions (the `exclusion_revision` column stays
at 0), alerting, retention length changes and any visual change. Keyset pagination
is a separate optional decision (ADR-0042).

## Required amendments to accepted contracts

Applied on acceptance; identifiers of unchanged criteria stay stable.

| Contract | Change |
| --- | --- |
| SPEC-0006 | No formula change. "Receipt watermark" in window quality becomes the receipt time of the window's last observed interval. |
| SPEC-0007 | Observations and coverage are one table with nullable counts and no stored interval end. Hourly aggregates and retained points are removed; cumulative sums and extrema rows are the required caches. Partition drops record the retained floor. Derived dirty ranges become two cursors. |
| SPEC-0010 | No endpoint or schema change except: `/slos/{id}/preparation` job stages become `cumulative`, `extrema` and `current`; `snapshot_id` only if ADR-0042 is accepted. |
| SPEC-0019 | AC-12 is satisfied by extrema rows and cumulative sums instead of persisted points and tiles. "Exact aggregates and invalidation", "Work and resource budgets" (tiles, summaries, wire, display blocks) and the hourly-aggregate wording are replaced by this spec. Latency, currentness, pagination admission (unless ADR-0042) and browser rules are unchanged. |
| ADR-0034 | Retry, temporary-error, consistency-check and visibility rules apply unchanged to the two cursors and the current refresh. |

## Behavior and contracts

### Observation rows

Table `almanac_sli_observations`, monthly range partitions on `interval_start`:

| Column | Type | Rule |
| --- | --- | --- |
| `sli_id`, `measurement_revision`, `interval_start` | uuid, bigint, timestamptz | primary key, in this order |
| `outcome` | text | `observed`, `complete_empty`, `incomplete`, `failed` |
| `good_count`, `total_count` | numeric(38,12) null | non-null exactly when `outcome='observed'`; `0 <= good <= total` |
| `error_code`, `missing_components` | as today | same checks as today |
| `result_digest`, `execution_id`, `delivery_id`, `agent_id`, `session_generation`, `configuration_revision`, `lease_id`, `agent_created_at`, `recorded_at` | as today | provenance unchanged |
| `ingestion_generation` | bigint | source generation that accepted the row |
| `cum_good`, `cum_total` | numeric null | running sums over observed rows with `interval_start <=` this row |
| `cum_observed`, `cum_known_empty`, `cum_zero_traffic` | bigint null | running counts; all five cumulative columns null or all set |

Indexes: the primary key, and a partial index `(sli_id, measurement_revision,
interval_start) WHERE outcome='observed'`. The interval end is `interval_start +
cadence` of the revision and is never stored.

Revision row `almanac_sli_source_generations` gains: `cumulative_through`
(timestamptz, every row with `interval_start < cumulative_through` has correct
cumulative columns), `cumulative_generation`, `retained_from` and the five
`floor_*` columns (cumulative values at `retained_from`, zero at creation), and
the ADR-0034 work columns `lease_epoch`, `lease_until`, `attempts`, `next_attempt_at`,
`failing_since`, `last_error`.

### Acceptance of an observation

In the acceptance transaction, after the row insert, the storage function:

1. increments `generation` and sets `ingestion_generation` on the row;
2. sets `cumulative_through = LEAST(cumulative_through, interval_start)`;
3. for every SLO whose current definition uses this revision, widens the extrema
   cursor's dirty range to cover `[hour(interval_start), hour(interval_start) +
   max(window, 6 h) + 1 h)`.

Duplicates and conflicts do none of this. There are no triggers.

### Cumulative worker

Claims revision rows whose `cumulative_through` is before their latest
`interval_start + cadence`, with the ADR-0034 lease and retry rules. Each batch
recomputes the cumulative columns for rows with `interval_start >= cumulative_through`
in ascending order, at most 50,000 rows per transaction, seeded from the last row
before `cumulative_through` or from the floor when none exists, then advances
`cumulative_through` to the last processed interval end and, when caught up, sets
`cumulative_generation = generation`. Statement and lock timeouts are those of
SPEC-0019. Freshness target: a new observation at the series tail is cumulative
within 2 s under the controlled workload.

### Window totals

For a cadence-aligned window `[a, b)` of one revision, with `cum(x)` the cumulative
columns of the last row with `interval_start < x` (or the floor when `x <=
retained_from`, or zeros when no row exists):

- if `b <= cumulative_through`: totals `= cum(b) - cum(a)`;
- else if fewer than 7,200 rows lie in `[cumulative_through, b)`: totals
  `= cum(cumulative_through) - cum(a) + sum of those rows`;
- else the window is pending (`ErrDerivedPending`).

First observation is the first observed row with `interval_start >= max(a,
retained_from)` and `< b`; last observation is the last observed row with
`interval_start < b` and `>= a`; latest receipt is that last row's `recorded_at`.
The totals feed the existing `slo.EvaluateOperationalTotals`, whose expected,
expired, missing, quality, freshness and performance rules are unchanged.
`RetentionStart` passed to it is `max(retained_from, now - 365 d)` aligned to the
cadence, so expired counts match the retained floor.

### Extrema rows

Table `almanac_slo_extrema`, key `(slo_id, definition_revision, measurement_revision,
exclusion_revision, calculation_version, resolution_seconds, range_start)` with
`resolution_seconds IN (3600, 86400)`, `source_generation`, `published_at` and one
`marks` jsonb column:

```json
{"window":  {"first":M,"last":M,"min":M,"max":M,"states":["observed","stale"],"quality":["complete"],"freshness":["fresh"],"transitions":[17,18]},
 "burn_1h": {...}, "burn_6h": {...}, "attainment": {...},
 "compliance": {"offset":1440,"good":"...","total":"...","observed":1440,"known_empty":0,"zero_traffic":0}}
```

A mark `M` is `{"offset":n,"good":"d","total":"d","observed":n,"known_empty":n,
"zero_traffic":n}` where `offset` counts cadence endpoints after `range_start`
(endpoint time `= range_start + offset * cadence`, `1 <= offset <= range/cadence`).
`good`/`total` are exact decimal strings of the window sums at that endpoint, or
absent for endpoints with no observed interval. A lane whose window is not a
multiple of the cadence stores `null`. `transitions` lists offsets where the
sample state, quality state or freshness differs from the previous endpoint, at
most 1,500 per lane; when more exist the list is omitted and the presence sets
stand alone (ADR-0027). `compliance` exists only on daily rows and holds the
rolling-window evaluation at the last endpoint of the day.

Minimum and maximum compare the exact attainment `good/total` by cross
multiplication; equal values keep the earlier endpoint. Zero-traffic endpoints do
not compete. For the budget lane the minimum attainment is the minimum remaining
budget and for burn lanes it is the maximum burn, so one mark set serves the three
rolling displays.

### Extrema worker

One cursor row per SLO revision: `dirty_from`, `dirty_to`, `built_through`, work
columns. The worker claims a cursor, evaluates every cadence endpoint of each hour
in `[dirty_from, min(dirty_to, last closed hour))` and of each hour in
`[built_through, last closed hour)` using window totals, writes the hourly rows,
then writes daily rows for days whose 24 hourly rows exist and whose end has
passed, at most 24 hours per transaction. A pending window defers the batch
(temporary). `source_generation` of a row is the revision generation at build.
Target: each closed hour is built within 5 minutes for every SLO at the 1,000-SLO
fixture.

### Reads

- **Current status** (`/status`, list rows, detail header): window, 1 h, 6 h and
  the attainment interval at the effective time from window totals; urgency,
  exhaustion and generation stamps as today. Published by the current refresh when
  the generation or cadence boundary changes, as today.
- **List sparkline**: 30 buckets over 7 days. Closed hours use hourly extrema
  rows; the open hour uses the current value. Rebuilt when an hour closes or the
  cursor rebuilds a covered hour; otherwise only the last bucket is refreshed.
  Stored in the projection payload as today.
- **Detail graphs**: for the requested range, closed days (ranges over 28 days)
  or closed hours (28 days and shorter) come from extrema rows; the open hour and
  non-aligned edges are evaluated live, at most 7,200 endpoints per read. Display
  buckets select first, last, minimum and maximum among their candidates, union
  the presence sets into `display_coverage`, and keep transitions while the series
  stays within 1,500 points. Sample values are recomputed from the mark's exact
  counts with the canonical evaluator; the API sample shape is unchanged.
- **Daily compliance**: from daily rows; the current day is unfinished as today.
- **Exact-value pages**: the detail payload is pinned for five minutes under a
  generation as today; its digest is computed at insertion and checked on read.
  No other read recomputes a digest.
- **Preparation and diagnostics**: `/preparation` lists the cumulative cursor,
  the extrema cursor and the current failure row with attempts, next attempt and
  reason; retry resets the addressed cursor. The one-minute consistency check and
  metrics classify SLOs as up to date, catching up, failing or stopped from the
  same rows.

### Workers and polling

Three loops remain (source cumulative/extrema, current, maintenance) but each
sleeps until the earliest `next_attempt_at` or cadence boundary it owns, capped at
one second, instead of polling every second with full scans.

### Retention

Partition drop first copies `cum(partition end)` into the revision floor and sets
`retained_from`. Extrema rows with `range_start < now - 365 d` are deleted in
bounded batches. Derived generations and pinned payloads keep their five-minute
lifetime. Retention previews and audit entries are unchanged.

### Migration

One migration creates the new tables and cursors, copies coverage and values
partition by partition into `almanac_sli_observations`, seeds `cumulative_through`
to the earliest interval so the worker computes all sums, seeds every SLO cursor
with `built_through` at its eligible start, and drops the old tables, triggers and
indexes only after the copy row counts match. It runs under the existing explicit
upgrade step and must be resumable and preserve the maintainer's data.

## Failure and recovery

ADR-0034 rules apply: temporary errors reschedule without counting; five failures
back off to five-minute then hourly retries; seven days stops the cursor and
marks it; new observations restart it; retries are audited. A cumulative cursor
that cannot advance makes dependent windows pending, never successful. An extrema
cursor that is behind leaves later hours pending in graphs with the existing
`history_preparing` reason. Current status is never blocked by extrema work.

## Acceptance criteria

- AC-01: For every window in the SPEC-0019 focused and integrated fixtures, totals
  from cumulative lookups equal the canonical sweep over raw rows, including
  fractional and maximum counts, gaps, known-empty, zero traffic, validity and
  retention edges.
- AC-02: Detail samples for 7d, 90d, 1y, window and both burn windows on the
  seeded 20-SLO fleet are identical to those produced by the current retained
  path at the same generation, for every lane and every selected point.
- AC-03: A late observation up to 365 days old changes exactly the windows that
  contain it, in both current status and graphs, within the currentness budgets.
- AC-04: Ingestion of a duplicate or conflicting delivery changes no cumulative or
  extrema row and no cursor.
- AC-05: Each crash point (before and after cursor claim, cumulative chunk commit,
  extrema commit, acknowledgment) recovers without lost or duplicate work.
- AC-06: The preserving migration on a copy of the preview database keeps every
  observation, produces matching current statuses for all SLOs and leaves no old
  table.
- AC-07: After one hour of steady ingestion for 20 SLOs, derived bytes reported by
  the runtime metrics are below 1 MB per SLO and background statements per minute
  per SLO are below 20 while idle.
- AC-08: `make test-perf` at `PERF_DAYS=35` and `PERF_DAYS=365` passes the existing
  page budgets; the 1y detail read issues no more than 400 rows of extrema and
  7,200 live endpoints.
- AC-09: Diagnostics show cursor state, reasons and retry for failing work; the
  consistency check restarts stopped cursors on new observations.

## Dependencies and open questions

- Acceptance of ADR-0040 and ADR-0041; ADR-0042 is independent.
- Keeping provenance on the observation row is assumed; moving it to receipts is
  deferred.
- Whether `snapshot_id` stays in the overview response depends on ADR-0042.

## Verification evidence

Not verified yet.

## Change notes

- 2026-11-09: Drafted from the SLI/SLO efficiency review.
