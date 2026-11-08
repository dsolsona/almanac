#!/bin/sh
# Dev-only: run as root inside the selected local PostgreSQL container, never in production.
set -eu
umask 077
directory=/run/almanac-agent
fail() { echo "Agent seed: $1" >&2; exit 1; }
[ ! -L "$directory" ] || fail 'unsafe agent directory'
mkdir -p "$directory"
[ ! -L "$directory/.seed.lock" ] || fail 'unsafe seed lock'
exec 9>"$directory/.seed.lock"
flock -x 9
chown 10001:10001 "$directory"
chmod 0700 "$directory"

file=$directory/token
if [ ! -e "$file" ] && [ ! -L "$file" ]; then
  file=$directory/token.pending
  if [ ! -e "$file" ] && [ ! -L "$file" ]; then
    id=$(cat /proc/sys/kernel/random/uuid)
    secret=$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')
    printf 'almanac_agent_v1_%s.%s' "$id" "$secret" > "$file"
    chown 10001:10001 "$file"
  fi
fi
[ -f "$file" ] && [ ! -L "$file" ] || fail 'saved token must be a regular file'
[ "$(stat -c '%u:%a' "$file")" = '10001:600' ] || fail 'saved token must belong to UID 10001 with mode 600'
[ "$(wc -c < "$file")" -le 2048 ] || fail 'saved token is too large'
token=$(cat "$file")
[ "$(wc -c < "$file")" -eq "${#token}" ] || fail 'invalid saved token bytes'
rest=${token#almanac_agent_v1_}
[ "$rest" != "$token" ] || fail 'invalid saved token'
id=${rest%%.*}
secret=${rest#*.}
case "$id" in ????????-????-????-????-????????????) ;; *) fail 'invalid saved token ID' ;; esac
case "$id" in *[!0-9a-f-]*) fail 'invalid saved token ID' ;; esac
case "$secret" in *[!A-Za-z0-9_-]*) fail 'invalid saved token secret' ;; esac
[ "${#secret}" -eq 43 ] || fail 'invalid saved token secret'
canonical=$(printf '%s=' "$secret" | tr '_-' '/+' | openssl base64 -d -A | openssl base64 -A | tr '+/' '-_' | tr -d '=')
[ "$canonical" = "$secret" ] || fail 'saved token secret must use canonical Base64'
verifier=$(printf '%s=' "$secret" | tr '_-' '/+' | openssl base64 -d -A | sha256sum)
verifier=${verifier%% *}
unset token rest secret

psql --no-psqlrc --quiet -v ON_ERROR_STOP=1 -U almanac -d almanac -v id="$id" -v verifier="$verifier" <<'SQL'
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10s';
SELECT credential_epoch AS epoch FROM almanac_installation WHERE singleton FOR UPDATE \gset
SELECT EXISTS(SELECT 1 FROM almanac_agents WHERE name='local-dev') AS existing \gset
\if :existing
  SELECT EXISTS(
    SELECT 1 FROM almanac_agents a JOIN almanac_agent_credentials c ON c.agent_id=a.id
    WHERE a.name='local-dev' AND a.enabled AND a.deleted_at IS NULL
      AND c.id=:'id' AND c.verifier=decode(:'verifier', 'hex') AND c.credential_epoch=:'epoch'
      AND c.issued_at<=now() AND c.expires_at>now() AND c.revoked_at IS NULL
      AND (c.retire_at IS NULL OR c.retire_at>now())
  ) AS valid \gset
  \if :valid
  \else
    DO $$ BEGIN
      RAISE EXCEPTION 'Agent seed: local-dev already exists but its saved credential is invalid; no records were changed.';
    END $$;
  \endif
\else
  INSERT INTO almanac_agents(name, display_name, created_at, updated_at)
    VALUES ('local-dev', 'local-dev', now(), now()) RETURNING id AS agent_id \gset
  INSERT INTO almanac_agent_credentials(id, agent_id, verifier, credential_epoch, issued_at, expires_at)
    VALUES (:'id', :'agent_id', decode(:'verifier', 'hex'), :'epoch', now(), now()+interval '365 days');
  INSERT INTO almanac_audit_events(actor, subject_id, action, outcome, occurred_at, request_id) VALUES
    ('local-development-seed', :'agent_id', 'agent.created', 'success', now(), 'local-development-seed'),
    ('local-development-seed', :'id', 'agent.credential.issued', 'success', now(), 'local-development-seed');
\endif
COMMIT;
SQL

# Retain a pending token on failure so a committed seed can recover after interruption.
if [ "$file" != "$directory/token" ]; then mv "$file" "$directory/token"; fi
echo 'Agent local-dev is seeded; make run starts it automatically after onboarding.'
