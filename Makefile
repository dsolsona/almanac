.PHONY: setup shell stop-dev build verify _enter preview generate bootstrap run start stop logs credentials agent verification-init verification-report verification-handoff _verify_all _verify_go _verify_web _verify_go_race _verify_smoke _verify_auth_bench _verify_providers _verify_sqlc_generated _report_sizes _ensure_web_assets

# Go comes from the dev image's PATH. An inherited GOROOT can make that driver
# execute a different installation's compiler, despite GOTOOLCHAIN=local.
unexport GOROOT

verification-init:
	@test -n "$${VERIFICATION_PLAN:-}" || { echo 'VERIFICATION_PLAN must name the task budget JSON' >&2; exit 2; }
	@python3 scripts/verify.py init

verification-report:
	@python3 scripts/verify.py report

verification-handoff:
	@python3 scripts/verify.py handoff

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

# Shell in the dev container with the fixture running; CMD runs a command instead.
shell:
	@$(MAKE) --no-print-directory _enter MODE="$(MODE)" ENTER_CMD="$${CMD:-exec bash}"

# Container images (ADR-0029): the published app and agent images and the never-published dev image.
# IMAGE_REGISTRY and IMAGE_TAG select the publish destination; publish builds linux/amd64 and linux/arm64.
IMAGE_TAG ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE_PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: images publish
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

# Verification runs inside the dev container (ADR-0029): from the host, verify enters it and
# scripts/verify.py owns the mode table, receipts and the timing ledger. VERIFY_MODE is accepted
# as an alias of MODE while the command catalog is re-approved.
MODE ?= $(VERIFY_MODE)
verify:
	@if [ -n "$${ALMANAC_IN_DEV_CONTAINER:-}" ]; then python3 scripts/verify.py gate $(MODE); \
	else $(MAKE) --no-print-directory _enter MODE="$(MODE)" ENTER_CMD="python3 scripts/verify.py gate $(MODE)"; fi

# Enter the dev container. The Docker-driving modes add compose.dev.socket.yaml: the daemon
# socket, the checkout at its host path and the daemon host's network.
_enter:
	@set -eu; files='-f compose.dev.yaml'; \
	  case "$${MODE:-}" in compose|providers) files="$$files -f compose.dev.socket.yaml"; \
	    export ALMANAC_HOST_CHECKOUT="$(CURDIR)"; \
	    export ALMANAC_DOCKER_GID=$$(docker run --rm -v /var/run/docker.sock:/var/run/docker.sock almanac-dev:latest stat -c %g /var/run/docker.sock) ;; esac; \
	  image=$$(docker image inspect --format '{{.Id}}' almanac-dev:latest 2>/dev/null || echo unknown); \
	  docker compose $$files run --rm -e ALMANAC_DEV_IMAGE_ID="$$image" dev bash -c "$${ENTER_CMD}"

.PHONY: _verify_all
_verify_all:
	python3 scripts/check-image-pins.py
	$(MAKE) _verify_web
	$(MAKE) _verify_go

_verify_web:
	cd web && node --check scripts/measure-backend-browser.mjs
	cd web && node --check scripts/check-telemetry-runtime.mjs
	cd web && node --check scripts/check-local-compose.mjs
	cd web && node --check scripts/check-slo-browser.mjs
	cd web && node --check scripts/capture-telemetry-delivery.mjs
	cd web && npm run typecheck
	cd web && ./node_modules/.bin/vitest run --config vitest.config.ts
	cd web && npm run build
	cd web && node scripts/check-production.mjs

.PHONY: _verify_providers
_verify_providers:
	cd web && node scripts/check-local-providers.mjs

_ensure_web_assets:
	@if [ ! -f web/dist/index.html ]; then \
	  echo 'Building missing embedded web assets'; \
	  cd web && npm run build; \
	fi

