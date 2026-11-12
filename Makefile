.PHONY: setup shell stop-dev build _enter preview generate bootstrap run start stop logs credentials agent agent-seed images publish test test-integration test-browser test-e2e test-perf check verify coverage _test _test_integration _test_browser _coverage _verify_sqlc_generated _verify_generated _report_sizes _ensure_web_assets _disk_check _disk_trim

.DEFAULT_GOAL := setup

define MAKE_HELP
Almanac: make TARGET [OPTION=value]

Start and daily use
  help                  Show this command guide; starts no services
  bootstrap             First app startup: build, initialize data and start HTTPS
  run                   Build/start the local app and its dependencies
  start                 Alias for run
  stop                  Stop the local app and agent; preserve data
  logs                  Show app logs from the local stack
  credentials           Print the initial administrator password

Development tools
  setup                 Build the dev image and install Go/npm dependencies once
  shell                 Open the development container; CMD runs one command
  stop-dev              Remove dev fixtures; keep dependency caches
  agent-seed            Create the reusable local-dev agent credential
  agent                 Run an agent with a privately pasted credential

Testing (automatically enters the development container)
  test                  Formatting, generated code, docs and Go/web unit tests
  test-integration      Integration tests against PostgreSQL and real binaries
  test-browser          UI catalogue/theme/size checks and production build
  test-e2e              Critical journeys on a fresh isolated application stack
  test-perf             Seeded SLO/SLI browser timings and exact-value checks
  check                 Run test, test-integration and test-browser
  verify                Alias for check
  coverage              Unit/integration coverage in .local/coverage/

Build and generation (run inside make shell)
  build                 Build web assets and the app/agent binaries
  generate              Generate code or format Go; choose GENERATE_MODE below
  preview               Start the Vite development server

Container images
  images                Build local app, agent and development images
  publish               Build/push app and agent images to IMAGE_REGISTRY

Optional Firecracker microVMs (Linux host; Docker runs inside each VM)
  vm-base               Build/rebuild the shared OS, tools and dependency disk
  vm-run                Boot a worker, start Codex in Tern and connect local Tern
  vm-setup              Prepare a worker for manual use; Codex is not started
  vm-list               List running, stopped and partially prepared VMs
  vm-start              Boot an existing VM and wait for SSH
  vm-codex              Reconnect Tern to Codex (legacy tmux remains attachable)
  vm-ssh                Open a guest shell in the task worktree
  vm-collect            Retrieve commits/edits/new files without applying them
  vm-stop               Orderly shutdown; keep disk and credentials
  vm-delete             Permanently delete one VM; explicit VM=name is required

Useful options
  ALMANAC_PORT=18088     Local app HTTPS port; reuse it for later app commands
  CMD="go version"      Command to run with make shell
  SOCKET=1              Give the dev shell access to its host Docker socket
  UPDATE_LOCK=1         Update npm lockfiles during make setup
  RACE=1                Enable Go race detection for test/test-integration
  ARGS="-g agent"       Select Playwright E2E/performance tests
  PERF_DAYS=35           Seeded history length for test-perf
  PERF_RUNS=10           Browser loads per page for test-perf
  E2E_MIN_FREE_GB=10     Minimum host free disk space before E2E/performance suites
  GENERATE_MODE=api     api | sqlc | format | go-guidelines
  IMAGE_TAG=tag         Image tag (default: git describe)
  IMAGE_PLATFORMS=list  Publish platforms (default: linux/amd64,linux/arm64)
  IMAGE_REGISTRY=path   Required registry/repository prefix for publish
  VM=name               Creation uses a random two-word name when omitted
  VM_PROMPT="task"      Codex task; default stdin: type it, Enter, then Ctrl-D
  VM_REPO=/path         Checkout snapshot to copy (default: current checkout)
  VM_CPUS=2             New named VM vCPUs
  VM_MEMORY_MIB=4096    New named VM memory
  VM_DISK_GIB=80        Base disk size; clones inherit it
  FIRECRACKER_STATE_DIR Optional override; VM state is shared across worktrees

Examples
  make bootstrap
  make setup && make check
  make shell CMD="make build"
  make shell CMD="make generate GENERATE_MODE=sqlc"
  make test-e2e ARGS="-g agent"
  make vm-base
  make vm-run VM_PROMPT="Investigate the API issue"
  cat task.txt | make vm-run
  make vm-codex VM=fuzzy-tiger

