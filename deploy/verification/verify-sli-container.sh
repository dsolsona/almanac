#!/bin/sh
set -eu

if [ "$#" -ne 0 ]; then
	echo 'verify-sli-container.sh does not accept arguments' >&2
	exit 2
fi

candidate='/candidate'
artifacts='/artifacts'
work='/tmp/almanac-linux-sli-candidate'
raw_dir='/tmp/almanac-linux-sli-raw'
verifier_home='/tmp/almanac-linux-sli-home'
fixed_database_url='postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable'

candidate_source_hash=${ALMANAC_CANDIDATE_SOURCE_SHA256:-}
if [ "${#candidate_source_hash}" -ne 64 ]; then
	echo 'candidate source SHA256 is missing or malformed' >&2
	exit 2
fi
case "$candidate_source_hash" in *[!0-9a-f]*) echo 'candidate source SHA256 is malformed' >&2; exit 2 ;; esac
if [ "${ALMANAC_LINUX_VERIFICATION_CONTAINER:-}" != '1' ] || \
	[ ! -r /etc/almanac-linux-verifier ] || \
	[ "$(cat /etc/almanac-linux-verifier)" != 'F2-T22-linux-verifier-v1' ] || \
	[ "${ALMANAC_VERIFICATION_SETUP_FINGERPRINT:-}" != "$(cat /etc/almanac-linux-verifier-fingerprint)" ]; then
	echo 'fixed Linux verifier image identity did not match the prepared setup' >&2
	exit 2
fi

if [ -e "$work" ] || [ -L "$work" ]; then
	echo 'candidate work directory already exists' >&2
	exit 2
fi
mkdir -p "$artifacts" "$raw_dir" "$verifier_home" "$work"
python3 "$candidate/deploy/verification/snapshot.py" verify "$candidate" "$ALMANAC_CANDIDATE_SOURCE_SHA256"
preflight_status=0
env -i HOME="$verifier_home" PATH='/usr/local/go/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
	ALMANAC_LINUX_VERIFICATION_CONTAINER=1 ALMANAC_TEST_DATABASE_URL="$fixed_database_url" \
	/bin/sh "$candidate/scripts/setup-database.sh" --check-container > "$raw_dir/database_preflight.log" 2>&1 || preflight_status=$?
python3 "$candidate/deploy/verification/export-artifacts.py" sanitize-log "$raw_dir/database_preflight.log" "$artifacts/steps/database_preflight.log"
if [ "$preflight_status" -ne 0 ]; then
	cat "$artifacts/steps/database_preflight.log" >&2
	printf 'verification_mode=focused_sli_journey\nfull_linux_gate=not_run\nstep_database_preflight_exit=%s\nfirst_failure=database_preflight\nfirst_failure_exit=%s\noverall_exit=%s\n' \
		"$preflight_status" "$preflight_status" "$preflight_status" > "$artifacts/verification-status.txt"
	exit "$preflight_status"
fi
cp -R "$candidate"/. "$work"/
mkdir -p "$work/.local/bin" "$work/.local/go"
for destination in "$work/.local/go/mod" "$work/web/node_modules" "$work/tools/openapi/node_modules"; do
	if [ -e "$destination" ] || [ -L "$destination" ]; then
		echo 'candidate dependency cache destination already exists' >&2
		exit 2
	fi
done
for dependency in /go/pkg/mod /opt/almanac/web/node_modules /opt/almanac/tools/openapi/node_modules; do
	if [ ! -d "$dependency" ]; then
		echo 'fixed verifier dependency cache is missing' >&2
		exit 2
	fi
done
ln -s /go/pkg/mod "$work/.local/go/mod"
ln -s /opt/almanac/web/node_modules "$work/web/node_modules"
ln -s /opt/almanac/tools/openapi/node_modules "$work/tools/openapi/node_modules"
ln -s /usr/local/go/bin/go "$work/.local/bin/go"
ln -s /usr/local/go/bin/gofmt "$work/.local/bin/gofmt"
ln -s /usr/local/bin/go-modern-guidelines "$work/.local/bin/go-modern-guidelines"
ln -s /usr/local/bin/oapi-codegen "$work/.local/bin/oapi-codegen"
ln -s /usr/local/bin/sqlc "$work/.local/bin/sqlc"

overall="$artifacts/verification.log"
summary="$artifacts/verification-status.txt"
: > "$overall"
: > "$summary"
printf 'step_database_preflight_exit=0\n' >> "$summary"
printf 'Evidence type: focused SLI browser journey only\n' | tee -a "$overall"
printf 'Candidate source SHA256: %s\n' "$ALMANAC_CANDIDATE_SOURCE_SHA256" | tee -a "$overall"
printf 'Verifier setup fingerprint: %s\n' "$ALMANAC_VERIFICATION_SETUP_FINGERPRINT" | tee -a "$overall"
printf 'verification_mode=focused_sli_journey\nselected_test=TestIntegrationExecutableBrowserSLICollectionJourney\nfull_linux_gate=not_run\n' >> "$summary"

finalize() {
	status=$?
	trap - EXIT
	if ! python3 "$work/deploy/verification/export-artifacts.py" collect "$work" "$artifacts"; then
		if [ "$status" -eq 0 ]; then status=1; fi
	fi
	printf 'overall_exit=%s\n' "$status" >> "$summary"
	printf 'Focused SLI journey exit status: %s\n' "$status"
	exit "$status"
}
trap finalize EXIT

run_journey() {
	raw="$raw_dir/sli_journey.log"
	safe="$artifacts/steps/sli_journey.log"
	start=$(date +%s)
	status=0
	env -i HOME="$verifier_home" PATH='/usr/local/go/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
		PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
		ALMANAC_LINUX_VERIFICATION_CONTAINER=1 \
		ALMANAC_TEST_DATABASE_URL="$fixed_database_url" \
		make verify VERIFY_MODE=journey BROWSER_SCENARIO=slis > "$raw" 2>&1 || status=$?
	end=$(date +%s)
	python3 "$work/deploy/verification/export-artifacts.py" sanitize-log "$raw" "$safe"
	{
		printf '\n=== focused SLI browser journey: exit %s, elapsed %ss ===\n' "$status" "$((end - start))"
		cat "$safe"
	} | tee -a "$overall"
	printf 'step_sli_journey_exit=%s\nstep_sli_journey_elapsed_seconds=%s\n' "$status" "$((end - start))" >> "$summary"
	if [ "$status" -ne 0 ]; then
		printf 'first_failure=sli_journey\nfirst_failure_exit=%s\n' "$status" >> "$summary"
		return "$status"
	fi
	if ! grep -F -- '--- PASS: TestIntegrationExecutableBrowserSLICollectionJourney (' "$safe" >/dev/null; then
		printf 'first_failure=sli_journey_test_not_observed\nfirst_failure_exit=1\n' >> "$summary"
		echo 'selected SLI browser journey did not execute and pass' | tee -a "$overall" >&2
		return 1
	fi
	printf 'first_failure=none\nfirst_failure_exit=0\n' >> "$summary"
}

cd "$work"
run_journey
