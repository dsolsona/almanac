#!/bin/sh
set -eu

script=/usr/local/lib/almanac/init.sh
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT HUP INT TERM
chmod 0755 "$root"
app=$root/app
pg=$root/postgres
mkdir -p "$app" "$pg"

fail() {
	echo "initializer check failed: $*" >&2
	exit 1
}

refuse_without_mutation() {
	label=$1
	before=$(hashes)
	if sh "$script" "$app" "$pg" >/dev/null 2>&1; then fail "accepted $label"; fi
	after=$(hashes)
	[ "$before" = "$after" ] || fail "changed files while refusing $label"
}

hashes() {
	find "$app" "$pg" -type f -exec sh -c 'for file do stat -c "%u:%g:%a %n" "$file"; sha256sum "$file"; done' sh {} + | sort
}

stable_secret_state() {
	for file in "$app/database-url" "$app/initial-admin-password" "$app/source-secret-keyset.json" "$app/tls.key" "$app/.initialized" "$pg/password"; do
		stat -c '%u:%g:%a %n' "$file"
		sha256sum "$file"
	done | sort
}

certificate_matches_key() {
	cert_public=$(openssl x509 -in "$app/tls.crt" -pubkey -noout 2>/dev/null | openssl pkey -pubin -outform DER 2>/dev/null | base64 -w0)
	key_public=$(openssl pkey -in "$app/tls.key" -pubout -outform DER 2>/dev/null | base64 -w0)
	[ "$cert_public" = "$key_public" ]
}

sh "$script" "$app" "$pg"
first=$(hashes)
sh "$script" "$app" "$pg"
second=$(hashes)
[ "$first" = "$second" ] || fail 'repeat initialization changed saved files'

for item in database-url initial-admin-password source-secret-keyset.json tls.key .initialized; do
	[ "$(stat -c '%u:%a' "$app/$item")" = '10001:600' ] || fail "$item owner or mode is unsafe"
done
[ "$(cat "$app/.initialized")" = 'almanac-local-v2' ] || fail 'completion marker version is incorrect'
[ "$(stat -c '%u:%a' "$app/tls.crt")" = '10001:644' ] || fail 'certificate owner or mode is unsafe'
[ "$(stat -c '%u:%a' "$pg/password")" = '10001:444' ] || fail 'database-only password mode is unsafe'
openssl x509 -in "$app/tls.crt" -checkhost localhost -noout >/dev/null 2>&1 || fail 'localhost SAN is missing'
openssl x509 -in "$app/tls.crt" -checkip 127.0.0.1 -noout >/dev/null 2>&1 || fail 'loopback IP SAN is missing'

if sh "$script" "$app" "$pg" >"$root/stdout" 2>"$root/stderr"; then
	:
else
	fail 'valid repeat initialization failed'
fi
if grep -F -f "$app/initial-admin-password" "$root/stdout" "$root/stderr"; then fail 'initializer leaked the admin password'; fi
if grep -F -f "$pg/password" "$root/stdout" "$root/stderr"; then fail 'initializer leaked the database password'; fi

# A valid certificate nearing expiry is renewed with the same key only.
stable_before=$(stable_secret_state)
openssl req -new -x509 -key "$app/tls.key" -sha256 -days 1 \
	-subj '/CN=localhost' \
	-addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
	-addext 'basicConstraints=critical,CA:TRUE' \
	-out "$root/near-expiry.crt" >/dev/null 2>&1
cp "$root/near-expiry.crt" "$app/tls.crt"
chown 10001:10001 "$app/tls.crt"
chmod 0644 "$app/tls.crt"
near_expiry_hash=$(sha256sum "$app/tls.crt")
sh "$script" "$app" "$pg"
[ "$(stable_secret_state)" = "$stable_before" ] || fail 'certificate renewal changed other saved credentials'
openssl x509 -in "$app/tls.crt" -checkend 604800 -noout >/dev/null 2>&1 || fail 'renewed certificate is not valid beyond the renewal threshold'
openssl x509 -in "$app/tls.crt" -checkhost localhost -noout >/dev/null 2>&1 || fail 'renewed certificate lost localhost DNS SAN'
openssl x509 -in "$app/tls.crt" -checkip 127.0.0.1 -noout >/dev/null 2>&1 || fail 'renewed certificate lost loopback IP SAN'
[ "$(stat -c '%u:%a' "$app/tls.crt")" = '10001:644' ] || fail 'renewed certificate owner or mode is unsafe'
certificate_matches_key || fail 'renewed certificate does not use the saved private key'
[ "$(sha256sum "$app/tls.crt")" != "$near_expiry_hash" ] || fail 'near-expiry certificate was not renewed'
renewed_hash=$(sha256sum "$app/tls.crt")
sh "$script" "$app" "$pg"
[ "$(sha256sum "$app/tls.crt")" = "$renewed_hash" ] || fail 'repeat initialization changed the renewed certificate'
[ "$(stable_secret_state)" = "$stable_before" ] || fail 'repeat initialization changed saved credentials after renewal'

