# ADR-0043: SLO status from due eligible measurements

- Status: accepted
- Created: 2026-11-10
- Accepted: 2026-11-10
- Approval: maintainer approved the proposed Healthy/Waiting/Partial/Budget-exhausted behavior and requested implementation on a new stacked branch.
- Related specs: [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md), [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md)
- Supersedes / superseded by: supersedes the shortened-eligibility and live-interval coverage rules of [ADR-0023](0023-slo-operational-urgency.md); all unrelated thresholds and historical compliance rules stand.

## Context

A newly created SLO is labelled Partial data even when every measurement due since
creation has arrived. Shortened eligibility forces Watch for the entire rolling
window, and current calculations count intervals still awaiting normal collection.
Operators cannot distinguish the age of an objective from missing observations.

## Decision

Current operational status judges complete eligible measurements since the SLO's
explicit evaluation start. A shorter eligible objective or burn window does not
itself make coverage partial, force Watch or make burn unavailable. Retain the
configured window and evaluation start as separate facts. Healthy requires fresh,
complete, positive-traffic data, the existing budget thresholds and an evaluable
complete eligible one-hour burn below 1. Missing, known-empty, zero-traffic, stale
and expired data retain their existing conservative meanings.

For live/default current reads, the latest expected interval end is the cadence
floor of the request's cadence-aligned snapshot time minus source settling delay, deterministic SLI dispatch phase
and one collection cadence. The last cadence is a bounded completion/delivery
allowance, reusing the existing observation-freshness allowance. Deadlines round up
to the next snapshot cadence tick, so publication and clock invalidation use the
same boundaries. This deadline
advances from the clock even if the agent stops; never anchor it to the newest
received observation. Intervals beyond that boundary are pending, not missing.
Already received whole observed or known-empty intervals may advance the cutoff
before that deadline; the clock-derived cutoff is always the minimum, so an old
receipt can never freeze it. No due eligible interval is Waiting for data. Once an interval is due, its absence
is a real gap; an entirely absent result is No data. Freshness uses request time,
not the older coverage boundary.

Live and explicit historical prepared responses have separate cache identities.
Live detail and current projections expire at the next snapshot tick or the first
freshness deadline, whichever comes first; scheduling, lists and service summaries
use that same deadline. A cached Healthy result cannot survive an observation's
freshness expiry within a snapshot tick. Pinned historical table values retain
their existing independent lifetime. Unavailable coverage must never be described
as no intervals being due.

Current snapshot evaluation time and generation evidence remain the requested
cadence boundary; the actual measurement bounds are disclosed by `current.window`
and each burn window. Current API/CLI evaluation uses the same due boundary.
Explicit historical `at` requests, chart points, daily compliance and their exact
canonical arithmetic retain their specified interval endpoints. Daily compliance
still requires its unshortened window. No historical observations are silently
added to a new SLO; selecting earlier measurements remains explicit.

The detail status shows evaluation start, measurement cutoff and observed/expected
interval counts in ordinary language. New SLOs with complete eligible data can be
Healthy immediately. An observed negative budget in a shortened window remains Budget exhausted, with
the existing provisional qualification where applicable. Preserve list geometry,
themes, accessibility and the single status chip.

## Alternatives considered

- Wording only: cheaper, but retains false Watch for an entire rolling window.
- Using the latest received observation as the cutoff: simple, but conceals an
  outage by freezing the evaluation horizon.
- Removing coverage checks: falsely treats genuine missing measurements as success.

## Consequences

Live values intentionally trail wall time by a disclosed bounded collection
allowance. Existing data gaps remain visible after their deadlines. Current
projections refresh normally; historical extrema do not require rebuilding because
their stored endpoints and mathematical meaning do not change. No new dependency,
schema shape, target default or collection schedule is introduced.

## Revisit conditions

Revisit the completion allowance with measured remote collection/delivery latency,
or when explicit per-source execution deadlines become part of the accepted contract.
