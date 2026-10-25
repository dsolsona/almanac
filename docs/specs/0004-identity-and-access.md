# SPEC-0004: Identity and access

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: not started
- Related ADRs: [ADR-0004](../adr/0004-identity-and-access.md) (accepted)
- Plan: none
- Supersedes / superseded by: none

## Purpose

Give one Almanac deployment a protected initial owner, named human users, and
separate least-privilege identities for agents and external producers. This refines
[SPEC-0001](0001-system-overview.md), [SPEC-0002](0002-agent-behavior.md), and
[SPEC-0003](0003-ingestion-and-recovery.md). The behavior and initial defaults below are accepted design contracts.
Explicitly deferred implementation choices, including REVIEW-I06, remain open.

## Scope and non-goals

Include first-run onboarding, local passwords, sessions, authorization, agent tokens,
producer keys, secret protection, audit, and UI/CLI management parity in the initial
release. Google SSO remains a product requirement but is a later delivery increment;
its future contract is retained below. It does not block the local-user release.
Defer custom roles, service-specific human membership, SCIM, additional identity
providers, MFA, self-service email delivery, and anonymous dashboards. One database
represents one deployment; identities have no cross-deployment authority.

## Behavior and contracts

### Initial ownership

Deployment tooling provisions an initial administrator login and password through
protected configuration and a mounted password file. It may generate a random
256-bit password or accept an operator-supplied password meeting the normal policy.
There is no shared default password and no separate onboarding claim secret. The
application never prints this password in logs or returns it through an API.

On first startup, a database transaction locks a singleton initialization record,
creates exactly one local administrator with a password verifier and
`must_change_password=true`, and marks the bootstrap account provisioned. Concurrent
startup attempts cannot create additional owners. A failed transaction leaves no
partial account. If required initial credentials are missing, setup readiness fails
closed with an operator-facing diagnostic.

The first login with that password creates a restricted session: it can change the
password or log out, but cannot read application data, manage users, register agents,
or issue credentials. The onboarding screen asks for the administrator's permanent
password. Require a different password satisfying the normal policy; complete the
change and mark onboarding complete atomically. Invalidate the initial password and
all restricted sessions, and issue a fresh normal session. Concurrent completions
produce one winner; stale sessions must authenticate again. Use the same throttling,
TLS, cookie and CSRF protections as normal authentication.

The CLI can complete this flow noninteractively using protected initial-password
and new-password files, through the same login/change-password API as the UI.
The initial account exists before onboarding, but full administrator access begins
only after rotation. Before that, expose only health, sign-in/onboarding assets and
the restricted authentication operations; background application work waits.

Restarts use the persisted account and completion state. They never regenerate or
reapply the initial password, including while rotation is pending. Changing or leaving
the mounted initial password file cannot reset the account or reopen onboarding;
it can be removed after successful initialization. Losing that password while rotation
is pending uses the same explicit offline recovery path as a lost ordinary password.
Loss of all administrators requires an explicit offline recovery operation with
database and deployment-secret access; it creates a single-use reset credential
and audit event without reopening onboarding. Recovery invalidates all affected
sessions, personal/automation tokens, and pending authentication, activation,
invitation, linking, and reset challenges before issuing its new recovery proof.
If initial rotation was pending, successful recovery redemption sets the permanent
password and completes onboarding atomically; merely issuing recovery proof never
unlocks application access.
This is a required recovery workflow,
not a currently available command. The deployment contract must define its invocation.

### Human roles and management parity

Authorization is deny-by-default on every API operation, including list filtering,
exports, asynchronous result reads, and individual items in ingestion batches.
UI visibility is not an authorization boundary. Human roles apply deployment-wide.

| Capability | Viewer | Editor | Administrator | Agent | Producer |
| --- | --- | --- | --- | --- | --- |
| Read services, SLI/SLO definitions, measurements, reports | Yes | Yes | Yes | Assigned execution definitions only | Explicit definition scope only |
| Create/edit/delete services, SLIs, SLOs, alert rules; import/export | No | Yes | Yes | No | No |
| Edit datasources, credentials, assignments; run connection diagnostics | No | No | Yes | Execute assigned work only | No |
| Preview SLI queries through an existing datasource | No | Yes | Yes | Execute assigned work only | No |
| Request/cancel ordinary backfill | No | Yes | Yes | Execute assigned ranges only | Explicit historical ingest only |
| Manually retrigger exhausted repair | No | No | Yes | Execute assigned ranges only | No |
| Manage users, roles, agents, producer identities, keys, SSO, audit | No | No | Yes | No | No |
| Submit observations / report agent health | No | No | No | Own authorized work | Explicit ingest scope / no health |
| Change own password, sessions, linked login, personal CLI tokens | Yes | Yes | Yes | No | No |

