# Maintenance-exclusion calculation implementation plan

Use the executing-plans workflow inline; no further delegation. The task record
is the execution ledger. The primary handles authorized integration/publication;
the delegated implementation does not publish independently.

**Goal:** implement the deterministic occurrence-calculation foundation required
by [SPEC-0016](../specs/0016-maintenance-exclusions.md) and
[ADR-0014](../adr/0014-maintenance-exclusion-semantics.md).

**Architecture:** carry a caller-scoped exclusion mask in `WindowInput`, union it
once, omit covered observations in the canonical sweep, and use the same mask for
expected coverage and freshness in the totals consumer. Retain exact rational
arithmetic and immutable source inputs. This is [F4-T05](../tasks/F4-T05.md), not
production maintenance support or adoption of the alert fallback experiment.

**Technology:** Go 1.27.3, standard library, existing `internal/slo` package.

## Constraints and alternatives

The primary selected extending the existing raw/totals calculation contract.
A separate adjusted calculator would duplicate canonical quality rules; filtering
in storage alone cannot subtract missing excluded intervals from expected
coverage. Neither alternative meets the shared-calculator requirement as directly.

- Bounds are half-open, whole cadence-aligned UTC instants; reject rather than round.
- No per-tick expansion for missing intervals or long exclusion spans.
- Masks supplied by later storage callers are SLO-scoped and revision-consistent.
- Raw facts, input masks and caller-owned rationals remain unchanged.
- With no mask, existing output and behavior remain unchanged.
- Full exclusion has null performance/budget/burn, distinct from zero traffic.
- A requested span before validity has no eligible population to exclude and keeps
  the existing `not_applicable` result. Alert full-window readiness remains a
  separate consumer check; maintenance does not shorten that requirement.
- Known-empty is unknown event population, not proof of zero excluded events.
- No persistence, API, UI, worker, production caller or incident state changes.
- Modern Go guidance and tests wait for release of the shared verification lane.

## Review focus

- Overlaps, duplicates and adjacency cannot subtract a cadence interval twice.
- Missing/expired excluded populations cannot yield a fabricated zero event total.
- Trailing maintenance cannot conceal an older missing eligible interval.
- Totals cannot claim more observed/empty intervals than the relevant masked population.
- Rolling advancement and shared source slices cannot mutate prior results or another scope.

## Step 1: Define and test canonical exclusion semantics

**Files:** create `internal/slo/exclusion.go` and
`internal/slo/exclusion_test.go`; extend `internal/slo/operational.go`.

**Interfaces:** `ExclusionInterval{Start, End time.Time}`;
`WindowInput.Exclusions []ExclusionInterval`; `WindowResult.Excluded int64`;
`WindowResult.ExcludedGood, ExcludedTotal *string`. Existing evaluator signatures
remain unchanged. Full exclusion uses state/reason `excluded`, performance
`unknown`, and no numerical performance values.

- [x] After guideline reading, add minimal interface declarations and the numerical
  `TestExclusionOccurrencePopulation` regression. Expected adjusted values are
  900/1000, 0.9 attainment, 100 bad, 10 allowance, -90 remaining, burn 10;
  unmasked attainment is 0.5. Excluded events are 100 good/1000 total.
- [x] Run `make shell CMD='go test ./internal/slo -run Exclusion -count=1 -v'`.
  Expected red: the declared mask does not yet remove maintenance observations.
- [x] Copy, validate, sort and union masks without mutating input. Count overlaps
  arithmetically using prefix durations/binary search; skip excluded observations
  in eligible arithmetic while retaining separate excluded statistics. Adjust
  expected/expired counts and freshness from that same union.
- [x] Run the same regression; expect the exact independent values above.
- [x] Add table tests for overlaps/duplicates/adjacency, masks outside the window,
  clipped validity/retention, unknown excluded event totals, missing inside/outside,
  trailing maintenance freshness, zero traffic, all-excluded, zero pre-validity
  eligibility and invalid alignment.
  Observe relevant failures before their corrections.
- [x] Add rolling-sweep and two-scope tests checking input masks/observations and
  previous result snapshots are unchanged. Add cancellation coverage for mask work.

## Step 2: Consume exact masked totals consistently

**Files:** extend `internal/slo/aggregate.go`, add
`internal/slo/exclusion_aggregate_test.go`, extend shared types in `operational.go`.

**Interfaces:** `ExclusionTotals{Good, Total *big.Rat; Observed, KnownEmpty int64}`;
`WindowTotals.Excluded ExclusionTotals`. Existing ordinary totals describe only
non-excluded retained eligible intervals; excluded statistics describe only their
excluded counterparts. Counts and observation metadata cannot prove the upstream
SQL's correctness: later storage integration must independently verify selection
against canonical facts and fence a consistent exclusion/source revision.

- [x] Add independent aggregate fixtures for the 900/1000 example, overlapping
  masks, partial excluded observations, known-empty excluded outcomes, wholly
  excluded windows and trailing maintenance. Compare both paths with expected
  numerical values and quality, not only with each other.
- [x] Run `make shell CMD='go test ./internal/slo -run Exclusion -count=1 -v'`;
  preserve the failing aggregate assertions before implementation.
- [x] Validate nonnegative exact counts, good <= total, paired optional excluded
  counts, excluded observed/empty count limits, and eligible first/last metadata.
  Reject excluded statistics without a matching effective mask. Publish excluded
  event sums only when every excluded eligible interval has observed exact counts.
- [x] Run the focused command again; require all named assertions to pass.

## Step 3: Verify compatibility and hand off

- [x] Run `make shell CMD='go test ./internal/slo -count=1 -v'` to cover shared sweep,
  aggregate, occurrence and urgency behavior. This is the bounded shared-impact
  check; do not run unrelated application suites or start a deployment.
- [x] Run `make shell CMD='make generate GENERATE_MODE=format'`; inspect scope.
- [x] Inspect the final diff for aliasing, arithmetic overflow/bounds, cancellation,
  no-mask changes and stale metadata. Record exact commands/results/durations in
  F4-T05, leaving integration and feature completion pending primary acceptance.
- [x] Provide the primary the diff and future storage obligations: resolve explicit
  SLO/revision mask, validate all effective grids before activation, select only
  eligible whole intervals, separately aggregate excluded observations, retain
  revision provenance, invalidate/fence derived publication, and implement the
  excluded wire/display/alert lifecycle contracts before any production caller.
