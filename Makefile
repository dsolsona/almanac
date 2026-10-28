.PHONY: setup build verify preview generate bootstrap run start stop logs credentials agent _verify_all _verify_go _verify_go_portability _verify_web _verify_go_race _verify_smoke _verify_integration _verify_auth_bench _verify_providers _verify_sqlc_generated _report_sizes _ensure_web_assets

setup:
	@set -eu; case "$${SETUP_MODE:-all}" in \
	  go) sh scripts/setup-go.sh ;; \
	  web) case "$${UPDATE_LOCK:-}" in '') ;; 1) (cd web && npm install --package-lock-only); (cd tools/openapi && npm install --package-lock-only) ;; *) echo 'UPDATE_LOCK must be 1 or unset' >&2; exit 2;; esac; \
	    (cd web && npm ci && ./node_modules/.bin/playwright install chromium); \
	    (cd tools/openapi && npm ci) ;; \
	  all) case "$${UPDATE_LOCK:-}" in '') ;; 1) (cd web && npm install --package-lock-only); (cd tools/openapi && npm install --package-lock-only) ;; *) echo 'UPDATE_LOCK must be 1 or unset' >&2; exit 2;; esac; \
	    sh scripts/setup-go.sh; \
	    (cd web && npm ci && ./node_modules/.bin/playwright install chromium); \
	    (cd tools/openapi && npm ci) ;; \
	  database) sh scripts/setup-database.sh ;; \
	  verification) sh scripts/verify-linux.sh setup ;; \
	  *) echo 'SETUP_MODE must be all, web, go, database, or verification' >&2; exit 2 ;; \
	esac

build:
	cd web && npm run typecheck && npm run build
	GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build" .local/bin/go build -o bin/almanac ./cmd/almanac
	GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build" .local/bin/go build -o bin/almanac-agent ./cmd/almanac-agent
	$(MAKE) _report_sizes

generate:
	@set -eu; case "$${GENERATE_MODE:-api}" in \
	  api) export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	    test -x .local/bin/oapi-codegen || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	    test -x tools/openapi/node_modules/.bin/openapi-typescript || { echo 'OpenAPI setup missing; run make setup' >&2; exit 2; }; \
	    .local/bin/oapi-codegen -config api/oapi-codegen.yaml -o internal/api/generated.go api/openapi.yaml; \
	    tools/openapi/node_modules/.bin/openapi-typescript api/openapi.yaml -o web/src/api/generated.ts ;; \
	  format) test -x .local/bin/gofmt || { echo 'Pinned gofmt missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	    find cmd internal web -name '*.go' -type f -print0 | xargs -0 .local/bin/gofmt -w ;; \
	  sqlc) test -x .local/bin/sqlc || { echo 'sqlc setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; .local/bin/sqlc generate -f sqlc.yaml ;; \
	  go-guidelines) test -x .local/bin/go-modern-guidelines || { echo 'Go guidelines missing; run make setup SETUP_MODE=go' >&2; exit 2; }; .local/bin/go-modern-guidelines list --go-version 1.27.3; .local/bin/go-modern-guidelines explain json_v2 stdlib_uuid errors_as_type ;; \
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

logs:
	docker compose -f compose.yaml logs app

credentials:
	@echo 'This is the initial password only. It is no longer current after you rotate it during onboarding.' >&2
	docker compose -f compose.yaml exec -T app sh -c 'cat /run/almanac-secrets/initial-admin-password; printf "\n"'

agent:
	@sh scripts/local-agent.sh

