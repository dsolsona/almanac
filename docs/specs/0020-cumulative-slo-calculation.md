# SPEC-0020: Retained SLO extrema over exact hourly aggregates

- Status: draft
- Created: 2026-11-09
- Accepted: pending
- Approval: pending
- Implementation: not started
- Related ADRs: [ADR-0040](../adr/0040-cumulative-sums-and-compact-extrema.md), [ADR-0041](../adr/0041-one-observation-table-with-cumulative-sums.md) (independent), [ADR-0042](../adr/0042-keyset-overview-pagination.md) (optional)
- Plan: [Lean SLO derived data](../plans/0030-lean-slo-derived-data.md)
- Supersedes / superseded by: amends SPEC-0006, SPEC-0007, SPEC-0010 and SPEC-0019 as listed below; supersedes no spec entirely. The file name keeps the first draft's title for link stability.

## Purpose

Keep every accepted SLO behavior, latency and accuracy requirement while storing
derived history as compact extrema rows instead of per-cadence points and tiles,
and removing the queues that kept those copies coherent. Operators get the same
pages, exact values and self-healing with fewer moving parts.

## Scope and non-goals

Includes extrema rows, the extrema cursor and worker, the read paths for current
status, list sparkline, detail graphs, daily compliance and exact-value pages,
diagnostics, retention and the preserving migration. Occurrence objectives on a
single SLI only, as today. The hourly aggregates, dirty-hour marking, source
generations and batched window query are unchanged inputs.

Excludes new calculation methods, exclusions (`exclusion_revision` stays 0),
alerting, retention length changes and any visual change. The observation table
merge (ADR-0041) and keyset pagination (ADR-0042) are separate decisions.

## Required amendments to accepted contracts

Applied on acceptance; identifiers of unchanged criteria stay stable.

| Contract | Change |
| --- | --- |
| SPEC-0006 | No formula or watermark change. |
| SPEC-0007 | Retained points and tiles are removed; extrema rows are the required cache of calculated history. Hourly aggregates stay. A rebuilt historical hour evaluates against the cutoff in force at rebuild; untouched rows keep their original boundary. |
| SPEC-0010 | No endpoint or schema change except `/slos/{id}/preparation` job stages `aggregate`, `extrema` and `current`; `snapshot_id` only if ADR-0042 is accepted. |
| SPEC-0019 | AC-12 is satisfied by extrema rows; its "coherent late corrections" means coherent with the facts that exist when the correction is applied. "Work and resource budgets" paragraphs about tiles, summaries, wire encodings, display blocks and representation upgrades are replaced by this spec. In "Currentness", "corrupt payloads remain unavailable" is met by JSON validation and generation-key checks; stored digests are kept only on pinned exact-value payloads. Exact aggregates and invalidation, pools, admission, latency, currentness budgets, pagination admission (unless ADR-0042) and browser rules are unchanged. |
| ADR-0034 | Retry, temporary-error, consistency-check and visibility rules apply unchanged to the dirty-hour work, the extrema cursor and the current refresh. |

## Behavior and contracts

### Window totals

Unchanged: `evaluateDerivedWindows` and `retained_window_batch.sql` produce the
exact sums, interval counts, first/last observation and maximum receipt time of
any cadence-aligned window from hourly aggregates plus cadence-aligned raw edges,
and report the window as pending while a dirty hour intersects its interior or as
failed when that hour's work is exhausted. `slo.EvaluateOperationalTotals` turns
totals into quality, freshness and performance.

### Extrema rows

Table `almanac_slo_extrema`, key `(slo_id, definition_revision, measurement_revision,
exclusion_revision, calculation_version, resolution_seconds, range_start)` with
`resolution_seconds IN (3600, 86400)`, `range_end`, `source_generation`,
`retention_start` (the cadence-aligned retention boundary used at build),
`published_at` and one `lanes` jsonb column:

```json
{"window":     {"marks":[M, ...], "states":[...], "quality":[...], "freshness":[...]},
 "burn_1h":    {...} , "burn_6h": {...}, "attainment": {...},
 "compliance": {"day_end":"2026-11-02T00:00:00Z", "mark": M}}
```

A mark `M` is the complete input of one evaluation at one endpoint:

| Field | Meaning |
| --- | --- |
| `at` | The endpoint, an epoch-aligned multiple of the cadence inside `(range_start, range_end]` |
| `labels` | Subset of `first`, `last`, `minimum`, `maximum`; empty for a transition-only mark |
| `good`, `total` | Exact decimal window sums; `"0"` when no observed interval |
| `observed`, `known_empty`, `zero_traffic` | Exact interval counts |
| `first_observation_at`, `last_observation_at` | First observed interval start and last observed interval end; present exactly when `observed > 0` |
| `latest_receipt_at` | Maximum receipt time over observed and known-empty contributions, or absent |
| `state`, `quality`, `freshness` | The sample state, quality state and freshness the evaluator produced at build |
| `retention_start` | Optional; present when the mark was evaluated under a boundary different from the row's, as happens when a daily row combines hours rebuilt at different times. The reader uses it over the row's value. |

