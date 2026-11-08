# Agents and collection

Read [agent protocol](../../../../docs/specs/0009-agent-protocol-and-ownership.md),
[backfill and repair](../../../../docs/specs/0008-backfill-and-repair.md),
[providers](../../../../docs/specs/0015-datasource-providers.md), and
[restart ownership](../../../../docs/adr/0022-restartable-agent-ownership.md).

- Trace where collection, diagnostics, coordination and ingestion execute. Compare
  component responsibilities and authority with the current architecture contract.
- Trace registration, acquisition, restart, takeover, reconnect and credential
  changes. Identify which process may query and which results the backend may accept
  before and after each lease or ownership boundary.
- Check capability negotiation, configuration revisions, acknowledgement and effective
  changes against the protocol. Examine how edits affect observations already collected.
- Separate reachability, collection progress and observation freshness. Healthy
  heartbeats cannot make old measurements current.
- Check collection scheduling, settled interval selection and initial execution.
  Reconnect or catch-up cannot silently skip intervals or duplicate conflicting values.
- Verify buffers, admission limits, cancellation and retries against the documented
  resource policy. Trace competing workloads through shared provider and database
  resources to check isolation and scheduling guarantees.
- Follow transport failures and lost acknowledgements through replay, deduplication
  and error reporting without assuming every submission succeeded or is safe to repeat.

Describe failures as an event sequence with agent/process identities, revision or
lease boundaries, and the resulting accepted, lost, rejected or stale observations.
