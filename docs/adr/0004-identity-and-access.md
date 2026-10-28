# ADR-0004: Identity and access

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related specs: [SPEC-0004](../specs/0004-identity-and-access.md),
  [SPEC-0002](../specs/0002-agent-behavior.md), and
  [SPEC-0003](../specs/0003-ingestion-and-recovery.md)
- Supersedes / superseded by: Agent registration authorization is narrowed by
  [ADR-0019](0019-session-authorized-agent-registration.md); repeat password and recent reauthentication requirements are superseded by [ADR-0020](0020-session-authorized-management.md); process-replacement waiting is superseded by [ADR-0022](0022-restartable-agent-ownership.md). Other identity decisions remain in force.

## Context

A single-tenant deployment still needs several human users and independently
revocable agents and producer jobs. First-run password creation must establish
ownership without allowing the first unauthenticated visitor to seize a new server.
Remote agents need source credentials, while producer jobs need narrowly scoped
SLI definitions and ingestion. These authorities must remain distinguishable.

Local authentication must work without Google. Google authentication establishes an
external identity but does not decide membership or administrator privileges.
Persistent sessions and remote execution outlive individual HTTP requests, making
revocation a state change with explicit ordering and propagation limits.

## Decision

Propose database-backed local identities, opaque sessions, fixed human roles, and
separate resource-scoped Agent, Producer, and Worker principals as specified in
SPEC-0004. Its detailed
security and lifetime defaults are accepted starting values, subject to verification.
The originally deferred library selection in REVIEW-I06 is resolved by
[ADR-0017](0017-recoverable-secret-encryption.md), accepted on 2026-10-26.

Provision one initial administrator in the database at first startup using a
password supplied through a protected deployment secret file. Deployment tooling
may generate it randomly or the operator may set it explicitly. The first login
can only set a different permanent password or log out; application access, user
management and token creation remain unavailable until that change completes.

This replaces the separate claim-secret proposal: the operator handles one initial
password and the same login/password-change flow used elsewhere. The UI still
provides onboarding by guiding that mandatory change. Store the initialization
state transactionally so concurrent startup creates one owner and restarts never
reapply a deployment password. Persist password verifiers rather than plaintext;
keep a local administrator and an explicit offline recovery path.
Use Argon2id for human passwords and fast verifiers for high-entropy random tokens.
Browser sessions use protected cookies and independent CSRF protection. Protected
CLI setup uses the same restricted login and password-change API noninteractively. Administrator-issued
automation grants authorize bounded sensitive operations without repeated browser
interaction; ordinary personal tokens cannot mint credentials. Grant creation and
expansion require reauthentication, and current administrator authority is checked
on every automated operation. Automation user administration excludes administrator
creation, promotion, reset, and authentication-link changes that could escalate its
own authority. Password changes, resets, disablement, and recovery invalidate all
affected sessions, automation/personal tokens, and pending authentication challenges.
Worker scopes cover assigned private-API claims and
results; only delivery claims grant access to associated notification secrets.

Deliver database-backed local users first. Google SSO remains planned, but does not
block the initial release: it adds provider configuration, callbacks, admission and
account-linking behavior that can follow a useful local-user product. Keep stable
local user IDs so external identities can be linked later without replacing users.

