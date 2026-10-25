# ADR-0003: Bounded buffering and historical isolation

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Maintainer approved Crit review `b7b16f696fed`; all comments resolved.
- Related specs: [SPEC-0002](../specs/0002-agent-behavior.md),
  [SPEC-0003](../specs/0003-ingestion-and-recovery.md) (accepted)
- Related decisions: [ADR-0001](0001-collection-and-ingestion-boundaries.md),
  [ADR-0002](0002-agent-communication.md)
- Supersedes / superseded by: none

## Context

Agent/backend connectivity may fail while the agent can still query its datasource.
Keeping every unacknowledged observation without limits is not sustainable. Operators
prefer current collection to continue, retaining recent observations, while the
backend attempts to recover missing history later.

Backfill and automatic repair are best effort. They must use operations and
processing channels separate from live collection and must not impact its schedule.
Shared datasource limits and backend storage resources still need explicit budgets.

These product directions are agreed in SPEC-0002. This ADR formalizes the resource
strategy and its consequences; detailed delivery semantics are accepted in SPEC-0003.

## Decision

1. Retain unacknowledged live results in a bounded in-memory ring buffer. Keep
   collecting when full, evicting older retained results to admit recent results.
   Bound payloads, bookkeeping, and in-flight batches together. Exact capacity and
   ordering details are part of the ingestion contract.
2. Treat local buffering as temporary retention. Process loss can lose buffered
   observations. Do not claim durable or lossless delivery.
3. Let the backend detect missing expected intervals and request targeted historical
   collection through the assigned agent. Recovery depends on provider history and
   replay support. Unsupported or unrecoverable history remains visible as gaps.
4. Isolate live and historical operations with separate queues, execution capacity,
   result buffers, and submission budgets. Historical work must not use the live ring.
5. Throttle or pause historical admission when its own capacity or shared-resource
   headroom is insufficient. Preserve live scheduling. Historical retries and repair
   loops are bounded and cannot create uncontrolled background load.
6. Use common observation validation and identity semantics across these separate
   operations. A late live result and a historical result for the same observation
   cannot both contribute to totals.
7. Run backend self-healing periodically with persisted, finite retry budgets.
   After exhausting retries, leave the affected ranges visibly missing with the
   failure reason and stop automatic attempts for that cycle. Administrators may
   manually retrigger selected ranges through the UI or CLI, creating a new bounded
   and auditable cycle. Neither a routine loop pass nor a restart resets exhausted
   budgets, and manual healing must not duplicate active jobs or impact live work.

This decision does not choose a configuration lease duration, permit stale
credentials indefinitely, or require separate binaries/network sockets for every
operation. Data retention capacity and execution authority are separate concerns.

## Alternatives considered

| Alternative | Tradeoff |
| --- | --- |
| Stop live collection when the result buffer fills | Bounds memory but sacrifices current observations; conflicts with the chosen retention policy |
| Keep an unbounded in-memory queue | Preserves more unacknowledged data until memory is exhausted; unacceptable resource behavior |
| Use a durable local disk spool initially | Survives restarts but adds disk quotas, recovery, corruption handling, and deployment state; unnecessary for the agreed best-effort initial model |
| Put historical and live work in one queue with priorities | Simpler bookkeeping, but already-running historical queries and retained results can occupy all resources; separate bounded capacity makes the guarantee explicit |

A durable spool remains a possible future option if deployments require a stronger
delivery guarantee. It would not remove the need for bounded capacity or historical
isolation.

## Consequences

- Recent observations are favored; older gaps can become permanent if providers
  no longer retain the data or historical definitions cannot be replayed.
- Gap detection must rely on persisted expected intervals and coverage, not only
  loss reports that disappear with an agent restart.
- Repair state must persist retry counts, next-attempt time, exhaustion, and manual
  restart history. Loop frequency and retry settings require explicit limits.
- Historical progress may slow or stop under sustained live load. Report that state
  clearly; do not make up a completion deadline.
- Provider admission and backend persistence budgets must protect live work as well
  as local worker pools. Separate goroutines alone do not isolate shared resources.
- Tests must exercise overflow, lost acknowledgements, restarts, duplicate results,
  historical saturation, and provider throttling.
- Exact numerical budgets and overflow behavior for oversized items need explicit
  contracts and representative workload validation before implementation.

## Revisit conditions

Revisit if loss across restarts becomes unacceptable, providers cannot replay the
required history, or measured resource contention shows the isolation policy cannot
meet live scheduling requirements. Any stronger delivery promise requires an explicit
spec change rather than silently treating the ring as durable storage.