Editor previews may supply query text against an existing authorized datasource,
but cannot override its endpoint, authentication, network settings, or assignment.
Source connection tests and tests of unsaved source settings require Administrator.
Editor access to alert rules excludes reading or replacing destination secrets.
Viewer-visible metadata redacts connection strings and secret-bearing settings.
Agent, Producer, and Worker are fixed machine roles, not assignable to human users.
Worker has no permissions in the table; its private-API permissions are defined below.
Ordinary administrators cannot retrieve stored plaintext credentials through a
management API. At least one enabled administrator with a local password must
remain; disabling, deleting, or demoting the last such account fails atomically.
Creating a local user returns a one-use 24-hour activation credential; redemption
sets its first password. Pending accounts cannot authenticate or hold the protected
last-administrator position.

UI and CLI offer the same create, inspect, update, disable, rotate, and revoke
operations and errors. Local CLI login exchanges credentials for an opaque session;
When Google SSO ships, Google-only users authorize a CLI session through a browser approval bound to a
one-use CLI challenge; browser reauthentication also unlocks sensitive CLI actions.
Personal tokens have explicit management operation scopes, a 30-day default and
90-day maximum lifetime, and never exceed the user's current role. Ordinary tokens
cannot mint credentials, change authentication settings, or reset passwords.

An administrator may explicitly grant an automation token `credentials:admin` with
allowed credential kinds, target identities or resource selectors, and maximum
issuable scopes and lifetimes. Separate `users:admin` and `authentication:admin`
grants authorize their corresponding sensitive operations. `users:admin` automation
is limited to non-administrator targets: it cannot create/promote an administrator,
reset an administrator password, change its own role, or link authentication methods.
`authentication:admin` cannot change account bindings, admission roles, or login trust
in a way that creates administrator access; those actions require interactive
administrator reauthentication. Initial grants and scope
expansion require administrator reauthentication within five minutes. Thereafter,
scoped operations, including issuance, rotation, and revocation, run noninteractively
through API/CLI without repeated browser approval. Every call requires the owner's
current enabled Administrator role. Automation cannot create or expand automation
authority; rotation preserves the grant and cannot exceed its original expiry.
Each issuance is bounded by the grant's credential targets and scope ceiling;
permission to issue an agent token never implies permission to issue a human token.
Grants, operations, and one-time secret outputs are audited and protected identically
to interactive operations. Explicit scopes provide authority, not an exemption from
last-administrator protection or other invariants.

Disabling a user invalidates all their sessions, personal/automation tokens, and
pending login, activation, linking, invitation, and reset challenges;
demotion immediately removes administrator automation authority. Disabling a key's
creator does not implicitly disable an independently owned machine identity; its
administrator-managed status is explicit.

### Maintenance and annotation permissions

[SPEC-0016](0016-maintenance-exclusions.md) adds Viewer read access, Editor future
maintenance scheduling and Administrator changes affecting elapsed time, with the
specified prospective-end exception. [SPEC-0017](0017-slo-timeline-annotations.md)
adds Viewer annotation reads and Editor/Admin management. Automated annotation
credentials require explicit `annotations:write` and SLO scope; they do not grant
maintenance, datasource-secret or credential-administration authority. Agent and
Producer credentials do not acquire annotation authority implicitly. The existing
credential lifecycle, role ceilings and revocation checks apply.

### Passwords and sessions

Local logins use a unique case-insensitive login name; password text is never
silently trimmed or truncated. Proposed policy is 15–128 Unicode code points,
with spaces allowed and a common-password denylist; no composition rules or
scheduled password expiry. Store salted, versioned Argon2id verifiers in PostgreSQL;
minimum parameters are 19 MiB, two iterations, parallelism one, with deployment
benchmarking allowed to raise cost. [OWASP password storage](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)

Use random 256-bit opaque session secrets; persist only their verifiers. Rotate
on login and privilege change. Sessions expire after 30 minutes idle or 12 hours
absolute, using server time. Browser cookies use `Secure`, `HttpOnly`, `SameSite=Lax`,
`Path=/`, and the `__Host-` prefix, with no Domain. State-changing cookie requests
require a session-bound CSRF token and same-origin validation; GET has no mutations.
Do not store browser authentication in local storage. Logout revokes server state.
[OWASP session management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)

