# SPEC-0010: API and CLI

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: in progress
- Related ADRs: [ADR-0009](../adr/0009-contracts-and-cli-parity.md),
  [ADR-0016](../adr/0016-authentication-retry-behavior.md)

## Purpose and contracts

Use versioned JSON HTTP APIs as the shared UI and automation surface. Propose
OpenAPI 3.0.3 for generated server/client compatibility, `net/http`, `oapi-codegen`
Go types/handlers, and generated TypeScript types. Pin generator versions when
scaffolding. Agent WebSocket messages have separate versioned JSON schemas with
shared type generation. Operational health types/handlers, CLI version/help and
UI-only serving are implemented in [F1-T04](../tasks/F1-T04.md). Explicit database
upgrades and their JSON/error contract are implemented in [F1-T05](../tasks/F1-T05.md).
Agent management HTTP operations are implemented under [F2-T06](../tasks/F2-T06.md);
ingestion and agent protocols remain unimplemented. Local authentication HTTP handlers
are verified under [F1-T13](../tasks/F1-T13.md), with backend executable exposure
under [F1-T14](../tasks/F1-T14.md). Noninteractive administrator setup is implemented
under [F1-T15](../tasks/F1-T15.md), using generated client operations and ephemeral
sessions. Browser local sign-in and administrator onboarding use the same operations
under [F1-T18](../tasks/F1-T18.md); reusable CLI login and profiles are implemented
under [F2-T10](../tasks/F2-T10.md).

Management routes use `/api/v1`; private worker operations use `/internal/v1` and
separate scopes/listener controls. Agent operations live under `/api/agent/v1` and
use agent credentials. Shared ingestion validation is reached through operation-
specific live/historical entry points with separate admission budgets.

## Operation inventory and CLI parity

| API resource/action | CLI shape | Permission |
| --- | --- | --- |
| services, folders, labels; list/get/create/update/delete | `almanac services ...`, `folders ...`, `labels ...` | Viewer read, Editor mutate |
| datasources and capability catalog | `almanac datasources ...`, `providers list` | Viewer read redacted; Admin mutate/test connection/auth |
| SLI/SLO definitions, status, history and reports | `almanac slis ...`, `slos ...`, `reports ...` | Viewer read, Editor mutate |
| query preview / connection test | `almanac slis test`, `datasources test` | Editor SLI preview on an existing source; Admin arbitrary source/connection tests |
| agent creation, tokens and assignment | `almanac agents ...` | Admin |
| backfill jobs and cancellation | `almanac backfills ...` | Editor; manual exhausted-healing restart Admin |
| missing ranges and repair cycles | `almanac repairs list/retry` | Viewer read; Admin retry |
| alerts, policies, destinations and notification silences | `almanac alerts ...`, `policies ...`, `destinations ...`, `silences ...` | Role rules in SPEC-0011 |
| maintenance exclusion preview, scheduling and revisions | `almanac maintenance ...` | Viewer read; Editor future changes; Admin elapsed-time changes under SPEC-0016 |
| timeline annotations and event export | `almanac annotations ...` | Viewer read; Editor/Admin write; explicitly scoped automation under SPEC-0017 |
| user/session/API-key administration | `almanac users ...`, `tokens ...` | Self-service scoped actions; Admin global |
| OpenSLO validation, plan, apply, activation and export | `almanac definitions validate/plan/apply/activate/deactivate/export` | Viewer validate/export redacted; Editor apply or store-inactive SLI/SLO definitions; Admin datasource/secret changes |
| observation submission | `almanac ingest --file ...` | Scoped producer or agent operation |

Commands are proposed names; generated help and API contracts become authoritative
at implementation. Every UI mutation must have an API operation and a noninteractive
CLI form, excluding browser-only OIDC redirects and visual layout. `almanac setup`
accepts protected initial-admin-password and new-password files, signs in with the
restricted initial credential and completes mandatory password rotation through the
same APIs as the UI. It cannot obtain ordinary admin authority before rotation. Browser sessions require step-up
for sensitive actions. Explicit Admin automation tokens may perform their granted
credential-administration scope noninteractively, with expiry and audit; ordinary
personal tokens cannot mint additional credentials.

## HTTP behavior

Resource IDs are opaque. Lists use stable opaque cursor pagination, default 50/max 200,
with cursor-bound filters and ordering. SLI list and observation cursors are bounded
to 512 bytes. Browser and CLI SLI list responses have a 10 MiB bound to accommodate
maximum-length queries after JSON escaping plus the bounded list projection. The
requested page limit is an upper bound: if the exact enriched response would exceed
the cap, return its largest fitting prefix with a cursor immediately after that row.
A one-record page must fit within the declared per-field bounds; otherwise the
response violates the contract. Projection fields remain complete and exact. Other
response limits remain independent.
Public names are validated and immutable
where referenced by OpenSLO; renaming display text is separate. Maximum body defaults
1 MiB management/4 MiB ingestion; OpenSLO import explicitly allows 5 MiB and 1,000
documents. Reject before unbounded decoding. Large exports
are streamed or asynchronous with explicit resource limits.

Use ETag/resource versions and `If-Match` for updates/deletes; conflicts return 409
and never silently overwrite. Create/command requests accept an idempotency key
scoped to actor and operation, retained 24h. Same key/different body conflicts. The
observation delivery contract uses its own seven-day replay horizon, not 24h command
receipts. Both mechanisms authorize every replay.

