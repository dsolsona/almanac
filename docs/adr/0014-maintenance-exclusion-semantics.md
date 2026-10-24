# ADR-0014: Exclude maintenance from the evaluated population

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `0de0c0e42db6`; approved 2026-10-24 with no changes requested
- Related specs: [SPEC-0016](../specs/0016-maintenance-exclusions.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: replaces only [ADR-0005](0005-measurement-semantics.md)'s initial exclusion deferral; remaining measurement rules stay in force

## Context

Operators need maintenance periods to stop contributing events to the final SLO
calculation. Notification silences only stop delivery; they do not satisfy this
requirement. Counting maintenance traffic as successful would also answer the wrong
question: it would improve the result with events the operator meant to exclude.

If eligible traffic succeeds 900 times out of 1,000, its success rate is 90%.
Adding 1,000 maintenance requests as successes would raise it to 95% regardless of
how the service actually behaved during maintenance. Removing those requests from
both counts leaves the intended 90% result.

The earlier calculation design deferred this feature. This decision makes it an
initial-release feature while keeping raw observations immutable and explainable.

## Decision

Use versioned, SLO-scoped maintenance exclusions applied at evaluation time.
Subtract the union of excluded whole intervals from the evaluated population and
expected evaluation coverage. Do not stop collection, replace failures with success,
delete measurements, or stretch the report window to compensate.

Use one exclusion snapshot across counts, budget, burn, coverage and report metadata.
For composites apply the selected SLO's mask consistently to all child contributions.
Fully excluded windows have no evaluable result. Store immutable revisions and audit
records; edits invalidate caches and fence old worker evaluations. Reports retain
which revision they used. Original measurements remain available for inspection.

Allow future scheduling for Editors and audited retrospective changes for Admins.
Retroactive edits can legitimately change a newly generated historical report, so
show both the reason and changed exclusion snapshot. Never rewrite downloaded reports,
sent notifications or original query/target history. The role policy is defined
in SPEC-0016 and its accepted REVIEW-E01 record.

Notification silences remain separate operations. Graphs show maintenance as a shaded
interval, with a label explaining its mathematical effect. Informational annotations
have no calculation or notification effect, as specified separately in SPEC-0017.

## Alternatives considered

- Treat maintenance observations as successful: keeps a denominator but can inflate
  reliability and allowed failures with traffic outside the agreed population.
- Only mute notifications: useful operationally, but maintenance continues to spend
  budget and affect the final result.
- Delete source observations: makes exclusion irreversible and affects other SLOs
  sharing the same SLI.
- Allow arbitrary fractional intervals: convenient input, but requires inventing
  where requests occurred inside a collected interval.
- Allow unrestricted retrospective edits: less friction, but makes it too easy to
  change reported compliance without a clear authorization and evidence trail.

## Consequences

Reports can show raw service behavior and maintenance-adjusted compliance together.
Smaller eligible populations can make estimates noisier; never hide the denominator
or excluded duration. Versioned exclusion state becomes part of cache keys, worker
fencing, exports and alert evidence. This adds real implementation work beyond drawing
bands on a chart, but avoids conflicting numbers across surfaces.

## Revisit conditions

Revisit when recurring calendars, dynamic scope, finer-than-observation boundaries
or another retrospective correction policy is needed. Review the resulting changes
to existing report reproducibility and permissions explicitly.
