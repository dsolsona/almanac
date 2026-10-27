# SPEC-0009: Agent protocol and ownership

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: in progress
- Related: [SPEC-0002](0002-agent-behavior.md), [SPEC-0004](0004-identity-and-access.md), [ADR-0002](../adr/0002-agent-communication.md), [ADR-0012](../adr/0012-agent-leases-and-protocol.md)

## Protocol and versioning

Agent initiates an authenticated TLS WebSocket session. HTTP configuration and
ingestion use the same scoped identity. Proposed application protocol is `almanac.agent.v1`,
independent of WebSocket's version. Support current protocol major only initially;
additive optional fields retain compatibility, unknown mandatory operations fail
explicitly. Software version is informational; provider/configuration capabilities
decide executable work. Do not silently fall back to another transport.

Each application message includes kind, message ID, session generation, and typed
payload. Commands additionally carry command ID and deadline. Unknown enum variants
are errors unless a schema explicitly marks them extensible. Limit control messages
to 256 KiB and queue to 128 messages/2 MiB; diagnostic result bodies have a separate
bounded HTTPS result operation if they exceed64 KiB, up to 1 MiB. No raw trace payloads.

| Direction | Message kinds |
| --- | --- |
| Agent → backend | hello, heartbeat, configuration-applied/failed, command-accepted/completed/failed, progress |
| Backend → agent | welcome, heartbeat-ack, configuration-changed, diagnostic-request/cancel, historical-work-available, shutdown/revoked |

Every envelope carries a decimal-string `session_generation`. A first-acquire hello
uses the reserved value `"0"` to mean that ownership is not yet confirmed; a hello
from a live process resuming known ownership carries its positive generation. All
non-hello messages require a positive generation selected by the backend. Each
hello includes a caller-generated 32-byte resume token and fresh 32-byte request
nonce. The token is process-memory-only. A first acquire uses its proof before
creating ownership; retries with the same proof recover the pending generation,
and reconnects from that live process resume the known generation. Token possession
alone never displaces another live owner.

The welcome echoes the hello nonce and provides the selected protocol, positive
generation, UTC server time, authenticated `agent_id`, positive desired configuration
revision, current ownership renewal sequence, heartbeat interval, and durable
execution lease. The lease repeats and
must match agent ID, generation, and configuration revision. Its
`allowed_measurements` entries bind one canonical SLI ID to one positive measurement
revision; duplicate SLI IDs are invalid. An empty grant list permits no query.

A heartbeat includes a fresh nonce, last successfully applied configuration
revision (`"0"` means not yet applied), explicit scheduler health (`starting`,
`ready`, `degraded`, or `stopped`) and separate collection progress
(`not_started`, `idle`, `running`, or `blocked`), current lease ID and expected
renewal sequence. The first renewal expects sequence `"0"`;
the heartbeat-ack echoes the nonce and returns the durable renewed lease with a
positive next sequence. Configuration revisions are positive from the first
snapshot onward. The HTTP configuration snapshot identifies its agent, positive
revision, and an `assignments` array. The response ETag is strong and has the form
`"agent-config:<agent_id>:<configuration_revision>"`; unchanged `If-None-Match` may
return 304 without a body. Empty assignments can never be paired with a nonempty
execution grant. Capabilities are refreshed in hello and are not repeated in each
heartbeat. These wire details implement accepted nonce-correlated renewal and
ownership semantics; they do not change ownership policy.

This increment defines hello, welcome, heartbeat, heartbeat-ack,
configuration-changed, configuration-applied/failed, shutdown, and revoked. The
diagnostic and historical-work message kinds remain unsupported until their owning
increment; an unrecognized or unsupported kind fails closed. Configuration failures
use bounded safe codes (`invalid_snapshot`, `incompatible`, `apply_failed`), and
shutdown/revocation use enumerated safe reasons rather than arbitrary diagnostic
text.

Handshake rejection uses WebSocket policy close status with one of the stable safe
reasons `active_owner`, `credential_revoked`, `incompatible_protocol`, or
`session_rejected`. The agent treats active ownership, revocation, and protocol
incompatibility as actionable terminal outcomes; transient transport and backend
availability errors reconnect with bounded backoff. Server `shutdown` is a
server-to-agent application message that requests reconnect. A locally stopped
agent ends the WebSocket with a normal close and does not send a server-only
`shutdown` message.

