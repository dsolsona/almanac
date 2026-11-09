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

Keep every accepted SLO behavior, latency and accuracy requirement while reducing
derived storage and background churn. The original 9 MB/SLO-year estimate excluded
temporal evidence, transitions and historical prefixes; it is not a demonstrated
size or an acceptance limit. F9-T06 measures the complete replacement.

## Scope and non-goals

Includes the observation table layout, cumulative sums, historical prefix evidence,
extrema rows, the cumulative/extrema cursors and retained dirty-hour work, the read paths for current status, list sparkline, detail
graphs, daily compliance and exact-value pages, diagnostics, retention and the
preserving migration. Occurrence objectives on a single SLI only, as today.

Excludes new calculation methods, exclusions (the `exclusion_revision` column stays
at 0), alerting, retention length changes and any visual change. Keyset pagination
is a separate optional decision (ADR-0042).

## Required amendments to accepted contracts

Applied on acceptance; identifiers of unchanged criteria stay stable.

| Contract | Change |
| --- | --- |
| SPEC-0006 | No formula or receipt-watermark change. Complete temporal evidence remains required; the earlier last-interval receipt shortcut is withdrawn because migration cannot reconstruct it for already-expired inputs. |
| SPEC-0007 | Observations and coverage are one table with nullable counts and no stored interval end. Cumulative sums, retained prefix evidence and extrema replace bulk points/tiles; exact hourly aggregates and dirty hours remain as the independent fallback. Canonical retention and its API cutoff stay at 365 days. |
| SPEC-0010 | No endpoint or schema change except: `/slos/{id}/preparation` supports `aggregate`, `cumulative`, `extrema` and `current`; `snapshot_id` only if ADR-0042 is accepted. |
| SPEC-0019 | AC-12 is satisfied by extrema, cumulative sums and preserved prefix evidence instead of persisted points/tiles. Tile-specific encoding is replaced by this spec; exact hourly fallback, pool/admission/byte budgets, latency, currentness, pagination admission (unless ADR-0042) and browser rules remain. |
| ADR-0034 | Retry, temporary-error, consistency-check and visibility rules apply unchanged to both cursors, dirty-hour work and current refresh. |

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
| `cum_good`, `cum_total` | numeric null | running sums over observed rows with `interval_start <=` this row, in the revision's fixed cumulative coordinate system |
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

For a new revision the coordinate starts at revision eligibility with zero sums.
An upgrade whose older raw inputs already expired anchors zero at the earliest
retained/admissible canonical boundary instead; it cannot recover absolute sums
since revision creation. Include gaps before the first stored row, so a later
authorized backfill before that row still falls within the fixed coordinate.
The anchor is fixed and identified with its evidence boundary. Differences within
that coordinate are exact; a boundary before it requires a historical baseline,
never an invented zero population. All later checkpoints/floors use that anchor.

### Acceptance of an observation

In the acceptance transaction, after the row insert, the storage function:

1. increments `generation` and sets `ingestion_generation` on the row;
2. sets `cumulative_through = LEAST(cumulative_through, interval_start)`;
3. explicitly marks the accepted interval's dirty hour, preserving the existing
   aggregate generation, retry reset and range rules;
