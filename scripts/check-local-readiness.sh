#!/bin/sh
set -eu

port=${ALMANAC_PORT:-18088}
case "$port" in ''|*[!0-9]*) echo 'invalid local readiness port' >&2; exit 2 ;; esac
if [ "${#port}" -gt 5 ] || [ "$port" -eq 0 ] || [ "$port" -gt 65535 ]; then
	echo 'invalid local readiness port' >&2
	exit 2
fi

umask 077
certificate=$(mktemp "${TMPDIR:-/tmp}/almanac-readiness.XXXXXX")
trap 'rm -f "$certificate"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if ! docker compose -f compose.yaml exec -T app cat /run/almanac-secrets/tls.crt > "$certificate"; then
	echo 'could not read the local application public certificate for host readiness' >&2
	exit 1
fi

attempt=1
while [ "$attempt" -le 3 ]; do
	status=$(curl -q --noproxy '*' --resolve "localhost:$port:127.0.0.1" \
		--cacert "$certificate" --connect-timeout 2 --max-time 5 --fail --silent --show-error \
		--output /dev/null --write-out '%{http_code}' "https://localhost:$port/readyz") || status=''
	if [ "$status" = 200 ]; then exit 0; fi
	if [ "$attempt" -lt 3 ]; then sleep 1; fi
	attempt=$((attempt + 1))
done
echo "local host readiness failed at https://localhost:$port; the port may be occupied or forwarding unavailable" >&2
exit 1
