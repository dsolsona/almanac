# ADR-0029: Build, test and ship only through Docker images

- Status: proposed
- Created: 2026-11-04
- Accepted: pending
- Approval: the maintainer set the direction in the 2026-11-04 working conversation ("we will run everything under Docker, tests included; a development image, and published images for the app and the agent"); this record is the written form for acceptance
- Related specs: none; governs [testing](../testing.md), [commands](../commands.md), the Makefile, Dockerfiles and Compose files
- Supersedes / superseded by: supersedes the isolated Linux lane (F2-T22) and the host toolchain installed by `make setup`

## Context

The project was developed on Linux and the tooling assumes it. On 2026-11-04
the maintainer tried to run the real-stack journeys on an Apple silicon Mac
with Colima and none could run. The causes were not Docker itself:

1. **Images pinned to one architecture.** PostgreSQL is pinned by an amd64
   manifest digest, so arm64 hosts emulate it. In the Linux lane the emulated
   database stalled after the first storage test and 170 of 173 cases then
   failed to connect.
2. **Tests hand host files to containers.** Integration tests start the
   VictoriaMetrics fixture with `docker run --mount type=bind,src=<t.TempDir()>`.
   On macOS that is `/var/folders/…`, which Docker Desktop shares by default
   and Colima does not.
3. **Two toolchains.** `make setup` installs Go, generators and Chromium on
   the host, and a separate 290-line lane rebuilds the same toolchain in an
   image with its own orchestrator, six helpers, a source snapshot and
   attestation step, a marker file and an environment flag that tests branch
   on. Inside that lane VictoriaMetrics already runs as a native binary.
4. **Linux-only Go packages.** `internal/secretfile` and `internal/cliprofile`
   are built with a Linux-only tag; host runs of `secrets provision`, token
   files and CLI sign-in fail on macOS. Under this decision the product runs
   in Linux containers, so this is a documented limit rather than a blocker.

The maintainer wants one way to develop, test and build on any machine with
Docker Engine, Docker Desktop or Colima, with nothing installed on the host
beyond Docker, and the tooling simplified in the process.

## Decision

Proposal, in the maintainer's stated direction.

1. **Docker is the only runtime.** Development, verification and builds run
   inside containers. The host needs Docker (Engine, Desktop or Colima) and
   `make`; no Go, Node, Chromium, generators or fixture binaries are
   installed on it. `make setup` builds or pulls images only.
2. **Three images from one Dockerfile.** A `dev` stage carries the pinned Go
   and Node toolchains, the generators, Playwright with Chromium, the
   PostgreSQL client and the pinned VictoriaMetrics binary. An `app` stage
   builds the embedded UI and the `almanac` executable onto a minimal
   runtime. An `agent` stage builds `almanac-agent` onto its own minimal
   runtime. `app` and `agent` are the published artifacts; `dev` is never
   published. Toolchain pins exist once, in this file.
3. **Multi-architecture everywhere.** Every base image and fixture image is
   pinned by its multi-arch index digest so the runtime resolves the host
   platform. `make setup` checks that each resolved image matches the host
   and fails with a clear message rather than emulating. Published images
   are built for `linux/amd64` and `linux/arm64`.
4. **Verification runs in the dev image.** `make verify [MODE=…]` starts the
   `dev` service from `compose.dev.yaml` and runs the same script the host
   target ran before. The checkout is bind-mounted at `/workspace` as the
   only host mount. Go module and build caches, `node_modules` and the
   Playwright browser cache are named volumes so they never cross the
   virtiofs boundary. `make shell` opens a shell in the same service for the
   inner loop; editors that want a host toolchain use the image through
   their container tooling.
5. **Fixtures are services or baked binaries, never sibling containers.** The
   dev container has no Docker socket. PostgreSQL is a service in
   `compose.dev.yaml` with a tmpfs data directory and no published port; the
   dev service shares its network namespace so the fixed loopback URL keeps
   working. VictoriaMetrics starts as the baked binary; the `docker run`
   fixture path and the lane marker are removed.
6. **Deployment checks are the one Docker-driving mode and run from the dev
   container through the host daemon.** The Compose journey (`MODE=compose`)
   builds and starts the published `app` image through the host's Docker.
   For that mode only, the dev service mounts the Docker socket and the
   checkout at its host path, so build contexts resolve on the host. The
   browser reaches the stack by joining the stack's network and using the
   service name; the local initializer issues its certificate for `localhost`,
   `127.0.0.1` and the service alias. No other mode receives the socket.
7. **One verification script, fewer layers.** `scripts/verify.py` absorbs
   `verification-controls.py` and `integration-runner.py`, keeping dependency
   based integration selection, pass receipts and the timing ledger. It owns
   the mode table and the single fixture identity. The Makefile becomes thin
   targets that enter the container. `scripts/verify-linux.sh`,
   `scripts/setup-database.sh`, `scripts/setup-go.sh` and
   `deploy/verification/` are deleted. Receipts record OS, architecture, the
   image digest and the Git revision with a dirty flag.
8. **The command catalog is replaced.** Modes become `verify` (ordinary),
   `MODE=unit|browser|go|go-race|integration|journey|compose|performance|smoke`,
   plus `setup`, `build` (images), `publish`, `shell`, `run`/`stop`. The
   task-specific modes (`slo-unit`, `slo-integration`, `slo-journey`,
   `agent-lifecycle`, `linux`, `linux-sli`, `go-portability`,
   `verification-tools`) are retired; their selections become ordinary
   `integration` and `journey` selectors. Database URLs are no longer passed
   on the command line.
9. **Platform limit recorded, not fixed.** The Linux-only protected-file
   packages stay as they are because the product runs in Linux containers.
   The README states that the `almanac` CLI and `almanac-agent` are
   supported as containers or on Linux hosts.

## Alternatives considered

- **Native toolchains on Linux and macOS with Docker for services only.**
  Works, but keeps two environments to pin and means reimplementing the
  Linux-only packages for macOS. The maintainer prefers one runtime.
- **Keep the Linux lane and repin its database image.** Removes the
  emulation but keeps the parallel orchestration, the host toolchain and the
  bind-mounted fixtures. Rejected as a partial fix.
- **Docker-in-Docker for the Compose journey.** Avoids the host socket but
  needs a privileged container and a second daemon with its own image cache.
  Rejected; mounting the socket for that one mode is the simpler, standard
  pattern and the mode already builds and starts the real images.
- **Testcontainers or a Go Docker client for fixtures.** Adds a dependency
  and a socket requirement for all integration tests. Rejected; one Compose
  service and one baked binary cover the two fixtures.

## Consequences

- Benefits: one environment on every machine; no host installs; native
  images on both architectures; about 1,500 lines of lane and setup code
  removed; the fixture identity defined once; published app and agent images
  built from the same file that builds the dev image.
- Costs: Docker is mandatory even for a unit test; the first `make setup`
  builds the dev image; headed browser debugging happens inside a container;
  source reads cross virtiofs on macOS (caches do not); the command catalog,
  testing guide, README and AGENTS.md command rules change and need
  re-approval.
- Limitations: no native macOS or Windows execution of the CLI or agent;
  the Compose journey is the only mode that touches the host daemon.
- Follow-up: [plan 0026](../plans/0026-platform-agnostic-verification.md)
  implements this and records evidence on one Linux host and one macOS host
  with Colima at default settings.

## Revisit conditions

A need to run the CLI or agent natively on macOS or Windows; a fixture that
cannot ship as a multi-arch image or a baked binary; measured dev-loop
latency on macOS that the named caches do not resolve.
