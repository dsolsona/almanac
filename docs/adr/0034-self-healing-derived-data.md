# ADR-0034: Derived SLO data that keeps itself up to date

- Status: accepted
- Created: 2026-11-06
- Accepted: 2026-11-06
- Approval: on 2026-11-06 the maintainer asked that the backend "auto-heal itself: if the SLO processing fails, it should resume", with something running all the time "that ensures SLOs are up to date, all data is processed". The maintainer reviewed this record in Crit and asked for the current pipeline to be explained, for the goal to come before the mechanisms, and for failures to appear in traces.
- Related specs: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md)
- Supersedes / superseded by: amends the bounded-retry rule of [ADR-0024](0024-precomputed-operational-reads.md) and [ADR-0025](0025-fast-operational-journeys-and-tracing.md). Work that fails repeatedly no longer waits for a manual retry.

## Context

### How SLO data is collected and calculated today

**1) Collection.** An agent runs each SLI's good and total queries once per cadence interval, for
example every 60 seconds, and sends the result. The backend stores it in one transaction:
- **a)** an *interval record* (`almanac_sli_coverage`): the interval's start and end, its outcome
  (observed, known empty, failed or incomplete) and its provenance (agent, lease, delivery, receipt
  time);
- **b)** for observed intervals only, the interval's *values* (`almanac_sli_observation_values`): the
  good and total counts.

These raw observations are the source of truth. They are kept for 365 days (SPEC-0007).

**2) Marking.** In the same transaction, a database trigger on the interval record:
- **a)** increments the SLI's *source generation*, a counter of accepted changes;
- **b)** marks the record's hour as dirty (`almanac_derived_dirty_ranges`).

**3) Hourly aggregates.** A background loop (`RunDerived`) takes dirty hours in batches of 100 and
rebuilds each one's sums of good, total and interval counts (`almanac_sli_hourly_aggregates`). A
30-day window then reads about 720 hourly rows instead of 43,200 one-minute rows. They are kept for
the same 365 days and can be rebuilt from the raw observations at any time.

**4) SLO calculations.** Every SLO value comes from the hourly aggregates plus the raw intervals at
the window's two edges:
- **a)** *Current values*: attainment, remaining budget and burn rates now
  (`almanac_slo_current_projections`). `RunDerived` refreshes them every second when the source
  generation has changed.
- **b)** *Retained series*: one point per cadence interval per SLO, for up to a year
  (`almanac_slo_series_points`), plus compact hourly tiles for charts (`almanac_slo_series_tiles`).
  New points are added as current values are published. A *repair* job builds the earlier history
  from the SLO's eligible start, for example when an SLO is created with a historical start. This
  runs in the `RunHistory` loop. The SLO detail page's charts read the points and tiles.
- **c)** *History blocks* (`almanac_slo_history_blocks`): the 7-day budget chart on the SLO list. On
  every turn of `RunHistory`, each SLO whose list chart is not ready gets a job
  (`almanac_slo_history_jobs`) to compute its last 7 days. The job writes the block and marks the
  chart ready. Jobs for the detail page's longer ranges still exist in the schema, but since ADR-0025
  only tests create them, because the detail page reads the retained series instead.

**5) Pages** read only these prepared results and never calculate on page load (ADR-0024,
ADR-0025). That is what keeps them fast.

In this record, **derived data** means everything in 3) and 4). It is computed from the raw
observations and can always be recomputed from them. Each derivation is a queue of *work items*,
such as a dirty hour, an SLO's current values, an SLO's repair or a history job. A loop claims an
item, computes it, publishes the result, and removes or advances the item.

**Scale.** At a 60-second cadence, one SLI keeps 525,600 interval records and as many value rows
per year. Each SLO keeps 525,600 retained points, plus about 8,760 hourly aggregates for its SLI.

### Why the pipeline looks like this, and what is worth changing