Existing VM commands default to almanac; delete always requires a name.
Build/generate/preview use the dev image toolchains; setup is the default target.
Advanced overrides: GO, GOFMT, SQLC, OAPI_CODEGEN, GO_GUIDELINES, GOFLAGS,
ALMANAC_DEV_UID and ALMANAC_DEV_GID. See docs/commands.md and docs/firecracker.md.
endef

.PHONY: help
help: export ALMANAC_MAKE_HELP := $(value MAKE_HELP)
help:
	@printf '%s\n' "$$ALMANAC_MAKE_HELP"

# Go comes from the dev image's PATH. An inherited GOROOT can make that driver
# execute a different installation's compiler, despite GOTOOLCHAIN=local.
unexport GOROOT

# Toolchains come from the development image (ADR-0029); the names resolve on its PATH.
GO ?= go
GOFMT ?= gofmt
SQLC ?= sqlc
OAPI_CODEGEN ?= oapi-codegen
GO_GUIDELINES ?= go-modern-guidelines
DEV_COMPOSE := docker compose -f compose.dev.yaml
export ALMANAC_DEV_UID ?= $(shell id -u)
export ALMANAC_DEV_GID ?= $(shell id -g)

# Optional Linux microVM lifecycle. Prompts default to stdin; no host Docker socket is shared.
export VM ?=
export VM_REPO ?= $(CURDIR)
export VM_PROMPT ?= -
override export VM_PROMPT := $(value VM_PROMPT)
.PHONY: vm-base vm-setup vm-run vm-list vm-start vm-ssh vm-codex vm-stop vm-delete vm-collect
vm-base:
	@bash scripts/firecracker-setup.sh base $${VM:+"$$VM"} --repo "$$VM_REPO"
vm-setup:
	@bash scripts/firecracker-setup.sh setup $${VM:+"$$VM"} --repo "$$VM_REPO"
vm-run:
	@bash scripts/firecracker-setup.sh run $${VM:+"$$VM"} --repo "$$VM_REPO" --prompt "$$VM_PROMPT"
vm-list:
	@bash scripts/firecracker-setup.sh list
vm-start:
	@bash scripts/firecracker-setup.sh start "$${VM:-almanac}"
vm-ssh:
	@bash scripts/firecracker-setup.sh ssh "$${VM:-almanac}"
vm-codex:
	@bash scripts/firecracker-setup.sh codex "$${VM:-almanac}"
vm-stop:
	@bash scripts/firecracker-setup.sh stop "$${VM:-almanac}"
vm-delete:
	@test -n "$$VM" || { echo 'Use make vm-delete VM=NAME; deletion is permanent' >&2; exit 2; }
	@bash scripts/firecracker-setup.sh delete "$$VM"
vm-collect:
	@bash scripts/firecracker-setup.sh collect "$${VM:-almanac}"

# Build the dev image for this user, pull the fixture images, check all against the daemon's
# platform and warm the Go module and npm caches inside the container. Network-capable.
setup:
	@set -eu; case "$${UPDATE_LOCK:-}" in ''|1) ;; *) echo 'UPDATE_LOCK must be 1 or unset' >&2; exit 2;; esac; \
	  $(DEV_COMPOSE) build dev; \
	  $(DEV_COMPOSE) pull --quiet postgres victoriatraces; \
	  daemon=$$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}'); \
	  for image in $$($(DEV_COMPOSE) config --images); do \
	    actual=$$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$$image"); \
	    test "$$actual" = "$$daemon" || { echo "$$image is $$actual but the Docker daemon runs $$daemon" >&2; exit 1; }; \
	    echo "$$image: $$actual"; \
	  done; \
	  if [ "$${UPDATE_LOCK:-}" = 1 ]; then $(DEV_COMPOSE) run --rm dev sh -c 'cd web && npm install --package-lock-only && cd ../tools/openapi && npm install --package-lock-only'; fi; \
	  $(DEV_COMPOSE) run --rm dev sh -c 'go mod download && cd web && npm ci && cd ../tools/openapi && npm ci'

# Shell in the dev container with the fixture running; CMD runs a command instead; SOCKET=1 adds Docker.
shell:
	@$(MAKE) --no-print-directory _enter SOCKET="$(SOCKET)" ENTER_CMD="$${CMD:-exec bash}"

# Container images (ADR-0029): the published app and agent images and the never-published dev image.
# IMAGE_REGISTRY and IMAGE_TAG select the publish destination; publish builds linux/amd64 and linux/arm64.
IMAGE_TAG ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE_PLATFORMS ?= linux/amd64,linux/arm64

