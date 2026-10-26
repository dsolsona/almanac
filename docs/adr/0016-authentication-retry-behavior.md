# ADR-0016: Recover authentication state without automatic replay

- Status: accepted
- Created: 2026-10-26
- Accepted: 2026-10-26
- Approval: Maintainer approved checking session state and asking for sign-in when
  necessary after a lost login/password-change response, rather than resubmitting
  the action automatically, in the working conversation on 2026-10-26.
- Related specs: [SPEC-0004](../specs/0004-identity-and-access.md),
  [SPEC-0010](../specs/0010-api-and-cli.md)
- Supersedes / superseded by: narrows the authentication scope of
  [ADR-0009](0009-contracts-and-cli-parity.md); other contract decisions remain accepted

## Context

A connection can fail after login or password rotation commits but before the
client receives its response. These operations issue opaque session secrets;
PostgreSQL retains only their verifiers. Password rotation also invalidates the
previous password and sessions. Generic command receipts cannot reconstruct a
lost session cookie from its verifier.

The general API idempotency requirement needs an explicit authentication boundary
so clients do not assume a repeated login or password change replays the original
response safely.

## Decision

Authentication context, login, password change and logout do not use the 24-hour
management-command receipt mechanism. Reject a supplied Idempotency-Key explicitly
with501 `capability_unavailable`; do not silently ignore it or cache session secrets
for response replay. Normal management create/command receipts remain required.

After an uncertain login or password-change response, the client checks session
context. It must not automatically resubmit the mutation. If no usable session
remains, explain the uncertainty and ask the user to sign in again; after a password
change, the new password may already be active. A valid session alone does not prove
that a particular password change completed, so do not announce success solely from
that check. An unavailable context check preserves the unknown outcome.

Noninteractive CLI commands report the uncertain result and recovery instructions
without prompting, looping or replaying the mutation. Logout remains naturally
idempotent under its normal authorization and CSRF checks. UI and CLI share the
same server operations and recovery semantics.

## Alternatives considered

- Store recoverable authentication responses for replay. This adds a sensitive
  credential store, encryption/key lifecycle, and replay authorization after the
  original credential is invalidated. It is unnecessary for the selected behavior.
- Retry automatically without receipts. Login may create another session, while
  password rotation may already have invalidated the submitted proof. This obscures
  the outcome and cannot guarantee replay of the first operation.

## Consequences

No response receipt retains plaintext session secrets. A lost successful response
can require another deliberate sign-in. Clients must distinguish success, rejection
and an unknown outcome; they cannot infer rollback from a network error.
Tests must cover lost responses and verify that clients do not automatically repeat
authentication mutations. This decision does not claim those client flows exist yet.

## Revisit conditions

Revisit if a concrete automation requirement needs recoverable authentication
responses, with a separately reviewed secret-storage and replay-authorization design.
