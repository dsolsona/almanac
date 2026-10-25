# Local authentication service implementation plan

> One isolated Luna implementer; coordinator owns interface decisions, review,
> integration and stacked PR publication. No PR merges.

Goal: connect verified storage and password primitives into a real local
authentication flow before public transport exposure.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md).
Task: [F1-T12](../tasks/F1-T12.md).
Architecture: a backend application service over existing PostgreSQL operations,
one bounded password hasher, explicit clock and protected-file adapter.

## Global constraints

No new dependencies/schema. No public routes or authentication bypasses. Bootstrap
never reapplies a file after ownership exists. Current-password proof, complete new
policy and fresh time precede authoritative issuance/rotation. Comparable unknown
user work, shared admission and no secrets in persisted state or diagnostics.
Tests use real persistence and hashing for the journey; preserve existing evidence.
Only approved fixed-purpose command families, no new approvals or direct compilers.

## Task 1: Authentication orchestration

- [ ] Extract the protected-file and disposable-database helpers without regressions.
- [ ] Implement bootstrap/login/rotation/authenticate/logout with bounded work.
- [ ] Verify real journey, admission, proof races, failure and secret boundaries.
- [ ] Run focused/combined integration/race and full gate; review independently.
- [ ] Record evidence and open a stacked PR.