- **Start and end.** A source returns counts *over* an interval, for example the increase in requests
  from 12:00 to 12:01, not a reading *at* a moment. The record therefore stores the half-open
  interval it covers, `[start, end)` (SPEC-0003, ADR-0005), and calculations never split it. With a
  fixed cadence, the end is always the start plus the cadence, so storing it is redundant: the
  calculation already checks that rule.
- **Two tables.** The split keeps the counts out of rows that have none: failed, incomplete and known
  empty intervals have provenance but no values. It also lets the database enforce
  `good <= total` on value rows only. The cost is a second write, a join in every aggregation, and the
  very inconsistency behind this incident: a record without its values. One table with nullable
  counts, and a check that counts exist exactly when the outcome is observed, would make that state
  impossible.
- **Triggers.** ADR-0024 requires that accepting an observation advances the source generation and
  marks its hour in the same transaction. A trigger guarantees that for every write path, including
  the ingestion API, imports and test fixtures. The cost is hidden logic, and a rule that fires only
  for the table it is attached to, which is why writing the values never re-marked the hour. Doing it
  explicitly in the one storage function that accepts observations would be easier to read and test.
- **Retention.** The 365 days of raw observations were chosen on 2026-10-31 to avoid lossy
  aggregation in the first implementation (SPEC-0007). The hourly aggregates and retained points
  are caches with the same horizon.

These are design choices with migrations attached, so this record does not change them. They are
listed under **Open questions** for the maintainer to decide.

### What went wrong

Each work item gets five attempts. Any failure the code does not recognise as "waiting for something else" uses one attempt. After the fifth, the item is never claimed again, and only a manual retry in the diagnostics view brings it back. Failures stored a fixed reason (`retained_series_repair_failed`, `history_preparation_failed`), and their log lines and trace spans carried no error. Each queue recognised a different set of temporary database errors.

The performance suite (ADR-0033) seeds 35 days of history for 20 SLOs. On 2026-11-05 and 2026-11-06, all 20 repairs stopped after five attempts, and some history jobs did too. Their SLOs were left without long history.

**Root cause.** With the error logged and the consistency checks named, both jobs fail with `slo: inconsistent operational facts: zero total but not every observed interval is zero traffic`.
- **What the aggregator stored:** hours whose intervals counted as observed but whose totals were zero.
- **How that happened:** the seed wrote each interval's record and its values in separate commits, and the aggregator ran in between. Ingestion writes both in one transaction, so production does not produce this state.
- **Why it stayed wrong:** only inserting the record marks the hour, so writing the values later never rebuilt it.
- **What it cost:** every calculation that read those hours failed until its work stopped for good.

The trigger was invalid test data. The defect is that the backend turned a passing inconsistency into permanent damage, and nobody could see why.

## Goal

Every SLO's derived data matches its raw observations, without an operator having to act.

- **Freshness:** new observations reach the hourly aggregates, the current values and the retained series within a few cadence intervals under normal load.
- **Recovery:** a failure is retried until it succeeds. No derivation stops for good because of a temporary or repairable problem.
- **Consistency:** derived data that contradicts the raw observations is never published. If such data is found later, it is rebuilt from the raw observations.
- **Completeness:** derived work that should exist but does not, such as lost or never-created work, is detected and created.
- **Visibility:** at any moment, each SLO is either up to date, catching up, or failing with a stated reason. That state shows in the UI's existing "unavailable" labels, in metrics, in logs and in traces.

## Decision

1. **Retries continue, with a hard limit.**
   - A failed work item is retried after 2, 4, 8 and 16 seconds.
   - After the fifth failure in a row, it is retried every 5 minutes for 24 hours, then every hour.
   - While it keeps failing, its SLO shows the existing "unavailable" label, as today, and the
     diagnostics view lists the item with its reason.
   - After 7 days of continuous failure, it stops and is marked *failed: needs attention* in the
     diagnostics view and the metrics. A week of the same error means a bug or a broken definition,
     not a passing problem.
   - A stopped item starts again by itself when anything it depends on changes: new observations
     for its SLI, a new definition or measurement revision, or a rebuilt aggregate. "Retry now" in
     the diagnostics view also restarts it.
   - The first success clears all of this.
   - Work for data outside the 365-day retention, or for a definition that has since changed, is
     still dropped. That data should not be computed.
