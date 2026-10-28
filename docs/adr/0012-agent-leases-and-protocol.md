# ADR-0012: Agent leases and protocol

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related spec: [SPEC-0009](../specs/0009-agent-protocol-and-ownership.md)
- Supersedes / superseded by: process-replacement waiting is superseded by [ADR-0022](0022-restartable-agent-ownership.md); offline lease and datasource reassignment decisions remain in effect

## Context and decision

An agent must keep collecting through short backend outages while administrators
can revoke credentials or reassign a datasource. A disconnected process cannot
instantly learn a revocation. Buffer capacity alone does not solve execution authority.

Propose backend-recorded expiring execution leases bound to agent/configuration/
ownership generation, refreshed by authenticated heartbeats. Default offline validity
is one hour. Keep reachability deadlines short and independent. Fence accepted writes
immediately on revoked authorization or changed ownership; preserve historical grant
evidence for otherwise permitted delayed results.

Normal takeover waits for old authority expiry. Force takeover is an explicit audited
operation acknowledging possible overlap in external queries, never duplicate accepted
observations. Historical work has short renewable claims and pauses when disconnected.
Use protocol-major negotiation plus explicit provider/configuration capabilities.

## Alternatives and consequences

- Stop on every disconnect: simplest authorization boundary but wastes the intended
  buffering behavior on brief outages.
- Continue indefinitely: maximizes collection continuity but leaves removed assignments
  and credentials executable without a finite bound.
- Short one-minute leases: reduce stale authority but make ordinary outages quickly
  depend on backfill; one hour is the proposed compromise, not an established requirement.
- Assume token possession means a unique process: copied credentials would silently
  duplicate collection; explicit ownership/resume evidence is needed.

Operators must understand normal versus forced takeover. A wire/ownership spec must
test partitions, process restarts, revocation, clock skew and late responses. In-memory
buffer eviction and offline execution expiry remain independent settings.

## Review and revisit

**REVIEW-P01/P02** require maintainer review before implementation. Revisit the duration
and fencing policy if actual operational availability or revocation needs differ.
