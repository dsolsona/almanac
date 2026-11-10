# Commands

Everything builds, tests and ships through Docker
([ADR-0029](adr/0029-platform-agnostic-development-and-verification.md)); the test commands follow
[ADR-0033](adr/0033-simple-test-suite.md). The host needs Docker (Engine, Desktop or Colima) and
`make`.

Run `make help` for a grouped terminal reference to all public targets, common
options and examples. It works before setup and starts no services. Use this
page for the full workflow and execution rules.

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

### Optional Firecracker development host

On x86_64 Linux, [the Firecracker guide](firecracker.md) describes a private
development VM whose Docker daemon and containers run inside the guest:

```sh
bash firecracker-setup.sh check   # read-only host prerequisite check
make vm-base                     # build/rebuild dependencies from a fresh seed; uses sudo
make vm-base VM=seed              # adopt/warm this seed and stop it before sealing the base
make vm-setup VM=api              # CoW clone, independent checkout and host logins
make vm-run VM=api VM_PROMPT='Describe your task' # Codex in background
cat task.txt | make vm-run        # generate a random name such as fuzzy-tiger
make vm-run                      # type the task, Enter, then Ctrl-D to launch
cat task.txt | make vm-run VM=ui  # prompt from stdin by default
make vm-list                     # running, stopped and partial guests
make vm-start VM=api
make vm-ssh VM=api                # guest terminal
make vm-codex VM=api              # attach to Codex
make vm-stop VM=api               # preserve disk
make vm-delete VM=api             # permanently delete one guest; explicit VM required
make vm-collect VM=api            # retrieve changes without applying them
bash firecracker-setup.sh setup api # direct script equivalent; requires a base
bash firecracker-setup.sh start   # boot the prepared disk; uses sudo
bash firecracker-setup.sh ssh     # guest terminal
bash firecracker-setup.sh codex   # guest Codex in tmux
bash firecracker-setup.sh run api --prompt 'Describe your task' # named background Codex; first use clones the base
bash firecracker-setup.sh run ui --prompt - # read the task from stdin
bash firecracker-setup.sh codex api # attach to that worker's terminal
bash firecracker-setup.sh collect api # retrieve commits, tracked changes and new files; apply nothing
bash firecracker-setup.sh status  # read-only running/stopped status
bash firecracker-setup.sh stop    # orderly shutdown and owned network cleanup; preserves disk
```

These optional host commands do not replace the Docker application commands
below; run those from the guest checkout. Unnamed `setup`, `run` and `base` reserve
a random adjective-animal name and print it. Other lifecycle commands default to
the original `almanac` guest; deletion requires an explicit name.
`VM_REPO=/path/to/checkout` (or script `--repo`) selects the source
snapshot for base preparation or first setup/run. Host `gh` and file-based Codex
logins are copied privately to workers. Base definitions, rebuild semantics and
credential limits are in [the guide](firecracker.md).

### Application and verification

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

make run                      # build and start the local stack at https://localhost:18088 (alias: make start)
make credentials              # the one-time initial administrator password
make agent                    # run a local agent with a pasted credential
make logs                     # application logs
make stop                     # stop the local stack and keep its data
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
