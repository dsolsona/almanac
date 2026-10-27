# ADR-0018: Agent WebSocket library

- Status: accepted
- Created: 2026-10-26
- Accepted: 2026-10-27
- Approval: Maintainer explicitly approved Coder WebSocket v1.8.15 in the working conversation.
- Related specs: [SPEC-0009](../specs/0009-agent-protocol-and-ownership.md)
- Supersedes / superseded by: none

## Context

[ADR-0002](0002-agent-communication.md) already selects outbound authenticated
WebSocket coordination and separate HTTPS configuration/ingestion. A maintained
framing implementation is needed in both the backend and separate agent executable.
Application ownership, durable execution grants and health remain Almanac concerns.

## Decision

Pin `github.com/coder/websocket` at **v1.8.15** for both endpoints.
The upstream [release](https://github.com/coder/websocket/releases/tag/v1.8.15)
is published; its [module](https://github.com/coder/websocket/blob/v1.8.15/go.mod)
declares Go1.23 and no module dependencies, compatible with Almanac's Go1.27.3.
Its context-aware read/write API and explicit read limits fit cancellation and
bounded control messages. These are API properties, not Almanac test evidence.
[Upstream API](https://pkg.go.dev/github.com/coder/websocket@v1.8.15)

Keep compression disabled. Explicitly configure the accepted 256KiB message limit;
the library default is smaller. Maintain application queue bounds of 128 messages
and 2MiB, accounting for in-flight payloads. Require `almanac.agent.v1` negotiation,
verified TLS, the scoped agent credential and current authorization before upgrade.
Never carry credentials in URLs or treat WebSocket ping/pong as application health.

Use one application reader and one bounded writer loop, with explicit cancellation
and shutdown ownership. Use Almanac's JSON v2 validation for typed message envelopes;
library JSON helpers do not replace required-field or protocol validation. Keep the
dependency in transport code, outside deterministic lease/configuration logic.

## Alternatives considered

- **Gorilla WebSocket:** a viable implementation with read limits and a documented
  one-reader/one-writer concurrency model. Its deadline-oriented reads/writes would
  require additional cancellation integration. Prefer Coder's context-bearing API
  for this application; this is an engineering judgment, not a benchmark result.
  [Gorilla API](https://pkg.go.dev/github.com/gorilla/websocket)
- **Implement framing directly:** removes the package dependency but makes Almanac
  maintain framing, close-handshake and protocol-compliance behavior. That work does
  not advance SLI collection and increases review scope.
- **Change transport:** not proposed; the accepted WebSocket/HTTPS split already
  addresses interactive diagnostics and independent observation ingestion.

## Consequences

This adds one pinned runtime dependency. Almanac still must test real TLS endpoints,
authentication/revocation, protocol mismatch, oversized messages, slow peers,
bounded queues, disconnect/reconnect and cancellation. Library selection proves
none of the ownership, lease or collection acceptance criteria by itself.

This choice is accepted for transport implementation. Credential authentication,
durable ownership and schema preparation remain independent prerequisites.

## Revisit conditions

Revisit for upstream maintenance/security concerns, incompatibility with supported
proxies/toolchains, or measured inability to meet Almanac's bounded shutdown and
control-channel budgets. Upgrades require pinned-version verification and relevant
protocol regression tests; no automatic major-version substitution.