2. **Temporary errors are not failures.** One rule, shared by every queue, decides what is temporary:
   - waiting for another derivation, such as an hour that is not aggregated yet;
   - database serialization conflicts (`40001`) and deadlocks (`40P01`);
   - lock timeouts (`55P03`) and cancelled statements (`57014`);
   - the worker's own time limit running out.

   A temporary error reschedules the item within seconds and does not count towards the five failures.
3. **Inconsistent data is not published, and is repaired if found.**
   - The aggregator does not store an hour that has observed intervals without values. The hour stays queued and is retried a few seconds later, so it is aggregated once the values are committed. This fixes the root cause.
   - If a calculation still finds aggregates that contradict themselves, for example data stored before this fix, it marks the hours involved for rebuilding and retries.
4. **A periodic consistency check covers what the queues cannot.** Once a minute, the backend checks every active SLO:
   - its current values come from the latest source generation;
   - its retained series reaches from its eligible start to now;
   - its overview history matches the published values.

   Any missing work is created. The same check counts the SLOs that are up to date, catching up or failing, and the age of the oldest unprocessed hour. These go to metrics and to one log line per minute while anything is behind. This is how a stuck backend becomes visible without opening the database.
5. **Every failure carries its cause.**
   - Log lines include the error: the database error code and message, or the name of the consistency check that failed.
   - Trace spans for derived work record the error as well. The span status, the error description and a recorded exception event carry the same text, so a failing item can be found and diagnosed in VictoriaTraces.
   - Stored failure reasons record the class of error instead of a fixed string.

## Open questions

For the maintainer to decide. Each would change the storage schema, so each needs its own record:

1. **One table for intervals and their counts**, with nullable counts and a check that they exist
   exactly when the outcome is observed. This removes a join from every aggregation and makes a
   record without its values impossible.
2. **Explicit marking instead of triggers.** The storage function that accepts observations
   advances the source generation and marks dirty hours itself, in the same transaction.
3. **Drop the stored interval end**, since the start and the cadence determine it.
4. **Remove the unused detail-range history jobs** from the schema and code.
5. **Tiered retention.** Keep raw observations only for the longest SLO window in use, or a fixed
   cap such as 30 days, and keep only aggregated history beyond it: daily performance for a year
   and monthly summaries. Users need full detail across the SLO window, daily performance over the
   past year and monthly trends. They do not need minute-level charts from ten months ago. At a 60 s
   cadence this keeps about 43,200 raw rows per SLI instead of 525,600. It also limits windows to
   that cap and changes what older history can show, which SPEC-0007 must decide.

## Alternatives considered

- **Raise the attempt limit.** Rejected: it delays the same dead end.
- **Keep manual retry as the recovery path.** Rejected: a self-hosted monitoring product cannot depend on an operator noticing a diagnostics page.
- **Recompute all derived data on a schedule.** Rejected: it spends the same effort on healthy SLOs as on broken ones, and competes with live collection. The consistency check only creates work where something is missing.
- **Recompute on page load when prepared data is missing.** Rejected by ADR-0024 and ADR-0025: pages must stay fast and never wait for preparation.

## Consequences

- **Bounded cost:** a persistent fault costs one retry every five minutes per item until it is fixed, and the metrics and diagnostics show it.
- **Retention:** the 365-day rule and definition changes still drop work, as before.
- **Tests:**
  - An integration test reproduces the original failure: interval records, aggregation, then values, then an SLO with a historical start. It checks that the history is built correctly.
  - Further tests cover retries after the fifth failure and work created by the consistency check.
- **The performance suite** stays a page-speed test. Its seed writes each day's records and values in one transaction, as ingestion does.

## Revisit conditions

- Revisit if failing work measurably competes with live collection.
- Revisit if a class of error is found that genuinely needs an operator.
- Revisit if the one-minute consistency check becomes expensive at large fleet sizes. It could then check a rotating slice of SLOs per minute.
