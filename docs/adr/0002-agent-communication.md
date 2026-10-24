# ADR-0002: Agent communication

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Maintainer approved Crit review `acca7410b041` with no changes requested.
- Related specs: [SPEC-0002: Agent behavior](../specs/0002-agent-behavior.md)
- Related decisions: [ADR-0001](0001-collection-and-ingestion-boundaries.md)
- Supersedes / superseded by: none

## Context

Agents need timely configuration updates, interactive diagnostics, health reporting,
and historical-work dispatch. A user testing a query should not wait for the next
scheduled collection. Agents connect outbound from the networks containing their
datasources. Custom producers also need a straightforward ingestion API.

WebSocket provides bidirectional messages on an established connection and defines
Ping, Pong, and Close frames. These transport mechanisms do not define Almanac's
configuration acknowledgement or command recovery semantics.
[RFC 6455](https://datatracker.ietf.org/doc/html/rfc6455)

Long polling holds a request until an event or timeout and then renews it. It can
deliver work promptly while a request is pending; it does not impose the fixed
delay of periodic polling. Request renewal, proxy behavior, and timeouts need
handling. [RFC 6202](https://www.rfc-editor.org/info/rfc6202/)

The choice therefore concerns session management and communication structure,
not whether long polling can provide a responsive user experience.

## Decision

Use an authenticated outbound WebSocket connection for agent coordination
and HTTPS request/response APIs for configuration snapshots and observation ingestion.

| Path | Purpose |
| --- | --- |
| WebSocket | Session hello/capabilities, configuration-change notices, application heartbeats, configuration acknowledgements, diagnostic commands and bounded results, work notifications and progress |
| HTTPS configuration API | Fetch a complete versioned configuration snapshot authorized for the agent |
| HTTPS ingestion API | Submit observations and execution outcomes with explicit acceptance results, including delayed and historical submissions |

This separation keeps large observation batches off the coordination connection
and gives custom producers a submission path without implementing the agent session.
It does not eliminate shared network congestion; scheduling and buffering still
need resource limits. The exact batch and execution-outcome envelopes belong in
the ingestion contract.

### Authentication and compatibility

Authenticate the WebSocket handshake and HTTP requests using the scoped agent token
in an authorization header. Do not put tokens in URLs. Use encrypted communication
for deployed agents; TLS termination and any local-development exceptions belong
in deployment configuration.

The application handshake negotiates an Almanac protocol version and advertises
capabilities. This version is separate from the WebSocket standard version, agent
software release, provider configuration formats, and configuration revision.
Incompatible peers receive actionable errors. Revocation must invalidate active
sessions as well as subsequent requests; the propagation deadline and mechanism
remain part of the identity contract.

### Session and reconnect semantics

- A successful connection establishes transport availability, not collection
  readiness. Reconcile desired configuration and execution ownership before
  enabling new backend-dispatched work.
- Configuration notifications carry a desired revision. The agent fetches the
  latest snapshot and acknowledges successful application. Reconnect always
  reconciles revision state even if notifications were lost.
- Application heartbeats carry identity and applied revision. Ping/Pong may help
  detect broken connections, but does not establish scheduler progress.
- Commands have identities and deadlines. Socket writes are not proof of receipt,
  execution, or result acceptance. Expired diagnostics are not replayed.
- Bound message sizes, outgoing queues, query execution, and write durations.
  Slow consumers must not cause unbounded memory growth. Query execution must not
  block the session's heartbeat and message handling loops.
- Reconnect with bounded exponential backoff and jitter; expose sustained failure.
  Reconcile unfinished durable work from backend state. A session is not the sole
  record of a backfill job or a reason to discard unacknowledged observations.

The wire contract must define message schemas, acknowledgement behavior, error
codes, deadlines, and compatibility policy before implementation. In-memory
observation buffering and automatic gap repair remain governed by SPEC-0002 and
the forthcoming ingestion/backfill contracts.

### Runtime boundaries

The backend component owning an agent's connection dispatches commands to it.
Management handlers use a dispatch boundary rather than depending directly on
socket objects. Initially, these responsibilities may share a process.

Multiple backend instances require authoritative connection ownership and command
routing across instances. A shared hostname or connection affinity alone does not
route commands arriving on another instance. The runtime ADR must choose that
coordination mechanism before claiming support for multiple connection-serving
instances. This ADR adds no broker dependency and makes no availability promise.

## Alternatives considered

| Approach | Benefits | Costs and reason not selected |
| --- | --- | --- |
| Periodic polling | Straightforward request/response loop | Dispatch waits for another poll unless request frequency is increased; poor fit for interactive tests |
| Long polling plus HTTP submissions | Prompt dispatch with ordinary HTTP requests; viable alternative | Requires request renewal and application rules for outstanding polls; a persistent bidirectional session fits the proposed control interactions more directly |
| WebSocket for both coordination and observations | One connected transport for agent traffic | Large result batches share the coordination path; custom producers still need an HTTP submission interface |

Do not implement an automatic second transport initially. Supporting two agent
transports would require compatibility and recovery coverage for both. If deployment
evidence favors long polling, reconsider the transport rather than silently
falling back to behavior with different semantics.

## Consequences

- Interactive work and configuration notices can be dispatched as events arrive,
  without coupling dispatch to collection frequency. Query execution latency and
  capacity limits still affect the total time a user waits.
- Deployments must support WebSocket connections through their proxies, with
  compatible idle timeouts and connection draining. Verify this in deployment tests.
- Agents implement both the coordination protocol and HTTP submission/configuration
  clients. Common authentication and error conventions should keep them coherent.
- Application-level correlation, deduplication, deadlines, and recovery are required;
  choosing WebSocket does not provide exactly-once execution.
- Long-lived connections consume backend resources. Connection limits, heartbeat
  intervals, queue budgets, and load expectations need measured validation.
- Browser updates are a separate transport decision. Agent WebSocket selection
  does not require the UI to use WebSocket.

## Revisit conditions

Reconsider if supported deployment environments cannot reliably carry WebSocket,
if measured connection costs exceed the supported scale, or if the final protocol
becomes simpler with long polling. Preserve configuration reconciliation and
ingestion semantics if the coordination transport changes.