# An already-expired, otherwise valid certificate is also renewed in place.
stable_before=$(stable_secret_state)
openssl x509 -in "$app/tls.crt" -signkey "$app/tls.key" -days -1 -out "$root/expired.crt" >/dev/null 2>&1 || fail 'could not create expired certificate fixture'
openssl x509 -in "$root/expired.crt" -checkend 0 -noout >/dev/null 2>&1 && fail 'expired certificate fixture is not expired'
cp "$root/expired.crt" "$app/tls.crt"
chown 10001:10001 "$app/tls.crt"
chmod 0644 "$app/tls.crt"
expired_hash=$(sha256sum "$app/tls.crt")
sh "$script" "$app" "$pg"
[ "$(stable_secret_state)" = "$stable_before" ] || fail 'expired certificate renewal changed other saved credentials'
openssl x509 -in "$app/tls.crt" -checkend 604800 -noout >/dev/null 2>&1 || fail 'expired certificate was not renewed beyond the threshold'
openssl x509 -in "$app/tls.crt" -checkhost localhost -noout >/dev/null 2>&1 || fail 'expired certificate renewal lost localhost DNS SAN'
openssl x509 -in "$app/tls.crt" -checkip 127.0.0.1 -noout >/dev/null 2>&1 || fail 'expired certificate renewal lost loopback IP SAN'
[ "$(stat -c '%u:%a' "$app/tls.crt")" = '10001:644' ] || fail 'expired certificate renewal owner or mode is unsafe'
certificate_matches_key || fail 'expired certificate renewal does not use the saved private key'
[ "$(sha256sum "$app/tls.crt")" != "$expired_hash" ] || fail 'expired certificate was not replaced'
renewed_hash=$(sha256sum "$app/tls.crt")
sh "$script" "$app" "$pg"
[ "$(sha256sum "$app/tls.crt")" = "$renewed_hash" ] || fail 'repeat initialization changed the expired-certificate renewal'
[ "$(stable_secret_state)" = "$stable_before" ] || fail 'repeat initialization changed saved credentials after expired-certificate renewal'

saved=$app/initial-admin-password
mv "$saved" "$app/password.save"
refuse_without_mutation 'missing completed-state secret'
mv "$app/password.save" "$saved"
chmod 0644 "$saved"
refuse_without_mutation 'unsafe completed-state mode'
chmod 0600 "$saved"

printf '\n' >> "$saved"
refuse_without_mutation 'newline in saved admin password'
truncate -s 64 "$saved"

pg_saved=$root/postgres-password.save
cp "$pg/password" "$pg_saved"
printf '%064d' 1 > "$pg/password"
refuse_without_mutation 'database password mismatch'
cp "$pg_saved" "$pg/password"
chown 10001:10001 "$pg/password"
chmod 0444 "$pg/password"

key_saved=$root/tls-key.save
cp "$app/tls.key" "$key_saved"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$root/alternate.key" >/dev/null 2>&1
cp "$root/alternate.key" "$app/tls.key"
chown 10001:10001 "$app/tls.key"
chmod 0600 "$app/tls.key"
refuse_without_mutation 'certificate and key mismatch'
cp "$key_saved" "$app/tls.key"
chown 10001:10001 "$app/tls.key"
chmod 0600 "$app/tls.key"

mv "$saved" "$root/admin-password.save"
ln -s "$root/admin-password.save" "$saved"
if sh "$script" "$app" "$pg" >/dev/null 2>&1; then fail 'accepted a symlinked saved secret'; fi
[ -L "$saved" ] && [ "$(readlink "$saved")" = "$root/admin-password.save" ] || fail 'changed symlinked saved state while refusing it'
rm "$saved"
mv "$root/admin-password.save" "$saved"

partial=$root/partial-app
partial_pg=$root/partial-pg
mkdir -p "$partial" "$partial_pg"
printf x > "$partial/database-url"
if sh "$script" "$partial" "$partial_pg" >/dev/null 2>&1; then fail 'accepted partial initialization state'; fi

# Upgrade a valid v1 local installation exactly once for datasource key support.
legacy_app=$root/legacy-app
legacy_pg=$root/legacy-pg
cp -a "$app" "$legacy_app"
cp -a "$pg" "$legacy_pg"
legacy_credentials_before=$(for file in database-url initial-admin-password tls.crt tls.key; do sha256sum "$legacy_app/$file"; done; sha256sum "$legacy_pg/password")
rm "$legacy_app/source-secret-keyset.json"
printf '%s\n' 'almanac-local-v1' > "$legacy_app/.initialized"
chown 10001:10001 "$legacy_app/.initialized"
chmod 0600 "$legacy_app/.initialized"
sh "$script" "$legacy_app" "$legacy_pg"
[ "$(cat "$legacy_app/.initialized")" = 'almanac-local-v2' ] || fail 'legacy marker was not upgraded'
[ "$(stat -c '%u:%a' "$legacy_app/source-secret-keyset.json")" = '10001:600' ] || fail 'legacy keyset owner or mode is unsafe'
[ "$(for file in database-url initial-admin-password tls.crt tls.key; do sha256sum "$legacy_app/$file"; done; sha256sum "$legacy_pg/password")" = "$legacy_credentials_before" ] || fail 'legacy upgrade changed an existing credential'
legacy_state=$(find "$legacy_app" "$legacy_pg" -type f -exec sh -c 'for file do sha256sum "$file"; done' sh {} + | sort)
sh "$script" "$legacy_app" "$legacy_pg"
[ "$(find "$legacy_app" "$legacy_pg" -type f -exec sh -c 'for file do sha256sum "$file"; done' sh {} + | sort)" = "$legacy_state" ] || fail 'repeat legacy upgrade changed saved key material'

echo 'PASS initializer checks'
