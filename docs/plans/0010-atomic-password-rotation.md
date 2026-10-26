# Atomic password rotation implementation plan

> Execute with one isolated Luna implementer, independent review and coordinator
> acceptance. Publish a stacked PR; do not merge it.

Goal: implement the transaction underlying first-password rotation and ordinary
local password changes before exposing authentication routes.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md), AC-01/03/09/10.
Task: [F1-T10](../tasks/F1-T10.md).
Architecture: generated PostgreSQL queries and installation→user→session locking,
with supplied server time and existing credential revision/session invalidation.

## Global constraints

Go1.27.3 and PostgreSQL18.6. No new schema or dependencies. No plaintext passwords
or raw session secrets in storage. No hashing under locks. Password, revision,
session replacement, initial completion and audits commit together. Old digests
must never become usable through replacement. External password proof and full
creation policy remain caller obligations; no public API in this task.
Only existing approved fixed-purpose commands; never request new approvals.

## Task 1: Atomic rotation storage

- [x] Add failing acceptance cases for initial and ordinary rotation.
- [x] Implement validated rotation and generated transaction queries.
- [x] Verify concurrency, authority rejection, digest collision and audit rollback.
- [x] Run focused, PostgreSQL/race and combined gates; obtain independent reviews.
- [x] Reconcile evidence and open [PR #6](https://github.com/dsolsona/almanac/pull/6).
