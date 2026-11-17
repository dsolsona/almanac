# ADR-0053: Coordinate maintenance exclusion and notification suppression

- Status: proposed
- Created: 2026-11-17
- Accepted: pending detailed contract and mock review
- Approval: maintainer approved the direction on 2026-11-17: exclusion, service/SLO/all-service scope and automatic suppression; detailed rules below remain proposed
- Related specs: [SPEC-0023](../specs/0023-maintenance-management.md), [SPEC-0016](../specs/0016-maintenance-exclusions.md), [SPEC-0017](../specs/0017-slo-timeline-annotations.md)
- Supersedes / superseded by: proposes replacing ADR-0014's separate opt-in silence workflow and initial explicit-SLO-only scope; exclusion arithmetic and immutable provenance remain unchanged

## Context

Operators schedule maintenance around services. Requiring them to find each SLO
and separately arrange notification silencing risks leaving objectives uncovered.
Exclusion alone cannot guarantee silence: rolling evaluation may still breach
because of eligible failures before maintenance.

The maintainer approved excluding observations rather than counting failures as
successes, retaining raw measurements and showing maintenance on SLO timelines.
This extends the existing maintenance authority. The reviewed mock and
[implementation plan](../plans/0046-maintenance-management.md) show how the work
can proceed in stages.

## Decision

Propose one maintenance workflow with two coordinated effects: calculation
exclusion and notification suppression. Preserve separate typed semantics and
authorities internally. A contextual annotation still cannot change either.

Support selected services, explicit SLOs, or all services. Preview affected SLOs.
Propose resolving service membership at activation and recording subsequent
membership changes prospectively while active. A new SLO receives coverage only
from joining; a departing SLO retains elapsed coverage. All-service selection
includes new services and their SLOs during the window. Historical reads use the
recorded membership, never current service membership.

Suppress new notifications, reminders and recovery delivery during active
maintenance. Preserve incident state and evidence; excluded evaluation cannot
manufacture recovery. Resume ordinary eligibility after the last overlapping
window ends, without replaying suppressed notifications. Exact cooldown and
notification deduplication integration require the detailed contract before code.

### Proposed publication and alert boundary

An installation-wide scope may cover more SLOs than one existing maintenance
revision can safely fan out to. Publish the scheduled selector and its authority
revision atomically before any effective start. Resolve and persist memberships
in restartable bounded pages, keyed by the authority revision. Until an affected
calculation can prove it read the complete membership at its cutoff, show
preparation or last-known values with the original provenance. A late page does
not move the scheduled start. Retries of an older revision cannot publish over
a cancellation, shortening or newer selector revision.

Opening a new occurrence is currently the user-visible trigger, independent of
external transport. The proposed first stage reads effective suppression at
the occurrence decision transaction's authority boundary. That read must not
wait for scope fanout, and uncommitted work under a stale authority must retry.
If an already-open occurrence remains breaching, it stays open. A fully
excluded evaluation stays unavailable. A genuine clear evaluation with
complete eligible data can resolve a pre-existing occurrence; the future
delivery adapter may still suppress its recovery message.

For an active window, current occurrence decisions use the authority effective
at the evaluation boundary. Catch-up retains the suppression evidence from
that boundary. Resuming after the last applicable window permits a new
occurrence only at the first complete eligible evaluation that satisfies the
policy. The suppression record must distinguish plain overlap from
notification delivery, which remains a later adapter concern.

## Alternatives considered

- Explicit SLOs and separate silences: already specified, but burdens service
  maintenance with two operations and risks incomplete coverage.
- Count failures as successes: inflates attainment and allowance; rejected in
  favor of excluding both good and total observations.
- Snapshot service membership at scheduling: simple and reproducible, but misses
  objectives added before or during maintenance. Recorded prospective membership
  covers operational intent while preserving historical scope.
- Stop all evaluation and erase incidents: obscures incidents that predate
  maintenance and loses explanation; suppress delivery while preserving evidence.

## Consequences

Service membership becomes a versioned input to exclusion and suppression.
Changes must fence stale calculations and notification sends consistently.
Large service/all-service scopes require bounded fanout beyond the existing
100-explicit-SLO write limit; the implementation protocol remains unapproved.

The mock reuses the current shell and accepted SLO detail B composition. It is a
throwaway prototype; UI acceptance and application verification remain separate.

## Revisit conditions

Resolve active membership policy, notification resume/cooldown behavior,
composite scope and bounded bulk activation before backend implementation.