A password change requires the current password and atomically invalidates all
existing sessions, personal/automation tokens, and pending authentication, linking,
activation, invitation, and reset challenges; successful change issues a fresh
session to the authenticated caller. An administrator can issue a one-time,
15-minute reset credential, shown once and delivered out of band. Issuance revokes
all affected sessions, personal/automation tokens, and existing authentication,
linking, activation, invitation, and reset challenges, then suspends password login
until redemption;
redemption atomically consumes the credential and sets a new password. Pending
resets can be cancelled by an administrator. Recovery never returns an old password.
Login and reset failures use non-enumerating responses and comparable work for
unknown users. Proposed throttles are five failures per account and 20 per source
address per 15 minutes, with shared database enforcement and `Retry-After`; recovery
and existing sessions remain usable. Trusted proxy rules govern source addresses.

### Google admission and linking (later delivery)

This section specifies the planned follow-up feature. The initial local-user release
ships no Google button, configuration form, callback or account-linking operation;
unsupported API operations return a clear unsupported-capability response. Keep
local user IDs independent of future external identity bindings.

When delivered, SSO is disabled until an administrator configures the Google client and exact
HTTPS callback URI. Use server-side authorization-code flow, state, nonce, and
PKCE S256. Validate issuer, signature, audience, expiry, nonce, and authorized party
when present through a maintained OIDC library. Request only `openid email profile`;
do not retain Google access/refresh tokens after login. Identify accounts by validated
issuer and `sub`, never by mutable email alone. [Google OpenID Connect](https://developers.google.com/identity/openid-connect/openid-connect)

Admission is invitation-only: an administrator creates a pending user with a role
and exact email, and separately conveys a random one-use invitation valid for
24 hours. First Google login must present that invitation and verified matching
email; consume it and bind issuer/subject atomically. An optional Workspace-domain
restriction validates the signed `hd` claim as an additional restriction, never as
an invitation substitute. No automatic registration or administrator grant occurs.

Matching an existing email does not link accounts. Linking requires authentication
and recent reauthentication of the existing account, then a fresh Google flow.
Unique issuer/subject constraints prevent linking one Google identity to two users.
Unlinking cannot remove the last usable login method or protected local administrator.
Google outage blocks new Google authentication; existing Almanac sessions follow
their normal limits. Disabling an Almanac user remains authoritative immediately.
Google account suspension is observed on subsequent provider authentication, not
promised as instantaneous local session revocation.

### Machine credentials and agent setup

An administrator creates a named agent and receives its identity, one-time token,
and Docker and Helm setup instructions matching released deployment artifacts.
Instructions reference a mounted secret or Kubernetes Secret; copied example files
contain placeholders, not plaintext tokens. Lost creation responses require issuing
a replacement credential; the original plaintext is never retrievable.

Agent tokens grant only that identity's configuration, coordination, capabilities,
health, and authorized submissions. Producer keys bind a stable producer identity,
explicit SLI IDs, and scopes `definitions:read`, `ingest:live`, and optionally
`ingest:historical`. Historical permission includes a bounded earliest allowed
interval and remains subject to global retention; no wildcard SLI grants by default.
Definition reads exclude datasource settings and unrelated SLIs. Neither machine
credential can perform management operations or impersonate a human.

A Worker identity is assigned alert evaluation, notification delivery, or both.
Its token has private-API scopes `work:claim`, `evaluation:read`, `evaluation:result`,
and/or `notification:deliver` limited to the assigned work class. Claims carry
expiring generations; reads, result commits, and delivery authorization validate
the current claim. Evaluation reads expose only required definitions/measurements.
A delivery worker receives destination secrets only for its currently assigned
notification claim over TLS, never source credentials or arbitrary destination
secret lookup. Worker tokens cannot manage users/resources, ingest measurements,
or execute metric collection. Embedded workers use the same authorization boundary.

All tokens contain a public lookup ID plus a random 256-bit secret, with only a
SHA-256 verifier stored and constant-time verification. Agents, producers, and workers default
to 90-day expiry, configurable up to one year; show expiry warnings 14 days ahead.
Rotation creates a new credential for the same identity and assignments. Default
old-token overlap is ten minutes, explicitly reducible to zero; emergency revocation
has no overlap. At most two credentials may be active per identity. Revoking a
credential leaves the identity intact; disabling the identity revokes all its
credentials and cancels its future work. Tokens never appear in URLs or audit payloads.

### Revocation, execution, and delayed ingestion

Each request checks current credential, principal, and scope state. Authorization
and the protected database mutation serialize against revocation, so work committing
before revocation is ordered before it and work ordered after it cannot bypass it.
Database unavailability fails closed. Existing agent sockets revalidate before
commands, renewals, and configuration reads, and close within ten seconds of
revocation; a socket is not permanent authorization.

The proposed offline execution lease is at most one hour, renewed with a ten-second
healthy heartbeat and separate 30-second unavailable threshold. Session ownership
and offline execution authority are distinct: a disconnected session does not
immediately release permission to query. A disconnected agent may execute
only while its existing lease remains valid; expiry stops new queries and cancels
in-flight work. No renewal is granted to a revoked credential or disabled identity.
Thus cooperative remote execution stops within the outstanding lease, while backend
requests are denied as soon as revocation commits. Leases cannot force a compromised
agent to forget a source password: source-system revocation is also needed.

Every ingestion attempt, including duplicate receipts, requires a currently valid
credential. For delayed results additionally check the recorded assignment epoch,
execution grant, allowed operation/range, and effective definition revision. A
replacement token for the same enabled identity may deliver previously authorized
buffered live work within the replay horizon; expiry of its execution lease alone
does not erase the recorded live-delivery grant. This exception applies only to
buffered live results. Historical results require a currently valid chunk lease
and generation, including at commit; expiry or cancellation denies their acceptance. A still-owned assignment may retain explicitly recorded delivery
authority for old intervals, but that record grants no new execution. Disabling the identity
or explicitly withdrawing the execution grant denies delayed acceptance as well.
Reassignment fences old-owner writes immediately, including delayed submissions;
normal reassignment or duplicate-instance takeover waits for outstanding execution
authority to expire. A force action requires explicit acknowledgment of possible
query overlap and does not restore old-owner ingestion rights. Other replay limits
belong to the protocol and ingestion contracts.

For example, agent A collects 09:59 under revision 1, rotates its token at 10:00,
and submits at 10:02. The old revoked token fails; its replacement may succeed if
the 09:59 execution grant and replay horizon remain valid. Neither token authorizes
A to query a datasource reassigned to B or submit B's new intervals.

### Recoverable secrets and audit

Encrypt datasource passwords, destination secrets, and the Google client secret
at rest with authenticated encryption, proposed AES-256-GCM with unique nonces,
key identifiers, and resource/field identity as associated data. Master keys live
outside PostgreSQL in deployment secrets. Keep old keys during a resumable rewrap;
remove them only after validation and backup recovery planning. Missing keys fail
closed with redacted diagnostics. Backups require a separately protected key backup.

Use a library-backed encryption adapter rather than implementing cryptography.
[Tink for Go AEAD](https://pkg.go.dev/github.com/tink-crypto/tink-go/v2/aead) is a
candidate for AES-GCM and associated data; its
[keysets](https://developers.google.com/tink/design/keysets) support a primary
write key and retained read keys during rotation. This is not an automatic database
field integration: the persistence/application boundary must explicitly encrypt
before storage and decrypt only for authorized execution. Ordinary management reads
return redacted metadata without decrypting secret values. Library choice and
ciphertext format remain a pre-implementation decision under REVIEW-I06.


Restore activation rotates the installation credential epoch and invalidates every
restored session, human/automation/machine token, and pending authentication,
activation, invitation, linking, and reset challenge. Credential verification binds
to that epoch; merely restoring an old verifier cannot resurrect authority. Keep
the restored deployment isolated while offline recovery establishes administrator
access, then explicitly reissue machine credentials. Fence restored ownership and
claims and reconcile outstanding old-owner execution authority before enabling
network access or dispatch. The restore workflow must conservatively wait for the
maximum outstanding execution period after isolating the old deployment, or use
an explicit force procedure acknowledging remote query overlap. Never infer that
restoring database lease timestamps stopped an agent outside that database.

Only the assigned agent receives source secrets needed for its current work over
TLS. Configuration and diagnostics redact secrets from logs and returned errors.
A management response shows whether a secret exists, never its value. Provider
errors and preview results require redaction and bounded sizes before user display.

Persist audit events for setup/recovery, authentication failures, role and account
changes, credential lifecycle, secret replacement, SSO linking/configuration, and
administrative operations. Include actor, subject/resource, action, outcome, server
time, request ID, and redacted changes; never credentials or full submitted payloads.
Security mutations and their success audit events commit atomically. Administrators
can read/export but cannot edit audit events; default retention is 400 days with
operator-configured deletion policy. This does not promise tamper-proof storage
against a database administrator. Failed-login audit is bounded against flooding.

## Failure and recovery

Return `401` for invalid/expired/revoked authentication and `403` for authenticated
but insufficient authority; unauthorized resource lookups may return `404` to avoid
disclosure. Return `409` for last-administrator, setup, or version conflicts; `429`
for throttling; `503` for unavailable authorization storage. No failure returns
plaintext secrets. UI and CLI retain actionable redacted codes and request IDs.

## Acceptance criteria

- AC-01: Concurrent startup provisions one initial administrator. Initial login can
  only rotate the password or log out; API/CLI bypass attempts fail. Rotation requires
  a different password, atomically completes onboarding and invalidates all restricted
  sessions and the initial password. Missing credentials fail closed; restart and
  changed mounted passwords cannot reset ownership, even before first rotation.
- AC-02: Exercise every role-matrix row through API, UI, and CLI, including direct
  forbidden requests, list filtering, exports, diagnostics, and asynchronous reads.
- AC-03: Password/session/reset tests cover expiry boundaries, replay, CSRF, session
  fixation, throttling, user disablement, and concurrent last-admin removal.
- AC-04 (Google follow-up): Google tests reject bad issuer/audience/nonce/signature, reused invitations,
  uninvited users, email-only account linking, and incorrect signed domain claims.
- AC-05: Agent setup provides usable Docker/Helm instructions; rotation preserves
  identity/assignments and one-time display never becomes plaintext retrieval.
- AC-06: Producer keys cannot write another SLI, fetch secrets, or submit history
  without scope; role reduction and scope revocation affect retries as well.
- AC-07: Revocation races with ingest commit, socket dispatch, and lease renewal
  preserve the stated ordering and finite bounds, including disconnected agents.
- AC-08: Authorized delayed delivery with a replacement token succeeds; revoked
  tokens and withdrawn grants fail without producing coverage or revealing receipts.
- AC-09: Database/key outage fails closed; encryption rotation and restored backups
  preserve access, and logs/API/audit never expose fixture credentials.
- AC-10: Security mutations have atomic audit events and equivalent UI/CLI workflows;
  ordinary human tokens cannot exceed current roles or mint credentials. Scoped
  administrator automation works without prompts; demotion, target mismatch, scope
  expansion, and attempts to delegate automation authority fail.
- AC-11: Workers access only assigned work and associated notification secrets;
  expired claims, wrong work classes, and attempts to read datasource secrets fail.
- AC-12: Editor previews use existing datasource connection/authentication settings;
  arbitrary endpoint or credential overrides require Administrator.

## Dependencies and open questions

- REVIEW-I01: Confirm deployment-wide Viewer/Editor/Administrator roles and the
  restriction of source diagnostics/credentials to administrators, with Editor
  previews through existing datasources and explicitly scoped admin automation.
- REVIEW-I02: Confirm deployment-provided initial admin password with mandatory
  first-login rotation and restricted access until completion; no separate claim secret.
  Keep offline recovery and a local administrator. Google is a later increment with
  the proposed invitation-only linking contract, not a first-release gate.
- REVIEW-I03: Confirm provisional session, password, throttling, reset, machine-token,
  overlap, personal/automation-token, worker-token, and audit-retention defaults
  against operator workflows; confirm automation grant target/scope ceilings.
- REVIEW-I04: Review the one-hour offline execution limit with protocol ownership;
  it preserves collection during shorter outages but delays safe takeover and
  remote revocation. Longer outages intentionally leave gaps.
- REVIEW-I05: Confirm master-key operations and delayed-delivery grant withdrawal.
  Detailed endpoint/schema and deployment artifacts must be specified before code.
- REVIEW-I06: Evaluate Tink for Go as the preferred encryption/keyset candidate,
  against the standard-library AES-GCM alternative. Select and pin the library,
  ciphertext format, key-loading and rotation procedure in ADR-0004 before code;
  neither option removes Almanac's authorization, re-encryption or backup duties.

## Verification evidence

Not implemented. Acceptance criteria require concurrent database tests, a fake OIDC
provider for the later Google increment, controlled clocks, fault injection, secret-disclosure checks, and browser
checks in both themes/mobile layouts. Document inspection is not security validation.

## Change notes

- 2026-10-24: Proposed identity, onboarding, credentials, and authorization contract.
