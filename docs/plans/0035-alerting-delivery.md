# Alerting delivery plan

Goal: deliver the initial in-Almanac alerting outcome described by
[SPEC-0021](../specs/0021-alert-policy-engine.md),
[SPEC-0022](../specs/0022-alert-management-ui.md) and
[ADR-0051](../adr/0051-durable-alert-policy-state-machine.md).
The maintainer authorized continued implementation and validation through completion
on 2026-11-13. This includes resolving the calculation evidence gate; it does not
authorize merging, silent contract changes or unsupported performance claims.

The primary agent executes the plan using the executing-plans workflow. Use
dedicated task worktrees and verified dependency PRs. Preserve the selected A list,
card-based details and compact single-page policy creation. Destinations, notes,
assignment and acknowledgement expiry remain deferred as agreed.

## Review focus

- Pending calculations preserve ordered boundaries and open occurrences.
- Source/configuration/lease changes fence stale writes, including acknowledgements.
- Catch-up records every outcome or explicit gap; historical recovery cannot page.
- Backfill and evaluation retain ingestion and interactive navigation budgets.
- Evidence is exact, revisioned, durable and consistent between UI/API/CLI.

## Delivery stages

1. **Calculation gate (F4-T02, PR #95):** replace the oversized SQL backfill fixture
   with single-SLI transactions capped at 720 observations; retain the previous
   failed evidence. Enable the existing private metrics listener only in the test
   stack. Record an ingestion-only baseline and contention histograms, browser/server
   traces, incomplete work and process deadlines. Complete admission/refusal and
   physical-plan bounds checks. Fix measured defects and rerun only their affected
   cases. Select numerical limits/end-to-end target from the resulting evidence
   before adopting the shared-calculation amendment; keep exact semantic tests.
2. **Durable engine:** add migrations and canonical policy validation, exact paired
   condition evaluation, durable cursors/claims and fenced atomic evaluation,
   occurrence/evidence transitions. Add global acknowledgement, administrative
   cancellation, SLO archive/version protection, exclusions and bounded catch-up.
   Use deterministic time, separate bounded admission and real PostgreSQL tests
   for crashes, races, deferrals, gaps and independent policies sharing an SLO.
3. **Management operations:** extend OpenAPI source and regenerate bindings, expose
   authenticated policy/occurrence/evidence/health and acknowledgement operations,
   and equivalent CLI commands. Verify permissions, expected versions, request
   reconciliation, archived dependencies and failure behavior through real binaries.
4. **Selected UI:** use shared controls/charts for policy creation/editing,
   filterable/groupable clickable alert lists, global acknowledgement, history,
   evidence and lifecycle cards. Cover waiting/unavailable/reconstructed/admin
   outcomes with actual API data. Compare both themes at 360/1024/1440 px, including
   keyboard, touch, errors, exact counts and SLO navigation.
5. **Integrated acceptance:** run the declared 100/200-policy fixtures, including
   60-minute live operation, rollover/late data, missing-data cohort, handoff,
   acknowledgement, worker interruption/restart and 30-minute catch-up. Prove
   cursor continuity and current-work fairness while ingestion/browser budgets
   hold. Record complete timing distributions and pending ages, review the final
   delta, publish/update draft PRs and address authorized review/required CI.

Each stage receives concrete task records and focused commands before editing its
production code. Do not start the engine by bypassing a failed calculation gate;
resolve the evidence or document a justified alternative within the agreed design.
No stage is done merely because a draft PR is open.

### Unwired foundation sequence, 2026-11-14

After the authorized stack integration, the primary chooses a bounded staging
alternative under the maintainer's instruction to own implementation and work
structure: [F4-T29](../tasks/F4-T29.md) first proves exact decisions and lifecycle
reduction without any runtime, database or calculator adoption. Durable foundations
can then consume deterministic supplied evidence before production adapter work.
The failed calculation/capacity evidence remains failed. Calculator adoption,
production integration/activation and release still require their contracts and
acceptance; proposed architecture and public limits are not accepted by this
sequencing choice. The earlier gate must not be misreported as passed.

The maintainer explicitly confirmed this sequence: build the unwired state
machine and durable storage first; keep calculator adoption, production activation
and release behind the existing correctness/performance gates.