`marks` is ordered by `at` and contains exactly the endpoints the existing
reducer selects: first, last, minimum, maximum and both endpoints of every
change in state, quality or freshness, deduplicated. When those exceed 1,500 for
a lane, only the four labelled marks are kept and the presence sets stand alone
(ADR-0027). The presence sets list the distinct states, quality states and
freshness values of all endpoints in the range. A lane whose window is not a
multiple of the cadence is `null`. A range with no endpoint (cadence longer than
the range) stores lanes with empty `marks` and empty sets. Expected, expired and
missing counts are not stored; the reader recomputes them from `at`, the lane
window, validity and the mark's `retention_start` when present, otherwise the
row's boundary.

Minimum and maximum compare the exact attainment `good/total` by cross
multiplication; ties keep the earlier endpoint; zero-traffic endpoints do not
compete. The same marks serve budget (minimum attainment is minimum remaining
budget) and burn (maximum burn).

`compliance` exists on daily rows only: `day_end` is the UTC midnight and `mark`
the rolling-window evaluation at the last endpoint at or before it.

### Extrema cursor and worker

One row per SLO revision: `built_through`, `dirty_from`, `dirty_to`, and the
ADR-0034 work columns. The series-invalidation trigger (or, after ADR-0041, the
storage function) sets `dirty_from = LEAST(dirty_from, hour(t))` and
`dirty_to = GREATEST(dirty_to, hour(t) + max(window, 6 h) + 1 h)` for every SLO
whose current definition uses the revision, in the acceptance transaction. While
the old point pipeline is still deployed the same trigger also rewinds the
series repair cursor, so old tiles cannot stay falsely ready; that update goes
away with the pipeline. Duplicates and conflicts mark nothing; a duplicate with
a new delivery ID still records its receipt.

The worker claims a cursor and builds hours in `[dirty_from, min(dirty_to, last
closed hour))`, then hours in `[built_through, last closed hour)`, at most 24
hours per transaction. For one hour it evaluates every endpoint in
`(range_start, range_end]` for every lane with one batched query (at most 960
windows per call, as today), reduces each lane with `reduceRetainedLaneLimit`,
stores the row with the current `source_generation` and `retention_start`, and
advances. A pending window defers the batch as temporary. Daily rows are built
when all 24 hourly rows exist and the day has closed, by reducing the hourly
marks with the same rules, including transitions across hour boundaries, and
adding `compliance`. Hours and days inside a dirty range are rebuilt and replace
their rows conditionally on `source_generation` and lease epoch.

Target: each closed hour is built within 5 minutes for every SLO at the
1,000-SLO fixture. Rebuild cost after a late observation is at most
`(window + 7 h) / 1 h` hourly rows plus their days.

### Reads

- **Current status** (`/status`, list rows, detail header): unchanged mechanism,
  window, 1 h, 6 h and the attainment interval from the batched query at the
  effective time. The current refresh no longer publishes a point or extends a
  repair cursor.
- **List sparkline**: 30 buckets over 7 days. Closed hours use hourly rows; the
  open hour uses the current value. Rebuilt in the current refresh when an hour
  closes or a covered row's `published_at` changed; otherwise only the last
  bucket is replaced. Stored in the projection payload as today;
  `budget_history_state` is `ready` unless a covering hour is missing, then
  `preparing`, or its cursor is exhausted, then `unavailable`.
- **Detail graphs**: ranges over 28 days use daily rows for closed days and
  hourly rows for the partial days at both ends; ranges of 28 days or less use
  hourly rows. The open hour and cadence-aligned edges are evaluated live with
  the batched query, at most 7,200 endpoints per read; a pending live edge
  reports the existing `history_preparing` reason. Each mark becomes a sample by
  calling `slo.EvaluateOperationalTotals` with the mark's sums, counts and
  times, `At = at`, the lane window and `RetentionStart` from the mark when
  present, otherwise from the row,
  then `operationalSample`. Display buckets select with `reduceRetainedDisplay`
  and union presence sets into `display_coverage`. The API sample shape is
  unchanged. `history_preparation` reports ready/total seconds over the
  requested range, counting dirty holes as not ready.
- **Daily compliance**: from daily rows; the current day is unfinished as today.
- **Exact-value pages**: the detail payload is pinned for five minutes under a
  generation as today; its digest is computed at insertion and checked on read.
  No other read recomputes a digest.
- **Preparation and diagnostics**: `/preparation` lists dirty-hour work, the
  extrema cursor and the current failure row with attempts, next attempt and
  reason; retry resets the addressed item. The one-minute consistency check and
  metrics classify SLOs as up to date, catching up, failing or stopped from the
  same rows; `derived_bytes` sums the remaining derived tables and indexes.

