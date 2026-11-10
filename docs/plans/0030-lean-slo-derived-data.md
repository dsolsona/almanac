# Lean SLO derived data implementation plan

Implement with one primary agent per task; tasks 1, 4, 5 and the baseline of
task 6 can run concurrently with the main chain in separate worktrees.

**Goal:** keep every accepted SLO behavior and budget while replacing per-cadence
points, tiles, summaries, the repair cursor and history jobs with compact extrema
rows over the existing exact hourly aggregates.
**Architecture:** [ADR-0040](../adr/0040-cumulative-sums-and-compact-extrema.md);
independent [ADR-0041](../adr/0041-one-observation-table-with-cumulative-sums.md);
optional [ADR-0042](../adr/0042-keyset-overview-pagination.md).
**Spec:** [SPEC-0020](../specs/0020-cumulative-slo-calculation.md) and the
accepted contracts it amends.
**Base:** `ea501bc` on `main`. On 2026-11-09 the maintainer delegated the design
decision to the primary agent. The maintainer subsequently selected `b7d8a5c`
as authoritative and authorized implementation of all tasks through stacked PRs.
Merge remains unauthorized.

## Constraints and review focus

- Tasks 2 and 3 start after ADR-0040 and SPEC-0020 are accepted; task 1 is
  routine work inside accepted contracts; tasks 4 and 5 wait for their own ADRs.
- Every step leaves the application working end to end. Old readers and workers
  run until the new reader proves equivalence (SPEC-0020 AC-01, AC-02); only
  then are old tables dropped.
- The hourly aggregates, dirty hours and batched window query are not modified.
- Migrations are preserving and resumable; rehearse on a copy of the preview
  data before touching the maintainer's installation. Provisional numbers follow
  main's 0044; recheck the integrated tip at each task.
- Follow [command discipline](../commands.md); `make check` and `make test-e2e`
  at each task boundary; `make test-perf` where the task says so.

## Dependency order

```
T01 quick wins ──────────────────────────────┐
T06 baseline measurement ─┐                  │
T02 extrema rows + detail reader ─► T03 switch and delete ─► T06 after measurement
T04 observation table merge (ADR-0041, independent) ────────┘
T05 keyset pagination (ADR-0042, independent) ──────────────┘
```

## Task 1: Quick wins inside accepted contracts ([F9-T01](../tasks/F9-T01.md))

- [ ] Defer hourly tile work to the end of its hour; verify the open hour is
  still served from points.
- [ ] Delete production-dead code: the no-runtime read branch, raw-fact overview
  candidates, sweep builders, the display-block cache and detail-kind history
  jobs. Keep the helpers the retained reader and current refresh use.
- [ ] `make test`, `make test-integration`, `make test-e2e`.

## Task 2: Extrema rows and the new detail read path ([F9-T02](../tasks/F9-T02.md))

- [ ] Migration `0045`: extrema table, cursor table, trigger retargeted to the cursor.
- [ ] Extrema worker on the batched query and the existing reducer; new reader
  composing graphs, compliance and pinned payloads from rows plus live edges.
- [ ] AC-01 and AC-02 equivalence against the retained reader; AC-03, AC-04,
  AC-05, AC-10; switch the detail endpoint; `make test-perf` at 35 and 365 days.

## Task 3: Current status, sparkline and removal of the old pipeline ([F9-T03](../tasks/F9-T03.md))

- [ ] Current refresh without point publication; sparkline from hourly rows;
  diagnostics and consistency check over dirty hours, extrema and current;
  due-time wake; digest recompute removed.
- [ ] Migration `0046` drops points, tiles, tile work, repair, display blocks,
  history jobs and the admission singleton; code and queries deleted.
- [ ] `make check`, `make test-e2e`, `make test-perf`; AC-06 to AC-09.

## Task 4: One observation table ([F9-T04](../tasks/F9-T04.md), after ADR-0041 is accepted)

- [ ] Migration (next free number): merged table, explicit marking, drop of the
  old tables, triggers and redundant indexes.
- [ ] Ingestion and every query rewritten; AC on duplicates, marking and rehearsal.

## Task 5: Keyset overview pagination ([F9-T05](../tasks/F9-T05.md), after ADR-0042 is accepted)

- [ ] Page and count queries, cursor encoding, API contract review for `snapshot_id`.
- [ ] Remove the snapshot tables, admission lock, fingerprint and byte budgets.

## Task 6: Before/after evidence ([F9-T06](../tasks/F9-T06.md))

- [ ] Before task 2: record canonical and derived bytes by table and index,
  statements per minute and page timings on the seeded fleet at `PERF_DAYS=35`.
- [ ] After task 3: repeat, add `PERF_DAYS=365`, include a late-data rebuild and
  a retention advance, and record the results in SPEC-0020 and status.
