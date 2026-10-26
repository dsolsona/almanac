#!/bin/sh
set -eu

script=/usr/local/lib/almanac/init.sh
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT HUP INT TERM
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

sh "$script" "$app" "$pg"
first=$(hashes)
sh "$script" "$app" "$pg"
second=$(hashes)
[ "$first" = "$second" ] || fail 'repeat initialization changed saved files'

for item in database-url initial-admin-password tls.key .initialized; do
	[ "$(stat -c '%u:%a' "$app/$item")" = '10001:600' ] || fail "$item owner or mode is unsafe"
done
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

echo 'PASS initializer checks'