images:
	docker build --file deploy/app/Dockerfile --tag almanac:$(IMAGE_TAG) --tag almanac-local:dev .
	docker build --file deploy/agent/Dockerfile --tag almanac-agent:$(IMAGE_TAG) --tag almanac-local-agent:dev .
	docker build --file deploy/dev/Dockerfile --build-arg USER_ID=$$(id -u) --build-arg GROUP_ID=$$(id -g) --tag almanac-dev:$(IMAGE_TAG) --tag almanac-dev:latest .

publish:
	@test -n "$${IMAGE_REGISTRY:-}" || { echo 'IMAGE_REGISTRY must name the registry prefix, for example ghcr.io/dsolsona' >&2; exit 2; }
	python3 scripts/check-image-pins.py
	docker buildx build --platform $(IMAGE_PLATFORMS) --file deploy/app/Dockerfile --tag "$(IMAGE_REGISTRY)/almanac:$(IMAGE_TAG)" --push .
	docker buildx build --platform $(IMAGE_PLATFORMS) --file deploy/agent/Dockerfile --tag "$(IMAGE_REGISTRY)/almanac-agent:$(IMAGE_TAG)" --push .

build:
	cd web && npm run typecheck && npm run build
	GOTOOLCHAIN=local GOPROXY=off $(GO) build -o bin/almanac ./cmd/almanac
	GOTOOLCHAIN=local GOPROXY=off $(GO) build -o bin/almanac-agent ./cmd/almanac-agent
	$(MAKE) _report_sizes

generate:
	@set -eu; case "$${GENERATE_MODE:-api}" in \
	  api) export GOTOOLCHAIN=local GOPROXY=off; \
	    command -v $(OAPI_CODEGEN) >/dev/null || { echo 'oapi-codegen is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	    test -x tools/openapi/node_modules/.bin/openapi-typescript || { echo 'OpenAPI setup missing; run make setup' >&2; exit 2; }; \
	    $(OAPI_CODEGEN) -config api/oapi-codegen.yaml -o internal/api/generated.go api/openapi.yaml; \
	    tools/openapi/node_modules/.bin/openapi-typescript api/openapi.yaml -o web/src/api/generated.ts ;; \
	  format) command -v $(GOFMT) >/dev/null || { echo 'gofmt is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	    find cmd internal web -name '*.go' -type f -print0 | xargs -0 $(GOFMT) -w ;; \
	  sqlc) command -v $(SQLC) >/dev/null || { echo 'sqlc is missing; run inside the dev container (make shell)' >&2; exit 2; }; $(SQLC) generate -f sqlc.yaml ;; \
	  go-guidelines) command -v $(GO_GUIDELINES) >/dev/null || { echo 'go-modern-guidelines is missing; run inside the dev container (make shell)' >&2; exit 2; }; $(GO_GUIDELINES) list --go-version 1.27.3; $(GO_GUIDELINES) explain json_v2 stdlib_uuid errors_as_type ;; \
	  *) echo 'GENERATE_MODE must be api, format, sqlc, or go-guidelines' >&2; exit 2 ;; \
	esac

preview:
	cd web && npm run dev -- --host 127.0.0.1 --port 5198 --strictPort

bootstrap:
	$(MAKE) run

run start:
	@set -eu; port=$${ALMANAC_PORT:-18088}; case "$$port" in ''|*[!0-9]*) echo 'ALMANAC_PORT must be a decimal port from 1 to 65535' >&2; exit 2;; esac; test "$${#port}" -le 5 || { echo 'ALMANAC_PORT must be a decimal port from 1 to 65535' >&2; exit 2; }; port=$$(printf '%s' "$$port" | sed 's/^0*//'); test -n "$$port" && test "$$port" -gt 0 && test "$$port" -le 65535 || { echo 'ALMANAC_PORT must be a decimal port from 1 to 65535' >&2; exit 2; }; command -v curl >/dev/null 2>&1 || { echo 'curl is required for local startup' >&2; exit 2; }; export ALMANAC_PORT=$$port; docker compose -f compose.yaml build; docker compose -f compose.yaml up -d --wait --wait-timeout 90 --force-recreate app; sh scripts/check-local-readiness.sh; sh scripts/local-dev-agent.sh start; printf '\nAlmanac is ready at https://localhost:%s\nAdmin login: admin\nRetrieve the one-time initial password with: make credentials\n' "$$port"