verify:
	@if [ "$${VERIFY_MODE:-}" = "linux-sli" ]; then sh scripts/verify-linux.sh sli; \
	elif [ "$${VERIFY_MODE:-}" = "linux" ]; then sh scripts/verify-linux.sh verify; \
	elif [ "$${VERIFY_MODE:-}" = "go" ]; then $(MAKE) _verify_go; \
	elif [ "$${VERIFY_MODE:-}" = "go-portability" ]; then $(MAKE) _verify_go_portability; \
	elif [ "$${VERIFY_MODE:-}" = "go-race" ]; then $(MAKE) _verify_go_race; \
	elif [ "$${VERIFY_MODE:-}" = "auth-bench" ]; then $(MAKE) _verify_auth_bench; \
	elif [ "$${VERIFY_MODE:-}" = "integration" ]; then $(MAKE) _verify_integration; \
	elif [ "$${VERIFY_MODE:-}" = "journey" ]; then $(MAKE) _verify_journey; \
	elif [ "$${VERIFY_MODE:-}" = "compose" ]; then python3 scripts/local-agent-tty-test.py && cd web && node scripts/check-local-agent.mjs && node scripts/check-local-compose.mjs && node scripts/check-local-compose-cancellation.mjs; \
	elif [ "$${VERIFY_MODE:-}" = "providers" ]; then $(MAKE) _verify_providers; \
	elif [ "$${VERIFY_MODE:-}" = "smoke" ]; then $(MAKE) _verify_smoke; \
	elif [ "$${VERIFY_MODE:-}" = "unit" ]; then \
	  if [ -n "$${TEST_FILE:-}" ]; then \
	    case "$${TEST_FILE}" in tests/*.test.tsx) ;; *) echo 'TEST_FILE must select a tests/*.test.tsx file' >&2; exit 2;; esac; \
	    case "$${TEST_FILE}" in *[!A-Za-z0-9_./-]*|*..*) echo 'TEST_FILE contains unsupported characters' >&2; exit 2;; esac; \
	    cd web && if [ ! -f "$${TEST_FILE}" ]; then echo 'TEST_FILE must name an existing test file' >&2; exit 2; fi && \
	      ./node_modules/.bin/vitest run --config vitest.config.ts "$${TEST_FILE}"; \
	  else cd web && ./node_modules/.bin/vitest run --config vitest.config.ts; fi; \
	elif [ "$${VERIFY_MODE:-}" = "browser" ]; then \
	  set --; \
	  if [ -n "$${TEST_FILE:-}" ]; then \
	    case "$${TEST_FILE}" in browser/*.spec.ts) ;; *) echo 'TEST_FILE must select a browser/*.spec.ts file' >&2; exit 2;; esac; \
	    case "$${TEST_FILE}" in *[!A-Za-z0-9_./-]*|*..*) echo 'TEST_FILE contains unsupported characters' >&2; exit 2;; esac; \
	    if [ ! -f "web/$${TEST_FILE}" ]; then echo 'TEST_FILE must name an existing test file' >&2; exit 2; fi; \
	    set -- "$${TEST_FILE}"; \
	  fi; \
	  if [ -n "$${BROWSER_PROJECT:-}" ]; then \
	    case "$${BROWSER_PROJECT}" in desktop-light|desktop-dark|intermediate-light|intermediate-dark|mobile-light|mobile-dark) ;; *) echo 'Unknown BROWSER_PROJECT' >&2; exit 2;; esac; \
	    set -- "$$@" --project "$${BROWSER_PROJECT}"; \
	  fi; \
	  cd web && ./node_modules/.bin/playwright test --config playwright.config.ts "$$@"; \
	elif [ -z "$${VERIFY_MODE:-}" ]; then \
	  $(MAKE) _verify_all; \
	else echo 'unknown VERIFY_MODE (use unit, browser, go, go-portability, go-race, auth-bench, integration, providers, compose, smoke, linux, or linux-sli)' >&2; exit 2; fi

.PHONY: _verify_all
_verify_all:
	$(MAKE) _verify_web
	$(MAKE) _verify_go

_verify_web:
	cd web && npm run typecheck
	cd web && ./node_modules/.bin/vitest run --config vitest.config.ts
	cd web && npm run build
	cd web && node scripts/check-production.mjs

.PHONY: _verify_providers
_verify_providers:
	cd web && node scripts/check-local-providers.mjs

.PHONY: _verify_go_portability
_verify_go_portability: _ensure_web_assets
	@set -eu; test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/cache-darwin-amd64" GOOS=darwin GOARCH=amd64 CGO_ENABLED=0; \
	  out="$(CURDIR)/.local/portability/darwin-amd64"; mkdir -p "$$out"; \
	  echo 'Compile internal/command tests for darwin/amd64 (ordinary tags; compile only)'; \
	  .local/bin/go test -c -o "$$out/command.test" ./internal/command; \
	  echo 'Compile internal/command tests for darwin/amd64 (integration tag; compile only)'; \
	  .local/bin/go test -c -tags=integration -o "$$out/command.integration.test" ./internal/command

_ensure_web_assets:
	@if [ ! -f web/dist/index.html ]; then \
	  echo 'Building missing embedded web assets'; \
	  cd web && npm run build; \
	fi

_verify_go:
	@set -eu; test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  case "$${GO_PACKAGE:-./...}" in ./...|./internal/config|./internal/command|./internal/setupclient|./internal/cliprofile|./internal/uihttp|./internal/authhttp|./internal/backendhttp|./internal/agenthttp|./internal/agentclient|./internal/storage|./internal/identity|./internal/authentication|./internal/agentmanagement|./internal/agentaccess|./internal/agentownership|./internal/agentprotocol|./internal/agentruntime|./internal/agentcmd|./internal/agentws|./internal/secretfile|./internal/datasource|./internal/prometheus|./internal/datasourcehttp|./internal/datasourceclient|./internal/sli|./internal/slihttp) ;; *) echo 'GO_PACKAGE must select a package with repository tests' >&2; exit 2;; esac; \
	  export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  unformatted=$$(find cmd internal web -name '*.go' -type f -print0 | xargs -0 .local/bin/gofmt -l); \
	  test -z "$$unformatted" || { echo "Go files need formatting: $$unformatted" >&2; exit 1; }; \
	  target="$${GO_PACKAGE:-./...}"; \
	  case "$$target" in ./...|./internal/command|./internal/uihttp) $(MAKE) _ensure_web_assets ;; esac; \
	  test_list=$$(mktemp .local/go-tests.XXXXXX); trap 'rm -f "$$test_list"' EXIT; \
	  if ! .local/bin/go test -list . "$$target" > "$$test_list"; then echo 'Go test discovery failed' >&2; cat "$$test_list"; exit 1; fi; \
	  tests=$$(awk '/^Test/{n++} END{print n+0}' "$$test_list"); \
	  test "$$tests" -gt 0 || { echo 'selected Go packages contain zero tests' >&2; exit 1; }; \
	  echo "Selected Go tests: $$tests"; \
	  .local/bin/go vet "$$target"; \
	  .local/bin/go test "$$target"; \
	  if [ -z "$${GO_PACKAGE:-}" ]; then \
	    echo 'Compile database acceptance tests without running them'; \
	    .local/bin/go test -tags=integration -run '^$$' ./internal/storage ./internal/authentication ./internal/agentmanagement ./internal/agenthttp ./internal/authhttp ./internal/command; \
	    $(MAKE) _verify_sqlc_generated; \
	    mkdir -p bin; .local/bin/go build -o bin/almanac ./cmd/almanac; \
	    .local/bin/go build -o bin/almanac-agent ./cmd/almanac-agent; \
	    $(MAKE) _report_sizes; \
	    $(MAKE) _verify_generated; \
	    $(MAKE) _verify_smoke; \
	  fi

_verify_go_race: _ensure_web_assets
	@set -eu; export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  test_list=$$(mktemp .local/race-tests.XXXXXX); trap 'rm -f "$$test_list"' EXIT; \
	  packages='./internal/uihttp ./internal/command ./internal/agentclient ./internal/setupclient ./internal/cliprofile ./internal/storage ./internal/identity ./internal/authentication ./internal/agentmanagement ./internal/agenthttp ./internal/authhttp ./internal/backendhttp ./internal/secretfile ./internal/agentaccess ./internal/agentcmd ./internal/agentws ./internal/agentruntime ./internal/agentownership ./internal/agentprotocol ./internal/datasource ./internal/sourcesecret ./internal/prometheus ./internal/datasourcehttp ./internal/datasourceclient ./internal/sli ./internal/slihttp'; \
	  if ! .local/bin/go test -list . $$packages > "$$test_list"; then echo 'race test discovery failed' >&2; cat "$$test_list"; exit 1; fi; \
	  tests=$$(awk '/^Test/{n++} END{print n+0}' "$$test_list"); \
	  test "$$tests" -gt 0 || { echo 'selected race packages contain zero tests' >&2; exit 1; }; \
	  echo "Selected race tests: $$tests"; \
	  .local/bin/go test -race $$packages

_verify_auth_bench:
	@set -eu; test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  benchmarks=$$(.local/bin/go test -run '^$$' -list '^BenchmarkPassword(Hash|Verify)$$' ./internal/identity); \
	  printf '%s\n' "$$benchmarks"; \
	  test "$$(printf '%s\n' "$$benchmarks" | awk '/^Benchmark/{n++} END{print n+0}')" -eq 2 || { echo 'expected both named password benchmarks' >&2; exit 1; }; \
	  .local/bin/go test -run '^$$' -bench '^BenchmarkPassword(Hash|Verify)$$' -benchtime=10x -count=1 -timeout=2m ./internal/identity

_verify_integration: _ensure_web_assets
	@set -eu; test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  test -x .local/bin/sqlc || { echo 'sqlc setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  test -n "$${ALMANAC_TEST_DATABASE_URL:-}" || { echo 'ALMANAC_TEST_DATABASE_URL must select the isolated local fixture' >&2; exit 2; }; \
	  case "$${GO_PACKAGE:-all}" in ./internal/storage|./internal/authentication|./internal/agentmanagement|./internal/agenthttp|./internal/authhttp|./internal/command|all) ;; *) echo 'GO_PACKAGE must select a real database acceptance package or all' >&2; exit 2 ;; esac; \
	  export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  target="$${GO_PACKAGE:-all}"; \
	  packages="$$target"; if [ "$$target" = all ]; then packages='./internal/storage ./internal/authentication ./internal/agentmanagement ./internal/agenthttp ./internal/authhttp ./internal/agentcmd ./internal/command'; fi; \
	  test_list=$$(mktemp .local/integration-tests.XXXXXX); trap 'rm -f "$$test_list"' EXIT; \
	  if ! .local/bin/go test -tags=integration -list . $$packages > "$$test_list"; then echo 'integration test discovery failed' >&2; cat "$$test_list"; exit 1; fi; \
	  tests=$$(awk '/^TestIntegration/{n++} END{print n+0}' "$$test_list"); \
	  test "$$tests" -gt 0 || { echo 'selected integration packages contain zero tests' >&2; exit 1; }; \
	  command_tests=''; storage_tests=''; selected_packages=''; \
	  for name in storage command; do \
	    case " $$packages " in *" ./internal/$$name "*) \
	      if ! .local/bin/go test -tags=integration -list '^TestIntegration' "./internal/$$name" > "$$test_list"; then echo "$$name integration test discovery failed" >&2; exit 1; fi; \
	      selected=$$(python3 scripts/select-integration-tests.py "$$name" "$$test_list") || exit 1; \
	      case "$$name" in storage) storage_tests="$$selected" ;; command) command_tests="$$selected" ;; esac; \
	      selected_packages="$$selected_packages $$name" ;; esac; \
	  done; \
	  echo "Selected PostgreSQL integration tests: $$tests"; \
	  $(MAKE) _verify_sqlc_generated; \
	  fixture_check=--check; \
	  case "$${ALMANAC_LINUX_VERIFICATION_CONTAINER:-}" in '' ) ;; 1) fixture_check=--check-container ;; *) echo 'ALMANAC_LINUX_VERIFICATION_CONTAINER must be unset or 1' >&2; exit 2 ;; esac; \
	  sh scripts/setup-database.sh "$$fixture_check"; \
	  $(MAKE) build; \
	  json_artifact="$(CURDIR)/.local/integration-go-test.jsonl"; exit_artifact="$(CURDIR)/.local/integration-go-test.exit"; \
	  package_artifacts="$(CURDIR)/.local/integration-test-json"; mkdir -p "$$package_artifacts"; \
	  : > "$$package_artifacts/selected-packages.txt"; \
	  for package in $$packages; do printf '%s\n' "$${package##*/}" >> "$$package_artifacts/selected-packages.txt"; done; \
	  for name in $$selected_packages; do \
	    case "$$name" in storage) selected="$$storage_tests" ;; command) selected="$$command_tests" ;; esac; \
	    printf '%s\n' "$$selected" > "$$package_artifacts/$$name-selected.txt"; \
	    for run in $$selected; do : > "$$package_artifacts/$$name-$$run.jsonl"; printf 'pending\n' > "$$package_artifacts/$$name-$$run.exit"; done; \
	  done; \
	  printf 'running\n' > "$$exit_artifact"; : > "$$json_artifact"; \
	  for package in $$packages; do package_name=$${package##*/}; : > "$$package_artifacts/$$package_name.jsonl"; printf 'pending\n' > "$$package_artifacts/$$package_name.exit"; done; \
	  for package in $$packages; do \
	    package_name=$${package##*/}; package_json="$$package_artifacts/$$package_name.jsonl"; \
	    package_exit="$$package_artifacts/$$package_name.exit"; printf 'running\n' > "$$package_exit"; \
	    runs=package; case "$$package" in ./internal/storage) runs="$$storage_tests" ;; ./internal/command) runs="$$command_tests" ;; esac; \
	    for run in $$runs; do \
	      selector='^TestIntegration'; run_json="$$package_json"; run_exit="$$package_exit"; \
	      case "$$package" in ./internal/storage|./internal/command) \
	        selector="^$${run}$$"; run_json="$$package_artifacts/$$package_name-$$run.jsonl"; run_exit="$$package_artifacts/$$package_name-$$run.exit" ;; esac; \
	      printf 'running\n' > "$$run_exit"; go_status=0; \
	      .local/bin/go test -json -race -tags=integration -p=1 -run "$$selector" -count=1 -timeout=2m "$$package" > "$$run_json" || go_status=$$?; \
	      if [ "$$run_json" != "$$package_json" ]; then cat "$$run_json" >> "$$package_json"; fi; \
	      cat "$$run_json" >> "$$json_artifact"; printf '%s\n' "$$go_status" > "$$run_exit"; \
	      if [ "$$go_status" -ne 0 ]; then \
	        printf '%s\n' "$$go_status" > "$$package_exit"; printf '%s\n' "$$go_status" > "$$exit_artifact"; \
	        summary_status=0; python3 scripts/summarize-integration-tests.py || summary_status=$$?; \
	        exit "$$go_status"; \
	      fi; \
	    done; \
	    printf '0\n' > "$$package_exit"; \
	  done; \
	  printf '0\n' > "$$exit_artifact"; \
	  summary_status=0; python3 scripts/summarize-integration-tests.py || summary_status=$$?; \
	  exit "$$summary_status"

