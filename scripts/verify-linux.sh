#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
cd "$root"

if [ "$#" -ne 1 ]; then
	echo 'usage: verify-linux.sh setup | verify | sli' >&2
	exit 2
fi
operation=$1
case "$operation" in
	setup|verify|sli) ;;
	*) echo 'usage: verify-linux.sh setup | verify | sli' >&2; exit 2 ;;
esac

postgres_image='postgres:18.6@sha256:9770878138a2e83abcb10856d30cc3f24dae0d317bb956db6ab9159de6c3c80d'
fixed_database='almanac_f1_storage'
fixed_username='almanac'
fixed_password='almanac_f1_t05_local_only'
fixed_port='15487'
artifact_dir=''
snapshot_dir=''
build_context=''
postgres_id=''
verifier_id=''
run_state='not_started'
run_detail='verification did not start'
setup_fingerprint=''
candidate_hash=''
started_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
started_epoch=$(date '+%s')
report="$root/.local/linux-report.md"
mkdir -p "$root/.local"

ensure_local_docker() {
	if [ -n "${DOCKER_HOST:-}" ]; then
		echo 'Linux verification requires the active local Docker context; DOCKER_HOST overrides are unsupported' >&2
		return 2
	fi
	context=$(docker context show) || return $?
	endpoint=$(docker context inspect "$context" --format '{{.Endpoints.docker.Host}}') || return $?
	case "$endpoint" in
		unix://*|npipe://*) ;;
		*) echo 'Linux verification will not send its source snapshot to a remote Docker endpoint' >&2; return 2 ;;
	esac
	return 0
}

setup_image_tag() {
	setup_fingerprint=$(python3 deploy/verification/fingerprint.py "$root") || return $?
	if [ "${#setup_fingerprint}" -ne 64 ]; then
		echo 'verifier setup fingerprint is malformed' >&2
		return 2
	fi
	case "$setup_fingerprint" in *[!0-9a-f]*) echo 'verifier setup fingerprint is malformed' >&2; return 2 ;; esac
	verifier_image="almanac-linux-verifier:$setup_fingerprint"
}

