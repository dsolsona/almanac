#!/bin/sh
set -eu

check_only=0
container_check=0
if [ "$#" -gt 0 ]; then
	if [ "$#" -ne 1 ]; then
		echo 'usage: setup-database.sh [--check | --check-container]' >&2
		exit 2
	fi
	case "$1" in
		--check) check_only=1 ;;
		--check-container) container_check=1 ;;
		*) echo 'usage: setup-database.sh [--check | --check-container]' >&2; exit 2 ;;
	esac
fi

image='postgres:18.6@sha256:9770878138a2e83abcb10856d30cc3f24dae0d317bb956db6ab9159de6c3c80d'
container='almanac-f1-t05-postgres18.6'
port='15487'
database='almanac_f1_storage'
username='almanac'
password='almanac_f1_t05_local_only'

if [ "$container_check" -eq 1 ]; then
	url="postgres://$username:$password@127.0.0.1:$port/$database?sslmode=disable"
	if [ "${ALMANAC_LINUX_VERIFICATION_CONTAINER:-}" != '1' ] || \
		[ ! -r /etc/almanac-linux-verifier ] || \
		[ "$(cat /etc/almanac-linux-verifier)" != 'F2-T22-linux-verifier-v1' ]; then
		echo 'container fixture check is only available inside the fixed Linux verifier image' >&2
		exit 2
	fi
	if [ "${ALMANAC_TEST_DATABASE_URL:-}" != "$url" ]; then
		echo 'container fixture check requires the fixed F1-T05 integration URL' >&2
		exit 2
	fi
	if ! command -v psql >/dev/null 2>&1; then
		echo 'PostgreSQL client is missing from the Linux verifier image' >&2
		exit 2
	fi
	actual_identity=$(psql --no-psqlrc --set=ON_ERROR_STOP=1 --tuples-only --no-align \
		--command="SELECT current_database() || '|' || current_user || '|' || current_setting('server_version_num') || '|' || host(inet_server_addr()) || '|' || inet_server_port()::text" \
		"$url" 2>/dev/null) || {
		echo 'fixed PostgreSQL integration fixture is unreachable from the Linux verifier namespace' >&2
		exit 2
	}
	expected_identity="$database|$username|180006|127.0.0.1|$port"
	if [ "$actual_identity" != "$expected_identity" ]; then
		echo 'container fixture identity mismatch; expected PostgreSQL 18.6, fixed database/user, loopback address, and port 15487' >&2
		observed=$(printf '%s' "$actual_identity" | LC_ALL=C tr -cd 'a-zA-Z0-9_.:|/-' | cut -c1-256)
		printf 'observed database|user|server_version_num|address|port: %s\n' "$observed" >&2
		exit 2
	fi
	printf 'Verified isolated PostgreSQL fixture: %s, user %s, server 18.6, loopback port %s\n' "$database" "$username" "$port"
	exit 0
fi

# Preserve Docker's diagnostic: an unreachable daemon or denied socket is not
# evidence that the pinned image is absent.
image_id=$(docker image inspect "$image" --format '{{.Id}}' || true)
if [ -z "$image_id" ]; then
	if [ "$check_only" -eq 1 ]; then
		echo 'Required pinned PostgreSQL 18.6 image is not available locally' >&2
		exit 2
	fi
	docker pull "$image"
	image_id=$(docker image inspect "$image" --format '{{.Id}}')
fi
container_id=$(docker container ls -aq --filter "name=^/${container}$")
if [ -n "$container_id" ]; then
	actual_image=$(docker inspect --format '{{.Config.Image}}' "$container_id")
	actual_image_id=$(docker inspect --format '{{.Image}}' "$container_id")
	actual_fixture=$(docker inspect --format '{{index .Config.Labels "almanac.fixture"}}' "$container_id")
	actual_task=$(docker inspect --format '{{index .Config.Labels "almanac.task"}}' "$container_id")
	actual_ports=$(docker inspect --format '{{json .HostConfig.PortBindings}}' "$container_id")
	actual_env=$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$container_id")
	if [ "$actual_image" != "$image" ] || [ "$actual_image_id" != "$image_id" ] || \
		[ "$actual_fixture" != 'integration' ] || [ "$actual_task" != 'F1-T05' ] || \
		[ "$actual_ports" != '{"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":"15487"}]}' ] || \
		! printf '%s\n' "$actual_env" | grep -Fxq "POSTGRES_DB=$database" || \
		! printf '%s\n' "$actual_env" | grep -Fxq "POSTGRES_USER=$username" || \
		! printf '%s\n' "$actual_env" | grep -Fxq "POSTGRES_PASSWORD=$password"; then
		echo 'Existing PostgreSQL container name is occupied by a non-matching fixture; leaving it untouched' >&2
		exit 2
	fi
	if [ "$(docker inspect --format '{{.State.Running}}' "$container_id")" != true ]; then
		if [ "$check_only" -eq 1 ]; then
			echo 'PostgreSQL integration fixture is not running' >&2
			exit 2
		fi
		docker start "$container_id" >/dev/null
	fi
else
	if [ "$check_only" -eq 1 ]; then
		echo 'PostgreSQL integration fixture is not present' >&2
		exit 2
	fi
	container_id=$(docker run --detach --pull=never --name "$container" \
		--label almanac.fixture=integration --label almanac.task=F1-T05 \
		--publish "127.0.0.1:$port:5432" \
		--env "POSTGRES_DB=$database" --env "POSTGRES_USER=$username" \
		--env "POSTGRES_PASSWORD=$password" "$image")
fi

ready=0
for _ in $(seq 1 40); do
	if docker exec "$container_id" psql -v ON_ERROR_STOP=1 -U "$username" -d "$database" -Atqc 'SELECT 1' >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 1
done
if [ "$ready" -ne 1 ]; then
	echo 'Pinned PostgreSQL test fixture did not become ready within 40 seconds' >&2
	exit 1
fi
if [ "$check_only" -eq 1 ]; then
	printf 'Verified PostgreSQL fixture: %s (loopback port %s)\n' "$container_id" "$port"
else
	printf 'Fixture container: %s\nPostgreSQL endpoint: 127.0.0.1:%s\nTest URL: postgres://%s:%s@127.0.0.1:%s/%s?sslmode=disable\n' \
		"$container_id" "$port" "$username" "$password" "$port" "$database"
fi
