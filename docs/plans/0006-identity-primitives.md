# Local identity primitives implementation plan

> For agentic workers: use superpowers:subagent-driven-development with one isolated
> Luna implementer. Coordinator owns review, Git operations and integration.

Goal: give local bootstrap/login a bounded password verifier and opaque session
secret implementation. This is one prerequisite, not a complete authentication flow.
Architecture: an internal identity package uses the Go Argon2id implementation
and standard cryptographic randomness/digests; it owns no network or database I/O.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md),
[ADR-0004](../adr/0004-identity-and-access.md).
Task: [F1-T06](../tasks/F1-T06.md).

## Global constraints

Go1.27.3; passwords contain15–128 Unicode code points without trimming or
normalization; Argon2id minimum19 MiB/two iterations/one lane. Session secrets
have256 bits of random entropy and only verifiers belong in persistence. No
authentication route or backend-mode enablement in this task. Approved command
families only; one implementer, coordinator-owned Git, no publishing.

## Review focus

- Persisted work factors or malformed encodings must not trigger arbitrary work.
- Cancellation must release admission without leaving detached hash goroutines.
- Unicode/space handling cannot silently change the user's password.
- Session encodings must be canonical and digests independently checked.
- Internal verifier checks do not replace application denylist, throttling,
  credential-epoch or transactional revocation enforcement in subsequent work.

## Task 1: Password and session-secret primitives

Read [F1-T06](../tasks/F1-T06.md) for exact signatures, encodings, ownership,
negative cases, fixed command selectors and benchmark requirements.

- [ ] Establish failing boundary, encoding and bounded-admission tests.
- [ ] Implement Argon2id verification and canonical random session secrets.
- [ ] Run focused/full/race checks and bounded benchmark; report evidence.
- [ ] Review, integrate, verify and reconcile remaining authentication scope.
