# SPEC-0002: Agent behavior

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Maintainer approved Crit review `8974c5d4f692`; all comments resolved.
- Implementation: not started
- Related ADRs: [ADR-0001](../adr/0001-collection-and-ingestion-boundaries.md)
  and [ADR-0002](../adr/0002-agent-communication.md)
- Plan: none
- Supersedes / superseded by: none

## Purpose and scope

Define the lifecycle of `almanac-agent`: registration, connection, capabilities,
configuration, collection, diagnostics, health, and historical work. This refines
[SPEC-0001](0001-system-overview.md) and supplies requirements for choosing the
communication transport.

Established requirements are remote-only collection, backend-owned versioned
configuration, explicit capabilities, one assigned agent per datasource, prompt
interactive diagnostics, heartbeats, and provider-aware backfill. The lifecycle
below is accepted, with unresolved policy and protocol details identified
explicitly. ADR-0002 selects WebSocket for coordination and HTTPS for configuration
snapshots and ingestion. Executable packaging and exact wire schemas remain open.

## Identity and connection

An administrator creates an agent in the application. The application returns an
agent token and provides setup instructions for supported deployment methods,
including Docker and Helm. Which deployment artifacts ship in the first release
will be specified in the deployment plan; instructions must match real artifacts.

The token maps to the registered agent identity. An administrator can revoke tokens
and issue replacements for that identity. Rotation changes credentials without
creating a new logical agent or losing datasource assignments. The agent uses the
token to authenticate and initiates communication with the backend; it requires
no inbound connection from the backend.

Agent tokens automatically receive an agent role when RBAC is defined. Its scope
is limited to required operations for that agent: obtaining assigned configuration,
reporting capabilities and health, receiving assigned work, and submitting results.
It does not grant user administration or access to another agent's configuration
or work. Token storage, presentation, expiry, rotation overlap, and revocation of
active sessions need explicit security contracts before implementation.

An authenticated connection identifies the agent software version, supported
protocol versions, and provider capabilities. A rejected handshake produces an
actionable error; it must not enter a retry loop that hides an incompatibility.
Transient connection failures use bounded retry delays with jitter. Exact bounds
are part of the communication ADR and implementation configuration.

Only one current generation may save results for an agent identity. Under
[ADR-0022](../adr/0022-restartable-agent-ownership.md), a compatible authenticated
cold start with a fresh process proof replaces the previous process immediately,
including after a crash. A superseded process stops when notified and cannot
reacquire automatically. An unreachable old process may query until offline expiry,
but its results are rejected. Keep one desired replica per identity.

## Capabilities

Capabilities describe what the installed agent can actually execute. They include
provider identity, compatible provider configuration formats, and supported
operations such as collection, connection tests, query previews, and backfill.
Software release version is diagnostic information, not a capability substitute.

The backend checks compatibility when assigning a datasource or accepting a
configuration change and again before dispatching work. The agent independently
validates received work. Unsupported operations return explicit errors.

Capabilities are refreshed on connection. If an upgrade or downgrade changes them,
the backend reconciles existing assignments and exposes incompatibilities.
Affected work is blocked; unrelated compatible work may continue. Queries,
provider-specific settings, and supported measurement semantics may also impose
capability requirements beyond a provider name.

## Configuration synchronization

The backend maintains a monotonically increasing desired revision
for each agent's effective configuration. The agent reports its last successfully
applied revision separately from the latest revision it received.

Effective configuration includes assigned datasources, credential material or
references available to that agent, frequency, and SLI measurement definitions.
The backend must provide a coherent snapshot. Changing any execution-relevant
setting produces a new revision. Routine health updates do not.

On connection or reconnection, the agent checks for the current desired revision.
During a session, the chosen transport makes changes discoverable without waiting
for a datasource's collection interval. Notifications are hints to reconcile;
missed notifications must not permanently strand the agent on old configuration.

Configuration application behavior:

1. Fetch and validate the complete desired configuration and its compatibility.
2. Prepare the new schedule without starting duplicate work.
3. Switch configuration atomically and acknowledge the applied revision.
4. On validation failure, report that revision and a redacted diagnostic. Do not
   claim that configuration was applied.

Whether a rejected revision permits continued execution of an older configuration
is unresolved. During disconnection, the recovery behavior below uses
the last applied configuration. Its validity limit and work fencing must be
decided together so removed assignments and revoked credentials do not authorize
execution indefinitely.

Results identify the configuration and measurement definition that produced them.
The ingestion contract must specify acceptance of in-flight results after edits or
reassignment; timestamps alone cannot establish which definition was executed.

## Scheduled collection

