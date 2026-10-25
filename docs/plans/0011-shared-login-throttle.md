# Shared login admission implementation plan

> Use one isolated Luna implementer, independent task/final review and coordinator
> acceptance. Open a stacked PR; never merge it without maintainer authorization.

Goal: enforce shared account/source failure budgets while password checks execute
outside locks, before exposing login routes.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md), passwords and sessions.
Task: [F1-T11](../tasks/F1-T11.md).
Architecture: PostgreSQL attempt records, ordered transaction advisory locks,
generated SQL and explicit timestamps. No additional service or dependency.

## Global constraints

Five account failures and20 source failures per rolling15minutes. Pending checks
reserve capacity for one minute; expired results cannot authorize authentication.
Success frees only its reservation, never earlier failures. Unknown users take
the same storage path. Persist canonical-key digests, no passwords or raw input.
Bound cleanup and lock waits; preserve existing identity/session acceptance cases.
Only already approved fixed-purpose commands; never request new approvals.

## Task 1: Shared admission storage

- [x] Add schema4 and real-database acceptance tests.
- [x] Implement begin/finish operations and generated queries.
- [x] Verify concurrency, rolling boundaries, completion and bounded cleanup.
- [x] Run focused/integration/race and combined gates; review independently.
- [x] Record acceptance and publish [PR #7](https://github.com/dsolsona/almanac/pull/7).
