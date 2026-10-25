#!/bin/sh
set -eu

binary=bin/almanac
listen=127.0.0.1:18089
url=http://127.0.0.1:18089
log=.local/ui-smoke.log
mkdir -p .local
"$binary" serve --components=ui --backend-url=http://127.0.0.1:18088 --listen="$listen" >"$log" 2>&1 &
server_pid=$!
cleanup() {
	kill -TERM "$server_pid" 2>/dev/null || true
	wait "$server_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

ready=0
attempt=0
while [ "$attempt" -lt 50 ]; do
	if curl --fail --silent "$url/" | rg --quiet 'Almanac'; then
		ready=1
		break
	fi
	if ! kill -0 "$server_pid" 2>/dev/null; then
		cat "$log" >&2
		exit 1
	fi
	attempt=$((attempt + 1))
	sleep 0.1
done
if [ "$ready" -ne 1 ]; then
	cat "$log" >&2
	exit 1
fi
status=$(curl --silent --output /dev/null --write-out '%{http_code}' "$url/readyz")
if [ "$status" != 503 ]; then
	echo "expected backend outage to report 503, got $status" >&2
	exit 1
fi
kill -TERM "$server_pid"
if ! wait "$server_pid"; then
	cat "$log" >&2
	exit 1
fi
trap - EXIT INT TERM
