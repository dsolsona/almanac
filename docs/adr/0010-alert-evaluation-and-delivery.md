# ADR-0010: Alert evaluation and delivery

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related specs: [SPEC-0011](../specs/0011-alerting-and-notifications.md)
- Supersedes / superseded by: none

## Context

Alerts need consistent calculations, durable incident transitions and recoverable
notification attempts. Agent collection must continue independently of alerting.
Separately runnable alert processing should not require another component to own
PostgreSQL schema upgrades or duplicate ingestion/calculation semantics.

## Decision

Propose two logical workers: periodic evaluation and notification delivery. Both
consume the same private backend application operations; only the backend accesses
application PostgreSQL. Embedded workers use an in-process adapter with equivalent
authorization, validation and fencing; remote workers use authenticated private
HTTP APIs with scoped Worker credentials. They may share an executable/runtime deployment but have independent
work queues, concurrency budgets, cancellation and health indicators. No worker
queries an external metric source. Delivery performs bounded outbound webhook I/O.

The backend supplies a consistent calculation snapshot and policy revision, plus
exact good/total count decimals, target and effective SLI/SLO definition identities
for each window. The worker compares exact rational values by cross multiplication;
rounded display burn is never used for decisions. Semantic definition activation
supersedes affected incidents and resets timers atomically with effective-state
transition; new unsupported bindings become capability-inactive.
Evaluation submits a result against a fenced lease, expected state version and
snapshot ID. The backend validates freshness/revision and atomically commits the
incident transition and outbox event. Unique evaluation identities make retries
idempotent. A stale lease cannot commit. Per-incident ordering prevents overlapping
workers from overwriting newer state. Calculation algorithm ownership stays in
the backend; the worker owns scheduling and rule comparison.

Delivery claims durable outbox work through the backend API with expiring fenced
leases. Completion/failure acknowledgement names the lease and attempt. A send
that succeeds before a crash can repeat after lease expiry: this is bounded
at-least-once delivery. Stable event IDs and sequence numbers let receivers
handle duplicates/order. Retries, suppression checks, rate limits and terminal
failures remain visible; no in-memory queue is the authoritative record.

Remote workers use distinct Worker credentials scoped to evaluation or delivery;
embedded adapters enforce the corresponding operation scope. Delivery gets
only the destination material needed for its claimed attempt; secrets are not
returned through management read APIs. Backend and workers apply the same egress
policy. Runtime configuration must specify signing-key access and rotation, lease
expiry, payload bounds and global/per-destination concurrency before implementation.

## Alternatives considered

- In-process callbacks after ingestion avoid polling but couple ingestion latency
  to alert work and miss time-driven no-data detection without another scheduler.
- Workers accessing PostgreSQL directly simplify transactions but expand schema
  ownership, database credentials and schema upgrade coupling across process boundaries.
- An external message broker can isolate delivery throughput but adds operations
  and a second durability boundary before workload evidence justifies it.

## Consequences

The backend retains the transactional boundary and can support independently
scaled workers. Private API availability becomes a dependency; outages delay
alerts rather than enabling a database fallback. Snapshot/fencing APIs and their
integration tests are real implementation cost. Processor lag and delivery lag
must be monitored separately from collection freshness. Exactly-once webhook
receipt cannot be promised. Notification destination security applies even when
workers run inside a trusted network.

<!-- REVIEW-U05: Accept private API workers and backend-owned outbox over direct database access? This favors one persistence boundary at the cost of additional internal API contracts. -->

## Revisit conditions

Revisit if measured evaluation throughput or delivery backlog cannot meet latency
budgets with bounded API batching, or independent deployment requirements demand a
new durability boundary. Record any change in a superseding ADR.