For that later increment, use invitation-only Google OIDC, bind accounts by issuer
and subject, and require
existing-account authentication before linking another login. Treat email and
Workspace-domain claims as admission checks, not stable local identity or authority.
Use the official [Google OIDC contract](https://developers.google.com/identity/openid-connect/openid-connect)
for validation and [OWASP password storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html)
for password-verifier selection.

Authorize every request against current database state; serialize writes with
revocation. Agent sockets revalidate work and lease renewal and close within ten
seconds of revocation on a healthy backend. An outstanding offline execution lease
may last one hour: it preserves collection through transient outages and also bounds
cooperative remote revocation. Ownership changes immediately fence old submissions;
normal takeover waits for execution expiry. Force takeover exposes possible query
overlap for explicit acknowledgment. A credential's expiry/revocation never grants
an exception for ingestion retries, even if the underlying work was once authorized.
Expired execution leases can retain delivery authority only for buffered live work;
historical submissions require a current chunk lease and generation.

Encrypt recoverable source and destination secrets with deployment-managed keys
outside the database. Separate credential verifiers from recoverable secrets.
Restore activation rotates an installation credential epoch to invalidate all
restored credentials/challenges, establishes administrator access through offline
recovery, and reissues machine credentials while isolated. Reconcile old execution
authority before reopening networking; restored database state cannot cancel agents.
Record security mutations and redacted audit success atomically; management APIs
cannot retrieve stored plaintext secrets. UI and CLI share authorization contracts.

## Encryption implementation candidate

The following evaluation records the original rationale. The maintainer selected
Tink and its integration format in [ADR-0017](0017-recoverable-secret-encryption.md).

Prefer evaluating [Tink for Go](https://github.com/tink-crypto/tink-go) for the
recoverable-secret adapter: it provides authenticated encryption and keysets for
rotation, reducing application-owned cryptographic plumbing. With the proposed
SQL-based persistence layer, Almanac still owns the calls at write/authorized-read
boundaries, resource/field binding, redaction and the resumable re-encryption job.
At the time of this evaluation, the dependency and ciphertext format remained
deferred under REVIEW-I06; ADR-0017 now resolves that choice.

The standard library's
[AES-GCM with random nonces](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce)
is a smaller-dependency alternative, but leaves key identifiers, keyset management
and rotation integration to Almanac. Neither approach requires a cloud service.
Keep keys outside the database; library-managed rotation does not replace secure
key provisioning, backup or old-key retirement checks.

## Alternatives considered

| Alternative | Tradeoff |
| --- | --- |
| Separate claim secret followed by password creation | Protects initial ownership but adds a second secret and a dedicated claim flow; the proposed restricted initial login provides the same ownership check |
| Open first-visitor onboarding | Convenient, but exposes initial ownership to network races |
| Google-only authentication | Removes local password operations but prevents independent onboarding and outage recovery |
| Automatic email linking or domain-wide registration | Simplifies admission while conflating an external claim with local membership |
| Self-contained long-lived bearer claims | Reduces database reads but complicates prompt role changes and revocation |
| Human administrator tokens for collectors | Simplifies initial wiring while granting unrelated management authority |
| Immediate offline stop or 60-second lease | Faster source-access cutoff, but sharply reduces useful outage buffering |
| Indefinite offline execution | Preserves collection but prevents a finite revocation or safe reassignment guarantee |
| Encrypt every credential, including login passwords | Makes credentials recoverable unnecessarily and increases breach consequences |

## Consequences

Database availability becomes necessary for authorization; no stale cached allow
is used during failure. Short transactions must order revocation and protected writes
without holding locks across network calls. Secret rotation, recovery, and audit
retention are operational responsibilities, not just API settings.

The one-hour proposal trades faster safe takeover and remote cutoff for continuity.
A compromised agent may retain decrypted credentials, so source-system credential
rotation remains necessary. When Google SSO is delivered, OIDC account suspension does not revoke existing local
sessions automatically. Fixed deployment-wide human roles are intentionally coarse;
producer, agent, and worker scoping provide the required machine isolation.
Administrator automation tokens carry consequential authority and therefore need
explicit target/scope ceilings and lifecycle audit; broad personal tokens are not
an alternative. Audit retention follows the 400-day storage default.

## Revisit conditions

Revisit when operators require per-service human access, additional identity
providers, MFA, compliance-specific hashing, faster remote revocation, external
secret managers, or independently scalable authorization. Review SPEC-0004's
REVIEW-I01–I06 assumptions before implementation, especially the offline lease and
credential expiration defaults. This proposal does not claim security verification.
