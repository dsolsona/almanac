# Noninteractive setup implementation plan

> One isolated Luna implementer; coordinator owns interfaces, real-fixture execution,
> integration and publication. Use subagent-driven development; no PR merges.

Goal: complete initial administrator onboarding through the verified HTTPS API using
protected password files, with truthful handling of lost responses.
Spec: [SPEC-0004](../specs/0004-identity-and-access.md),
[SPEC-0010](../specs/0010-api-and-cli.md); exact scope/interfaces in
[F1-T15](../tasks/F1-T15.md). Architecture: generated API client operations with a
bounded TLS/cookie workflow, invoked by the existing CLI command boundary. No DB
access from the setup client, profile storage or UI work. Generated client output
uses its pinned runtime support library under the existing generator architecture.

Review focus: credentials crossing redirects/logs; false success after lost response;
ordinary-password rotation through repeated setup; partial file validation after
network side effects; cleanup errors hiding the actual setup outcome.

## Task 1: Noninteractive setup client and command

- [x] Write failing client/command boundary tests for the named acceptance cases.
- [x] Enable generated client operations and implement bounded verified TLS workflow.
- [x] Add protected-file command inputs, safe JSON/text results and exit mappings.
- [x] Exercise lost-response behavior without replay or inferred completion.
- [x] Verify the real executable setup journey against PostgreSQL/backend/TLS.
- [x] Run integrated checks, review actual diffs, correct findings and record acceptance.
- [x] Publish the verified increment stacked on the backend runtime PR.

Published as [PR #12](https://github.com/dsolsona/almanac/pull/12), based on the
backend runtime PR. Product candidate and evidence are recorded in F1-T15.

Preserve preview5198 and serialize the existing PostgreSQL fixture. General CLI login,
profiles, browser onboarding and combined serving remain separate increments.
