#!/bin/sh
set -eu

case "${ALMANAC_FIXTURE_SLI:-}" in
    ''|*[!a-z0-9-]*) echo 'Set SLI to the demo SLI resource name.' >&2; exit 2 ;;
esac
case "${ALMANAC_FIXTURE_RANGE:-}" in
    1d|7d|14d|30d) days=${ALMANAC_FIXTURE_RANGE%d} ;;
    *) echo 'Set RANGE to 1d, 7d, 14d, or 30d.' >&2; exit 2 ;;
esac

# Read both files before opening the transaction. The marking query shares the
# same transaction as observation insertion, including when it fails.
seed=$(cat scripts/seed-demo-sli.sql)
mark=$(cat internal/storage/testdata/mark_observation_fixture.sql)
docker compose -p almanac-local -f compose.yaml exec -T postgres \
    psql --no-psqlrc --single-transaction --quiet --set ON_ERROR_STOP=1 \
    --set "sli=$ALMANAC_FIXTURE_SLI" --set "days=$days" \
    -U almanac -d almanac -f - <<SQL
$seed
PREPARE mark_fixture(uuid) AS
$mark
EXECUTE mark_fixture(:'fixture_id');
SQL