### Workers and polling

Three loops remain (source aggregates, current, maintenance plus extrema) but
each sleeps until the earliest `next_attempt_at` or cadence boundary it owns,
capped at one second, instead of polling every second with full scans.

### Retention

Raw partitions and hourly aggregates keep today's 365-day rules. Extrema rows
whose `range_end` is at or before the retention cutoff are deleted in bounded
batches. A row rebuilt after late data uses the cutoff in force at rebuild and
reports intervals before it as expired; untouched rows keep their values and
`retention_start`. Derived generations and pinned payloads keep their five-minute
lifetime. Retention previews and audit entries are unchanged.

### Migration

Migration A (F9-T02) creates `almanac_slo_extrema` and the cursor table, seeds
each cursor with `built_through` at the SLO's eligible start, and extends the
series-invalidation trigger to widen the extrema cursor while retaining its
series-repair invalidation. The point, tile and repair pipeline keeps running.
The worker builds history from existing aggregates and raw rows.
Migration B (F9-T03) drops the old tables after every consumer has switched.
Both run under the existing explicit upgrade step and preserve the maintainer's
data. Provisional numbers are 0045 and 0046 after main's 0044; recheck the tip.

## Failure and recovery

ADR-0034 rules apply: temporary errors reschedule without counting; five failures
back off to five-minute then hourly retries; seven days stops the cursor and
marks it; new observations restart it; retries are audited. A behind or
exhausted cursor leaves later hours pending in graphs with the existing reasons;
current status is never blocked by extrema work. A dirty hour that cannot be
rebuilt makes windows that contain it pending or failed, never successful.

## Acceptance criteria

- AC-01: For the SPEC-0019 focused fixture and a randomized fixture with gaps,
  known-empty, zero traffic, fractional and 26-digit counts, every hourly and
  daily row's marks equal the endpoints and values the retained reducer selects
  from a full per-endpoint evaluation at the same generation, including ties,
  transitions, presence sets, 15 s, 60 s, 70 s and 7,200 s cadences, hours with
  no endpoint and compliance endpoints before midnight.
- AC-02: Detail samples for 7d, 90d, 1y, window and both burn windows on the
  seeded 20-SLO fleet deep-equal those of the retained reader at the same
  generation for every lane, selected point, label, `display_coverage`,
  compliance day and summary, including receipt watermarks and freshness after
  out-of-order arrivals.
- AC-03: A late observation up to 365 days old rebuilds exactly the hours in
  `[t, t + max(window, 6 h) + 1 h)` and their days, within the currentness
  budgets, while unaffected current windows stay usable throughout.
- AC-04: Duplicate and conflicting deliveries change no canonical row, extrema
  row or cursor; a duplicate with a new delivery ID still records its receipt.
- AC-05: Each crash point (before and after claim, after an hourly commit, before
  acknowledgment) recovers without lost or duplicate work; concurrent ingestion
  during a build cannot be acknowledged away.
- AC-06: The preserving migration on a copy of the preview database keeps every
  observation and aggregate, produces matching current statuses and, after the
  worker catches up, matching graphs for every SLO except endpoints whose window
  reaches before the raw cutoff, which show `expired_history` for that part.
- AC-07: On the 35-day fixture, derived bytes by table and index are reported
  against the pre-change baseline and show the reduction; on the 365-day fixture
  absolute sizes are reported, since the old pipeline at that scale exceeds the
  test host and no pre-change baseline is required. Background statements per
  minute per SLO are below 20 while idle.
- AC-08: `make test-perf` at `PERF_DAYS=35` and `PERF_DAYS=365` passes the
  existing page budgets; a 1y detail read reads at most 400 extrema rows and
  evaluates at most 7,200 live endpoints.
- AC-09: Diagnostics show dirty-hour, extrema and current work with reasons and
  retry; the consistency check restarts stopped cursors on new observations.
- AC-10: A 28-day window rebuilt after late data at an endpoint 364 days old
  reports the pre-cutoff intervals as expired, keeps exact totals for the
  retained part, and a neighbouring untouched hour keeps its original values.

## Dependencies and open questions

- Acceptance of ADR-0040. ADR-0041 and ADR-0042 are independent.
- `snapshot_id` in the overview response depends on ADR-0042.

## Verification evidence

Not verified yet.

## Change notes

- 2026-11-09: Drafted from the SLI/SLO efficiency review with cumulative sums.
- 2026-11-09: Branch review found stale-sum, retention-floor, global-fence and
  cadence-offset defects in that draft. Rewritten without cumulative sums over
  the existing hourly aggregates; marks carry complete inputs and absolute
  endpoints; late data beyond the cutoff uses the explicit expired outcome.
