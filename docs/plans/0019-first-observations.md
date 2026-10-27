# First observations delivery plan

> Use subagent-driven development: up to two `gpt-6-luna` implementers on independent
> scopes with stable shared interfaces; the coordinator owns integration and
> acceptance. Follow the current [ways of working](../ways-of-working.md).

Goal: an administrator registers a remote agent, creates a Prometheus datasource,
tests a query on that agent, creates an SLI and sees collected counts and health.
Direct producers can submit observations through the ingestion API.

Architecture: extend the existing authenticated Go backend and PostgreSQL storage.
Agents initiate WebSocket coordination and use HTTPS for configuration and ingestion.
UI and CLI consume the same management API; collection never executes in the backend.

Specs: [SPEC-0014 F2](../specs/0014-delivery-and-verification.md),
[agent behavior](../specs/0002-agent-behavior.md),
[identity](../specs/0004-identity-and-access.md),
[protocol](../specs/0009-agent-protocol-and-ownership.md),
[API/CLI](../specs/0010-api-and-cli.md),
[ingestion](../specs/0003-ingestion-and-recovery.md),
[storage](../specs/0007-storage-and-retention.md) and
[providers](../specs/0015-datasource-providers.md).

## Global constraints

- Single tenant; reuse local authentication, audit transactions, schema upgrades,
  generated contracts, shared UI controls and the existing Compose workflow.
- Agent credentials are separate from human sessions. Their possession never
  authorizes management operations. Restricted onboarding cannot create agents.
- Preserve configured expiry, rotation, revocation and ownership boundaries.
  Receipt replay must not retrieve a previously issued plaintext credential.
- Heartbeat reachability, scheduler progress and accepted-data freshness remain
  separate. An authenticated socket alone does not establish healthy collection.
- No new command approvals or generic execution wrappers. Use approved setup,
  generate, build and verify families. Preserve the user's running Compose stack.
- No SLO/budget dashboard claim: calculations are F3. Historical repair and alerting
  remain F4; F2 must preserve the interfaces and isolation needed for them.

## Delivery order and observable checkpoints

The requested local provider fixtures are the first bounded task:
[F2-T01](../tasks/F2-T01.md). They establish real VictoriaMetrics/TraceQL source
checks before Almanac adapter acceptance, without changing the product sequence.
[F2-T02](../tasks/F2-T02.md) is the first application implementation task:
transactional registration with current-authority checks, audit and safe receipts.
[F2-T03](../tasks/F2-T03.md) connects password verification and one-time token
issuance to that transaction before exposing management operations.
[F2-T04](../tasks/F2-T04.md) supplies safe reads, revoke and disable with current
authority, optimistic versions and safe historical receipts. Rotation follows.
[F2-T05](../tasks/F2-T05.md) adds bounded overlap and one-time replacement issuance,
plus the thin management service methods for those guarded storage operations.
[F2-T06](../tasks/F2-T06.md) adds their authenticated HTTP contracts and runtime
routes before CLI and UI management.
[F2-T07](../tasks/F2-T07.md) supplies the bounded browser client before screens,
preserving exact versions, one-time secret handling and explicit mutation retry.
[F2-T08](../tasks/F2-T08.md) connects registration and history to shared UI controls;
credential lifecycle forms follow after that first browser journey is verified.
[F2-T09](../tasks/F2-T09.md) adds those lifecycle forms against the verified
management API before connected-agent and datasource work.
[F2-T10](../tasks/F2-T10.md) supplies reusable CLI authentication and protected
profiles before agent-management command parity.
[F2-T11](../tasks/F2-T11.md) supplies all seven agent-management CLI operations,
protected one-time token output and real executable acceptance before connectivity.
[F2-T12](../tasks/F2-T12.md) validates current agent credentials against PostgreSQL
as the authentication prerequisite for coordination and configuration operations.

1. **Agent registration and credentials.** Define management contracts and the
   missing reauthentication/receipt boundaries; implement transactional agent and
   token lifecycle, then expose the same operations through API, CLI and an Agents
   page. Registration works without pretending that a collector is connected.
2. **Connected agent.** Deliver the agent executable and real outbound handshake,
   capabilities, heartbeat, revision reconciliation and ownership fencing. Show
   never-connected, connected, unreachable and incompatible conditions accurately.
3. **Datasource and remote query test.** Add Prometheus connection settings and
   assignment, protected source-secret storage, and bounded remote diagnostics.
   Follow accepted [ADR-0017](../adr/0017-recoverable-secret-encryption.md) for
   source-secret encryption. Unsaved query previews never write production observations.
4. **First observations.** Define an SLI, schedule remote queries, ingest and persist
   counts, display missing/empty/failure states, and exercise direct ingestion.
   UI and CLI offer corresponding operations within this slice.

These are dependency checkpoints, not pre-authorized speculative task briefs.
Prepare an exact bounded task once its interfaces and predecessors are established.
The first implementation brief must account for existing session authorization,
transactional audit, missing step-up proof, scoped receipts and machine-token storage.
No management endpoint may bypass those requirements to accelerate the first screen.
The readiness audit confirms all five prerequisites need either extension or new
implementation; F1 supplies no agent management endpoint or reusable CLI profile.
The management wire contracts, persistence, handlers and browser/CLI consumers
now exist in F2-T02–T11. The next usable increment connects the remote agent and
exposes its actual status, completing checkpoint 2 above.
Existing lease/takeover choices are already accepted: the
[review guide](../review-guide.md) explicitly treats inline proposal wording as
decision traceability, not a repeated approval requirement.

## Review focus and verification ownership

| Condition | Required evidence | Owning checkpoint |
| --- | --- | --- |
| Lost token-creation response or repeated command | One resource/receipt; original token cannot be retrieved; explicit replacement path | Registration |
| Revocation racing a write, rotation with two active tokens, expired credential | Database ordering, bounded overlap and stable agent identity; no stale authorization | Registration/connection |
| Copied agent token, reconnect, stale revision or unsupported capability | Real protocol tests prevent silent duplicate execution and reject unsupported work | Connection |
| Slow query with healthy heartbeat or stalled collection | Separate health signals; diagnostics do not wait for the live schedule | Connection/datasource |
| Successful empty query, missing interval, duplicate or partial submission | Deterministic ingestion and real database evidence; no fabricated success | Observations |

Use focused unit checks for validation and state transitions; real PostgreSQL checks
for authorization, receipts, audit and persistence; real agent/backend tests for
coordination; compact Chromium journeys for each delivered screen. Run the integrated
candidate gate after combining changes. Keep broader theme/mobile checks at affected
UI boundaries, not around every backend edit.

## Current execution state

- [x] Confirm merged foundation and Compose baseline at `5a3e9bd`.
- [x] Establish isolated branch/worktree `feat/f2-agents` / `.worktrees/f2-agents`.
- [x] Complete code-to-contract readiness audit.
- [x] Define first bounded task interfaces and management acceptance cases.
- [x] Deliver and verify registration through UI/API/CLI.
- [x] Deliver and verify connected-agent status.
- [ ] Deliver and verify datasource diagnostics.
- [ ] Deliver and verify collected and directly ingested observations.

F2-T01–T06 establish verified provider fixtures and authenticated management API.
F2-T07–T11 complete browser and CLI management. F2-T17/T19 deliver connected-agent
status. Datasource diagnostics and the complete observation journey remain outstanding; individual
task records carry their verification evidence.