4. for every SLO whose current definition uses this revision, widens the extrema
   cursor's dirty range to cover `[hour(interval_start), hour(interval_start) +
   max(window, 6 h) + 1 h)`.

While the retained reader is deployed (F9-T02 and F9-T03), also perform the old
series-repair rewind and version increment in this transaction. Replacing the
triggers must preserve both their invalidation effects, including when the repair
cursor was already caught up. Remove only series-specific marking in F9-T04 after
all readers switch; dirty-hour marking remains for the exact fallback.

Duplicates and conflicts do none of this. There are no ingestion triggers.

### Cumulative worker

Claims revision rows whose `cumulative_through` is before their latest
`interval_start + cadence`, with the ADR-0034 lease and retry rules. Each batch
recomputes the cumulative columns for rows with `interval_start >= cumulative_through`
in ascending order, at most 50,000 rows per transaction, seeded from the last row
before `cumulative_through` or from the floor when none exists, then advances
`cumulative_through` to the last processed interval end and, when caught up, sets
`cumulative_generation = generation`. Claims/publication are fenced by source
generation and lease epoch: a concurrent insert that lowers the cursor cannot be
overwritten by an older batch acknowledgment. Statement and lock timeouts are those of
SPEC-0019. Freshness target: a new observation at the series tail is cumulative
within 2 s under the controlled workload.

### Window totals

First clip `[a,b)` to revision/SLO validity and the applicable evidence boundary,
aligned to the canonical cadence. For current reads the evidence boundary is the
published canonical cutoff. Historical reads use the preserved boundary below.
Let `T = cumulative_through`. A trusted `P(x)` is the cumulative vector of the last
row before `x`, with `x <= T`. With no predecessor, use the retained floor, never
zero unless that floor is zero. A boundary before the physical floor needs a
retained checkpoint or historical baseline; it cannot borrow the current floor.

- If `b <= T`, totals are `P(b) - P(a)`.
- If `a < T < b` and the unprocessed suffix has at most 7,200 rows, totals are
  `P(T) - P(a) + sum([T,b))`.
- If `T <= a` and `[a,b)` has at most 7,200 rows, sum `[a,b)` directly. Never
  consult `cum(a)` on an unprocessed row or add rows before `a`.
- Otherwise use the existing exact hourly-aggregate reader with aligned raw
  edges. Only dirty hours intersecting this window affect readiness; exact raw
  replacement of dirty contributions shares the 7,200-row read budget. Preserve
  its non-hour-cadence handling, bounded batch queries and current-first admission.

`T` is maintenance progress, not a global read-availability fence. In particular,
an observation 100 days back must not block an unaffected current 1 h/28 d window
while cumulative repair traverses the intervening history. Only actual missing
dependencies after these paths may be pending during explicit repair/import;
ordinary navigation and current publication retain SPEC-0019's budgets.

First observation is the first observed interval start in the eligible window;
last observation is the last observed interval **end**. Latest receipt keeps the
canonical evaluator's maximum receipt watermark across observed/known-empty
contributions. Use indexed temporal lookups, exact hourly receipt summaries and
bounded edges over canonical/prefix evidence. First/last observation are null
exactly when observed count is zero; receipt can exist for known-empty coverage.
Feed these fields and exact counts to `slo.EvaluateOperationalTotals`;
quality/freshness rules stay. Preserve the existing watermark in upgrade baselines
rather than claiming to recover a different receipt from already-expired facts.

Counterexamples required in AC-01: `T=12:00`, unprocessed rows at 12:00 and
12:01, window `[12:01,12:02)`; a window straddling `T`; and a nonzero retained
floor followed by a gap before the first retained row. All must return the exact
window population without reading null/stale cumulative columns.

### Extrema rows

Table `almanac_slo_extrema`, key `(slo_id, definition_revision, measurement_revision,
exclusion_revision, calculation_version, resolution_seconds, range_start)` with
`resolution_seconds IN (3600, 86400)`, `source_generation`, `published_at` and one
`marks` jsonb column:

Each of `window`, `burn_1h`, `burn_6h` and `attainment` stores first/last/min/max
marks, distinct sample/quality/freshness presence sets, and transition marks.
A mark contains:

| Field | Meaning |
| --- | --- |
| `offset_seconds` | Actual endpoint minus UTC `range_start`, an integer in `(0,resolution_seconds]`; the endpoint itself must be epoch-aligned to the source cadence. |
| `good`, `total` | Exact decimal sums for evaluator input, including zero for no observations; public nullability still comes from the evaluator. |
| `observed`, `known_empty`, `zero_traffic` | Exact interval counts. |
| `first_observation_at`, `last_observation_at` | First observed interval start and last observed interval end; both present exactly when `observed > 0`. |
| `latest_receipt_at` | Existing maximum receipt watermark, including known-empty contributions; preserved exactly even when the last observed interval has an older receipt. |
| `evidence_from` | Cadence-aligned retained-input boundary used to compute this evaluation, preserved when raw data expires. |

This is the complete sufficient input to `EvaluateOperationalTotals`, together
with the immutable definition, endpoint and window duration. Do not substitute
bucket-wide presence sets for an individual mark's temporal evidence.

An hourly bucket can have no cadence endpoint; store an explicit empty lane.
Cadences that do not divide an hour/day, and those exceeding an hour, remain
supported without shifting endpoints. A burn window not divisible by the cadence
has a null lane. Daily compliance stores both the UTC `day_end` and the actual
cadence-aligned evaluation endpoint, which may precede midnight.

For every quality/state/freshness transition, keep full marks on **both** sides,
deduplicated with extrema. Use the existing reducer's 1,500 selected-point limit,
not a limit on transition offsets alone. If those points cannot fit, keep extrema
and presence sets under ADR-0027. Daily rows reduce hourly candidates using the
same rules, including transitions between hours. Reconstructing selected samples
requires no extra canonical lookup merely to recover omitted mark fields.

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
then writes daily rows for days whose 24 hourly rows (including explicit empty
ones) exist and whose end has
passed, at most 24 hours per transaction. A pending window defers the batch
(temporary). `source_generation` of a row is the revision generation at build.
Dirty work invalidates only hours whose lane inputs contain the changed interval;
daily rows containing changed hourly rows are rebuilt as well. Historical repairs
use preserved prefixes below. The source and cursor generation fences must prevent
an older worker clearing newer dirty work. Target: each closed hour is built within
5 minutes for every SLO at the 1,000-SLO fixture; capacity remains unverified.

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
  or closed hours (28 days and shorter) come from extrema rows. Partial UTC days
  use their closed hourly rows; the open hour and
  non-aligned edges are evaluated live, at most 7,200 endpoints per read. Display
  buckets select first, last, minimum and maximum among their candidates, union
  the presence sets into `display_coverage`, and keep transitions while the series
  stays within 1,500 points. Sample values are recomputed from the mark's exact
  counts, temporal evidence and stored evidence boundary with the canonical
  evaluator; the API sample shape is unchanged. Retain bounded exact handling at
  recent publication boundaries; normal navigation cannot wait for an hour job.
- **Daily compliance**: from daily rows; the current day is unfinished as today.
- **Exact-value pages**: the detail payload is pinned for five minutes under a
  generation as today; its digest is computed at insertion and checked on read.
  No other read recomputes a digest.
- **Preparation and diagnostics**: `/preparation` lists dirty-hour aggregate work, the cumulative cursor,
  the extrema cursor and the current failure row with attempts, next attempt and
  reason; retry resets the addressed cursor. The one-minute consistency check and
  metrics classify SLOs as up to date, catching up, failing or stopped from the
  same rows.

### Workers and polling

Three loops remain (source cumulative/extrema, current, maintenance) but each
sleeps until the earliest `next_attempt_at` or cadence boundary it owns, including
the independently maintained dirty-hour fallback, capped at
one second, instead of polling every second with full scans.

### Retention

Canonical observation APIs keep the published 365-day cutoff even while physical
monthly partitions contain older rows. Before deletion, the cumulative worker must
be correct through the partition boundary, and the drop transaction fences against
newly accepted work. Copy the boundary vector into the revision floor and advance
`retained_from` atomically with removal. A null/stale cumulative floor postpones
the drop with a visible reason; it never manufactures a zero floor.

One floor cannot reproduce every historical window. Retain derived boundary
checkpoints in `almanac_sli_cumulative_prefixes`, keyed by SLI/revision/interval
start, before canonical rows are deleted. A checkpoint retains the five exact
cumulative fields, interval outcome, its receipt time for observed/known-empty
contributions, and the generation/evidence boundary. It has no producer credentials or
delivery provenance and is not exposed as canonical observations. Preserve an
anchor vector before the first checkpoint. Its partial observed index supports
the same first/last temporal lookups as live rows.

Keep checkpoints back to the earliest lower bound needed by any retained
historical evaluation of that revision, including supported burn windows and
immutable definitions, not merely current definitions. This is at most the
retained endpoint horizon plus the longest applicable lookback, clipped to
original eligibility. Query prefix differences and temporal evidence across
checkpoints and live rows. Keep the corresponding exact hourly temporal summaries
through this dependency horizon for receipt maxima; bounded edges use checkpoints.
They share one cumulative coordinate system, so a
window crossing the physical floor remains exact. Capture checkpoints from the
earliest canonical data already available during migration; do not wait until
its partition is dropped. Copies and pruning run in bounded maintenance batches.

Previously computed historical values keep their original `evidence_from`;
today's raw cutoff must not turn known history into expired/partial values. Data
unavailable when an evaluation was originally built remains explicitly expired.
Retain checkpoints while needed to reselect **any** endpoint after late data,
not just endpoints that happened to be old extrema. Prune only after no retained
evaluation can use them. Their size scales with cadence and lookback and must be
measured separately from extrema; no fixed 9 MB total is promised.

Migration cannot recover raw facts that expired before checkpoints existed.
For those windows, copy the existing retained point's per-lane exact totals,
temporal evidence, evidence boundary and source generation to
`almanac_slo_prefix_baselines`, keyed by SLO/revisions/lane/endpoint. Preserve
every cadence endpoint whose eligible input prefix cannot be reconstructed,
including non-extrema, and its original quality inputs. On repair, add only
newly accepted intervals inside that window after its baseline generation, as
`preserveRetainedPrefixes`/`ReadRetainedLateTotals` do today; then reselect extrema.
Before a canonical partition is dropped, fold all post-baseline deltas from its
snapshot into affected baselines and advance their generation conditionally;
otherwise a correction could disappear when its raw delta expires. Repeated
folds/restart must not count a delta twice. Preserve baseline receipt maxima.
Reuse bounded batched delta reads and existing read resource budgets. Do not
interpret absent baseline coverage as zero. This compatibility cache has no new
queue, expires with its endpoints and is charged to derived storage. Old tables
cannot be dropped unless every such endpoint has verified replacement evidence.

Delete extrema only when their range ends at/before the retained endpoint cutoff;
serve partially retained edge buckets with exact bounds. Derived generations and
pinned payloads keep their five-minute lifetime and existing byte accounting.
Retention previews and audit entries include prefix/baseline cost and stay required.

### Migration

F9-T02 creates observations/cursors/prefix checkpoints, copies coverage and values,
and replaces both ingestion triggers with their explicit effects. All legacy
readers, dirty hours, aggregate rebuilds and repair workers still run. F9-T03 adds
extrema and historical baselines; verify replacement evidence and both readers
at the same generation before switching detail reads. F9-T04 switches remaining
consumers and only then drops obsolete bulk points/tiles, repair, display blocks
and history jobs. Hourly aggregates, dirty hours and historical evidence remain.

Run under explicit upgrade semantics; bounded/resumable copy and validation precede
destructive cutover. Verify values, temporal evidence and provenance, not only row
counts. Existing transactional migrations cannot pretend to checkpoint progress
between partitions: use a durable staged copy before the final schema transaction
if needed. Keep old evidence until replacement verification succeeds.

Main already contains `0044_immediate_datasource_reassignment.sql`. Provisional
versions are T02 `0045`, T03 `0046`, T04 `0047`, optional T05 `0048`; recheck the
integrated migration tip before each task and keep the sequence contiguous. T05
uses the next free version if it runs independently before the main chain.

## Failure and recovery

ADR-0034 rules apply: temporary errors reschedule without counting; five failures
back off to five-minute then hourly retries; seven days stops the cursor and
marks it; new observations restart it; retries are audited. A cumulative cursor
that cannot advance is visible but cannot block windows served by the exact hourly
fallback. Only unresolved dependencies make an explicit repair window pending,
never successful. An extrema
cursor that is behind leaves later hours pending in graphs with the existing
`history_preparing` reason. Current status is never blocked by extrema work.

## Acceptance criteria

- AC-01: For every window in the SPEC-0019 focused and integrated fixtures, totals
  from cumulative lookups equal the canonical sweep over raw rows, including
  fractional and maximum counts, gaps, known-empty, zero traffic, validity and
  retention edges. Include all three cursor/boundary counterexamples above,
  nonzero floors, and windows entirely beyond the cursor with null/stale columns.
- AC-02: Detail samples for 7d, 90d, 1y, window and both burn windows on the
  seeded 20-SLO fleet are identical to those produced by the current retained
  path at the same generation, including the existing receipt watermark,
  for every lane, selected point, transition, freshness state and compliance day.
  Include 70 s and 7,200 s cadences, midnight and non-hour boundaries, and sparse
  windows with equal counts but different observation/receipt timestamps.
- AC-03: A late observation up to 365 days old changes exactly the windows that
  contain it, in both current status and graphs, within the applicable currentness
  budgets. A 100-day-old insertion leaves unaffected current windows usable before
  cumulative repair catches up, including alongside continuing live ingestion.
- AC-04: Ingestion of a duplicate or conflicting delivery changes no cumulative or
  extrema row and no cursor.
- AC-05: Each crash point (before and after cursor claim, cumulative chunk commit,
  extrema commit, acknowledgment) recovers without lost or duplicate work.
- AC-06: The preserving migration on a copy of the preview database keeps every
  observation, produces matching current statuses for all SLOs and leaves no old
  obsolete table after final cutover. Historical counts/quality and pinned values
  also match across expiry; missing prefix evidence prevents destructive cutover.
- AC-07: On identical 35/365-day fixtures, report all derived bytes (including
  indexes, hourly fallback, prefix checkpoints and migration baselines) and show a
  reduction from the measured pre-change total. Background statements per minute
  per SLO are below 20 while idle. The original <1 MB/SLO one-hour estimate is
  withdrawn; no unmeasured size threshold excludes required correctness evidence.
- AC-08: `make test-perf` at `PERF_DAYS=35` and `PERF_DAYS=365` passes the existing
  page budgets; the 1y detail read issues no more than 400 rows of extrema and
  7,200 live endpoints.
- AC-09: Diagnostics show cursor state, reasons and retry for failing work; the
  consistency check restarts stopped cursors on new observations.
- AC-10: Advance retention after building a complete 28 d window at an endpoint
  now 364 days old, expire its original raw prefix, then accept late data inside
  that window. All candidate endpoints, newly selected extrema, quality and exact
  tables equal a preserved independent oracle. Cover checkpoint and migration
  baseline paths, restart, partition gaps and intervals missing before migration.
  Advance retention again after the correction and confirm folding/pruning does
  not lose or double-count its delta or change the receipt watermark.
- AC-11: At both T02 and T03 boundaries, late ingestion into a completed old hour
  atomically marks legacy dirty hours and rewinds completed repair cursors. Old
  tiles cannot remain falsely ready; duplicate/conflicting deliveries mark nothing.

## Dependencies and open questions

- Acceptance of ADR-0040 and ADR-0041; ADR-0042 is independent.
- Keeping provenance on the observation row is assumed; moving it to receipts is
  deferred.
- Whether `snapshot_id` stays in the overview response depends on ADR-0042.
- These corrections remain proposed. Added prefix storage, retained hourly fallback
  and revised size criteria require acceptance with ADR-0040/0041 before T02.

## Verification evidence

Not verified yet.

## Change notes

- 2026-11-09: Drafted from the SLI/SLO efficiency review.
- 2026-11-09: Corrected cumulative bounds, temporal/transition evidence, expiry
  preservation, staged invalidation, unaffected-range availability, cadence offsets
  and migration sequencing after branch review. No implementation or acceptance claim.
