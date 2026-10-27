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
source_keyset_file=$app_dir/source-secret-keyset.json
source_keyset_id=00000000-0000-4000-8000-000000000019

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

validate_certificate_pair() {
	certificate=$1
	openssl x509 -in "$certificate" -noout >/dev/null 2>&1 || return 1
	openssl x509 -in "$certificate" -checkhost localhost -noout >/dev/null 2>&1 || return 1
	openssl x509 -in "$certificate" -checkip 127.0.0.1 -noout >/dev/null 2>&1 || return 1
	openssl pkey -in "$key_file" -noout >/dev/null 2>&1 || return 1
	cert_public=$(openssl x509 -in "$certificate" -pubkey -noout 2>/dev/null | openssl pkey -pubin -outform DER 2>/dev/null | base64 -w0) || return 1
	key_public=$(openssl pkey -in "$key_file" -pubout -outform DER 2>/dev/null | base64 -w0) || return 1
	[ "$cert_public" = "$key_public" ]
}

validate_completed() {
	check_owner_mode "$marker" 600 || return 1
	[ "$(cat "$marker")" = 'almanac-local-v2' ] || return 1
	check_owner_mode "$db_url_file" 600 || return 1
	check_owner_mode "$admin_file" 600 || return 1
	check_owner_mode "$key_file" 600 || return 1
	check_owner_mode "$source_keyset_file" 600 || return 1
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
	validate_certificate_pair "$cert_file"
}

validate_legacy_credentials() {
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
	validate_certificate_pair "$cert_file"
}

provision_source_keyset() {
	if [ -e "$source_keyset_file" ] || [ -L "$source_keyset_file" ]; then
		check_owner_mode "$source_keyset_file" 600 || return 1
		return 0
	fi
	chown 10001:10001 "$app_dir" || return 1
	chmod 0700 "$app_dir" || return 1
	if ! setpriv --reuid=10001 --regid=10001 --clear-groups /usr/local/bin/almanac secrets provision-source-keyset \
		--file="$source_keyset_file" --keyset-id="$source_keyset_id" >/dev/null 2>&1; then
		return 1
	fi
	check_owner_mode "$source_keyset_file" 600
}

write_completion_marker() {
	temp_marker=$(mktemp "$app_dir/.initialized.XXXXXX") || return 1
	if ! printf '%s\n' 'almanac-local-v2' > "$temp_marker" || ! chown 10001:10001 "$temp_marker" || ! chmod 0600 "$temp_marker"; then
		rm -f "$temp_marker"
		return 1
	fi
	mv -f "$temp_marker" "$marker"
}

renew_certificate_if_needed() {
	if openssl x509 -in "$cert_file" -checkend 604800 -noout >/dev/null 2>&1; then
		return 0
	fi

	temp_cert=$(mktemp "$app_dir/.tls.crt.XXXXXX") || return 1
	if ! openssl req -new -x509 -key "$key_file" -sha256 -days 365 \
		-subj '/CN=localhost' \
		-addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
		-addext 'basicConstraints=critical,CA:TRUE' \
		-out "$temp_cert" >/dev/null 2>&1; then
		rm -f "$temp_cert"
		return 1
	fi
	if ! chown 10001:10001 "$temp_cert" || ! chmod 0644 "$temp_cert" || ! check_owner_mode "$temp_cert" 644; then
		rm -f "$temp_cert"
		return 1
	fi
	if ! validate_certificate_pair "$temp_cert" || ! openssl x509 -in "$temp_cert" -checkend 604800 -noout >/dev/null 2>&1; then
		rm -f "$temp_cert"
		return 1
	fi
	mv -f "$temp_cert" "$cert_file"
}

if [ -e "$marker" ] || [ -L "$marker" ]; then
	if validate_completed; then
		:
	elif validate_legacy_credentials; then
		if ! provision_source_keyset; then
			echo 'local initializer: legacy source keyset provisioning failed' >&2
			fail_state
		fi
		if ! write_completion_marker; then
			echo 'local initializer: legacy completion marker update failed' >&2
			fail_state
		fi
		if ! validate_completed; then
			echo 'local initializer: upgraded local credential state did not validate' >&2
			fail_state
		fi
	else
		echo 'local initializer: legacy local credential state validation failed' >&2
		fail_state
	fi
	if ! renew_certificate_if_needed; then
		echo 'local initializer: localhost certificate renewal failed; saved credentials were preserved' >&2
		exit 1
	fi
	exit 0
fi

# Any pre-existing credential material without a completion marker is ambiguous.
for file in "$db_url_file" "$admin_file" "$cert_file" "$key_file" "$pg_password_file" "$source_keyset_file"; do
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
if ! provision_source_keyset || ! write_completion_marker; then fail_state; fi

if ! validate_completed; then fail_state; fi
