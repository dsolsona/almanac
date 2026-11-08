#!/bin/sh
# Compose's standard COMPOSE_FILE/COMPOSE_PROJECT_NAME also isolate the real-stack tests.
set -eu
export COMPOSE_FILE=${COMPOSE_FILE:-compose.yaml}
case "${1:-}" in
  seed)
    docker compose build init
    docker compose up --detach --wait --wait-timeout 90 postgres
    docker compose run --rm -T migrate
    docker compose exec -T --user root postgres sh -s < deploy/local/agent-seed.sh
    ;;
  start)
    seeded=$(docker compose exec -T --user root postgres sh -c 'if [ -f /run/almanac-agent/token ]; then echo seeded; fi')
    [ "$seeded" = seeded ] || exit 0
    umask 077
    certificate=$(mktemp "${TMPDIR:-/tmp}/almanac-agent-ca.XXXXXX")
    trap 'rm -f "$certificate"' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    docker compose exec -T app cat /run/almanac-secrets/tls.crt > "$certificate"
    docker compose exec -T --user root postgres sh -c '
      set -eu
      umask 077
      cat > /run/almanac-agent/ca.pem.pending
      chown 10001:10001 /run/almanac-agent/ca.pem.pending
      mv /run/almanac-agent/ca.pem.pending /run/almanac-agent/ca.pem
    ' < "$certificate"
    docker compose --profile agent build agent
    # The app was recreated; recreate the agent too so it joins the new network namespace.
    docker compose --profile agent up --detach --no-deps --force-recreate agent
    ;;
  *) echo 'usage: local-dev-agent.sh seed|start' >&2; exit 2 ;;
esac
