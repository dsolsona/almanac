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

## The development container

Everything runs under Docker ([ADR-0029](adr/0029-platform-agnostic-development-and-verification.md)).
`make setup` builds the development image for the current user, pulls the fixture
images, fails when an image's platform differs from the daemon's, and warms the
Go module and npm caches in named volumes; it is the only network-capable step.
`make shell` starts the tmpfs PostgreSQL and VictoriaTraces fixtures from
`compose.dev.yaml` and opens a shell in the container (`CMD='…'` runs one
command; Make expands `$`, so write `$$` for a literal dollar sign).
`make stop-dev` removes the fixtures; the caches stay.

`make verify [MODE=…]` from the host enters the container and runs
`python3 scripts/verify.py gate MODE`; inside the container it runs the script
directly. The database URL is preset in the container and is never passed on the
command line. `MODE=compose` and `MODE=providers` add `compose.dev.socket.yaml`,
which mounts the daemon socket, the checkout at its host path and the daemon
host's network; no other mode receives the socket. `VERIFY_MODE` remains an
accepted alias of `MODE` until the catalog below is re-approved.

## Recorded verification invocations

On 2026-10-29 the maintainer approved a closed catalog of host-side modes
(`verification-tools`, `slo-unit`, `slo-integration`, `slo-journey`,
`agent-lifecycle`, the `linux` lane and the host toolchain setup). Plan 0026
retired those modes and the host toolchain on 2026-11-05; their selections are
now `unit`, `integration INTEGRATION_TEST=…` and `journey BROWSER_SCENARIO=…`.
The invocations below are the recorded set for the Docker-only workflow. They
await the maintainer's re-approval (F7-T04); recording them here does not
approve them, and other selectors and command forms retain the exact-invocation
rule.

The controls live in `scripts/verify.py`. An active local
`.local/verification/task.json` enables task-wide budgets; worker checkouts share
the same state through a symlink. Source-bound pass/fail receipts are retained in
the canonical task state's `receipts/` directory, and every receipt records the
platform it ran on (OS, architecture, dev image id, Git revision with a dirty
flag). Missing, failed, stale or incomplete evidence prevents a ready handoff.
The local files remain editable; the guard prevents accidental loops and does
not establish a security boundary.

Every completed verification run refreshes the canonical project-root
`.local/verification/timing-ledger.json` and `timing-ledger.md`, including runs
from worker checkouts. `make verification-report` also refreshes and prints timing
totals before checking handoff readiness; failed readiness still returns nonzero.
The ledger deduplicates shared receipts, retains failures and reports per-mode run
counts, invocation elapsed time and overlapping wall time counted once. Invocation
time includes build/startup/cleanup. History without receipts is incomplete.
For checks outside these targets, append a record to the fixed local
`.local/verification/manual-runs.jsonl` with a unique `id`, `command`, `result`,
`started_unix`, `ended_unix` and `elapsed_seconds`; use null for unknown timings.
This file records evidence only and never executes its command text.

```sh
make setup
make shell
make shell CMD='go version'
make stop-dev
make verify
make verify MODE=unit
make verify MODE=unit TEST_FILE=tests/agents-client.test.tsx
make verify MODE=browser
make verify MODE=browser TEST_FILE=browser/catalogue.spec.ts
make verify MODE=browser TEST_FILE=browser/controls.spec.ts BROWSER_PROJECT=mobile-dark
make verify MODE=go
make verify MODE=go GO_PACKAGE=./internal/storage
make verify MODE=go-race
make verify MODE=auth-bench
make verify MODE=smoke
make verify MODE=integration
make verify MODE=integration INTEGRATION_PLAN=1
make verify MODE=integration INTEGRATION_TEST=TestIntegrationExecutableAgentManagementRouteAndSessionLifecycle
make verify MODE=integration INTEGRATION_SELECTION=all
make verify MODE=integration GO_PACKAGE=./internal/storage
make verify MODE=journey BROWSER_SCENARIO=onboarding
make verify MODE=journey BROWSER_SCENARIO=agents-lifecycle
make verify MODE=journey BROWSER_SCENARIO=slis
make verify MODE=providers
make verify MODE=compose
make verification-init VERIFICATION_PLAN=.local/verification-plan.json
make verification-report
make verification-handoff
make generate GENERATE_MODE=api
make generate GENERATE_MODE=go-guidelines
make generate GENERATE_MODE=format
make generate GENERATE_MODE=sqlc
make build
make images
make publish IMAGE_REGISTRY=<prefix>
python3 scripts/check-docs.py
python3 scripts/check-image-pins.py
```

