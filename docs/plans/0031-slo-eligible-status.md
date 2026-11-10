# SLO eligible status implementation plan

**Status:** Complete. The maintainer merged PR #82 in stack 74 on 2026-11-10;
`main` at `89b9eb2` has the verified `10e5723` file tree. [F3-T12](../tasks/F3-T12.md)
is done. Separate lean-SLO acceptance work remains in [F9-T06](../tasks/F9-T06.md).

**Goal:** A fresh SLO with complete due measurements can be Healthy without waiting
for its full rolling window, while actual collection gaps remain visible.

**Architecture:** Keep the existing exact evaluator and retained historical points.
Apply a clock-derived due boundary to live current calculations and remove
shortened-history warnings from operational triage. Preserve snapshot publication
time separately from actual measurement-window bounds.

**Tech stack:** Existing Go 1.27.3/PostgreSQL/React implementation, no dependencies.

**Spec:** [ADR-0043](../adr/0043-slo-status-from-due-eligible-measurements.md),
[SPEC-0006](../specs/0006-slo-calculation-and-reporting.md),
[SPEC-0012](../specs/0012-user-interface.md).

## Global constraints

- One primary agent, dedicated `.worktrees/slo-eligible-status`, no operator-data edits.
- No shell redirects, new command wrappers, auth bypass or relaxed checks.
- Explicit historical endpoints and daily compliance keep their existing contracts.
- Missing, stale, expired and zero-traffic observations never establish success.
- Verified stacked PR against `fix/slo-expired-history`; the maintainer merged it.

## Review focus

- Exact deadline boundaries and deterministic dispatch phase must not count pending intervals as missing.
- Advancing the clock without collection must reveal gaps instead of freezing Healthy.
- Recent SLO activation and historical opt-in must preserve their distinct eligible starts.
- Current/list/detail/CLI values and cache generations must remain coherent.
- Historical series, shortened daily compliance, low-budget and provisional-burn rules must not silently change.

## Task 1: Due current calculations and operational policy

Files: `internal/sli/observations.go`, `internal/sli/definition_test.go`,
`internal/slo/urgency.go`, `internal/slo/urgency_test.go`,
`internal/storage/slo_operational.go`, `internal/storage/slo_evaluation.go`,
storage unit/integration tests.

- [x] Add failing deterministic deadline, shortened-eligibility, waiting and real-gap tests; run `make test` and capture the failures.
- [x] Add `sli.LatestDueEnd(now time.Time, id string, cadenceSeconds, settlingSeconds int) time.Time` using the accepted fixed allowance. Apply it only to live current reads; preserve explicit historical `at` behavior.
- [x] Evaluate current objective/1h/6h windows at the same due endpoint, disclose their bounds and retain current snapshot/generation time. Compare freshness to actual request time. Remove shortened-history-only Watch/burn unavailability; keep negative-budget and real-gap qualifications.
- Already received complete intervals may advance that endpoint. Read only the bounded tail after the fixed clock deadline, using the canonical observation table; this prevents both a frozen outage horizon and withholding data that has already arrived.
- [x] Add PostgreSQL regression coverage for nine 99/100 observations: 98% gives 50% remaining and 0.5x burn, with complete eligible coverage; a later missing interval remains partial and stopped collection becomes stale. Compare default API/CLI arithmetic with explicit canonical evaluation at the disclosed cutoff.
- [x] Run `make test-integration`; diagnose every failure before another run.

## Task 2: User-visible explanation and delivery

Files: `web/src/slos/OperationalViews.tsx`, `web/tests/slo-operational.test.tsx`,
`web/e2e/collection.spec.ts`, accepted docs and task/status records.

- [x] Add failing status/copy tests and a real collection journey for a new 98% SLO. Assert Healthy and exact budget/burn without full-window history; cover Waiting, Partial data and Budget exhausted.
- [x] Show evaluation start, actual cutoff and observed/expected counts in the existing detail status area. Preserve retained layout and single-chip lists.
- [x] Run `make check`, then `make test-e2e`, then `make test-perf PERF_DAYS=35` sequentially. Record exact results, durations and gaps; do not touch the maintainer's running deployment.
- [x] Review the whole branch and fix confirmed findings.

Delivery completed: [PR #82](https://github.com/dsolsona/almanac/pull/82) was
published against `fix/slo-expired-history`, verified as position 10 in native
stack 74, and merged by the maintainer. All four required CI checks passed on
`10e5723`. Publication and published-head CI evidence are recorded on the PR;
local evidence is in
[F3-T12](../tasks/F3-T12.md).

Execution is inline under the repository's single-primary workflow. The maintainer
approved the described policy in conversation; no repeated approval is required.
