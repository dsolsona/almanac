# Command discipline

The maintainer requires uninterrupted work using already-approved commands.
These rules apply to the coordinator and every implementer, including future sessions.

- Check the current environment's approval rules before execution. This document
  records known invocations; it cannot grant execution permission.
- Reuse an approved invocation exactly. Similar prefixes, changed Make arguments,
  environment placement, shell flags, redirection and wrappers are not equivalent
  approval. Do not probe an uncertain command by submitting an approval request.
- Do not add arbitrary execution targets or use another tool to bypass restrictions.
  Fixed-purpose build/test tooling can be improved, but approval for a new invocation
  is a separate prerequisite. A newly documented command is not automatically approved.
- During unattended work, leave unavailable commands pending and continue independent
  authorized work. Do not ask for new approval. Include the exact usable checks in
  each worker brief, and report checks left unrun honestly.
- A sandbox failure does not justify inventing a new wrapper. Check existing allowed
  invocations first. If execution cannot proceed within them, defer that operation.

## Recorded verification invocations

These invocations have been approved in the current development environment. Recheck
availability in a new environment; do not extrapolate to other selectors or paths.

```sh
make verify
make verify VERIFY_MODE=compose
make verify VERIFY_MODE=go-race
make verify VERIFY_MODE=unit TEST_FILE=tests/agents-client.test.tsx
make verify VERIFY_MODE=unit TEST_FILE=tests/agents-page.test.tsx
make verify VERIFY_MODE=unit TEST_FILE=tests/auth-page.test.tsx
make verify VERIFY_MODE=browser TEST_FILE=browser/catalogue.spec.ts
make generate GENERATE_MODE=go-guidelines
make generate GENERATE_MODE=format
make generate GENERATE_MODE=sqlc
python3 scripts/check-docs.py
/usr/bin/bash -c 'make verify > .local/full-verification.log 2>&1'
/usr/bin/bash -lc 'make verify VERIFY_MODE=integration ALMANAC_TEST_DATABASE_URL=postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable'
/usr/bin/bash -lc 'make verify VERIFY_MODE=journey BROWSER_SCENARIO=onboarding ALMANAC_TEST_DATABASE_URL=postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable'
```

The onboarding invocation selects a real HTTPS/PostgreSQL browser journey. It passed
on the F2-T16 candidate and checks that the named journey actually executed; it does
not replace the full required integration gate.
Fixture credentials above belong only to the isolated local acceptance database.
Keep real credentials out of commands, logs and published artifacts.

Local Compose startup requires host curl as well as Docker Compose and Make. It
checks host HTTPS readiness with the application's public certificate before
reporting success; a failed check preserves the running services and volumes.

Git publication operations must likewise match current approval rules and existing
user authorization. Do not merge PRs merely because publishing them was authorized.

## Linux verifier entry points

The project defines these fixed-purpose commands for the isolated Linux lane. Their
presence here does not grant execution approval; check the active environment's exact
command approval before running them.

```sh
make setup SETUP_MODE=verification
make verify VERIFY_MODE=linux
make verify VERIFY_MODE=linux-sli
```

Setup may download pinned tool and browser dependencies into a local Docker image.
Verification is offline and rejects a stale setup fingerprint. It does not accept
command passthrough, environment forwarding, a custom database URL, or a host Docker
socket mount. The catalogue browser matrix and real integration journeys are part of
the fixed sequence; the existing host Compose journey remains separately selectable.
The focused SLI browser journey uses the fixed `BROWSER_SCENARIO=slis` selector,
which maps to `TestIntegrationExecutableBrowserSLICollectionJourney` in the integrated
candidate. The `linux-sli` mode uses the same isolated database fixture, candidate
snapshot, image fingerprint, cleanup and sanitized artifact export as the full Linux
lane, but executes only that real journey. It is focused evidence and does not replace
the full gate. These selector descriptions do not approve a new invocation.
