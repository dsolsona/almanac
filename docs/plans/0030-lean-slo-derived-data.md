# Lean SLO derived data implementation plan

Implement with one primary agent per task; tasks 1, 5 and the baseline of task 6 can
run concurrently with the main chain in separate worktrees.

**Goal:** keep every accepted SLO behavior and budget while reducing measured
derived storage and removing bulk point, tile, repair and history-job machinery.
Keep exact hourly fallback/dirty work and historical evidence for late corrections.
The original 9 MB/SLO-year estimate excluded those costs and is withdrawn.
**Architecture:** [ADR-0040](../adr/0040-cumulative-sums-and-compact-extrema.md),
[ADR-0041](../adr/0041-one-observation-table-with-cumulative-sums.md), optional
[ADR-0042](../adr/0042-keyset-overview-pagination.md).
**Spec:** [SPEC-0020](../specs/0020-cumulative-slo-calculation.md) and the
accepted contracts it amends.
**Base:** `ea501bc` on `main`; design candidate `a8f457d` before these revisions.

## Constraints and review focus

- Nothing in tasks 2 to 5 starts before the maintainer accepts the governing ADR
  and spec. Task 1 is routine work inside accepted contracts.
- Every step leaves the application working end to end. Canonical observations
  have one write path; legacy derived marking/readers remain through T03 and
  switch off in T04 only after replacement evidence and all consumers are verified.
- Keep temporal fields, both sides of retained transitions, arbitrary-cadence
  endpoints and historical input prefixes. A global cumulative cursor cannot be
  the only readiness path for unaffected current windows.
- Provisional migrations T02/T03/T04/T05 are 0045/0046/0047/0048 because main
  already has 0044. Recheck the integrated tip; independent T05 takes the next
  free contiguous version. Later tasks renumber without rewriting applied SQL.
- Exact equivalence with the canonical evaluator is checked before any old path
  is removed (SPEC-0020 AC-01, AC-02).
- The migration is preserving and resumable; rehearse it on a copy of the preview
  data before touching the maintainer's installation.
- Follow [command discipline](../commands.md); `make check` and `make test-e2e`
  at each task boundary; `make test-perf` where the task says so.

## Dependency order

```
T01 quick wins ─────────────────────────────┐
T06 baseline measurement ─┐                 │
T02 observation table ────┴─► T03 extrema ─► T04 switch and delete ─► T06 after measurement
T05 keyset pagination (only if ADR-0042 accepted) ─────────────────────┘
```

## Task 1: Quick wins inside accepted contracts ([F9-T01](../tasks/F9-T01.md))

- [ ] Defer hourly tile work to the end of its hour in the two point-publication
  queries; verify the open hour is still served from points.
- [ ] Delete production-dead code: the no-runtime read branch, raw-fact overview
  candidates, rolling/attainment/compliance sweep builders, the display-block cache
  and detail-kind history jobs, with their tests. Keep `WindowSweep` and the
  operational-sample helpers the retained reader still uses.
- [ ] Run `make test`, `make test-integration`, `make test-e2e`.

## Task 2: One observation table with cumulative sums ([F9-T02](../tasks/F9-T02.md))

- [ ] Migration `0045`: new canonical table, cursors, floors and shared prefix
  checkpoints; bounded preserving copy. Verify values/provenance and explicit
  replacements for both triggers before dropping coverage/values.
- [ ] Explicit dirty-hour and legacy repair invalidation; fenced cumulative worker;
  trusted-prefix/suffix cases and independent exact hourly fallback. Keep existing
  aggregate, point/tile and repair readers/workers operational.
- [ ] AC-01 boundary/floor equivalence, AC-03 unaffected current reads during
  distant repair, AC-05 crash/concurrent-ingestion fences, AC-11 intermediate
  invalidation; AC-06 migration rehearsal and retention checkpoints.

## Task 3: Extrema rows and the new detail read path ([F9-T03](../tasks/F9-T03.md))

- [ ] Migration `0046`: extrema and finite upgrade prefix baselines; copy every
  endpoint whose old raw prefix is unavailable, not just old extrema.
- [ ] Complete mark/transition inputs, elapsed-second offsets, hourly/daily worker;
  keep legacy repair active until T04. AC-10 covers expired prefixes and late-data
  reselection of previously unselected endpoints.
- [ ] New reader composing graphs, compliance and exact-value payloads from extrema
  rows and live edges; switch the detail endpoint when AC-02 holds on the seeded fleet.
- [ ] `make test-perf` at 35 and 365 days.

## Task 4: Current status from cumulative sums and removal of the old pipeline ([F9-T04](../tasks/F9-T04.md))

- [ ] Current refresh, list sparkline, diagnostics and consistency check on the
  cumulative/extrema cursors plus exact hourly fallback; due-time wake;
  digest recompute removed.
- [ ] Migration `0047` drops bulk points, tiles, tile work, repair, display blocks
  and history jobs after verified prefix replacement and consumer switch. Keep
  hourly aggregates, dirty hours, prefix evidence and pinned cache byte accounting.
- [ ] `make check`, `make test-e2e`, `make test-perf`; AC-03, AC-04, AC-07,
  AC-09, AC-10 and AC-11, including current reads during distant cumulative repair.

## Task 5: Keyset overview pagination ([F9-T05](../tasks/F9-T05.md), only if ADR-0042 is accepted)

- [ ] Page and count queries, cursor encoding, API contract review for `snapshot_id`.
- [ ] Provisional migration `0048` (next contiguous version if independent)
  removes only superseded overview snapshot structures/admission; pinned detail
  payloads retain their existing lifetime, integrity and byte limits.

## Task 6: Before/after evidence ([F9-T06](../tasks/F9-T06.md))

- [ ] Before task 2: record canonical and derived bytes, statements per minute and
  page timings on the seeded fleet at `PERF_DAYS=35`.
- [ ] Include fallback indexes, prefix checkpoints, upgrade baselines and dense
  transitions in storage/cost reporting; measure current reads during old repair.
- [ ] After task 4: repeat, add `PERF_DAYS=365`, and record the results in SPEC-0020
  and status. This supplies the measurements the review could not take.

## Proposal status

The maintainer requested corrections to the seven review findings on 2026-11-09.
The revised ADRs/spec remain proposed/draft. This authorizes document revision,
not acceptance or application implementation. No task is marked done.
