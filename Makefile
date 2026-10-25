.PHONY: setup build verify preview

setup:
	@case "$${UPDATE_LOCK:-}" in '') ;; 1) cd web && npm install --package-lock-only ;; *) echo 'UPDATE_LOCK must be 1 or unset' >&2; exit 2;; esac
	cd web && npm ci
	cd web && ./node_modules/.bin/playwright install chromium

build:
	cd web && npm run typecheck && npm run build

preview:
	cd web && npm run dev -- --host 127.0.0.1 --port 5198 --strictPort

verify:
	@if [ "$${VERIFY_MODE:-}" = "unit" ]; then \
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
	else echo 'unknown VERIFY_MODE (use unit or browser)' >&2; exit 2; fi

.PHONY: _verify_all
_verify_all:
	cd web && npm run typecheck
	cd web && ./node_modules/.bin/vitest run --config vitest.config.ts
	cd web && npm run build
	cd web && node scripts/check-production.mjs
