# Initial-owner persistence implementation plan

> Use subagent-driven development with one isolated Luna implementer. The
> coordinator owns review, Git operations, acceptance and stacked PR publication.

Goal: make provisioning the initial administrator atomic, concurrent-safe and
idempotent without exposing authentication operations prematurely.
Architecture: PostgreSQL singleton row locking orders provisioning; generated SQL
persists user, redacted audit and installation marker in the same transaction.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md), AC-01/09/10.
Task: [F1-T07](../tasks/F1-T07.md).

## Global constraints

PostgreSQL18.6, Go1.27.3, one tenant, no raw credentials in results/audit/errors.
No hashing while holding database locks. Schema upgrades remain explicit and
immutable. Restart does not reset credentials or complete onboarding. No public
routes, startup provisioning, password policy or authentication flow in this task.
Approved command families only; coordinator-owned Git; no PR merge.

## Review focus

- Failed audit insertion must roll back the user and initialization marker.
- Concurrent initialization must return one consistent persisted administrator.
- Missing schema/storage must not appear to be an unprovisioned installation.
- Repeated calls with empty/changed input must not reset persisted credentials.
- Updating migration tests must preserve real rollback/newer-history coverage.

## Task 1: Atomic owner storage

- [x] Add failing strict-verifier and PostgreSQL acceptance cases from F1-T07.
- [x] Add immutable schema, generated queries and bounded transactional operations.
- [x] Verify upgrade-from-v1, failure rollback, cancellation and repeated calls.
- [x] Run focused checks and isolated PostgreSQL integration; preserve evidence.
- [x] Review actual diff and run combined checks; ready for the stacked PR.
