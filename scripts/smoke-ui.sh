#!/bin/sh
set -eu

binary=bin/almanac
listen=127.0.0.1:0
log=.local/ui-smoke.log
mkdir -p .local
"$binary" serve --components=ui --backend-url=http://127.0.0.1:1 --listen="$listen" --shutdown-timeout=1s >"$log" 2>&1 &
server_pid=$!
cleanup() {
	kill -KILL "$server_pid" 2>/dev/null || true
	wait "$server_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

url=
ready=0
attempt=0
while [ "$attempt" -lt 20 ]; do
	address=$(sed -n 's/.*"address":"\([^"]*\)".*/\1/p' "$log" | tail -n 1)
	if [ -n "$address" ]; then
		url="http://$address"
		if curl --fail --silent --connect-timeout 0.25 --max-time 1 "$url/" | rg --quiet 'Almanac'; then
			ready=1
			break
		fi
	fi
	if rg --quiet '"msg":"listener failed"' "$log"; then
		cat "$log" >&2
		exit 1
	fi
	state=$(ps -o stat= -p "$server_pid" 2>/dev/null || true)
	case "$state" in
		''|Z*|*Z*) cat "$log" >&2; exit 1 ;;
	esac
	attempt=$((attempt + 1))
	sleep 0.1
done
if [ "$ready" -ne 1 ]; then
	cat "$log" >&2
	exit 1
fi
status=$(curl --silent --connect-timeout 0.25 --max-time 3 --output /dev/null --write-out '%{http_code}' "$url/readyz")
if [ "$status" != 503 ]; then
	echo "expected backend outage to report 503, got $status" >&2
	exit 1
fi

kill -TERM "$server_pid"
attempt=0
finished=0
while [ "$attempt" -lt 30 ]; do
	state=$(ps -o stat= -p "$server_pid" 2>/dev/null || true)
	case "$state" in
		''|Z*|*Z*) finished=1; break ;;
	esac
	attempt=$((attempt + 1))
	sleep 0.1
done
if [ "$finished" -ne 1 ]; then
	cleanup
	cat "$log" >&2
	echo 'server did not stop within the bounded smoke deadline' >&2
	exit 1
fi
if ! wait "$server_pid"; then
	cat "$log" >&2
	exit 1
fi
trap - EXIT INT TERM