stop:
	docker compose -f compose.yaml --profile agent stop

# Remove the dev container's fixtures (tmpfs, no data to preserve); caches stay in their volumes.
stop-dev:
	$(DEV_COMPOSE) down

logs:
	docker compose -f compose.yaml logs app

credentials:
	@echo 'This is the initial password only. It is no longer current after you rotate it during onboarding.' >&2
	docker compose -f compose.yaml exec -T app sh -c 'cat /run/almanac-secrets/initial-admin-password; printf "\n"'

agent:
	@sh scripts/local-agent.sh

agent-seed:
	@sh scripts/local-dev-agent.sh seed

# Tests (ADR-0033). Every target runs in the dev container (ADR-0029); from the host it enters it.
#   make test              fast: formatting, vet, generated code, docs, Go and web unit tests
#   make test-integration  Go tests against PostgreSQL and the real binaries, in parallel (RACE=1 adds -race)
#   make test-browser      the UI catalogue in six theme and size setups, and the production build check
#   make test-e2e          critical user journeys on a fresh Compose stack of the real images
#   make test-perf         page speed of the SLO and SLI lists and details on a seeded fleet
#   make check             what a pull request needs: test, test-integration and test-browser (CI adds e2e)
dev = if [ -n "$${ALMANAC_IN_DEV_CONTAINER:-}" ]; then $(MAKE) --no-print-directory $(1); else $(MAKE) --no-print-directory _enter ENTER_CMD="make --no-print-directory $(1)"; fi

test: ; @$(call dev,_test RACE=$(RACE))
test-integration: ; @$(call dev,_test_integration RACE=$(RACE))
test-browser: ; @$(call dev,_test_browser)
coverage: ; @$(call dev,_coverage)
check verify: test test-integration test-browser

# End-to-end and performance suites drive the host's Docker daemon, so they enter with the socket.
# ARGS passes Playwright arguments, for example ARGS='--project onboarding' or ARGS='-g agent'.
test-e2e:
	@$(MAKE) --no-print-directory _disk_check
	@$(MAKE) --no-print-directory _enter SOCKET=1 ENTER_CMD="sh scripts/e2e.sh $(ARGS)"; status=$$?; $(MAKE) --no-print-directory _disk_trim; exit $$status

# PERF_DAYS sets the history length (default 35) and PERF_RUNS the loads per page (default 10).
test-perf:
	@$(MAKE) --no-print-directory _disk_check
	@$(MAKE) --no-print-directory _enter SOCKET=1 ENTER_CMD="E2E_CONFIG=playwright.perf.config.ts PERF_DAYS=$${PERF_DAYS:-35} PERF_RUNS=$${PERF_RUNS:-10} sh scripts/e2e.sh $(ARGS)"; status=$$?; $(MAKE) --no-print-directory _disk_trim; exit $$status

# The suites build images on the host's Docker disk. Refuse to start with less than E2E_MIN_FREE_GB
# free on the host (default 10), and afterwards return freed blocks of Colima's disk file to macOS.
E2E_MIN_FREE_GB ?= 10
_disk_check:
	@free=$$(df -Pk "$(CURDIR)" | awk 'NR==2 {print int($$4 / 1048576)}'); \
	  if [ "$$free" -lt "$(E2E_MIN_FREE_GB)" ]; then echo "Only $$free GB free on the host disk; the suite needs $(E2E_MIN_FREE_GB) GB (E2E_MIN_FREE_GB). Free space first." >&2; exit 1; fi
_disk_trim:
	@if command -v colima >/dev/null 2>&1 && colima status >/dev/null 2>&1; then colima ssh -- sudo fstrim /var/lib/docker >/dev/null 2>&1 || true; fi

GO_TEST := GOTOOLCHAIN=local GOPROXY=off $(GO) test $(if $(RACE),-race)

_test:
	@mkdir -p .local
	$(MAKE) --no-print-directory _ensure_web_assets
	@set -eu; unformatted=$$(find cmd internal -name '*.go' -type f -print0 | xargs -0 $(GOFMT) -l); \
	  test -z "$$unformatted" || { echo "Go files need formatting (make generate GENERATE_MODE=format): $$unformatted" >&2; exit 1; }
	GOTOOLCHAIN=local GOPROXY=off $(GO) vet ./...
	GOTOOLCHAIN=local GOPROXY=off $(GO) vet -tags=integration ./...
	$(MAKE) --no-print-directory _verify_sqlc_generated _verify_generated
	python3 scripts/check-docs.py
	python3 scripts/check-image-pins.py
	python3 scripts/local-agent-tty-test.py
	python3 scripts/firecracker-setup-test.py
	python3 scripts/firecracker-tern-test.py
	python3 scripts/make-help-test.py
	cd web && node scripts/check-local-agent.mjs
	cd web && npm run typecheck
	cd web && ./node_modules/.bin/vitest run --config vitest.config.ts
	$(GO_TEST) ./...

