# ADR-0039: Cancel pending datasource reassignment without another pending move

- Status: accepted
- Created: 2026-11-08
- Accepted: 2026-11-08
- Approval: The maintainer clarified that moving a datasource back while its reassignment is pending cancels the operation and must remove the persistent rollback state, including when the restored agent is offline.
- Related specs: [SPEC-0018](../specs/0018-agent-management-usability.md), [SPEC-0009](../specs/0009-agent-protocol-and-ownership.md)
- Supersedes / superseded by: supersedes the pending-return completion rule in [ADR-0037](0037-immediate-datasource-reassignment.md) and the cancellation case of [ADR-0038](0038-datasource-configuration-acknowledgement.md)

## Context

Treating cancellation as another pending reassignment creates a repeating rollback
action. Returning to an offline original agent leaves that reverse operation pending
indefinitely, although the operator intended to cancel the first operation.

## Decision

Selecting the source of a still-pending reassignment cancels that operation. This
applies to the explicit move-back action and the ordinary agent picker. Restore
assignment immediately, publish new configurations for both agents, and finish
cancellation without creating a reverse pending row or waiting for acknowledgement.
Audit cancellation and retain ordinary configuration revision/version preconditions.
Carry explicit cancellation intent in the existing PUT (`cancel_reassignment: true`)
for the move-back action and a picker return to the displayed source. Under the
assignment locks, reject cancellation with 409 if acknowledgement already completed
the pending move. Automatically refresh that completed assignment in the UI; do not
silently reinterpret stale cancellation as a new reverse move. The optional flag
is valid for updates only, and does not bypass roles, settings or version checks.

Preserve fresh ownership boundaries and assignment authorization floors. Cancellation
must not revive revoked execution grants or accept buffered results from the displaced
agent. Agents apply their new configuration on reconnect. The restored agent's
reachability and configuration readiness remain visible separately; an offline agent
shows Not collecting, rather than Active or an endless Updating/rollback operation.

Once a reassignment has completed, selecting a different agent is a new operation
under ADR-0038. Returning to a third agent during a pending operation is also a new
operation. Neither is cancellation of the original pending move.

## Alternatives considered

- Another pending reverse move: retains the observed rollback loop.
- Restore old grants: would undo the required old-metric rejection fence.
- End cancellation while publishing fresh configurations: matches the operator's
  action and preserves assignment safety.

## Consequences

Cancellation confirms the management action, not collection health. Diagnostics
still require acknowledged current configuration, and observation freshness remains
separate. Real-stack tests cover an offline restored agent and the absence of a
new reverse pending operation.

## Revisit conditions

Revisit if operations gain explicit durable identifiers or a separate cancellation API.

Legacy pending moves retain the source as their current assignment. Cancellation
must also recognize that representation, including when the proposed replacement
has become disabled. Publish both configurations and advance the source's authority
floor without rewriting its existing measurement history or creating a reverse move.
