#!/usr/bin/env bash
set -euo pipefail

# Only development caches may enter the reusable base, never application data.
containers=$(docker container ls -aq)
for container in $containers; do
    test "$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$container")" = almanac-dev || {
        echo 'Seed has non-development containers; use a clean seed for the base' >&2; exit 1;
    }
done
volumes=$(docker volume ls -q)
for volume in $volumes; do
    case "$volume" in
        almanac-dev_go-mod|almanac-dev_go-build|almanac-dev_npm-cache|almanac-dev_web-node-modules|almanac-dev_openapi-node-modules|almanac-dev_web-test-results) ;;
        *) echo 'Seed has non-development volumes; use a clean seed for the base' >&2; exit 1 ;;
    esac
done

make setup
make stop-dev
containers=$(docker container ls -aq)
test -z "$containers"
