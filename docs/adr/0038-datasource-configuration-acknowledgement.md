# ADR-0038: Finish datasource updates on configuration acknowledgement

- Status: accepted
- Created: 2026-11-08
- Accepted: 2026-11-08
- Approval: The maintainer clarified in conversation that assignment is a configuration update, should display Updating, and should finish within a few seconds of pushing configuration and receiving the replacement's acknowledgement.
- Related specs: [SPEC-0009](../specs/0009-agent-protocol-and-ownership.md), [SPEC-0018](../specs/0018-agent-management-usability.md)
- Supersedes / superseded by: supersedes completion evidence and completion version changes in [ADR-0037](0037-immediate-datasource-reassignment.md)

Pending return/cancellation semantics are superseded by
[ADR-0039](0039-cancel-pending-datasource-reassignment.md).

## Context

Waiting for the first measurement couples configuration readiness to canonical
intervals and settling delay. It prevents immediate diagnostics even when the
replacement has applied the configuration. The page also retained its saved
pending state until an explicit refresh.

## Decision

Show Updating while the replacement applies its configuration. Complete and audit
the update when its current authenticated process acknowledges the published
assignment revision, including datasources with active SLIs. Measurement freshness
and query errors remain separate; Active does not assert successful collection.
Preserve ADR-0037's immediate assignment and strict rejection of old-agent writes.

Use the existing WebSocket configuration notice, fetch and acknowledgement path.
Connected v2 agents reconcile on the notice without waiting for their next heartbeat.
Poll pending datasource status automatically without replacing unsaved edits or
uncertain writes. Under normal connected operation, Updating should clear within
a few seconds and diagnostics should become available without reloading the page.
Unavailable agents retain an actionable pending state; there is no artificial
completion timeout.

Acknowledgement changes runtime progress, not the operator's configuration version.
Do not increment the datasource resource version or publish another configuration
merely to clear Updating. Preserve current generation and revision checks.

## Alternatives considered

- Wait for collection: measures freshness but imposes minute-scale configuration delay.
- Complete at publication: cannot confirm the replacement received or applied it.
- Acknowledge configuration: confirms the intended operation with existing protocol evidence.

## Consequences

Diagnostics can run before the first eligible live collection interval. Collection
still obeys canonical ownership boundaries and settling delay. Tests must distinguish
configuration completion from observation freshness and verify automatic UI recovery.

## Revisit conditions

Revisit if the protocol gains per-datasource apply failures; do not reintroduce
measurement freshness as a prerequisite for configuration completion.