write_report() {
	end_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
	end_epoch=$(date '+%s')
	elapsed=$((end_epoch - started_epoch))
	artifact_ref='not created'
	if [ -n "$artifact_dir" ]; then artifact_ref=${artifact_dir#"$root/"}; fi
	status_file=''
	if [ -n "$artifact_dir" ]; then status_file="$artifact_dir/verification-status.txt"; fi
	python3 deploy/verification/report.py "$report" "$started_utc" "$end_utc" \
		"$run_state" "$run_detail" "$elapsed" "${setup_fingerprint:-unavailable}" \
		"${candidate_hash:-unavailable}" "$artifact_ref" "$fixed_database" \
		"$fixed_username" "$fixed_port" "$status_file"
}

cleanup() {
	status=$?
	cleanup_error=0
	cleanup_detail=''
	trap - EXIT
	if [ -n "$verifier_id" ] && ! docker rm --force "$verifier_id" >/dev/null; then
		echo "failed to remove invocation-owned verifier container $verifier_id" >&2
		cleanup_error=1
		cleanup_detail='verifier container cleanup failed'
	fi
	if [ -n "$postgres_id" ] && ! docker rm --force --volumes "$postgres_id" >/dev/null; then
		echo "failed to remove invocation-owned PostgreSQL container $postgres_id" >&2
		cleanup_error=1
		cleanup_detail='PostgreSQL fixture cleanup failed'
	fi
	if [ -n "$snapshot_dir" ] && ! rm -rf "$snapshot_dir"; then
		echo "failed to remove invocation-owned source snapshot $snapshot_dir" >&2
		cleanup_error=1
		cleanup_detail='source snapshot cleanup failed'
	fi
	if [ -n "$build_context" ] && ! rm -rf "$build_context"; then
		echo "failed to remove invocation-owned setup context $build_context" >&2
		cleanup_error=1
		cleanup_detail='setup context cleanup failed'
	fi
	if [ "$cleanup_error" -eq 1 ]; then
		if [ "$status" -eq 0 ]; then
			status=1
			run_state='cleanup_failed'
			run_detail="$cleanup_detail"
		else
			run_detail="$run_detail; $cleanup_detail"
		fi
	fi
	if [ "$operation" = 'verify' ] || [ "$operation" = 'sli' ]; then
		if [ "$status" -ne 0 ] && [ "$run_state" = 'running' ]; then
			run_state='failed'
			if [ "$operation" = 'sli' ]; then
				run_detail="focused SLI browser journey exited $status; full Linux gate not run"
			else
				run_detail="first failing command exited $status"
			fi
		elif [ "$status" -ne 0 ] && [ "$run_state" = 'not_started' ]; then
			run_state='blocked'
			run_detail='verifier image, Docker context, or source snapshot was unavailable'
		fi
		if [ "$cleanup_error" -eq 1 ] && [ "$status" -ne 0 ]; then
			run_detail="$run_detail; $cleanup_detail"
		fi
		if ! write_report; then
			echo "failed to write Linux verification report: $report" >&2
			if [ "$status" -eq 0 ]; then status=1; fi
		fi
	fi
	exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

ensure_local_docker
setup_image_tag

if [ "$operation" = 'setup' ]; then
	mkdir -p "$root/.local"
	build_context=$(mktemp -d "$root/.local/linux-verification-build.XXXXXX")
	mkdir -p "$build_context/deploy/verification" "$build_context/web" "$build_context/tools/openapi"
	cp go.mod go.sum "$build_context/"
	cp web/package.json web/package-lock.json "$build_context/web/"
	cp tools/openapi/package.json tools/openapi/package-lock.json "$build_context/tools/openapi/"
	cp deploy/verification/Dockerfile "$build_context/deploy/verification/"
	printf '%s\n' "$setup_fingerprint" > "$build_context/setup-fingerprint"
	docker build --pull --build-arg "SETUP_FINGERPRINT=$setup_fingerprint" \
		--tag "$verifier_image" --file "$build_context/deploy/verification/Dockerfile" "$build_context"
	docker pull "$postgres_image"
	image_fingerprint=$(docker image inspect "$verifier_image" --format '{{index .Config.Labels "org.almanac.verification.setup-fingerprint"}}')
	if [ "$image_fingerprint" != "$setup_fingerprint" ]; then
		echo 'built verifier image did not retain its setup fingerprint label' >&2
		exit 1
	fi
	printf 'Prepared Linux verifier image: %s\nSetup fingerprint: %s\nPinned PostgreSQL image: %s\n' \
		"$verifier_image" "$setup_fingerprint" "$postgres_image"
	exit 0
fi

if [ "${ALMANAC_LINUX_VERIFICATION_CONTAINER:-}" = '1' ]; then
	echo 'recursive Linux verification is not allowed inside the verifier container' >&2
	exit 2
fi

image_fingerprint=$(docker image inspect "$verifier_image" --format '{{index .Config.Labels "org.almanac.verification.setup-fingerprint"}}' 2>/dev/null || true)
if [ "$image_fingerprint" != "$setup_fingerprint" ]; then
	run_detail='matching pinned verifier image is missing; run make setup SETUP_MODE=verification'
	echo "$run_detail" >&2
	exit 2
fi
postgres_image_id=$(docker image inspect "$postgres_image" --format '{{.Id}}' 2>/dev/null || true)
if [ -z "$postgres_image_id" ]; then
	run_detail='pinned PostgreSQL 18.6 image is missing; run make setup SETUP_MODE=verification'
	echo "$run_detail" >&2
	exit 2
fi

mkdir -p "$root/.local"
artifact_dir=$(mktemp -d "$root/.local/linux-verification.XXXXXX")
snapshot_dir=$(mktemp -d "$root/.local/linux-source.XXXXXX")
snapshot_manifest="$artifact_dir/source-manifest.txt"
python3 deploy/verification/snapshot.py create "$root" "$snapshot_dir" > "$snapshot_manifest"
candidate_hash=$(sed -n 's/^source_sha256=//p' "$snapshot_manifest")
source_count=$(sed -n 's/^source_files=//p' "$snapshot_manifest")
excluded_count=$(sed -n 's/^excluded_local_or_sensitive_files=//p' "$snapshot_manifest")
if [ "${#candidate_hash}" -ne 64 ]; then
	echo 'candidate source snapshot hash is malformed' >&2
	exit 2
fi
case "$candidate_hash" in *[!0-9a-f]*) echo 'candidate source snapshot hash is malformed' >&2; exit 2 ;; esac
if [ "$operation" = 'sli' ]; then
	evidence_mode='focused_sli_journey'
	container_script='/candidate/deploy/verification/verify-sli-container.sh'
	run_detail='focused SLI browser journey started; full Linux gate not run'
else
	evidence_mode='full_linux_gate'
	container_script='/candidate/deploy/verification/verify-container.sh'
	run_detail='fixed verification sequence started'
fi
printf 'candidate_source_sha256=%s\nsource_files=%s\nexcluded_local_or_sensitive_files=%s\n' \
	"$candidate_hash" "$source_count" "$excluded_count" > "$artifact_dir/source-attestation.txt"
printf 'verification_mode=%s\n' "$evidence_mode" >> "$artifact_dir/source-attestation.txt"

run_suffix=${artifact_dir##*.}
postgres_name="almanac-f2-t22-pg-$run_suffix"
verifier_name="almanac-f2-t22-runner-$run_suffix"
run_state='running'
printf 'Starting isolated PostgreSQL fixture for source %s\n' "$candidate_hash"
postgres_id=$(docker create --network none --name "$postgres_name" \
	--tmpfs /var/lib/postgresql:rw,nosuid,nodev,size=512m \
	--label almanac.fixture=integration --label almanac.task=F2-T22-linux \
	--label "almanac.run=$run_suffix" \
	--env "POSTGRES_DB=$fixed_database" --env "POSTGRES_USER=$fixed_username" \
	--env "POSTGRES_PASSWORD=$fixed_password" \
	"$postgres_image" postgres -p "$fixed_port")

actual_pg_image=$(docker inspect "$postgres_id" --format '{{.Config.Image}}')
actual_pg_image_id=$(docker inspect "$postgres_id" --format '{{.Image}}')
actual_pg_fixture=$(docker inspect "$postgres_id" --format '{{index .Config.Labels "almanac.fixture"}}')
actual_pg_task=$(docker inspect "$postgres_id" --format '{{index .Config.Labels "almanac.task"}}')
actual_pg_run=$(docker inspect "$postgres_id" --format '{{index .Config.Labels "almanac.run"}}')
actual_pg_network=$(docker inspect "$postgres_id" --format '{{.HostConfig.NetworkMode}}')
actual_pg_ports=$(docker inspect "$postgres_id" --format '{{json .HostConfig.PortBindings}}')
actual_pg_mounts=$(docker inspect "$postgres_id" --format '{{json .Mounts}}')
actual_pg_tmpfs=$(docker inspect "$postgres_id" --format '{{json .HostConfig.Tmpfs}}')
actual_pg_command=$(docker inspect "$postgres_id" --format '{{json .Config.Cmd}}')
actual_pg_env=$(docker inspect "$postgres_id" --format '{{range .Config.Env}}{{println .}}{{end}}')
{
	printf 'container_id=%s\ncontainer_image=%s\ncontainer_image_id=%s\nfixture_label=%s\ntask_label=%s\nrun_label=%s\nnetwork_mode=%s\nhost_port_bindings=%s\nmounts=%s\ntmpfs=%s\ncommand=%s\n' \
		"$postgres_id" "$actual_pg_image" "$actual_pg_image_id" "$actual_pg_fixture" "$actual_pg_task" "$actual_pg_run" "$actual_pg_network" "$actual_pg_ports" "$actual_pg_mounts" "$actual_pg_tmpfs" "$actual_pg_command"
	printf 'expected_database=%s\nexpected_role=%s\nexpected_server_version=18.6\nexpected_address=127.0.0.1\nexpected_port=%s\n' \
		"$fixed_database" "$fixed_username" "$fixed_port"
} > "$artifact_dir/fixture-definition.txt"
if [ "$actual_pg_image" != "$postgres_image" ] || [ "$actual_pg_image_id" != "$postgres_image_id" ] || \
	[ "$actual_pg_fixture" != 'integration' ] || [ "$actual_pg_task" != 'F2-T22-linux' ] || \
	[ "$actual_pg_run" != "$run_suffix" ] || [ "$actual_pg_network" != 'none' ] || \
	[ "$actual_pg_command" != '["postgres","-p","15487"]' ] || \
	[ "$actual_pg_mounts" != '[]' ] || \
	[ "$actual_pg_tmpfs" != '{"/var/lib/postgresql":"rw,nosuid,nodev,size=512m"}' ] || \
	{ [ "$actual_pg_ports" != 'null' ] && [ "$actual_pg_ports" != '{}' ]; } || \
	! printf '%s\n' "$actual_pg_env" | grep -Fxq "POSTGRES_DB=$fixed_database" || \
	! printf '%s\n' "$actual_pg_env" | grep -Fxq "POSTGRES_USER=$fixed_username" || \
	! printf '%s\n' "$actual_pg_env" | grep -Fxq "POSTGRES_PASSWORD=$fixed_password"; then
	echo 'created PostgreSQL container did not match the fixed isolated fixture definition' >&2
	exit 2
fi

docker start "$postgres_id" >/dev/null
if ! docker exec "$postgres_id" sh -c '
	set -eu
	awk '\''$2 == "/var/lib/postgresql" && $3 == "tmpfs" { found++ } END { exit found != 1 }'\'' /proc/mounts
	df -B1 --output=fstype,size,target /var/lib/postgresql | awk '\''NR == 2 { exit !($1 == "tmpfs" && $2 == 536870912 && $3 == "/var/lib/postgresql") } END { if (NR != 2) exit 1 }'\''
'; then
	echo 'PostgreSQL fixture data directory is not the bounded invocation-owned tmpfs' >&2
	exit 2
fi
ready=0
for _ in $(seq 1 40); do
	if docker exec "$postgres_id" pg_isready -h 127.0.0.1 -p "$fixed_port" -U "$fixed_username" -d "$fixed_database" >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 1
done
if [ "$ready" -ne 1 ]; then
	echo 'invocation-owned PostgreSQL fixture did not become ready within 40 seconds' >&2
	exit 1
fi
printf 'runtime_tmpfs_validation=passed\n' >> "$artifact_dir/fixture-definition.txt"

verifier_id=$(docker create --name "$verifier_name" --label almanac.task=F2-T22-linux \
	--label "almanac.run=$run_suffix" --network "container:$postgres_id" \
	--mount "type=bind,src=$snapshot_dir,dst=/candidate,readonly" \
	--mount "type=bind,src=$artifact_dir,dst=/artifacts" \
	--env "ALMANAC_CANDIDATE_SOURCE_SHA256=$candidate_hash" \
	--env "ALMANAC_VERIFICATION_SETUP_FINGERPRINT=$setup_fingerprint" \
	--env ALMANAC_LINUX_VERIFICATION_CONTAINER=1 \
	"$verifier_image" /bin/sh "$container_script")
docker start --attach "$verifier_id" || exit "$?"
if [ "$operation" = 'sli' ]; then
	run_state='focused_sli_passed'
	run_detail='focused SLI browser journey passed; full Linux gate not run'
else
	run_state='passed'
	run_detail='ordinary, race, integration, and catalogue browser gates passed'
fi
exit 0
