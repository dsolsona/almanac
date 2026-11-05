.PHONY: setup shell stop-dev build _enter preview generate bootstrap run start stop logs credentials agent images publish test test-integration test-browser test-e2e test-perf check verify coverage _test _test_integration _test_browser _coverage _verify_sqlc_generated _verify_generated _report_sizes _ensure_web_assets _disk_check _disk_trim

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

# Build the dev image for this user, pull the fixture image, check both against the daemon's
# platform and warm the Go module and npm caches inside the container. Network-capable.
setup:
	@set -eu; case "$${UPDATE_LOCK:-}" in ''|1) ;; *) echo 'UPDATE_LOCK must be 1 or unset' >&2; exit 2;; esac; \
	  $(DEV_COMPOSE) build dev; \
	  $(DEV_COMPOSE) pull --quiet postgres; \
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
	@set -eu; port=$${ALMANAC_PORT:-18088}; case "$$port" in ''|*[!0-9]*) echo 'ALMANAC_PORT must be a decimal port from 1 to 65535' >&2; exit 2;; esac; test "$${#port}" -le 5 || { echo 'ALMANAC_PORT must be a decimal port from 1 to 65535' >&2; exit 2; }; port=$$(printf '%s' "$$port" | sed 's/^0*//'); test -n "$$port" && test "$$port" -gt 0 && test "$$port" -le 65535 || { echo 'ALMANAC_PORT must be a decimal port from 1 to 65535' >&2; exit 2; }; command -v curl >/dev/null 2>&1 || { echo 'curl is required for local startup' >&2; exit 2; }; export ALMANAC_PORT=$$port; docker compose -f compose.yaml build; docker compose -f compose.yaml up -d --wait --wait-timeout 90 --force-recreate app; sh scripts/check-local-readiness.sh; printf '\nAlmanac is ready at https://localhost:%s\nAdmin login: admin\nRetrieve the one-time initial password with: make credentials\n' "$$port"

stop:
	docker compose -f compose.yaml stop

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
	@set -eu; unformatted=$$(find cmd internal -name '*.go' -type f -print0 | xargs -0 $(GOFMT) -l); \
	  test -z "$$unformatted" || { echo "Go files need formatting (make generate GENERATE_MODE=format): $$unformatted" >&2; exit 1; }
	GOTOOLCHAIN=local GOPROXY=off $(GO) vet ./...
	GOTOOLCHAIN=local GOPROXY=off $(GO) vet -tags=integration ./...
	$(MAKE) --no-print-directory _verify_sqlc_generated _verify_generated
	python3 scripts/check-docs.py
	python3 scripts/check-image-pins.py
	python3 scripts/local-agent-tty-test.py
	cd web && node scripts/check-local-agent.mjs
	cd web && npm run typecheck
	cd web && ./node_modules/.bin/vitest run --config vitest.config.ts
	$(MAKE) --no-print-directory _ensure_web_assets
	$(GO_TEST) ./...

# One `go test` for every package: packages run in parallel and integration tests call t.Parallel.
# Each test gets its own database in the dev container's PostgreSQL fixture.
_test_integration:
	$(MAKE) --no-print-directory build
	$(GO_TEST) -tags=integration -count=1 -timeout=10m ./...

_test_browser:
	cd web && ./node_modules/.bin/playwright test --config playwright.config.ts
	cd web && npm run build && node scripts/check-production.mjs

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
