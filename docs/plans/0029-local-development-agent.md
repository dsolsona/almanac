# Local development agent implementation plan

**Goal:** Seed `local-dev` once and start it with the local app thereafter.

**Contracts:** [ADR-0037](../adr/0037-local-development-agent.md),
[SPEC-0013](../specs/0013-runtime-and-operations.md),
[ADR-0033](../adr/0033-simple-test-suite.md).

**Approach:** Use the existing PostgreSQL container's shell, OpenSSL and psql for
dev-only seeding. Add a private agent volume and configure the existing profiled
agent service. A host script seeds or starts that service through Compose.
No product Go code, schema migration, dependency or production image change.

## Acceptance and review focus

- Fresh `make agent-seed` prepares secrets/schema without onboarding or passwords.
- Repeat seeding preserves one identity, one credential and the same private token.
- An existing unrelated `local-dev`, unsafe files or invalid saved authority fail
  visibly without overwriting records, credentials or onboarding.
- Failed seeding can resume using its pending token, without losing issued authority.
- `make run`/`make start` starts the seeded agent using TLS and without database secrets.
- A first app without a seed still starts normally; onboarding remains mandatory.
- Stop/start and app recreation reconnect the same agent; the UI reports connected.
- Agent and application logs/inspection contain no plaintext token.
- Operator preview and unrelated edits remain untouched; verification owns isolated fixtures.

## Implementation and checks

- [x] Add a real-stack regression in `web/e2e/local-dev-agent.spec.ts`, with seed/start
  command support in `web/e2e/support/stack.ts`. Observe failure before implementation.
- [x] Implement `deploy/local/agent-seed.sh`, `scripts/local-dev-agent.sh`, the
  `agent-seed` Make target and seeded startup. Configure the agent volume and service
  in `compose.yaml`; keep `make agent` compatible.
- [x] Run `make test` and `make check`; diagnose failures before any rerun.
  The maintainer waived E2E execution on 2026-11-08; the added journey remains unverified.
- [x] Update README, command catalog, runtime spec and decision index. Review the
  complete diff. The maintainer authorized commit and pull-request publication
  on 2026-11-08; merging remains unauthorized.

## Evidence

- Before implementation, the focused journey failed at the new `make agent-seed`
  invocation because the target did not exist; onboarding passed. Log:
  `.local/local-dev-agent-red.log` (2.7 seconds of Playwright execution).
- The first `make test` stopped at documentation validation: the new accepted ADR
  was missing from its index. Added the index entry; no check was bypassed.
- Compose configuration validation passed without starting services.
- No E2E run against the implementation was started; the maintainer waived it.
- `make check` passed: the new four-case orchestration check, 385 web unit tests,
  all Go unit/integration packages, 47 catalogue checks and production asset checks.
  Duration: 149 seconds. Its full output is `.local/local-dev-agent-check.log`.
- Review confirmed PostgreSQL 18 ignores the argument in `\quit 3` and exits zero.
  Replaced it with a transaction-aborting SQL exception under `ON_ERROR_STOP=1`;
  the existing collision/revocation journey covers that failure path but remains
  unrun. Final `make test` passed in 26 seconds after this correction, including
  the four orchestration cases, shell syntax, documentation/generated checks,
  385 web cases and Go units: `.local/local-dev-agent-final-unit.log`.
- ADR-0035/plan 0027 are already used on `origin/main`; the active service-catalog
  branch uses ADR-0036/plan 0028. This change uses ADR-0037/plan 0029 to avoid collision.
- Implementation is ready for maintainer review; real-stack seed, first-use,
  connection and restart acceptance remain unverified under the E2E waiver.
- Publication preparation: merged current `main` (`7582b31`) in the isolated
  `feat/local-dev-agent-pr` checkout, retaining both sets of documentation additions.
  `make check` passed in 196 seconds with 425 web units, all Go suites and 75
  catalogue/browser cases. Log: `/tmp/almanac-local-dev-agent-pr-check.log`.
- Independent review found that noncanonical Base64 pad bits could pass seed
  verification while the agent parser rejected the token. Added the same decode/
  encode equality check as the product parser, plus an E2E rejection case. That
  regression remains unrun under the waiver. Final `make test` passed in 26
  seconds: `/tmp/almanac-local-dev-agent-pr-final-unit.log`.

## PR #69 CI follow-up

- CI run `42488253074` on `512b6f0` passed unit, integration and catalogue suites,
  but E2E failed after onboarding: the new connection assertion dereferenced
  `body.agent.runtime`. The GET contract returns the agent directly; only the
  frontend client wraps that value with its ETag. The performance seed already
  reads `body.runtime`. One test failed and thirteen dependent cases did not run;
  the original log is `/tmp/almanac-pr69-e2e-failed.log`.
- Corrected the assertion to read `body.runtime`, use the existing `Agent` type,
  and require HTTP 200 through `expectOk`. Local E2E remains waived; CI verifies
  the actual connection and the later restart/revocation cases after publication.
- The downloaded CI trace confirms HTTP 200 with `runtime` at the response root
  and `reachability: connected`; seeding and initial connection succeeded before
  the test's incorrect property access. `make test` passes after correction in
  22 seconds: `/tmp/almanac-pr69-e2e-assertion-unit.log`.