The agent schedules SLIs at their datasource's collection frequency using bounded
concurrency and query timeouts. Providers execute native queries and translate
responses into the agreed measurement contract. The backend never takes over a
query when the agent is missing or incompatible.

On initial SLI creation, promptly collect the most recent complete settled interval
once configuration and execution authority are ready, as authorized by
[ADR-0021](../adr/0021-immediate-initial-collection.md). Save through normal ingestion.
Recurring dispatch uses a stable per-SLI phase across the cadence; provider
evaluation timestamps and canonical intervals do not move with that phase.
Configuration reordering and restarts do not randomize recurring dispatch. Persisted
attempt/coverage state distinguishes initial collection from restart; restart must
not deliberately requery an already completed interval or repeat the initial burst.
Phase algorithm v1 hashes the UTF-8 SLI ID with SHA-256, interprets the first eight
bytes as an unsigned big-endian integer, and takes its remainder modulo the cadence
in microseconds. It applies that many microseconds after settling, without changing
the canonical interval. Changing this algorithm requires an explicit migration.
Keep bounded concurrency, fair admission and visible missed work under saturation.

Each scheduled execution has an identity and records its intended interval,
attempt, completion status, and any failure. A successful query with no
observations is explicit and differs from a missing execution. Query completion
and backend acceptance of observations are separate events.

The ingestion contract must define retry identity, duplicate handling, result
acknowledgement, and partial batch behavior. The agent must not treat an uncertain
submission as safely accepted or as permission to produce different observations
for the same execution without an explicit correction rule.

Clock alignment, missed-interval catch-up, overlapping queries, scheduling origin,
and maximum concurrent work remain detailed scheduling decisions. Schedule and
deadline calculations must be deterministic enough to exercise with a test clock.

## Interactive diagnostics

Connection tests and SLI query previews execute on the datasource's assigned
agent. They must not wait for the next collection interval. Provider capability
checks happen before dispatch, and the backend exposes missing or incompatible
agents immediately when already known.

Command contract:

- A request carries a unique identifier, intended agent, operation, input, and
  deadline. Results correlate to that request.
- Unsaved settings may be tested without modifying active configuration.
- Diagnostics do not persist preview observations as production measurements.
- Pending requests show progress and finish with a result, an explicit error,
  cancellation, or timeout. Results arriving after expiration cannot revive a
  request as current success.
- Expired diagnostics are not replayed after reconnect. Repeated delivery of a
  still-valid command must not create uncontrolled duplicate executions.
- Secrets and sensitive provider responses are redacted from user-visible errors
  and logs. Authorization governs which users can test which settings.

The UI update transport is separate from the agent transport decision. Numeric
dispatch targets, response size limits, cancellation guarantees, and query
timeouts must be specified before diagnostics implementation.

## Heartbeats and collection health

The heartbeat loop is independent of collection and backfill frequency. Heartbeats
report the agent identity and applied configuration revision; the protocol also
provides collection progress sufficient to identify missing executions.

The backend tracks its own receipt time for liveness. Agent timestamps provide
execution context but must not alone determine reachability. A closed connection
is an early signal; heartbeat deadlines also detect connections that stall.

Operators can distinguish last heartbeat, desired/applied configuration revision,
last collection attempt, last successful query, last accepted observations, and
next expected execution. A responsive agent with a stalled scheduler must not
appear to be collecting normally. Health labels and deadline defaults remain
open; monitoring must allow for query duration and delivery delay without hiding
repeated missed executions.

## Historical backfill

An operator may request a historical range when setting up a datasource or SLI.
The backend validates assignment and advertised support; the assigned agent
executes the provider queries. Some providers can query the full range; others
require smaller chunks based on capabilities and datasource constraints.

Backfill must expose requested range, completed coverage, unavailable history,
failures, and cancellation state. It uses normal measurement validation and
ingestion semantics. It must not silently count unavailable history as completed
coverage or successful observations.

Backfill and automatic gap repair are best effort. Completion depends on provider
capabilities, retained history, replayable definitions, and available resources.
Unrecoverable intervals remain explicit gaps; acceptance of a job is not a promise
that the requested history can be recovered.

### Isolation from live collection

Live collection and historical work use distinct operations and processing channels.
Backfill and automatic repair share the historical-work path. Their queues,
execution capacity, result buffers, and submission concurrency are bounded
separately from live collection. Historical results must never enter or evict data
from the live ring buffer, and historical retries must not occupy live worker slots.

Backfill must not delay, skip, or change the current collection schedule. Under
resource pressure, throttle or pause historical work and preserve live collection.
If historical result delivery cannot keep up, stop admitting more historical
queries rather than borrowing live buffer capacity. Resumption follows the backfill
job and acknowledgement contract.

