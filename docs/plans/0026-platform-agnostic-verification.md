# Docker-only development, verification and images

- State: accepted 2026-11-04 (Crit session `d21016424a44`); F7-T01 to F7-T04 in review (catalog re-approval and Linux-host evidence pending); the Compose verifier's tracing-overhead gate retired by [ADR-0030](../adr/0030-browser-tracing-overhead-is-not-an-acceptance-check.md)
- Created: 2026-11-04
- Scope: tooling, images, fixtures and documentation; no product behaviour changes
- Base: `main`; independent of the open UI stack (PRs #54–#60), which still cites the retired modes until merged
- Owner: primary agent
- Governing records: [ADR-0029](../adr/0029-platform-agnostic-development-and-verification.md), [testing](../testing.md), [commands](../commands.md)

## Goal

Develop, verify and build the same way on any machine that has Docker
(Engine, Desktop or Colima) and `make`, with nothing else installed on the
host, from three small Dockerfiles: a development image, and the published
`app` and `agent` images.

## Sequence

Each task is a reviewable outcome with its own record and evidence; tasks are
sequential and each branches from the previous one.

- [ ] **F7-T01 Three Dockerfiles, three images** ([F7-T01](../tasks/F7-T01.md), in review). Add `deploy/dev/Dockerfile`
  (Go, Node, generators, Playwright Chromium, PostgreSQL client,
  VictoriaMetrics binary), `deploy/app/Dockerfile` (embedded UI and the
  `almanac` executable on a minimal runtime) and `deploy/agent/Dockerfile`
  (`almanac-agent` on its own minimal runtime), replacing the root
  Dockerfile; repin every base image by multi-arch index digest; add the
  pin-consistency check; `make build` builds the images, `make publish`
  pushes `app` and `agent` for `linux/amd64` and `linux/arm64`. Evidence:
  image builds on arm64 and amd64; `docker image inspect` shows the native
  platform on each host; the published images start with `version`; the
  consistency check fails when a pin is changed in only one file.
- [ ] **F7-T02 Development and fixture Compose file** ([F7-T02](../tasks/F7-T02.md), in review). `compose.dev.yaml`
  with the `dev` service (checkout at `/workspace`, named cache volumes,
  no socket) sharing the network namespace of a `postgres` fixture (tmpfs,
  loopback 15487) and a `victoriatraces` fixture (tmpfs, loopback 19428); `make setup` builds or pulls images and checks platforms;
  `make shell`; VictoriaMetrics tests use only the baked binary and the lane
  marker is removed; the Compose override files of deployment tests move to
  `.local/tmp`. Evidence: the integration suite and the browser journeys
  inside the dev container on both hosts; a Colima run at default settings.
- [ ] **F7-T03 Verification entry and the Compose journey** ([F7-T03](../tasks/F7-T03.md), in review). `scripts/verify.py`
  replaces `verification-controls.py`, `integration-runner.py`,
  `verify-linux.sh`, `setup-database.sh`, `setup-go.sh` and
  `deploy/verification/`, keeping selection, receipts and the ledger; the
  Makefile becomes thin targets; `MODE=compose` runs from the dev container
  with the host socket, the checkout mounted at its host path and the
  daemon host's network (amended from the stack-network alias design). Evidence: every mode natively through
  `make verify` on both hosts, receipts carrying OS, architecture, image
  digest and revision; the Compose journey on both hosts.
- [ ] **F7-T04 Documentation and approvals** ([F7-T04](../tasks/F7-T04.md), in review). Rewrite the testing guide's
  command table, the README setup and agent sections (containers or Linux
  hosts), AGENTS.md command rules and the command catalog for the new mode
  set; record the retired modes and the evidence from T01–T03 in status.

## Acceptance for the plan

- Fresh clone on macOS (arm64, Colima, default configuration) and on Linux
  (Docker Engine, amd64): `make setup`, `make build`, `make verify`,
  `make verify MODE=integration`, `make verify MODE=journey`,
  `make verify MODE=browser` and `make verify MODE=compose` pass without any
  host or runtime configuration.
- No test or script references a system temp directory in a container mount,
  a single-architecture digest, a host toolchain or the removed lane.
- Three Dockerfiles with matching pins build `dev`, `app` and `agent`; only
  `app` and `agent` are published.

## Out of scope

Native macOS or Windows execution of the CLI and agent; changing what the
suites test; the controlled performance entry's workload (only its fixture
start changes).
