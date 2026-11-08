# Commands

Everything builds, tests and ships through Docker
([ADR-0029](adr/0029-platform-agnostic-development-and-verification.md)); the test commands follow
[ADR-0033](adr/0033-simple-test-suite.md). The host needs Docker (Engine, Desktop or Colima) and
`make`.

## Command discipline

- Check the current environment's approval rules before running a command. This page records the
  project's commands; it cannot grant execution permission.
- Use the targets below as written. Do not add passthrough targets or wrap commands to get around
  restrictions.
- During unattended work, leave a command you cannot run pending, continue other authorized work and
  report what was not run.
- Commit, push, publish and merge only with the maintainer's authorization; publishing a PR does not
  authorize merging it.

## Catalog

The maintainer approved this catalog with ADR-0033 on 2026-11-05; it replaces the earlier
`make verify MODE=…` catalog.

```sh
make setup                    # build the dev image, pull fixtures, install Go and npm dependencies (network)
make shell                    # shell in the dev container with PostgreSQL and VictoriaTraces running
make shell CMD='go version'   # one command in the dev container (write $$ for a literal $)
make shell SOCKET=1           # the same, with the host's Docker socket (for scripts/e2e.sh up|down)
make stop-dev                 # remove the dev fixtures; caches stay

make test                     # fast checks and unit tests
make test-integration         # integration tests, in parallel; RACE=1 adds the race detector
make test-browser             # UI catalogue matrix and production build check
make test-e2e                 # end-to-end journeys; ARGS='e2e/collection.spec.ts' or ARGS='-g agent' selects
make test-perf                # page timings; PERF_DAYS=35 PERF_RUNS=10 by default
make check                    # test, test-integration and test-browser (alias: make verify)
make coverage                 # Go coverage report in .local/coverage/go.html

make generate GENERATE_MODE=api            # OpenAPI server and client code
make generate GENERATE_MODE=sqlc           # database query code
make generate GENERATE_MODE=format         # gofmt
make generate GENERATE_MODE=go-guidelines  # Modern Go guideline list for the Go version in go.mod
make build                                 # web assets and binaries
make images                                # app, agent and dev images
make publish IMAGE_REGISTRY=<prefix>       # multi-architecture images; only when the maintainer names a registry

make agent-seed               # once: prepare the dev database and seed local-dev with a private random token
make run                      # build/start the local stack and seeded agent at https://localhost:18088 (alias: make start)
make credentials              # the one-time initial administrator password
make agent                    # run a local agent with a pasted credential
make logs                     # application logs
make stop                     # stop the local stack and seeded agent, preserving data and token
make preview                  # Vite dev server for UI work
```

From the host, the test targets enter the dev container by themselves; inside it (`make shell`, CI)
they run directly. Run `make generate` and `make build` inside it, for example
`make shell CMD='make generate GENERATE_MODE=api'`. The database URL is preset in the container. Only
`test-e2e`, `test-perf` and `make shell SOCKET=1` receive the Docker socket.

Make suppresses an inherited `GOROOT` so the dev image's Go driver cannot select another
installation's compiler.

`make run` requires host Docker Compose, Make and curl. It checks HTTPS readiness with the
application's public certificate before reporting success; a failed check keeps the services and
volumes. Keep real credentials out of commands, logs and published artifacts.