Administrators can test a saved datasource through
`POST /api/v1/datasources/{id}/connection-tests`, supplying `{}`, its exact
`If-Match` version and an `Idempotency-Key`. Admission reads the saved assignment,
connection settings and retained credentials together; clients never need to
retrieve or resubmit those credentials. The operation returns a diagnostic receipt
and `Location`, like an unsaved connection test. It accepts no settings overrides.
The CLI exposes this through `datasources test --datasource-id ... --if-match ...
--idempotency-key ...`; unsaved tests use the separate `--input-file` form. Testing
edited settings with retained credentials requires saving first or explicitly
providing replacement credentials for an unsaved test.

Authentication context, login, password change and logout are excluded from the
24-hour management receipts under [ADR-0016](../adr/0016-authentication-retry-behavior.md).
They reject a supplied Idempotency-Key with501 `capability_unavailable`. After a
lost login/password-change response, clients check session context and ask for
sign-in when needed, without automatically resubmitting the mutation. A valid
session does not by itself prove a particular password change succeeded. Preserve
an unknown outcome if context cannot be checked. Noninteractive CLI calls report
the uncertainty and recovery instructions without prompting or replaying. Logout
remains idempotent under normal authorization/CSRF checks. This narrow exception
does not remove receipts from ordinary management actions.

Long operations return 202 plus a job/location and state; terminal status distinguishes
success, failure, cancellation and partial result. Polling/watching a job cannot
extend its deadline. Problem responses include stable code, request ID, field errors
and a safe message; do not leak source queries, secrets or database internals.
401 is unauthenticated, 403 forbidden, 404 absent/not-visible, 409 conflict, 422 invalid
contract, 429 overload with retry guidance, 503 dependency unavailable. APIs preserve
unknown/no-data as explicit state/null, never an invented zero.

Direct-submission credentials can read only the SLI definition fields needed to form
observations. Ordinary list/detail output never reveals secrets. Sensitive create-key
responses are explicit one-time outputs, not included in subsequent reads.

## CLI behavior

Default human tables on a terminal; `--output=json` yields a single versioned JSON
document. `--output=jsonl` is supported for streaming lists/events. Stdout contains
results only; diagnostics go to stderr. Proposed exit codes: 0 success, 1 internal or
remote failure, 2 usage/validation, 3 authentication/authorization, 4 not found, 5 conflict,
6 timeout/cancel, 7 partial failure. JSON includes stable error codes independent of
human wording. Do not parse human table output in automation examples.

Support backend profiles, request timeout, CA trust, `--wait` and explicit polling
deadline. Token inputs use environment or protected files, never positional arguments
or default process-list-visible flags. Noninteractive operations never prompt; require
explicit flags for destructive actions and missing parameters. Secret issuance writes
to an explicitly selected protected output or displays once when deliberately requested.

An interrupted client does not imply cancellation of a durable job; `jobs cancel` is
explicit and audited. Imports offer dry-run/plan, a plan digest, and optimistic apply;
if definitions changed since planning, reject and require replanning. Do not fetch
remote YAML includes, execute shell expressions, or embed credentials in exports.

The initial reusable session commands are `auth login`, `auth context` and
`auth logout`, each with an explicit `--profile-file`. Login also requires
`--backend-url`, `--login` and `--password-file`; `--ca-file` is optional. Output
is text or one schema-version1 JSON document, with safe `identity` and
`session_state` on success and the established error envelope otherwise. The whole
operation defaults to30s and permits at most2m, including profile-lock acquisition.

Profiles pin exact origin/CA trust and persist only those bindings plus the session
credential and schema version. Fresh context supplies current authority and CSRF.
Existing active sessions cannot be silently replaced. Restricted identities remain
restricted. Profile storage uses strict bounded JSON, private regular files, a
checked user-owned non-writable-by-others parent, a stable per-profile process lock
and atomic replacement with durability checks. Initial support is Linux; unsupported
platforms fail explicitly. The parent must already exist, and lock/temporary filename
namespaces are reserved. Local persistence failure never reports remote success as
CLI success; pre-dispatch local uncertainty is distinct from a possible remote
authentication mutation. Uncertain logout preserves the recoverable credential.

## Acceptance criteria

Agent registration uses the current administrator session as defined by
[ADR-0019](../adr/0019-session-authorized-agent-registration.md).
`POST /api/v1/agents` accepts name, display name and optional lifetime without
`current_password`; the CLI create operation likewise needs no password file.
Retain idempotency keys, CSRF, current authorization, audit and one-time token
semantics. For a failed response, explain the observed cause when known
(connection failure, service unavailable, timeout or unexpected response), retain
the pending registration and offer deliberate recovery. Never infer rollback
from a connection failure, automatically repeat a mutation, or describe an
unconfirmed registration as an empty list with no qualification.

- AC-01: UI and CLI invoke the same authorized operations and produce equivalent
  resource outcomes; the operation inventory has no unexplained UI-only mutations.
- AC-02: Every API operation has generated types, examples, error cases and declared
  roles; regeneration produces no unexplained drift.
- AC-03: Stale updates, repeated commands and repeated ingest have documented
  conflict/replay behavior and never bypass current authorization.
- AC-04: CLI scripts can parse JSON and exit codes; error logs never contaminate
  stdout, leak credentials, or hang waiting for input.
- AC-05: Pagination, bodies, jobs and exports have bounded resources and deadlines;
  unsupported operations fail explicitly.

## Review notes and evidence

- **REVIEW-A01:** OpenAPI3.0.3 is a conservative generator choice; reassess with
  selected generators before implementation if3.1 features are required.
- **REVIEW-A02:** Command names, limits and exit codes are provisional stable contracts
  to review before release. Keys are scoped machine identities, not browser sessions.
- Operational contract generation and basic noninteractive CLI behavior are
  verified. Agent management has authenticated authorization, resource versions and
  safe receipts under F2-T02–F2-T06; full management API/CLI parity and broader
  resource RBAC remain outstanding.
