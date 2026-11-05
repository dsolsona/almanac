#!/bin/sh
# End-to-end suite (ADR-0033): a fresh Compose stack of the real images under its own
# project name and ports, so the maintainer's `make run` stack and data are never touched.
#   sh scripts/e2e.sh [playwright args]   start a fresh stack, run the suite, remove the stack
#   sh scripts/e2e.sh up | down           manage the stack by hand (E2E_KEEP=1 keeps it after a run)
#   E2E_CONFIG=playwright.perf.config.ts  run the performance suite on the same kind of stack
# Runs inside the dev container with the Docker socket (make test-e2e).
set -eu

export COMPOSE_PROJECT_NAME=almanac-e2e
export ALMANAC_PORT="${E2E_PORT:-18188}"
export ALMANAC_METRICS_PORT="${E2E_METRICS_PORT:-18528}"
export ALMANAC_TRACES_PORT="${E2E_TRACES_PORT:-19528}"
export E2E_BASE_URL="https://localhost:${ALMANAC_PORT}"
export E2E_METRICS_URL="http://localhost:${ALMANAC_METRICS_PORT}"
export E2E_TRACES_URL="http://localhost:${ALMANAC_TRACES_PORT}"

compose() { docker compose -p "$COMPOSE_PROJECT_NAME" -f compose.yaml "$@"; }

down() {
  # Agents started by the suite carry this label; they are not Compose services.
  owned=$(docker ps -aq --filter label=almanac.e2e=owned)
  [ -z "$owned" ] || docker rm -f $owned >/dev/null
  compose --profile agent down --volumes --remove-orphans >/dev/null 2>&1 || true
}

up() {
  down
  compose build --quiet
  compose --profile agent build --quiet agent
  compose up --detach --wait --wait-timeout 120 app
}

case "${1:-}" in
  up) up; printf 'End-to-end stack ready at %s\n' "$E2E_BASE_URL" ;;
  down) down ;;
  *)
    up
    [ "${E2E_KEEP:-}" = 1 ] || trap down EXIT
    cd web
    ./node_modules/.bin/playwright test --config "${E2E_CONFIG:-playwright.e2e.config.ts}" "$@"
    ;;
esac
