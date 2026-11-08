# ADR-0037: Reassign datasources without waiting for the previous agent

- Status: accepted
- Created: 2026-11-08
- Accepted: 2026-11-08
- Approval: The maintainer approved the concrete design in conversation, clarified editor/admin reassignment, immediate configuration for both agents and rejection of every later submission from the old agent for the datasource, including buffered results.
- Related specs: [SPEC-0004](../specs/0004-identity-and-access.md), [SPEC-0009](../specs/0009-agent-protocol-and-ownership.md), [SPEC-0018](../specs/0018-agent-management-usability.md)
- Supersedes / superseded by: supersedes the datasource reassignment expiry/drain requirement in [ADR-0012](0012-agent-leases-and-protocol.md); offline execution leases and process replacement under [ADR-0022](0022-restartable-agent-ownership.md) remain unchanged

Completion evidence and completion version changes are superseded by
[ADR-0038](0038-datasource-configuration-acknowledgement.md).

Pending return/cancellation semantics are superseded by
[ADR-0039](0039-cancel-pending-datasource-reassignment.md).

## Context

An operator can lose an agent and start a replacement with a different identity.
The current datasource move withholds the replacement configuration until the old
agent acknowledges removal or its execution authority expires, potentially an hour
later. The move is then reported complete before the replacement has collected.
Neither behavior establishes that collection has recovered.

The backend already retains canonical observations, execution grants and separate
SLI ownership epochs. These can authorize accepted intervals independently of
whether an unreachable previous agent has stopped querying.

## Decision

The ordinary editor or administrator assignment action immediately transfers the
datasource to the selected replacement and publish both agents' new configuration.
It must not depend on the previous agent's reachability, configuration acknowledgement,
lease expiry or successful drain. There is no additional force mode or confirmation.
Existing authorization, version preconditions and uncertain-write recovery remain.

Perform the assignment, ownership partition and configuration changes atomically.
Reuse canonical ownership epochs without changing measurement revisions, query
definitions, cadence, original measurement validity or accepted history. End the old
ownership at the next canonical interval boundary and authorize the replacement
from that boundary. This is an interval correctness bound, not a wait for the old
agent: publish replacement configuration immediately so it can prepare and collect
as soon as its first interval is eligible. Source settling delays still apply.
Avoid zero-length epochs when several moves occur before a boundary; a superseded
future assignment must never gain ingestion rights.

Stop renewing the old agent's grants for the moved datasource and notify it to
remove that work. Reject every subsequent old-agent submission for that datasource,
including earlier buffered intervals and receipt replay, before deduplication.
If it reconnects, it receives the configuration without that datasource.
Unrelated datasource collection and grants continue normally.
Accepted observations are never deleted or rewritten.

Keep the move visibly pending while waiting for the replacement. Configuration
acknowledgement alone, heartbeat, connection tests, and backend publication are not
completion. For a datasource with active SLIs, complete the move when the backend
accepts the first complete collection result from any of its current SLIs under
the replacement's acknowledged configuration, current process generation and new
ownership epoch. Zero traffic counts as complete; failed or incomplete collection,
rejected results, historical results and old deliveries do not. Other SLIs retain
their individual collection and freshness states; completion does not claim all
queries are healthy. A datasource with no active SLIs completes when the replacement
acknowledges its datasource configuration because it has no collection to prove.

The datasource's public `agent_id` identifies the selected replacement immediately.
The pending move records source, target and request time, with states for waiting
for replacement configuration, waiting for collection and replacement unavailable.
An offline or incompatible replacement remains visibly pending with actionable
status; it does not restore the old assignment automatically. UI and CLI describe
the actual blocker and stop presenting old authority expiry as a start deadline.
Refresh remains a read. Completion is persisted and audited when its evidence
commits, advances the resource version and survives backend restarts.

An editor or administrator may choose another agent while recovery is pending, including
the previous agent. Treat this as another immediate, versioned assignment through
the same ownership rules, rather than restoring superseded grants. Agent deletion
is blocked by actual current assignments; move history alone must not keep the
unassigned previous agent reserved. Preserve that identity in audit and history.

Editors may select an agent from safe agent summaries and change assignment only;
datasource creation, connectivity, credentials and agent lifecycle remain restricted
to administrators. Enforce this distinction transactionally at the write boundary.

Use a preserving migration and update generated API/query contracts and both
clients. Previously pending drain/expiry moves must adopt the same recovery path
without waiting for their recorded expiry, deleting observations or rewriting
measurement revisions. Keep GET operations free of mutation.

## Alternatives considered

- Keep drain/expiry waiting or merely shorten the lease: still blocks the recovery
  the operator explicitly requested; shortening leases also reduces outage tolerance.
- Add a separate force action: duplicates the normal assignment workflow and adds
  an operator step for the same intentional replacement.
- Complete on assignment or configuration acknowledgement: avoids the expiry wait
  but reports recovery before the replacement has submitted collection results.
- Immediate ordinary assignment with completion based on collection: recommended;
  fulfills the requested recovery behavior using retained ownership evidence.

## Consequences

Recovery depends on replacement readiness, eligible measurement intervals and source
availability. It no longer depends on the lost agent's permission expiring.
An unreachable previous process can still query until its existing offline lease
expires; external query overlap is possible, while accepted intervals remain
single-owner. Changing assignment cannot remotely stop a partitioned process.

Verification must cover a real disconnected old agent with an unexpired lease,
replacement configuration and accepted collection, old-agent reconnect and replay,
interval boundaries, rapid repeated moves, unchanged unrelated collection,
replacement failure/recovery, deletion races, backend restart, empty datasources
and adoption of existing pending moves. UI/API/CLI must agree on the assignment
and completion evidence. Exercise the journey on the isolated Compose stack.

## Revisit conditions

Revisit completion evidence when datasource-wide collectors or new observation
types are added. Requiring an acknowledgement from an unreachable old agent must
not become a prerequisite for ordinary operator recovery again.
