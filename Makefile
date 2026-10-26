.PHONY: setup build verify preview generate _verify_all _verify_go _verify_web _verify_go_race _verify_smoke _verify_integration _verify_auth_bench _verify_sqlc_generated _report_sizes _ensure_web_assets

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
	  *) echo 'SETUP_MODE must be all, web, go, or database' >&2; exit 2 ;; \
	esac

build:
	cd web && npm run typecheck && npm run build
	GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build" .local/bin/go build -o bin/almanac ./cmd/almanac
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
	  *) echo 'GENERATE_MODE must be api, format, or sqlc' >&2; exit 2 ;; \
	esac

preview:
	cd web && npm run dev -- --host 127.0.0.1 --port 5198 --strictPort

verify:
	@if [ "$${VERIFY_MODE:-}" = "go" ]; then $(MAKE) _verify_go; \
	elif [ "$${VERIFY_MODE:-}" = "go-race" ]; then $(MAKE) _verify_go_race; \
	elif [ "$${VERIFY_MODE:-}" = "auth-bench" ]; then $(MAKE) _verify_auth_bench; \
	elif [ "$${VERIFY_MODE:-}" = "integration" ]; then $(MAKE) _verify_integration; \
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
	else echo 'unknown VERIFY_MODE (use unit, browser, go, go-race, auth-bench, integration, or smoke)' >&2; exit 2; fi

.PHONY: _verify_all
_verify_all:
	$(MAKE) _verify_web
	$(MAKE) _verify_go

_verify_web:
	cd web && npm run typecheck
	cd web && ./node_modules/.bin/vitest run --config vitest.config.ts
	cd web && npm run build
	cd web && node scripts/check-production.mjs

_ensure_web_assets:
	@if [ ! -f web/dist/index.html ]; then \
	  echo 'Building missing embedded web assets'; \
	  cd web && npm run build; \
	fi

_verify_go:
	@set -eu; test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  case "$${GO_PACKAGE:-./...}" in ./...|./internal/config|./internal/command|./internal/uihttp|./internal/authhttp|./internal/storage|./internal/identity|./internal/authentication|./internal/secretfile) ;; *) echo 'GO_PACKAGE must select a package with repository tests' >&2; exit 2;; esac; \
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
	    $(MAKE) _verify_sqlc_generated; \
	    mkdir -p bin; .local/bin/go build -o bin/almanac ./cmd/almanac; \
	    $(MAKE) _report_sizes; \
	    $(MAKE) _verify_generated; \
	    $(MAKE) _verify_smoke; \
	  fi

_verify_go_race: _ensure_web_assets
	@set -eu; export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  test_list=$$(mktemp .local/race-tests.XXXXXX); trap 'rm -f "$$test_list"' EXIT; \
	  if ! .local/bin/go test -list . ./internal/uihttp ./internal/command ./internal/storage ./internal/identity ./internal/authentication ./internal/secretfile > "$$test_list"; then echo 'race test discovery failed' >&2; cat "$$test_list"; exit 1; fi; \
	  tests=$$(awk '/^Test/{n++} END{print n+0}' "$$test_list"); \
	  test "$$tests" -gt 0 || { echo 'selected race packages contain zero tests' >&2; exit 1; }; \
	  echo "Selected race tests: $$tests"; \
	  .local/bin/go test -race ./internal/uihttp ./internal/command ./internal/storage ./internal/identity ./internal/authentication ./internal/authhttp ./internal/secretfile

_verify_auth_bench:
	@set -eu; test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  benchmarks=$$(.local/bin/go test -run '^$$' -list '^BenchmarkPassword(Hash|Verify)$$' ./internal/identity); \
	  printf '%s\n' "$$benchmarks"; \
	  test "$$(printf '%s\n' "$$benchmarks" | awk '/^Benchmark/{n++} END{print n+0}')" -eq 2 || { echo 'expected both named password benchmarks' >&2; exit 1; }; \
	  .local/bin/go test -run '^$$' -bench '^BenchmarkPassword(Hash|Verify)$$' -benchtime=10x -count=1 -timeout=2m ./internal/identity

_verify_integration:
	@set -eu; test -x .local/bin/go || { echo 'Go setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  test -x .local/bin/sqlc || { echo 'sqlc setup missing; run make setup SETUP_MODE=go' >&2; exit 2; }; \
	  test -n "$${ALMANAC_TEST_DATABASE_URL:-}" || { echo 'ALMANAC_TEST_DATABASE_URL must select the isolated local fixture' >&2; exit 2; }; \
	  case "$${GO_PACKAGE:-all}" in ./internal/storage|./internal/authentication|./internal/authhttp|all) ;; *) echo 'GO_PACKAGE must select ./internal/storage, ./internal/authentication, ./internal/authhttp, or all' >&2; exit 2 ;; esac; \
	  export GOTOOLCHAIN=local GOPROXY=off GOMODCACHE="$(CURDIR)/.local/go/mod" GOCACHE="$(CURDIR)/.local/go/build"; \
	  target="$${GO_PACKAGE:-all}"; \
	  packages="$$target"; if [ "$$target" = all ]; then packages='./internal/storage ./internal/authentication ./internal/authhttp'; fi; \
	  test_list=$$(mktemp .local/integration-tests.XXXXXX); trap 'rm -f "$$test_list"' EXIT; \
	  if ! .local/bin/go test -tags=integration -list . $$packages > "$$test_list"; then echo 'integration test discovery failed' >&2; cat "$$test_list"; exit 1; fi; \
	  tests=$$(awk '/^TestIntegration/{n++} END{print n+0}' "$$test_list"); \
	  test "$$tests" -gt 0 || { echo 'selected integration packages contain zero tests' >&2; exit 1; }; \
	  echo "Selected PostgreSQL integration tests: $$tests"; \
	  $(MAKE) _verify_sqlc_generated; \
	  sh scripts/setup-database.sh --check; \
	  $(MAKE) build; \
	  .local/bin/go test -race -tags=integration -p=1 -run '^TestIntegration' -count=1 -timeout=2m $$packages

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
