# ADR-0023: Separate SLO operational urgency from performance

- Status: accepted
- Created: 2026-10-29
- Accepted: 2026-10-29
- Approval: maintainer working-conversation go-ahead, "ok please start working", after the F3 tasks and this proposed policy were presented
- Related specs: [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md), [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: none

## Context

Operators need the reference's Needs attention, Watch and Healthy groups, budget
view, separate burn windows and aligned timelines. Existing performance and
quality fields cannot alone express operational urgency: incomplete data can have
useful observed arithmetic while full-window performance remains unknown.

slo-tracker's `internal/slomath/status.go` defaults warn below 20% budget or at
1 h burn >= 1x. Its list also uses a 50% watch threshold and partial-window check.
It labels budget exactly zero breached; Almanac instead defines attainment equal
to target as met. Copying the old status logic would change an accepted contract.
Its browser list also fetches status/history per row; duplicating that pattern
would make global grouping and bounded pagination difficult to trust.

## Decision

Use backend-owned, versioned operational urgency, independent of performance,
coverage, freshness and alert state. UI and CLI receive the same group and reasons;
the browser does not recalculate policy from rounded values.

Apply the following rules in order, using exact values:

| Group | Conditions |
| --- | --- |
| Needs attention | Stale observations; eligible no-data or expired history; complete-window violation; negative observed remaining budget (explicitly provisional when incomplete); observed remaining fraction < 0.20; or observed 1 h burn >= 1. |
| Watch | Otherwise incomplete/shortened eligibility, not yet eligible, zero traffic, unavailable short burn, observed remaining fraction < 0.50, or unavailable current evaluation. |
| Healthy | Otherwise fresh, complete, evaluable data; remaining fraction >= 0.50 and complete 1 h burn < 1. |

An unavailable read is an explicit error state, never persisted as a mathematical
result. If retained rows are shown after failure, mark them stale with their
evaluation time; do not relabel an old Healthy result as current.
Zero remaining budget is exhausted but is not a definitive violation at equality.
Show all reasons. Within Needs attention, choose the leading reason in this order:
stale observations, no data, expired history, complete-window violation, negative
observed budget, low observed budget, high observed 1 h burn. Within Watch use:
evaluation unavailable, not yet eligible, zero traffic, incomplete coverage/eligibility,
short burn unavailable, budget below half. Preserve secondary reasons rather than
hiding a known violation behind the leading freshness reason. A provisional
bad result can demand attention without changing full-window performance to violated.
These thresholds provide navigation triage, not paired-window notification rules.
No new notification or silence behavior is authorized by this decision.

### Windows and history

Calculate 1 h and 6 h burn independently from canonical counts and the same target;
never reuse the objective-window burn. Each window reports its actual bounds,
eligibility, quality, freshness and provisional reasons. An incompatible cadence
returns unavailable with a reason rather than silently changing the duration.
Shorter objective periods do not truncate these burn windows; measurement/SLO
validity and revision boundaries still apply under SPEC-0006.

For a timeline ending at each plotted instant, budget is the objective's rolling
evaluation, and burn is the selected 1 h or 6 h evaluation. Interval attainment
remains good/total over the displayed bucket, not an average of percentages.
Historical points use the definition effective at that time; pre-validity and
expired regions remain explicit. Current-summary, historical-series and daily
compliance fields share a consistent calculation snapshot and disclosed boundaries.

Define daily compliance as the rolling objective evaluated at each completed UTC
day's end, counting met/evaluable completed days and separately counting unknown
and not-applicable days. Today's partial day is not a completed compliant day.
Local display changes labels, not UTC compliance boundaries. This must remain
distinct from a day's standalone good/total ratio and from future calendar SLOs.

Return a bounded operational list projection with status, summary and sparkline
data. Apply search/filter/group/sort before pagination and disclose group totals
for the filtered result. Do not fan out a full detail evaluation for every browser
row. The exact endpoint, query budgets, cursor snapshot strategy and history
resolution contract must be reviewed in F3-T02 before implementation.
Keep at most 1,500 plotted points per series and exact values available through
bounded snapshot-consistent access. Downsampling cannot average rolling budgets
or bridge gaps; disclose buckets and preserve extrema under SPEC-0012.

### Exhaustion

Provide an explicitly labelled estimate: freeze the current allowed event budget
and assume the measured 1 h bad-event rate continues. Divide positive remaining
events by that rate; show exhausted at nonpositive remaining budget, and no finite
estimate for zero bad rate or unavailable/incomplete/stale inputs. Disclose that
rolling-window expiry and changes in traffic can alter the result. The backend
provides the estimate and assumptions; the browser never derives it from normalized
burn alone. This is an estimate, not a prediction of the actual rolling-window breach.

## Alternatives considered

- A browser-only grouping over per-row reads is closer to the old implementation,
  but allows policy drift, incomplete global grouping and excessive requests.
- Reusing full-window met/violated status is simpler, but loses triage and would
  make unknown coverage look like proof of failure or success.
- Fixed-duration exhaustion from normalized burn alone is cheap but unjustified
  for event-count budgets without traffic assumptions.

## Consequences

The reference's useful information becomes available without weakening Almanac's
exact arithmetic or missing-data semantics. Backend projection/history work and
API/CLI parity are required; this is not a CSS-only correction. Status cache design
is not selected by this decision. The exact wire and bounded query contracts require
coordinator review before implementation; visual requirements are confirmed in SPEC-0012.

## Revisit conditions

Revisit thresholds after operational evidence, and the projection design against
the accepted 100-SLO performance fixture. Calendar objectives, semantic revisions,
composites and maintenance must retain their own accepted evaluation contracts.