Separate processing channels do not by themselves isolate shared provider limits,
network bandwidth, CPU, or backend storage capacity. Admission and rate budgets
must preserve headroom for live collection across those resources. Acceptance
checks must exercise slow historical queries, saturated historical submissions,
and provider throttling while checking that live work is not queued behind them.
Exact budgets and provider-specific mechanisms belong in the detailed contracts.

Channel separation is a behavior and resource-isolation requirement. Physical
connections and API endpoint layout remain wire-contract choices under ADR-0002.

The dedicated backfill spec will determine chunk planner ownership, job identity,
progress persistence, retry/resume semantics, query range limits, and handling of
overlap with regular collection. Bounded resource use must protect current polls
and interactive diagnostics from starvation. Backfill support is a capability;
supporting live collection does not imply access to historical data.

## Disconnection, recovery, and shutdown

Disconnection becomes visible through reachability tracking. The backend never
substitutes local collection. Reconnect requires authentication, capability
exchange, configuration reconciliation, and explicit handling of outstanding work.

The offline behavior is to continue collection using the last applied
configuration within an explicit validity policy and buffer unacknowledged results
in memory. All submitted observations carry their original measurement timestamps
or intervals, including delayed results; submission time must not replace the time
the data describes. Query completion and ingestion receipt times are separate.

Use a bounded in-memory ring buffer for unacknowledged live collection results. When
capacity is reached, evict the oldest retained results to make room for newly
collected results. Buffer saturation does not stop current collection. Retention
therefore favors recent observations, while older gaps are repaired by the backend
through agent backfill where the provider can supply the history.

Enforce a byte budget as well as bounded bookkeeping; an item count alone does not
bound memory when payload sizes vary. Buffered results retain original measurement
timestamps, execution identity, and definition revision for retry and validation.
Acknowledged results can be released. The exact capacity, any additional age limit,
ordering within the live buffer, and oversized-result handling
remain implementation-contract decisions. In-flight batches must also have bounded
memory; moving results out of the ring must not create an unbounded retry queue.

Evictions are observable through bounded counters and loss summaries. Reporting
every lost result must not create another unbounded buffer. Backend gap detection
must work even if the agent restarts before reporting loss. Process termination
loses in-memory results; this design does not promise durable or lossless buffering.

Buffer capacity controls retained data, not permission to execute queries. The
separate configuration-validity and ownership policy remains unresolved. A full
buffer is not itself a reason to expire configuration or stop collection.

On reconnect, authenticate and reconcile current configuration and ownership before
resuming dispatch. Delayed submissions follow explicit ingestion rules for stale
definitions, removed assignments, expired history, and duplicate executions.
Missing or dropped results remain visible as coverage gaps.

### Automatic gap repair direction

The backend detects missing expected measurement intervals and
requests targeted historical collection through the assigned agent. This uses the
backfill machinery for selected gaps rather than re-querying an entire history.
It can recover observations lost during a long disconnection, buffer overflow,
or agent restart when the provider still retains the required data.

Gap detection must distinguish an execution that never delivered a result from a
successful query that produced no observations. Known empty results must not cause
an endless repair loop. Repairs obey provider capabilities and query range limits,
use stable interval boundaries, and respect the definition applicable to that
history. Duplicate or overlapping repair and delayed live results follow the
ingestion contract rather than being counted twice.

Unavailable history and non-replayable definitions remain explicit gaps. Repair
attempts need bounded retries, progress, and resource budgets that protect current
collection and diagnostics. The repair spec must define detection, eligibility,
deduplication, and stopping rules. Automatic repair is an accepted product direction;
its initial release scope and detailed scheduling policy remain open. It uses the
isolated historical-work channel and always yields to live collection.

The backend runs healing at intervals, persisting finite retry budgets. When attempts
are exhausted, the ranges remain visibly missing with a reason and automatic healing
stops for that cycle. An administrator can retrigger selected ranges through the UI
or CLI in a new bounded cycle. Detailed cadence and attempt limits are defined in the
recovery contract; periodic passes must not reset exhausted budgets.

Graceful shutdown stops accepting new work, bounds the time allowed for in-flight
queries and submissions, and reports incomplete work where communication permits.
The recovery contract must also handle abrupt process death without a final report.

## Acceptance criteria

- AC-01: Creating an agent returns its scoped token and matching setup instructions.
  The agent authenticates through an outbound connection; invalid credentials and
  incompatible protocols produce explicit failures. Administrators can revoke and
  replace tokens without replacing the agent identity.
- AC-02: Two processes cannot silently collect concurrently using one agent identity
  under the selected ownership mechanism.
