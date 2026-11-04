#!/bin/sh
# Check the PostgreSQL fixture of compose.dev.yaml (ADR-0029) before database tests:
# the fixed database, role, server 18.6 and loopback port 15487 reached through
# ALMANAC_TEST_DATABASE_URL. The fixture is started by Compose, never by this script.
set -eu

if [ "$#" -ne 1 ] || [ "$1" != '--check' ]; then
	echo 'usage: setup-database.sh --check' >&2
	exit 2
fi

port='15487'
database='almanac_f1_storage'
username='almanac'
password='almanac_f1_t05_local_only'
url="postgres://$username:$password@127.0.0.1:$port/$database?sslmode=disable"

if [ "${ALMANAC_TEST_DATABASE_URL:-}" != "$url" ]; then
	echo 'ALMANAC_TEST_DATABASE_URL must select the fixed fixture of compose.dev.yaml (the dev container presets it)' >&2
	exit 2
fi
if ! command -v psql >/dev/null 2>&1; then
	echo 'psql is missing; run inside the dev container (make shell)' >&2
	exit 2
fi
actual_identity=$(psql --no-psqlrc --set=ON_ERROR_STOP=1 --tuples-only --no-align \
	--command="SELECT current_database() || '|' || current_user || '|' || current_setting('server_version_num') || '|' || host(inet_server_addr()) || '|' || inet_server_port()::text" \
	"$url" 2>/dev/null) || {
	echo 'PostgreSQL fixture is unreachable on loopback port 15487; start it with make shell or make setup' >&2
	exit 2
}
expected_identity="$database|$username|180006|127.0.0.1|$port"
if [ "$actual_identity" != "$expected_identity" ]; then
	echo 'fixture identity mismatch; expected PostgreSQL 18.6, the fixed database and role, loopback address and port 15487' >&2
	observed=$(printf '%s' "$actual_identity" | LC_ALL=C tr -cd 'a-zA-Z0-9_.:|/-' | cut -c1-256)
	printf 'observed database|user|server_version_num|address|port: %s\n' "$observed" >&2
	exit 2
fi
printf 'Verified PostgreSQL fixture: %s, user %s, server 18.6, loopback port %s\n' "$database" "$username" "$port"
