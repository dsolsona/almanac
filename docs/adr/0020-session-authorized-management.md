# ADR-0020: Authorize signed-in management through the current session

- Status: accepted
- Created: 2026-10-28
- Accepted: 2026-10-28
- Approval: The maintainer explicitly approved removing repeat password prompts from signed-in actions, guarded agent deletion, readable credential durations and an explicit local agent launcher.
- Related specs: [SPEC-0018](../specs/0018-agent-management-usability.md), [SPEC-0004](../specs/0004-identity-and-access.md)
- Supersedes / superseded by: Supersedes the repeated password and recent reauthentication requirements in [ADR-0004](0004-identity-and-access.md); extends [ADR-0019](0019-session-authorized-agent-registration.md) beyond registration.

## Context

Registering an agent does not run its process. The local deployment lacks a simple
credential-driven launcher, and credential administration exposes transport units
and repeat password proof instead of an understandable operator workflow.
Disablement also leaves obsolete agent records in the ordinary list.

## Decision

An authenticated human uses their current session and current role for authorized
management actions, without supplying the password again or meeting a separate
login-freshness threshold. This includes credential rotation, revocation, agent
disablement/deletion and changing their password. Restricted onboarding sessions
can only complete onboarding or perform their existing limited actions. Sign-in
still needs credentials; creation/change of a password still needs the new value.
Recovery and one-time activation proofs retain their purpose.

Recheck session expiry, revocation, installation epoch, user status and current
authority in the transaction. Keep CSRF/origin/TLS checks, audit, idempotency,
optimistic concurrency and one-time secret delivery. Ordinary personal tokens
gain no credential-management permission. Automation scope boundaries remain.

Agent deletion is logical archival. Reject it atomically while any current
datasource is assigned, and return actionable dependency information. Successful
deletion revokes all credentials, prevents subsequent assignment and collection,
and excludes the agent from ordinary reads while preserving historical records.
Keep names reserved for historical identity clarity.

Local agent startup is an explicit operator action using a supplied credential,
separate from registering identities and starting the application. Do not silently
provision an agent or substitute a backend collection process.

## Alternatives considered

- Repeated password proof adds a second barrier against an exposed session but
  interrupts administration; the maintainer selected current-session authority.
- Automatically provisioning a local agent reduces steps but removes the explicit
  credential-driven testing workflow requested by the maintainer.
- Physical deletion loses identity history; disabling alone does not remove obsolete
  records from normal management. Logical deletion retains history and clear UX.

## Consequences

An attacker controlling an active authorized session can perform the same management
actions as its owner. Session protection and revocation remain essential. UI, API,
CLI, generated contracts and real acceptance checks must change together.
Datasource assignment and agent deletion must share a concurrency boundary. Protocol v2
negotiates drain-before-acknowledgement for normal reassignment; v1 peers retain
expiry-based handoff and remain compatible.

## Revisit conditions

Revisit if the maintainer requests configurable step-up authentication or evidence
shows the session boundary is inadequate. Do not reintroduce prompts implicitly.