- AC-03: An agent advertising only PromQL capability cannot receive unsupported
  provider work; compatible datasource assignments continue operating.
- AC-04: A configuration edit produces a discoverable revision. Receipt and
  successful application are distinguishable; failed validation is visible.
- AC-05: Reconnection reconciles configuration even if change notifications were
  missed. Old work follows the selected fencing and offline-validity rules.
- AC-06: Scheduled queries execute on the assigned agent at the configured
  datasource frequency with bounded concurrency and explicit results.
- AC-07: Diagnostics dispatch independently of scheduled collection, correlate
  results, expire, and do not populate production measurements.
- AC-08: Heartbeats continue independently of slow queries; backend checks identify
  missing agents and missed executions even when one of these signals is healthy.
- AC-09: A successful empty query, failed query, missing execution, and unacknowledged
  submission remain distinguishable.
- AC-10: Supported backfill executes using provider-appropriate ranges, exposes
  progress and unavailable history, and observes resource limits. Backfill and
  automatic repair are best effort; unrecoverable history remains explicit gaps.
- AC-11: Reconnect, duplicate submissions, process death, and shutdown obey the
  selected ingestion and recovery contracts without silently fabricating success.
- AC-12: Credentials are restricted to authorized operations and redacted from
  diagnostics, logs, and routine management responses. An agent token cannot manage
  users or access another agent's assignments.
- AC-13: Disconnected collection uses a bounded in-memory ring buffer. At capacity,
  it evicts the oldest retained results and keeps collecting new results without
  exceeding the configured memory budget. Delayed submissions retain original
  measurement timestamps; evictions and restart losses can be detected and repaired.
- AC-14: When automatic repair is enabled under its future spec, missing intervals
  trigger targeted agent backfill where supported. Known empty results and
  unavailable history do not cause unbounded repair attempts, and overlapping
  results are not counted twice.
- AC-15: Backfill and automatic repair use separate operations, queues, execution
  capacity, buffers, and submission budgets from live collection. Saturating the
  historical path does not evict live results or delay the current collection
  schedule; historical admission is throttled or paused to preserve live capacity.

## Dependencies and open questions

| Decision | Needed before |
| --- | --- |
| Token storage and issuance, agent RBAC, rotation overlap and active-session revocation | Authenticated agent implementation |
| Message schemas, protocol negotiation and compatibility window within ADR-0002 | Wire protocol implementation |
| Configuration format, revision granularity, acknowledgement and ownership fencing | Configuration and reassignment implementation |
| Offline configuration validity, buffer capacity, optional age limits, eviction ordering and oversized-result handling | Collection recovery implementation |
| Ingestion payloads, measurement definition identity, duplicates and acknowledgements | Observation submission implementation |
| Scheduling alignment, catch-up, deadlines and concurrency budgets | Collection scheduler implementation |
| Backfill chunk planning and job lifecycle | Historical collection implementation |
| Live/historical channel isolation, resource admission and provider rate budgets | Concurrent live and historical collection implementation |
| Automatic gap repair eligibility, detection, deduplication, limits and release scope | Automatic repair implementation |
| Heartbeat intervals, grace periods, status labels and alerting policy | Operational health implementation |

These questions do not prevent reviewing component behavior. They do prevent
claiming the affected protocol or runtime is ready to implement. With transport
selected in ADR-0002, resolve offline/ownership behavior and refine the wire
contracts into examples.

## Verification evidence

Not implemented or runtime verified. The criteria above require focused protocol,
scheduling, integration, and failure-recovery tests once the dependent contracts
are defined. Documentation checks validate links and publication constraints only.

## Change notes

- 2026-10-24: Initial draft following accepted SPEC-0001 and ADR-0001.
- 2026-10-24: Expanded token-based setup and scoped agent permissions; proposed
  bounded in-memory buffering and targeted gap repair following maintainer review.
- 2026-10-24: Accepted after Crit review, including buffering and the automatic
  repair direction; detailed limits and release scope remain open as documented.
- 2026-10-24: Linked accepted ADR-0002 and updated transport decision status.
- 2026-10-24: Maintainer specified a bounded ring buffer that evicts older data
  while collection continues, with backend backfill repairing recoverable gaps.
  Recorded this explicit direction from the working conversation; capacity and
  configuration-validity policy remain separate decisions.
- 2026-10-24: Maintainer confirmed best-effort backfill and repair, separate live
  and historical operations/channels, and preservation of the live schedule.
  Added isolation requirements and acceptance coverage.
- 2026-10-24: Maintainer requested periodic healing, bounded attempts, visible missing
  ranges after exhaustion, and manual administrator retriggering in Crit review
  `b7b16f696fed`.