Hello validation first enforces a bounded, nonempty, unique protocol-version list;
negotiation then selects v1 or reports incompatibility. An authenticated, well-formed
hello with no supported version records only a bounded per-agent attempt timestamp,
creates no ownership and issues no execution grant. Recording requires no active
owner, or the matching active generation and memory-only resume proof. A copied
credential without that proof cannot replace a healthy runtime status. Management
reads report `reachability: incompatible` only when that attempt is newer than the
stored runtime activity. When no compatible runtime has been observed,
`protocol: incompatible` is a safe sentinel meaning no version was negotiated, and
`software_version: unknown` means no peer software version was accepted. A later
successful v1 hello clears the attempt atomically with runtime status update. The
attempt record stores no untrusted version string or other peer-provided detail.
For an agent with prior runtime observations, the incompatible marker changes only
reachability; protocol, software version, capabilities, configuration revisions,
heartbeat and collection timestamps continue to describe the last accepted runtime.

The backend's welcome records selected protocol, ownership generation, server time,
desired config revision, heartbeat interval and an execution lease. Schemas and
generated examples live under `api/agent/` at implementation; OpenAPI owns HTTP paths.
An authenticated `configuration-failed` report is recorded only for the current
desired revision and one of the bounded failure codes above. It does not advance the
last successfully applied revision; management status exposes the failure code and
revision together and marks scheduling degraded and collection blocked. Successful
application of the desired revision clears both failure fields.

## Separate versions and authorization

Configuration revision changes whenever effective assignments or execution settings
change. Measurement revision changes only when SLI meaning/cadence changes. Ownership
generation changes on identity takeover or datasource reassignment. An execution lease
binds agent, ownership generation, configuration and allowed SLI revisions to validity
bounds. An access token authenticates the identity; it is not a permanent execution lease.

Backend keeps durable lease evidence. Leases are opaque random identifiers, not
client-editable claims. Each renewal responds to a fresh agent nonce. The agent
conservatively anchors the granted duration to that request's monotonic send time,
not response arrival; discard stale responses and never extend beyond the backend's
recorded validity. Cap duration by remaining backend validity and fail closed if
the clocks/round-trip evidence cannot establish a safe bound. Wall-clock timestamps
identify measured data. Query validity never relies solely on
an untrusted agent-reported time. Clock skew over 5s marks scheduling degraded and
requires operator correction; runtime refuses new future intervals beyond allowed
clock tolerance.

## Heartbeat, offline collection and ownership

Default heartbeat 10s; declare unreachable after 30s without receipt. Refresh a **one-hour
offline live-execution lease** while healthy. On disconnect the agent continues live
collection and bounded buffering until its last lease expires, then stops new queries
and cancels in-flight queries. Query deadlines are capped by remaining authority.
It may retain buffered data for later authorized submission. Heartbeat deadline is a
UI/health signal, not the lease duration.

Known credential revocation closes connected sessions within 10s; new HTTP operations
are denied immediately at authorization. A disconnected agent cannot learn revocation
instantly; its lease bounds how long it may still query. This is a deliberate proposal
for review, not a claim that token revocation remotely erases source credentials.

Only one instance may hold an identity's active execution ownership. Reconnect from
the same live process presents an unpredictable resume token bound to its generation.
A new process without that token must wait for previous execution expiry or request
an administrator force-takeover. Never persist resume tokens in a shared image or
claim that possession of a copied agent token proves it is the same process.

Datasource reassignment atomically marks the pending transition and freezes renewal
of the affected old execution grants. It normally waits until that recorded authority
expires, then activates the new agent. Other unaffected grants may renew. Definition
changes similarly freeze superseded grant renewal while waiting for acknowledgment
or expiry. Force reassignment records an explicit warning
that both may query until old lease expiry, increments generation, and rejects all
old-owner writes immediately. The system prevents duplicate accepted measurements;
it cannot physically cancel a disconnected old process instantly. This is an explicit
exception to normal single-owner execution, never silent duplication.

## Configuration and delayed results

GET `/api/agent/v1/configuration` returns a full revisioned snapshot and ETag; unchanged
requests may return 304. Agent validates, prepares and atomically applies it, then
acknowledges. Startup without valid configuration performs no collection. Failed
updates keep the last accepted config only until its original execution expiry;
do not renew invalid configuration indefinitely.

