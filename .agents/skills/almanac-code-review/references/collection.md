# Agents and collection

Read [agent protocol](../../../../docs/specs/0009-agent-protocol-and-ownership.md),
[backfill and repair](../../../../docs/specs/0008-backfill-and-repair.md),
[providers](../../../../docs/specs/0015-datasource-providers.md), and
[restart ownership](../../../../docs/adr/0022-restartable-agent-ownership.md).

- Keep external queries and connectivity diagnostics on the assigned remote agent.
  The backend accepts observations and coordinates work; it does not become a collector.
- Trace registration, acquisition, restart, takeover, reconnect and credential
  changes. Identify which process may query and which results the backend may accept
  before and after each lease or ownership boundary.
- Check capability negotiation, configuration revisions and acknowledgement before
  effective changes. Query edits cannot reinterpret observations already collected.
- Separate reachability, collection progress and observation freshness. Healthy
  heartbeats cannot make old measurements current.
- Check collection scheduling, settled interval selection and initial execution.
  Reconnect or catch-up cannot silently skip intervals or duplicate conflicting values.
- Verify finite buffers, admission limits, cancellation and retry budgets. Historical
  work uses separate operations/channels and must preserve live scheduling and capacity,
  including shared provider and database resources.
- Follow transport failures and lost acknowledgements through replay, deduplication
  and error reporting without assuming every submission succeeded or is safe to repeat.

Describe failures as an event sequence with agent/process identities, revision or
lease boundaries, and the resulting accepted, lost, rejected or stale observations.