`make generate`, `make build` and the Go-guideline listing run inside the dev
container (`make shell CMD='make generate GENERATE_MODE=go-guidelines'`);
`make verify` enters it by itself. `make publish` needs a registry prefix the
maintainer names. The journey selectors are the documented browser journeys
(`onboarding`, `lost-login`, `lost-password`, `agents`, `agents-close`,
`agents-lost`, `agents-unavailable`, `agents-lifecycle`, `slis`); a journey run
fails when the selected named journey does not execute and pass, and it does not
replace the full integration gate. Fixture credentials belong only to the isolated
fixture database inside the container. Keep real credentials out of commands,
logs and published artifacts.

Make suppresses an inherited `GOROOT` so the dev image's Go driver cannot select
another installation's compiler; the Go gate prints both versions before test
discovery.

Local Compose startup (`make run`) requires host Docker Compose, Make and curl. It
checks host HTTPS readiness with the application's public certificate before
reporting success; a failed check preserves the running services and volumes.

Git publication operations must likewise match current approval rules and existing
user authorization. Do not merge PRs merely because publishing them was authorized.

## Selective integration checks

The integration entry defaults to affected-case execution and compatible per-case
pass reuse. Its fixed selectors accept one discovered test name, a fixed package,
or explicit full execution; they do not accept arbitrary Go flags, regular
expressions, commands or database URLs. Every run checks the fixture's identity
on the live server (fixed database, role, PostgreSQL 18.6, loopback port 15487)
and reuses a pass only while the fixture's start time, the toolchain and every
input file of the case are unchanged; a restarted tmpfs fixture executes
everything afresh.

Every run prints its selection before executing. The latest coverage report is
`.local/integration-report.json`; immutable per-run reports and per-case Go events
live under `.local/integration-runs/`. Reused cases retain their original
provenance and are not counted as new executions or new test time. Interrupted and
failed cases run again on the next selection. `INTEGRATION_PLAN=1` performs normal
preflights and discovery but executes no integration cases; its receipt is
explicitly `planned`, never a passing gate.

## Approved controlled capacity entry

Current phase restriction, 2026-11-04: the maintainer rejected further deliberate
endurance waits and approved bounded T08 final checks. The entry below still
contains two 15-minute phases and real-clock/maintenance observations. It must
not run during T08 finalization, directly or through another mode. Retain its
failed/cancelled receipts; do not report the endurance workload as passed. Further
execution belongs to explicitly scoped [F3-T10](tasks/F3-T10.md) work. Bounded
manual checks use the existing isolated fixture and native approved commands,
record independent expectations/results and append their timing to the manual
ledger. Final integrated gates remain separate from endurance observation.

On 2026-11-02 the maintainer explicitly approved this entry for F3-T08, including
reruns only after diagnosing and fixing a failure; its invocation is now the one
below, inside the dev container, and awaits re-approval with the catalog above.
Record the failure, correction and replan before a rerun. Ordinary, integration
and Compose verification do not launch this separate workload:

```sh
make verify MODE=performance
```

On 2026-11-03 the maintainer narrowed this phase to ten SLOs with a year of history
and deferred larger tests to [F3-T10](tasks/F3-T10.md). The same approved invocation
now requires only that fleet; it must not start the 100/1,000-SLO loads or repeat
the preliminary one-SLO imports during this phase.

It requires the fixture of `compose.dev.yaml`, rejects custom test/Go selectors,
builds the candidate and runs the separately tagged ten-SLO/year capacity entry.
The workload retains its eight-hour execution bound; the Go process has
30 additional minutes for setup/cleanup. Existing disk admission, private normal
Compose deployments, independent numerical checks and VictoriaTraces evidence
remain mandatory. Required fleet cases must all run and pass, without skips.
Source-bound receipts and the timing ledger use the existing controls. Go events
and exit status are retained in `.local/performance-go-test.jsonl` and
`.local/performance-go-test.exit`; each fleet keeps its own resource/stage report.
Successful fleets clean up their owned containers, volumes, images and private
runtime directory. A failed fleet retains its owned deployment and protected
`.local/backend-performance-runtime-*` directory for diagnosis, including fixture-only
credentials. Do not publish that directory. Record the project/path and clean up
only those owned resources after diagnosis; preserve the operator's project/data.
Compile-only checks in the Go gate do not establish capacity acceptance.

The same fixed entry can reuse an explicitly recorded year dataset through local
`.local/performance-resume.json` metadata keyed only by `10` for this phase.
Each record names its original failed report, private runtime directory, owned
`almanac-perf-` project and five explicit loopback ports. Workspace paths, container
ownership/bindings, original year import and fleet identities must match before
reuse. The candidate is rebuilt and migrated normally; source TLS trust is renewed
through authenticated management, and real agents/readers reconnect. Acceptance
runs again on the candidate. Previous passing latency evidence is not substituted.
Required ten-SLO acceptance and the eight-hour limit remain unchanged. Larger
acceptance is deferred, not passed. There is no command, SQL, test-selector or
database-URL passthrough.
Source certificates cover the bounded run plus cleanup; ordinary short fixtures
retain their existing certificate lifetime and all TLS verification remains enabled.