_verify_go:
	@set -eu; command -v $(GO) >/dev/null || { echo 'Go is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	  case "$${GO_PACKAGE:-./...}" in ./...|./internal/config|./internal/command|./internal/setupclient|./internal/cliprofile|./internal/uihttp|./internal/authhttp|./internal/backendhttp|./internal/agenthttp|./internal/agentclient|./internal/storage|./internal/identity|./internal/authentication|./internal/agentmanagement|./internal/agentaccess|./internal/agentownership|./internal/agentprotocol|./internal/agentruntime|./internal/agentcmd|./internal/agentws|./internal/secretfile|./internal/datasource|./internal/prometheus|./internal/datasourcehttp|./internal/datasourceclient|./internal/sli|./internal/slihttp|./internal/slo|./internal/slohttp|./internal/sloclient) ;; *) echo 'GO_PACKAGE must select a package with repository tests' >&2; exit 2;; esac; \
	  export GOTOOLCHAIN=local GOPROXY=off; \
	  unformatted=$$(find cmd internal web -name '*.go' -type f -print0 | xargs -0 $(GOFMT) -l); \
	  test -z "$$unformatted" || { echo "Go files need formatting: $$unformatted" >&2; exit 1; }; \
	  target="$${GO_PACKAGE:-./...}"; \
	  case "$$target" in ./...|./internal/command|./internal/uihttp) $(MAKE) _ensure_web_assets ;; esac; \
	  $(GO) version; \
	  $(GO) tool compile -V; \
	  test_list=$$(mktemp .local/go-tests.XXXXXX); trap 'rm -f "$$test_list"' EXIT; \
	  if ! $(GO) test -list . "$$target" > "$$test_list"; then echo 'Go test discovery failed' >&2; cat "$$test_list"; exit 1; fi; \
	  tests=$$(awk '/^Test/{n++} END{print n+0}' "$$test_list"); \
	  test "$$tests" -gt 0 || { echo 'selected Go packages contain zero tests' >&2; exit 1; }; \
	  echo "Selected Go tests: $$tests"; \
	  $(GO) vet "$$target"; \
	  $(GO) test "$$target"; \
	  if [ -z "$${GO_PACKAGE:-}" ]; then \
	    echo 'Compile database acceptance tests without running them'; \
	    $(GO) test -tags=integration -run '^$$' ./internal/storage ./internal/authentication ./internal/agentmanagement ./internal/agenthttp ./internal/authhttp ./internal/command; \
	    echo 'Compile controlled capacity entry without running it'; \
	    $(GO) test -tags=integration,performance -run '^$$' ./internal/command; \
	    $(MAKE) _verify_sqlc_generated; \
	    mkdir -p bin; $(GO) build -o bin/almanac ./cmd/almanac; \
	    $(GO) build -o bin/almanac-agent ./cmd/almanac-agent; \
	    $(MAKE) _report_sizes; \
	    $(MAKE) _verify_generated; \
	    $(MAKE) _verify_smoke; \
	  fi

_verify_go_race: _ensure_web_assets
	@set -eu; export GOTOOLCHAIN=local GOPROXY=off; \
	  test_list=$$(mktemp .local/race-tests.XXXXXX); trap 'rm -f "$$test_list"' EXIT; \
	  packages='./internal/uihttp ./internal/command ./internal/agentclient ./internal/setupclient ./internal/cliprofile ./internal/storage ./internal/identity ./internal/authentication ./internal/agentmanagement ./internal/agenthttp ./internal/authhttp ./internal/backendhttp ./internal/secretfile ./internal/agentaccess ./internal/agentcmd ./internal/agentws ./internal/agentruntime ./internal/agentownership ./internal/agentprotocol ./internal/datasource ./internal/sourcesecret ./internal/prometheus ./internal/datasourcehttp ./internal/datasourceclient ./internal/sli ./internal/slihttp ./internal/slo ./internal/slohttp ./internal/sloclient'; \
	  if ! $(GO) test -list . $$packages > "$$test_list"; then echo 'race test discovery failed' >&2; cat "$$test_list"; exit 1; fi; \
	  tests=$$(awk '/^Test/{n++} END{print n+0}' "$$test_list"); \
	  test "$$tests" -gt 0 || { echo 'selected race packages contain zero tests' >&2; exit 1; }; \
	  echo "Selected race tests: $$tests"; \
	  $(GO) test -race $$packages

_verify_auth_bench:
	@set -eu; command -v $(GO) >/dev/null || { echo 'Go is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	  export GOTOOLCHAIN=local GOPROXY=off; \
	  benchmarks=$$($(GO) test -run '^$$' -list '^BenchmarkPassword(Hash|Verify)$$' ./internal/identity); \
	  printf '%s\n' "$$benchmarks"; \
	  test "$$(printf '%s\n' "$$benchmarks" | awk '/^Benchmark/{n++} END{print n+0}')" -eq 2 || { echo 'expected both named password benchmarks' >&2; exit 1; }; \
	  $(GO) test -run '^$$' -bench '^BenchmarkPassword(Hash|Verify)$$' -benchtime=10x -count=1 -timeout=2m ./internal/identity

.PHONY: _verify_performance
_verify_performance:
	@set -eu; umask 077; \
	  case "$${ALMANAC_TEST_DATABASE_URL:-}" in 'postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable') ;; *) echo 'performance requires the existing isolated local fixture' >&2; exit 2 ;; esac; \
	  test -z "$${GO_PACKAGE:-}$${TEST_FILE:-}$${BROWSER_SCENARIO:-}$${BROWSER_PROJECT:-}$${GOFLAGS:-}" || { echo 'performance has no custom selectors or Go flags' >&2; exit 2; }; \
	  command -v $(GO) >/dev/null || { echo 'Go is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	  export GOTOOLCHAIN=local GOPROXY=off; \
	  python3 scripts/verify.py fixture-check; \
	  $(MAKE) build; \
	  selected=$$($(GO) test -tags=integration,performance -list '^TestIntegrationControlledBackendPerformance$$' ./internal/command); \
	  test "$$(printf '%s\n' "$$selected" | awk '/^TestIntegrationControlledBackendPerformance$$/{n++} END{print n+0}')" -eq 1 || { echo 'controlled performance entry missing' >&2; exit 1; }; \
	  echo 'Selected controlled performance case: 10-SLO year-history acceptance; larger fleets deferred by maintainer'; \
	  artifact="$(CURDIR)/.local/performance-go-test.jsonl"; status_file="$(CURDIR)/.local/performance-go-test.exit"; \
	  printf 'running\n' > "$$status_file"; go_status=0; \
	  $(GO) test -json -tags=integration,performance -p=1 -run '^TestIntegrationControlledBackendPerformance$$' -count=1 -timeout=8h30m ./internal/command > "$$artifact" || go_status=$$?; \
	  printf '%s\n' "$$go_status" > "$$status_file"; \
	  echo "Controlled performance Go events: $$artifact"; \
	  if [ "$$go_status" -eq 0 ]; then python3 scripts/verify.py performance-evidence; else exit "$$go_status"; fi

_verify_journey:
	@set -eu; \
	  case "$${BROWSER_SCENARIO:-onboarding}" in \
	    onboarding|lost-login|lost-password) expected="TestIntegrationExecutableBrowserAuthentication/$${BROWSER_SCENARIO:-onboarding}"; journey='^TestIntegrationExecutableBrowserAuthentication$$/^'"$${BROWSER_SCENARIO:-onboarding}"'$$' ;; \
	    agents|agents-close|agents-lost|agents-unavailable) expected="TestIntegrationExecutableBrowserAgentRegistration/$$BROWSER_SCENARIO"; journey='^TestIntegrationExecutableBrowserAgentRegistration$$/^'"$$BROWSER_SCENARIO"'$$' ;; \
	    agents-lifecycle) expected='TestIntegrationExecutableBrowserAgentLifecycle'; journey='^TestIntegrationExecutableBrowserAgentLifecycle$$' ;; \
	    slis) expected='TestIntegrationExecutableBrowserSLICollectionJourney'; journey='^TestIntegrationExecutableBrowserSLICollectionJourney$$' ;; \
	    *) echo 'BROWSER_SCENARIO must select a documented browser journey' >&2; exit 2 ;; \
	  esac; \
	  command -v $(GO) >/dev/null || { echo 'Go is missing; run inside the dev container (make shell)' >&2; exit 2; }; \
	  test -n "$${ALMANAC_TEST_DATABASE_URL:-}" || { echo 'ALMANAC_TEST_DATABASE_URL must select the isolated local fixture' >&2; exit 2; }; \
	  export GOTOOLCHAIN=local GOPROXY=off; \
	  python3 scripts/verify.py fixture-check; \
	  $(MAKE) build; \
	  journey_log=$$(mktemp .local/journey-result.XXXXXX); \
	  if ! $(GO) test -race -tags=integration -p=1 -v -run "$$journey" -count=1 -timeout=2m ./internal/command > "$$journey_log" 2>&1; then cat "$$journey_log"; exit 1; fi; \
	  cat "$$journey_log"; \
	  grep -F -- "--- PASS: $$expected (" "$$journey_log" >/dev/null || { echo 'selected browser journey did not execute and pass' >&2; exit 1; }

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

_verify_smoke:
	@test -x bin/almanac || { echo 'binary missing; run make build or make verify VERIFY_MODE=go' >&2; exit 2; }
	sh scripts/smoke-ui.sh

_report_sizes:
	@set -eu; binary_bytes=$$(wc -c < bin/almanac); \
	  binary_gzip_bytes=$$(gzip -n -c bin/almanac | wc -c); \
	  web_assets_bytes=$$(du -sb web/dist | awk '{print $$1}'); \
	  printf 'Binary bytes: %s\nGzip binary bytes: %s\nWeb asset bytes: %s\n' "$$binary_bytes" "$$binary_gzip_bytes" "$$web_assets_bytes"