Datasource assignment changes advance desired configuration independently of the
last applied revision. When a current heartbeat grant trails desired configuration,
the configuration endpoint returns the existing temporary
`503 agent_configuration_unavailable` problem without a snapshot or source
credentials. The agent keeps its last applied assignments, continues heartbeat
renewal with that applied revision, and retries reconciliation after the heartbeat
acknowledges a grant for the newer revision. It applies only a snapshot whose agent,
revision and empty production measurement grant exactly match that lease. A
configuration notice never causes credentials or queries to use a revision that
the current grant does not authorize. This also lets a same-process reconnect with
an older grant reach heartbeat renewal before fetching the newer snapshot.

For query/cadence edits, prepublish the new settings and effective interval bounds.
Activate only at a common boundary after the agent acknowledges those bounds or its
prior grant expires. Old leases cannot authorize observations for the old revision
beyond that boundary. Expose the scheduled activation time; an edit request is not
proof that an offline agent has switched definitions. Externally submitted SLIs use
prospective effective bounds without an agent-lease wait.

Reconnection reauthenticates and fetches desired configuration before new dispatch.
Buffered observations may arrive under an older configuration if their original
measurement revision and ownership remain authorized and within replay/retention
limits. Reject revoked principals, fenced ownership, expired delivery attempts and
invalid definition intervals explicitly. A renewed token for the same permitted
identity may replay unchanged deliveries; token rotation is not a new producer.

## Diagnostics and work claims

Connection/query tests have a 30s end-to-end deadline and 20s provider execution timeout
by default. Backend starts the deadline at request creation, not receipt by the agent.
Agent deduplicates command IDs during validity; backend persists terminal result
receipts for 24h. After disconnect an unfinished diagnostic becomes failed/unknown
at its deadline and is never silently replayed as a new test.

Historical-work notifications are hints. Agents claim durable work over HTTP, with
two-minute renewable chunk leases. A lost notification does not lose the job; after
reconnect or every 30s while healthy the agent checks for eligible work. Notifications
and claim polling do not affect live collection scheduling.

## Concrete default budgets

| Setting | Proposed default |
| --- | --- |
| Live result memory including in-flight |64 MiB per agent |
| Historical result memory including in-flight | 8 MiB per agent |
| Live query concurrency | 8 per agent, 4 per datasource |
| Historical query concurrency | 2 per agent, 1 per datasource |
| Live HTTP submissions | 2 in flight |
| Historical HTTP submissions | 1 in flight |
| Ingestion item / request | 1 MiB / 4 MiB; at most 1,000 observations per item |
| Retry backoff | 1s initial, 60s cap, full jitter; respect longer server guidance |

These are tunable provisional limits, not performance guarantees. Historical work
cannot borrow live memory/slots. Counts and metadata are charged to budgets, including
copied serialization buffers. Oversized items fail visibly; provider chunking must
keep generated items within supported limits.

## Acceptance criteria

- AC-01: Incompatible protocol/capability/configuration is rejected without starting
  unsupported queries; unknown fields follow schema compatibility rules.
- AC-02: Lost notices reconcile on reconnect; applied configuration is never inferred
  from receipt alone.
- AC-03: Heartbeat remains responsive during slow queries; disconnect, lease expiry,
  and token revocation produce distinct observable states.
- AC-04: Duplicate instances and reassignment obey normal lease fencing; force takeover
  is explicit/audited and stale writes are rejected even if old queries still run.
- AC-05: Lost diagnostic acknowledgements/results do not create uncontrolled duplicate
  tests or replay expired commands.
- AC-06: Budgets bound memory/concurrency through retries and historical saturation;
  live scheduling retains its reserved capacity.

## Review notes and evidence

- **REVIEW-P01:** One-hour offline validity is a proposed compromise: longer outages
  cause collection to stop and rely on best-effort repair. The user has approved ring
  eviction, not this duration. It bounds stale authorization while allowing brief
  outages to buffer. Review this prominently.
- **REVIEW-P02:** Safe replacement may wait up to an hour; force takeover acknowledges
  temporary duplicate source queries while keeping accepted data fenced.
- **REVIEW-P03:** Budgets, heartbeat/deadlines and protocol-major support are provisional
  defaults requiring provider/load tests. No runtime or wire implementation exists.
