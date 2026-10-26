# Combined backend and embedded UI runtime implementation plan

> Use subagent-driven development with one isolated Luna implementer. Coordinator
> owns reviews, real fixture execution, integration and publication; no PR merges.

Goal: serve the existing production UI assets and authentication API from one
backend-owned HTTPS listener, ready for subsequent browser onboarding screens.
Architecture: reuse the backend lifecycle and a shared asset handler; no internal
HTTP proxy or duplicate database access. Existing UI-only deployment remains.
Tech stack: pinned Go1.27.3, net/http, embedded React assets, PostgreSQL18.6.
Spec: [SPEC-0013](../specs/0013-runtime-and-operations.md) and
[F1-T16](../tasks/F1-T16.md), under accepted ADR-0008/0011.

## Global constraints

- Only the backend accesses PostgreSQL.
- No combined server mode includes external collection.
- Reject unknown/duplicate component names and unsupported selected components.
- Preserve authentication, single ownership and bounded shutdown contracts.
- No new dependencies, frontend behavior, catalogue exposure or weakened checks.
- Use existing approved commands; no new approval requests or generic wrappers.

## Review focus

- Component ordering must not change security validation or silently omit alerts.
- Static fallback must not swallow API errors, unknown paths or encoded traversal.
- Backend-only serving must not accidentally acquire a public asset route.
- Combined requests must reach the same authenticated handler and ownership lease.
- Asset serving must not add a second listener, proxy hop or database dependency.

## Task 1: Shared assets and combined serving

Exact files/interfaces and acceptance cases: [F1-T16](../tasks/F1-T16.md).

- [x] Write and run focused failing config/asset/constructor checks for AC1/2.
- [x] Extract the shared asset handler and wire the explicit combined pair.
- [x] Extend real executable setup/lifecycle checks for combined mode, retaining
  the backend-only cases and all existing security/drain assertions.
- [x] Run focused checks and compile tagged tests; submit the frozen candidate.
- [x] Coordinator runs full/race/PostgreSQL/docs checks and independent reviews.
- [x] Record verified evidence and publish stacked on the setup CLI PR.

Published as [PR #13](https://github.com/dsolsona/almanac/pull/13), stacked on the
setup CLI PR. Candidate and acceptance evidence are recorded in F1-T16.

This increment does not complete combined alerts, secure remote UI authentication,
browser onboarding, or full F1 acceptance.