# One `go test` for every package: packages run in parallel and integration tests call t.Parallel.
# Each test gets its own database in the dev container's PostgreSQL fixture.
_test_integration:
	@mkdir -p .local
	$(MAKE) --no-print-directory build
	$(GO_TEST) -tags=integration -count=1 -timeout=10m ./...

# The catalogue tests include production-build checks, so the build comes first.
_test_browser:
	cd web && npm run build
	cd web && ./node_modules/.bin/playwright test --config playwright.config.ts
	cd web && node scripts/check-production.mjs

# Coverage of the unit and integration tests together; the report lands in .local/coverage.
_coverage:
	@mkdir -p .local/coverage
	$(MAKE) --no-print-directory build
	GOTOOLCHAIN=local GOPROXY=off $(GO) test -tags=integration -count=1 -timeout=10m -coverpkg=./internal/... -coverprofile=.local/coverage/go.out ./...
	GOTOOLCHAIN=local GOPROXY=off $(GO) tool cover -func=.local/coverage/go.out | tail -1
	GOTOOLCHAIN=local GOPROXY=off $(GO) tool cover -html=.local/coverage/go.out -o .local/coverage/go.html

# Enter the dev container. SOCKET=1 adds compose.dev.socket.yaml for the suites that drive Docker:
# the daemon socket, the checkout at its host path and the daemon host's network.
_enter:
	@set -eu; files='-f compose.dev.yaml'; \
	  if [ -n "$${SOCKET:-}" ]; then files="$$files -f compose.dev.socket.yaml"; \
	    export ALMANAC_HOST_CHECKOUT="$(CURDIR)"; \
	    export ALMANAC_DOCKER_GID=$$(docker run --rm -v /var/run/docker.sock:/var/run/docker.sock almanac-dev:latest stat -c %g /var/run/docker.sock); fi; \
	  docker compose $$files run --rm dev bash -c "$${ENTER_CMD}"

_ensure_web_assets:
	@if [ ! -f web/dist/index.html ]; then \
	  echo 'Building missing embedded web assets'; \
	  cd web && npm run build; \
	fi

_verify_sqlc_generated:
	@set -eu; command -v $(SQLC) >/dev/null || { echo 'sqlc is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	  command -v $(GO) >/dev/null || { echo 'Go is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	  temp_dir=$$(mktemp -d .local/sqlc-check.XXXXXX); temp_config=$$(mktemp .sqlc-check.XXXXXX.yaml); trap 'rm -rf "$$temp_dir"; rm -f "$$temp_config"' EXIT; \
	  sed "s|out: internal/storage/dbgen|out: $$temp_dir/dbgen|" sqlc.yaml > "$$temp_config"; \
	  $(SQLC) generate -f "$$temp_config"; \
	  diff -ru internal/storage/dbgen "$$temp_dir/dbgen"

_verify_generated:
	@set -eu; temp_dir=$$(mktemp -d); trap 'rm -rf "$$temp_dir"' EXIT; \
	  GOTOOLCHAIN=local GOPROXY=off $(OAPI_CODEGEN) -config api/oapi-codegen.yaml -o "$$temp_dir/generated.go" api/openapi.yaml; \
	  tools/openapi/node_modules/.bin/openapi-typescript api/openapi.yaml -o "$$temp_dir/generated.ts" >/dev/null; \
	  cmp internal/api/generated.go "$$temp_dir/generated.go"; cmp web/src/api/generated.ts "$$temp_dir/generated.ts"

_report_sizes:
	@set -eu; binary_bytes=$$(wc -c < bin/almanac); \
	  binary_gzip_bytes=$$(gzip -n -c bin/almanac | wc -c); \
	  web_assets_bytes=$$(du -sb web/dist | awk '{print $$1}'); \
	  printf 'Binary bytes: %s\nGzip binary bytes: %s\nWeb asset bytes: %s\n' "$$binary_bytes" "$$binary_gzip_bytes" "$$web_assets_bytes"
