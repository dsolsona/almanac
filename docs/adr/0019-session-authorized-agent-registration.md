# ADR-0019: Register agents using the administrator session

- Status: accepted
- Created: 2026-10-27
- Accepted: 2026-10-27
- Approval: The maintainer explicitly selected “Use the signed-in administrator session” for agent registration, retaining role checks, CSRF protection and auditing.
- Related specs: [SPEC-0004](../specs/0004-identity-and-access.md), [SPEC-0010](../specs/0010-api-and-cli.md)
- Supersedes / superseded by: Narrows the credential-issuance policy under [ADR-0004](0004-identity-and-access.md) for agent registration only; extended by [ADR-0020](0020-session-authorized-management.md).

## Context

Registering an agent currently asks an already signed-in administrator to provide
the password again. This adds friction to routine setup and makes recovery from
an unrelated request failure look like an authentication problem. An agent's
initial credential is part of its registration operation.

## Decision

An active, unrestricted administrator session authorizes agent registration and
issuance of its initial credential. Do not require an additional password or a
five-minute reauthentication window. Apply this rule to UI, API and CLI, including
receipt replay. Recheck current session and administrator authority in the storage
transaction, retaining installation epoch, expiry, revocation, account status and
credential-revision checks.

Browser requests retain same-origin, secure-transport and CSRF protections.
Registration remains audited and idempotent. Return the initial token once, only
after a fresh successful commit. Receipt replay never returns that token again.
Missing, restricted, expired or revoked sessions and non-administrators remain
unauthorized. Ordinary personal tokens gain no credential-issuance permission.

Remove `current_password` from the registration request and its UI/CLI inputs.
Credential rotation, revocation, disablement, other credential kinds and automation
grant creation retain their existing authorization policy.

## Alternatives considered

- Requiring the password for every registration provides another authentication
  check but interrupts routine administration and complicates retry recovery.
- Accepting only a login within five minutes reduces prompts but introduces a
  separate freshness threshold and still interrupts a valid administrator session.

## Consequences

Registration becomes a normal authenticated administrator operation. A stolen
active administrator session can register an agent without also knowing the
password; session protection, current authorization and auditing remain essential.
The API request schema and CLI registration flags change together with their
generated clients and acceptance checks. This decision does not establish the
cause of any reported registration failure; failure diagnosis and recovery must
be verified separately.

## Revisit conditions

Revisit if deployment requirements call for configurable step-up authentication
or evidence shows that the accepted administrator-session boundary is inadequate.
