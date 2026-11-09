# Lean SLO derived data implementation plan

Implement with one primary agent per task; tasks 1, 5 and the baseline of task 6 can
run concurrently with the main chain in separate worktrees.

**Goal:** keep every accepted SLO behavior and budget while cutting derived storage
to about 9 MB per SLO-year and removing the point, tile, repair and history queues.
**Architecture:** [ADR-0040](../adr/0040-cumulative-sums-and-compact-extrema.md),
[ADR-0041](../adr/0041-one-observation-table-with-cumulative-sums.md), optional
[ADR-0042](../adr/0042-keyset-overview-pagination.md).
**Spec:** [SPEC-0020](../specs/0020-cumulative-slo-calculation.md) and the
accepted contracts it amends.
**Base:** `259f7c1` on `main`.

## Constraints and review focus

- Nothing in tasks 2 to 5 starts before the maintainer accepts the governing ADR
  and spec. Task 1 is routine work inside accepted contracts.
- Every step leaves the application working end to end; no dual-write period
  longer than one task.
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

- [ ] Migration `0044`: new table, cursors, floor columns, preserving copy, drop of
  old tables and triggers after count verification.
- [ ] Explicit marking in the ingestion function; cumulative worker; window-totals
  helper; hourly-aggregate rebuild rewritten to read the new table so the existing
  readers keep working.
- [ ] Equivalence tests for AC-01 and crash tests for AC-05; migration rehearsal for AC-06.

## Task 3: Extrema rows and the new detail read path ([F9-T03](../tasks/F9-T03.md))

- [ ] Extrema table, cursor marking in ingestion, extrema worker.
- [ ] New reader composing graphs, compliance and exact-value payloads from extrema
  rows and live edges; switch the detail endpoint when AC-02 holds on the seeded fleet.
- [ ] `make test-perf` at 35 and 365 days.

## Task 4: Current status from cumulative sums and removal of the old pipeline ([F9-T04](../tasks/F9-T04.md))

- [ ] Current refresh, list sparkline, diagnostics and consistency check on the
  two cursors; due-time wake; digest recompute removed.
- [ ] Migration `0045` drops points, tiles, tile work, repair, hourly aggregates,
  dirty ranges, display blocks and history jobs; code and queries deleted.
- [ ] `make check`, `make test-e2e`, `make test-perf`; AC-03, AC-04, AC-07, AC-09.

## Task 5: Keyset overview pagination ([F9-T05](../tasks/F9-T05.md), only if ADR-0042 is accepted)

- [ ] Page and count queries, cursor encoding, API contract review for `snapshot_id`.
- [ ] Remove the snapshot tables, admission lock, fingerprint and byte budgets.

## Task 6: Before/after evidence ([F9-T06](../tasks/F9-T06.md))

- [ ] Before task 2: record canonical and derived bytes, statements per minute and
  page timings on the seeded fleet at `PERF_DAYS=35`.
- [ ] After task 4: repeat, add `PERF_DAYS=365`, and record the results in SPEC-0020
  and status. This supplies the measurements the review could not take.
