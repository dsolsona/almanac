# 0047 · SLI history and recovery

Status: fixture in review. Manual and automatic recovery are pending.
Base revision `8649d32`.

Deliver a local synthetic history command, operator-requested historical collection,
and automatic recovery after the backend and assigned agent reconnect. Deliver in
that order. SPEC-0008 and ADR-0003 govern manual collection and automatic repair.
SPEC-0005 governs historical measurement and evaluation validity.

## Local fixture

[Fixture delivery and verification](../tasks/F4-T63.md).

Take an existing demo SLI identifier and one of `1d`, `7d`, `14d`, and `30d`.
Accept constant event-count demo definitions and generate complete settled
intervals on the recorded collection grid. Preserve recorded observations and
report the requested range, inserted intervals, and enqueue linked SLO recalculation. Limit invocation to the local development stack.

The existing smoke seeder bulk inserts fixture history and marks affected derived
data. It advances the fixture SLI's historical validity before creating SLOs with
an explicit evaluation start. The existing-record command preserves both validity boundaries and reports the
eligible range alongside the requested range. A full retrospective demo requires
a separate explicit setup flow.

Exercise all duration presets, two linked SLOs, duplicate invocation, rejection
of ordinary SLIs, and failure before commit. At 60 seconds, a fully eligible
one-day range has 1,440 points before subtracting existing observations. An
unexcluded 99/100 population with a 99.5% objective consumes 200% of its budget.

## Manual historical collection

- [ ] Implement durable preview, admission, aligned range and revision planning,
  leases, cancellation, and progress in accordance with SPEC-0008 AC-01, AC-02,
  AC-05 and AC-06.
- [ ] Connect assigned-agent execution to separate historical slots and fenced
  submission. Mark derived data on accepted observations and verify late live
  arrivals and retried commits contribute at most once.
- [ ] Connect the SLI detail workflow and CLI after the API and agent path work
  together. Exercise Editor and Viewer access, range adjustment, progress,
  partial recovery and cancellation against the normal isolated deployment.
  Capture authenticated desktop light/dark and mobile evidence alongside the
  Playwright results. Preserve the retained SLI detail layout.

## Automatic recovery

- [ ] Admit overdue gaps through the same historical job path, with the delivery
  allowance, persistent scan cursors, and retry limits in SPEC-0008 AC-03 and
  AC-04. Preserve observed-empty coverage and concurrent live execution.
- [ ] Reproduce an outage with both processes stopped while source history is
  retained, restart, and verify repaired intervals and linked SLO results
  against independent counts. Verify a second restart preserves terminal retry
  state and a slow historical source leaves live collection on schedule.

Use [command discipline](../commands.md) and [testing](../testing.md) to select
focused tests, normal container journeys and required performance acceptance.
Keep incremental verification in task records and keep unverified stages open.

- [Historical validity](../specs/0005-openslo-and-resource-model.md)
- [Backfill and repair](../specs/0008-backfill-and-repair.md)
- [Historical isolation](../adr/0003-bounded-buffering-and-historical-isolation.md)