_verify_journey:
	@set -eu; \
	  case "$${BROWSER_SCENARIO:-onboarding}" in \
	    onboarding|lost-login|lost-password) expected="TestIntegrationExecutableBrowserAuthentication/$${BROWSER_SCENARIO:-onboarding}"; journey='^TestIntegrationExecutableBrowserAuthentication$$/^'"$${BROWSER_SCENARIO:-onboarding}"'$$' ;; \
	    agents|agents-close|agents-lost|agents-unavailable) expected="TestIntegrationExecutableBrowserAgentRegistration/$$BROWSER_SCENARIO"; journey='^TestIntegrationExecutableBrowserAgentRegistration$$/^'"$$BROWSER_SCENARIO"'$$' ;; \
	    agents-lifecycle) expected='TestIntegrationExecutableBrowserAgentLifecycle'; journey='^TestIntegrationExecutableBrowserAgentLifecycle$$' ;; \
	    slis) expected='TestIntegrationExecutableBrowserSLICollectionJourney'; journey='^TestIntegrationExecutableBrowserSLICollectionJourney$$' ;; \
	    *) echo 'BROWSER_SCENARIO must select a documented browser journey' >&2; exit 2 ;; \
	  esac; \
	  test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  test -n "$${ALMANAC_TEST_DATABASE_URL:-}" || { echo 'ALMANAC_TEST_DATABASE_URL must select the isolated local fixture' >&2; exit 2; }; \
	  export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  fixture_check=--check; \
	  case "$${ALMANAC_LINUX_VERIFICATION_CONTAINER:-}" in '' ) ;; 1) fixture_check=--check-container ;; *) echo 'ALMANAC_LINUX_VERIFICATION_CONTAINER must be unset or 1' >&2; exit 2 ;; esac; \
	  sh scripts/setup-database.sh "$$fixture_check"; \
	  $(MAKE) build; \
	  journey_log=$$(mktemp .local/journey-result.XXXXXX); \
	  if ! .local/bin/go test -race -tags=integration -p=1 -v -run "$$journey" -count=1 -timeout=2m ./internal/command > "$$journey_log" 2>&1; then cat "$$journey_log"; exit 1; fi; \
	  cat "$$journey_log"; \
	  grep -F -- "--- PASS: $$expected (" "$$journey_log" >/dev/null || { echo 'selected browser journey did not execute and pass' >&2; exit 1; }

_verify_sqlc_generated:
	@set -eu; test -x .local/bin/sqlc || { echo 'sqlc setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  temp_dir=$$(mktemp -d .local/sqlc-check.XXXXXX); temp_config=$$(mktemp .sqlc-check.XXXXXX.yaml); trap 'rm -rf "$$temp_dir"; rm -f "$$temp_config"' EXIT; \
	  sed "s|out: internal/storage/dbgen|out: $$temp_dir/dbgen|" sqlc.yaml > "$$temp_config"; \
	  .local/bin/sqlc generate -f "$$temp_config"; \
	  diff -ru internal/storage/dbgen "$$temp_dir/dbgen"

_verify_generated:
	@set -eu; temp_dir=$$(mktemp -d); trap 'rm -rf "$$temp_dir"' EXIT; \
	  GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build" .local/bin/oapi-codegen -config api/oapi-codegen.yaml -o "$$temp_dir/generated.go" api/openapi.yaml; \
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
