#!/bin/sh
# End-to-end suite (ADR-0033): a fresh Compose stack of the real images under its own
# project name and ports, so the maintainer's `make run` stack and data are never touched.
#   sh scripts/e2e.sh [playwright args]   start a fresh stack, run the suite, remove the stack
#   sh scripts/e2e.sh up | down           manage the stack by hand (E2E_KEEP=1 keeps it after a run)
#   E2E_CONFIG=playwright.perf.config.ts  run the performance suite on the same kind of stack
#   E2E_POSTGRES_STORAGE=disk           opt into a project-owned database volume
# Runs inside the dev container with the Docker socket (make test-e2e).
set -eu

export COMPOSE_PROJECT_NAME=almanac-e2e
export ALMANAC_PORT="${E2E_PORT:-18188}"
export ALMANAC_METRICS_PORT="${E2E_METRICS_PORT:-18528}"
export ALMANAC_TRACES_PORT="${E2E_TRACES_PORT:-19528}"
export ALMANAC_LOKI_PORT=13101
export ALMANAC_TEMPO_PORT=13201
export ALMANAC_TEMPO_OTLP_PORT=14319
export E2E_BASE_URL="https://localhost:${ALMANAC_PORT}"
export E2E_METRICS_URL="http://localhost:${ALMANAC_METRICS_PORT}"
export E2E_TRACES_URL="http://localhost:${ALMANAC_TRACES_PORT}"

# Normal suites retain their tmpfs database. Long retained-history experiments can
# opt into disk; export the same selection to every Playwright Compose invocation.
export E2E_POSTGRES_STORAGE="${E2E_POSTGRES_STORAGE:-memory}"
case "$E2E_POSTGRES_STORAGE" in
  memory|disk) ;;
  *) printf 'E2E_POSTGRES_STORAGE must be memory or disk\n' >&2; exit 2 ;;
esac
export E2E_ALERT_ACCEPTANCE="${E2E_ALERT_ACCEPTANCE:-0}"
case "$E2E_ALERT_ACCEPTANCE" in
  0) ;;
  1) export E2E_POSTGRES_STORAGE=disk; mkdir -p -m 0755 .local/alert-acceptance/control ;;
  *) printf 'E2E_ALERT_ACCEPTANCE must be 0 or 1\n' >&2; exit 2 ;;
esac
compose() {
  if [ "$E2E_ALERT_ACCEPTANCE" = 1 ]; then
    docker compose -p "$COMPOSE_PROJECT_NAME" -f compose.yaml -f compose.e2e.yaml -f compose.e2e.disk.yaml -f compose.e2e.alerts.yaml "$@"
    return
  fi
  if [ "$E2E_POSTGRES_STORAGE" = disk ]; then
    docker compose -p "$COMPOSE_PROJECT_NAME" -f compose.yaml -f compose.e2e.yaml -f compose.e2e.disk.yaml "$@"
  else
    docker compose -p "$COMPOSE_PROJECT_NAME" -f compose.yaml -f compose.e2e.yaml "$@"
  fi
}

down() {
  # Agents started by the suite carry this label; they are not Compose services.
  owned=$(docker ps -aq --filter label=almanac.e2e=owned)
  [ -z "$owned" ] || docker rm -f $owned >/dev/null
  compose --profile agent --profile sources down --volumes --remove-orphans >/dev/null 2>&1 || true
}

up() {
  down
  if [ "$E2E_ALERT_ACCEPTANCE" = 1 ]; then
    rm -f .local/alert-acceptance/control/pause .local/alert-acceptance/control/pause-source
  fi
  build_started=$(date +%s)
  compose build --quiet
  compose --profile agent build --quiet agent
  # A rebuild leaves the previous images untagged; remove this project's, never anyone else's.
  docker image prune --force --filter label=com.docker.compose.project="$COMPOSE_PROJECT_NAME" >/dev/null
  if [ "${E2E_SMOKE:-0}" = 1 ]; then
    printf 'Smoke image builds: %ss\n' "$(( $(date +%s) - build_started ))"
    stack_started=$(date +%s)
    if compose up --detach --wait --wait-timeout 120 app; then
      printf 'Smoke stack readiness: %ss\n' "$(( $(date +%s) - stack_started ))"
      return 0
    else
      startup_status=$?
    fi
  elif compose --profile sources up --detach --wait --wait-timeout 120 app loki tempo sources-ready; then
    return 0
  else
    startup_status=$?
  fi
  # Preserve public fixture readiness evidence before the EXIT trap removes it.
  # Do not dump container environments, application secrets or authentication state.
  startup_evidence=".local/e2e/startup-$(date -u +%Y%m%dT%H%M%SZ)"
  mkdir -p "$startup_evidence" || return "$startup_status"
  compose ps --all >"$startup_evidence/services.txt" 2>&1 || true
  if [ "${E2E_SMOKE:-0}" = 1 ]; then
    set -- app postgres migrate victoriametrics victoriatraces provider-ready
  else
    set -- loki tempo sources-ready
  fi
  compose logs --no-color --tail 200 "$@" >"$startup_evidence/providers.log" 2>&1 || true
  for startup_service in "$@"; do
    startup_container=$(compose ps --all --quiet "$startup_service") || startup_container=''
    if [ -n "$startup_container" ]; then
      docker inspect --format '{{json .State.Health}}' "$startup_container" >"$startup_evidence/$startup_service-health.json" 2>&1 || true
    fi
  done
  printf 'Startup readiness evidence: %s\n' "$startup_evidence" >&2
  return "$startup_status"
}

case "${1:-}" in
  up) up; printf 'End-to-end stack ready at %s\n' "$E2E_BASE_URL" ;;
  down) down ;;
  *)
    # The trap comes before the start, so a stack that fails its health checks is removed too, and
    # Playwright runs in a subshell so the trap's Compose paths still resolve from the repository root.
    [ "${E2E_KEEP:-}" = 1 ] || trap down EXIT
    up
    (cd web && ./node_modules/.bin/playwright test --config "${E2E_CONFIG:-playwright.e2e.config.ts}" "$@")
    ;;
esac
