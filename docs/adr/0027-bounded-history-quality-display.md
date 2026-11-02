# ADR-0027: Represent dense history quality within bounded graph responses

- Status: proposed
- Created: 2026-11-02
- Accepted: pending
- Approval: pending maintainer review; this proposal does not authorize implementation
- Related specs: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: proposes a limited amendment to [ADR-0025](0025-fast-operational-journeys-and-tracing.md)'s gap-display policy; exact calculation and retained-data decisions remain unchanged

## Context

An operational graph response permits at most 1,500 samples per series. Its
current contract preserves both sides of every fine-grained quality transition
and permits 422 `range_too_dense` when they cannot fit. One missing minute every
hour produces 8,760 separate gaps in a year: explicitly returning every boundary
cannot fit that bound, even when numerical values are otherwise constant.

The controlled ten-objective fixture reaches maintained 56-day history, then
fails long graph reads for the gap, empty-interval and zero-traffic cohorts.
The retained capacity report confirms HTTP 422 `range_too_dense`. Separately, nine concurrent
cold 28-day reads take approximately 0.96–1.24 seconds. This evidence establishes
failed availability/latency acceptance, not a capacity result or a proven fix.

The required outcome is useful, accurate graphs for supported retained ranges,
including dense missingness, without unbounded responses or hidden preparation.
A long graph cannot show thousands of individual minute-wide holes as separate
screen pixels. It must disclose the display resolution and retain exact detail
for a narrower range.

## Decision

**Proposal:** separate numerical extrema from coverage representation at the
display resolution. Do not change canonical observations, exact retained SLO
points, current calculations, urgency, burn windows or daily compliance.

1. Keep at most 1,500 numerical samples per series. Select actual first, last,
   minimum and maximum evaluations in each disclosed stable display bucket.
   Preserve their exact source counts, timestamps, qualification and values;
   never average budgets, burns or percentages to manufacture a plotted point.
2. Add at most 375 `coverage_buckets` per series, with explicit bounds, cadence,
   expected evaluation count and exact counts of the represented quality states.
   Freshness counts are separate because freshness and coverage can overlap.
   These are counts of endpoint qualifications, not event totals to use in SLO
   arithmetic. Combine only disjoint endpoint sets; handle boundaries explicitly.
3. If individual quality transitions fit, preserve them. Otherwise disclose mixed
   coverage at bucket resolution. Mark any bucket containing unavailable/missing
   evaluations conservatively and do not draw a continuous observed line through
   it. Do not imply a precise fine-grained gap location that the response omits.
   A narrower ordinary range request reveals the retained exact transitions.
   Empty, zero-traffic, partial, expired, excluded and stale states remain distinct.
4. Exact tables keep the actual represented evaluations and immutable generation
   binding. Their point values and quality cannot be changed to a bucket average
   or a synthetic bucket qualification. Coverage context is labelled separately.
5. A supported range cannot return `range_too_dense` solely because its quality
   transitions exceed the plotting limit. Actual service/resource failures remain
   explicit. Keep the existing memory, response-size, authorization and expiry
   limits; do not obtain acceptance by increasing them.

Retain compact quality summaries alongside the existing stable resolution
summaries. Revisions, late data and exclusions invalidate both atomically through
the existing maintenance fences. Unknown or unfinished regions stay unknown;
absence of a summary never establishes successful coverage. Reads assemble
available summaries and bounded edges without scheduling or polling graph jobs.

Preserve the current renderer, layout, navigation and controls. Reuse existing
coverage styling and accessible chart/table interactions. This proposal does not
activate [ADR-0026](0026-bounded-cold-operational-rendering.md).

### Contract amendments required if accepted

- SPEC-0012 and SPEC-0019: replace enumeration of every fine-grained quality
  boundary with the explicit bounded coverage representation above. Preserve
  exact arithmetic, conservative gaps, extrema, drill-down and all latency gates.
- OpenAPI operational detail/series: add the bounded coverage schema and replace
  the current 422 fallback for quality-transition density. Keep the 1,500-sample
  bound, exact-table generation contract and supported range/burn selectors.
- Chart and catalogue contracts: demonstrate mixed coverage, fine-range exact
  gaps, empty/zero traffic, stale/unknown states and keyboard/table access in both
  themes. Mixed regions must never look like proven continuous coverage.
- Maintenance/read contracts: specify the exact endpoint grid, half-open bucket
  membership, disjoint composition, validity/retention edges and coherent repair
  before implementation. Source-size and cold-read costs need measured evidence.

Acceptance includes a year of one-minute hourly gaps, alternating empty/zero
traffic, brief numerical peaks, near-equal unrounded values, non-hour edges and
late corrections. Independent expected counts/extrema must match. Both ordinary
and added-100-ms browser journeys, ten concurrent users and the fleet matrix
remain required; passing a reduced example does not establish capacity.

## Alternatives considered

- Keep rejecting dense ranges: preserves today's wire behavior but fails the
  requested supported-range availability.
- Increase point limits or return every minute: makes payload/render costs grow
  with retention and does not resolve finite screen resolution.
- Return a separate lossless gap mask or interval stream: preserves fine-grained
  locations in the coarse response, but needs a new bounded encoding, cadence/size
  admission and a renderer that does not create an element per transition. This
  is a stronger wire-level guarantee at greater implementation/storage cost;
  it should be selected instead if precise coarse-response gap locations are
  required. It does not by itself reduce numeric endpoint decoding costs.
- Drop transitions silently or connect observed points through omitted gaps:
  gives falsely continuous coverage and violates accuracy.
- Use bounded numerical extrema with explicit conservative coverage: preserves
  exact selected values and honest uncertainty while bounding display work.
  It requires a reviewed additive wire/display contract and maintenance changes.

## Consequences

At coarse resolution, a bucket can hide some valid line segments conservatively;
zooming restores finer locations. This limitation must be visible, not described
as exact minute-level gap rendering. Numerical/current SLO calculations remain
exact. Additional quality summaries have storage/write/migration costs that must
be measured. No speed or capacity claim follows from accepting this proposal.

## Revisit conditions

Revisit if measured payload, read, render or summary-write costs still miss the
accepted budgets, or operators cannot distinguish mixed coverage from exact gaps.
Do not weaken the latency gates or arithmetic to preserve this representation.
