#!/bin/sh
set -eu

app_dir=/run/almanac-secrets
postgres_dir=/run/postgres-secret

if [ "$#" -ne 0 ]; then
	if [ "$#" -ne 2 ]; then
		echo 'usage: almanac-local-init [APP_SECRET_DIR POSTGRES_SECRET_DIR]' >&2
		exit 2
	fi
	app_dir=$1
	postgres_dir=$2
	case "$app_dir:$postgres_dir" in
		/tmp/*:/tmp/*) ;;
		*) echo 'test initializer paths must be below /tmp' >&2; exit 2 ;;
	esac
	[ "${#app_dir}" -le 512 ] && [ "${#postgres_dir}" -le 512 ] && [ "$app_dir" != "$postgres_dir" ] || {
		echo 'test initializer paths must be distinct and at most 512 characters' >&2
		exit 2
	}
fi

case "$app_dir:$postgres_dir" in
	/*:/*) ;;
	*) echo 'initializer paths must be absolute' >&2; exit 2 ;;
esac

mkdir -p "$app_dir" "$postgres_dir"
if [ -L "$app_dir/.init.lock" ]; then
	echo 'local initializer: unsafe lock file; refusing initialization' >&2
	exit 1
fi
exec 9>"$app_dir/.init.lock"
flock -x 9
umask 077

marker=$app_dir/.initialized
db_url_file=$app_dir/database-url
admin_file=$app_dir/initial-admin-password
cert_file=$app_dir/tls.crt
key_file=$app_dir/tls.key
pg_password_file=$postgres_dir/password

fail_state() {
	echo 'local initializer: saved credential state is incomplete or unsafe; refusing to regenerate it' >&2
	exit 1
}

check_owner_mode() {
	file=$1
	want_mode=$2
	[ -f "$file" ] && [ ! -L "$file" ] || return 1
	[ "$(stat -c '%u:%a' "$file")" = "10001:$want_mode" ]
}

validate_completed() {
	check_owner_mode "$marker" 600 || return 1
	[ "$(cat "$marker")" = 'almanac-local-v1' ] || return 1
	check_owner_mode "$db_url_file" 600 || return 1
	check_owner_mode "$admin_file" 600 || return 1
	check_owner_mode "$key_file" 600 || return 1
	check_owner_mode "$cert_file" 644 || return 1
	check_owner_mode "$pg_password_file" 444 || return 1
	db_url=$(cat "$db_url_file") || return 1
	pg_password=$(cat "$pg_password_file") || return 1
	admin_password=$(cat "$admin_file") || return 1
	[ "$db_url" = "postgres://almanac:$pg_password@postgres:5432/almanac?sslmode=disable" ] || return 1
	case "$pg_password$admin_password" in *[!A-Fa-f0-9]*|'') return 1 ;; esac
	[ "$(wc -c < "$db_url_file" | tr -d '[:space:]')" = "${#db_url}" ] || return 1
	[ "${#pg_password}" -eq 64 ] || return 1
	[ "${#admin_password}" -ge 32 ] || return 1
	[ "$(wc -c < "$pg_password_file" | tr -d '[:space:]')" = 64 ] || return 1
	[ "$(wc -c < "$admin_file" | tr -d '[:space:]')" = 64 ] || return 1
	openssl x509 -in "$cert_file" -noout >/dev/null 2>&1 || return 1
	openssl x509 -in "$cert_file" -checkhost localhost -noout >/dev/null 2>&1 || return 1
	openssl x509 -in "$cert_file" -checkip 127.0.0.1 -noout >/dev/null 2>&1 || return 1
	openssl x509 -in "$cert_file" -checkend 0 -noout >/dev/null 2>&1 || return 1
	openssl pkey -in "$key_file" -noout >/dev/null 2>&1 || return 1
	cert_public=$(openssl x509 -in "$cert_file" -pubkey -noout 2>/dev/null | openssl pkey -pubin -outform DER 2>/dev/null | base64 -w0) || return 1
	key_public=$(openssl pkey -in "$key_file" -pubout -outform DER 2>/dev/null | base64 -w0) || return 1
	[ "$cert_public" = "$key_public" ]
}

if [ -e "$marker" ] || [ -L "$marker" ]; then
	if ! validate_completed; then fail_state; fi
	exit 0
fi

# Any pre-existing credential material without a completion marker is ambiguous.
for file in "$db_url_file" "$admin_file" "$cert_file" "$key_file" "$pg_password_file"; do
	if [ -e "$file" ] || [ -L "$file" ]; then fail_state; fi
done

db_password=$(openssl rand -hex 32)
admin_password=$(openssl rand -hex 32)
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$key_file" >/dev/null 2>&1
openssl req -new -x509 -key "$key_file" -sha256 -days 365 \
	-subj '/CN=localhost' \
	-addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
	-addext 'basicConstraints=critical,CA:TRUE' \
	-out "$cert_file" >/dev/null 2>&1
printf '%s' "$db_password" > "$pg_password_file"
printf 'postgres://almanac:%s@postgres:5432/almanac?sslmode=disable' "$db_password" > "$db_url_file"
printf '%s' "$admin_password" > "$admin_file"
chown 10001:10001 "$db_url_file" "$admin_file" "$cert_file" "$key_file" "$pg_password_file"
chmod 0600 "$db_url_file" "$admin_file" "$key_file"
chmod 0644 "$cert_file"
chmod 0444 "$pg_password_file"
printf '%s\n' 'almanac-local-v1' > "$marker"
chown 10001:10001 "$marker"
chmod 0600 "$marker"

if ! validate_completed; then fail_state; fi
