# Eligible alert-calculation controls

- State: review; harness verified, baseline-only measurement failed; calculation adoption open
- Task: [F4-T13](../tasks/F4-T13.md)
- Governing contracts: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md),
  [SPEC-0020](../specs/0020-cumulative-slo-calculation.md), and
  [plan 0035](0035-alerting-delivery.md)
- Base: `58e2eb6`; authorized harness correction and baseline-only diagnostic measurement.

## Outcome and constraints

The corrected fresh fixture declares measurement and SLO eligibility for all three days
of later backfill before populating its initial one day. The previous fixture put
864,000 SQL-injected observations before both validity bounds; real ingestion
rejects such observations. Preserve those failed results as synthetic stress
evidence. Earlier validity also changes initial missing coverage, so identify this
fixture separately and do not claim an unchanged historical baseline.

Backfill still writes observations directly through the SQL fixture and copied
provenance. It does not exercise historical grants, the agent's backfill buffer,
or historical ingestion/admission priority. Live collection and live ingestion
remain real agent traffic. Correct eligibility does not establish acceptance of
the historical delivery protocol.

Keep 200 SLIs/SLOs, four windows, 800 batches, two query workers, the existing
250-ms pacing per worker, 1,400 backfill batches of at most 720 rows for one SLI,
ten API clients, twenty browser cycles, and every timeout and acceptance budget.
Production calculation, scheduling, admission and authority code are outside scope.

## Implementation

Use a small pure TypeScript harness module for validated mode selection, SQL
construction and eligibility checks. This keeps the same Playwright workload and
allows focused negative checks without starting a performance run. Do not duplicate
the workload into three independently maintained runners.

- `paired` remains the default: baseline, candidate and independent raw reference
  run in the same snapshot, with the existing alternating baseline/candidate order.
- `baseline-only` executes only the baseline query.
- `candidate-only` executes only the candidate query.

Select with `ALERT_CALCULATION_MODE`. Reject empty/unknown explicit values before
experiment setup. Profile and result files identify the mode, fixture version,
validity/history bounds, planned and completed query counts, and whether independent
equivalence was checked. Single-query modes retain timing, failures, live-ingestion,
due-interval and browser/API acceptance assertions; they make no equivalence claim.

Each mode requires a fresh seed and its own ingestion-only baseline. Setup must
reject a previously measured fixture before starting work. A control with the same
backfill/API/browser load helps attribute interference; it does not replace the
paired calculation check or relax any acceptance requirement.

## Files and steps

- [x] Add `web/tests/alert-calculation-experiment.test.tsx` cases for invalid modes,
  forbidden work in single modes, and backfill ranges before either validity bound.
- [x] Run the focused red selection after the shared runtime lane is released.
- [x] Add `web/perf/alert-calculation.experiment.ts`: validated mode, selected SQL
  work, expected call counts and exact half-open backfill-bound validation.
- [x] Update `web/perf/alert-calculation.seed.ts` to declare the full eligible range,
  create partitions covering it, populate only one initial day, and record a new
  fixture identity and bounds in the fleet manifest.
- [x] Update `web/perf/alert-calculation.perf.ts` to validate persisted bounds before
  work, execute only the selected mode, preserve all failures and report truthful
  mode-specific counts and equivalence status. Include the helper in source hashes.
- [x] Run focused unit/type/syntax and documentation checks; inspect the diff.
- [x] Return the frozen diff and measurement commands for primary review.
- [x] After review and runtime authorization, seed a fresh disk-backed fixture and
  run baseline-only once; preserve its failures and validate persisted eligibility.
- [ ] Candidate-only and corrected paired measurements, each requiring a fresh
  fixture and its own ingestion baseline, remain separate follow-up work.

## Verification

```sh
make shell CMD='cd web && npm test -- tests/alert-calculation-experiment.test.tsx'
make shell CMD='cd web && npm run typecheck'
make shell SOCKET=1 CMD='cd web && ./node_modules/.bin/playwright test --config playwright.alert-calculation.config.ts --list'
```

The list command validates Playwright collection without starting the application
or loading the fixture. Three explicitly authorized rollback-only PostgreSQL smoke
transactions verified actual generated SQL for all modes against retained fixture
data; unique function prefixes and a final absence check ensure cleanup. No capacity
claim comes from this syntax/call-selection check. The paired check
must retain independent exact values; single modes cannot be reported as passing
that check. Record all commands actually run and remaining gaps in F4-T13.

Equivalence evidence is computed and persisted before throwing worker or latency
failures. Its state is `matched`, `mismatched`, `incomplete`, or `not_checked`.
PostgreSQL decimal text is compared without floating-point conversion, normalizing
only trailing fractional zeros. Pending/missing windows remain incomplete.

## Recorded baseline-only outcome

Primary integration at `58e2eb6` retained all limits and anchored history to the
manifest rather than the first observation (minute 17 is intentionally absent).
Fourteen focused tests pass in 0.863 s and three-test Playwright collection passes.
The [sanitized control record](../evidence/alert-calculation/2026-11-13-baseline-only-control.json)
pins the measured source hashes and image. Seed passes in 249.071 s; the control
fails in 182.838 s. All 800 baseline calls complete, with zero candidate/reference
calls and equivalence `not_checked`; all 1,400 backfill batches and 400 due live
intervals complete. No persisted observation precedes either validity bound.

The baseline-only workload exceeds ingestion and browser budgets without the
proposed fallback: ingestion p95 rises from 12.4–13.0 ms to 62.0–65.1 ms, cold-list
render reaches 1,799.3 ms, and the browser stops after 27 journeys on auth-context
503. A failed `LockSessionUser` span lasts 256 ms; the exact blocker is unknown.
This establishes neither a causal fix nor capacity. Direct SQL historical writes
bypass real historical admission, the browser shares the server VM, and no gate
is waived. Calculation adoption remains open; F4-T13 records the full evidence
and remaining checks.
