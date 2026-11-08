#!/bin/sh
# Development-only setup and startup for the local Compose agent.
set -eu
case "${1:-}" in
  seed)
    docker compose -f compose.yaml build init
    docker compose -f compose.yaml up --detach --wait --wait-timeout 90 postgres
    docker compose -f compose.yaml run --rm -T migrate
    docker compose -f compose.yaml exec -T --user root postgres sh -s < deploy/local/agent-seed.sh
    ;;
  start)
    seeded=$(docker compose -f compose.yaml exec -T --user root postgres sh -c 'if [ -f /run/almanac-agent/token ]; then echo seeded; fi')
    [ "$seeded" = seeded ] || exit 0
    umask 077
    certificate=$(mktemp "${TMPDIR:-/tmp}/almanac-agent-ca.XXXXXX")
    trap 'rm -f "$certificate"' EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    docker compose -f compose.yaml exec -T app cat /run/almanac-secrets/tls.crt > "$certificate"
    docker compose -f compose.yaml exec -T --user root postgres sh -c '
      set -eu
      umask 077
      cat > /run/almanac-agent/ca.pem.pending
      chown 10001:10001 /run/almanac-agent/ca.pem.pending
      mv /run/almanac-agent/ca.pem.pending /run/almanac-agent/ca.pem
    ' < "$certificate"
    docker compose -f compose.yaml --profile agent build agent
    # The app was recreated; recreate the agent too so it joins the new network namespace.
    docker compose -f compose.yaml --profile agent up --detach --no-deps --force-recreate agent
    ;;
  *) echo 'usage: local-dev-agent.sh seed|start' >&2; exit 2 ;;
esac
