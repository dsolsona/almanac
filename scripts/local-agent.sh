#!/bin/sh
set -eu
umask 077

fail() { printf 'Local agent: %s\n' "$1" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || fail 'Docker is required'
uid=$(id -u)
[ "$uid" -ne 0 ] || fail 'run the local agent as a non-root user'
app=$(docker compose -f compose.yaml ps --status running -q app) || fail 'cannot inspect the selected Compose application'
[ -n "$app" ] || fail 'the selected Compose app is not running; start it with make run'
health=$(docker inspect --format '{{.State.Health.Status}}' "$app") || fail 'cannot inspect the selected Compose app'
[ "$health" = healthy ] || fail 'the selected Compose app is not healthy; check make logs'
image=$(docker inspect --format '{{.Image}}' "$app") || fail 'cannot inspect the selected Compose image'
case "$image" in sha256:*) ;; *) fail 'the selected Compose image is unavailable' ;; esac

mkdir -p .local || fail 'cannot create local temporary directory'
directory=$(mktemp -d "$(pwd)/.local/almanac-agent.XXXXXXXX") || fail 'cannot create private temporary directory'
container=''
attach=''
tty_state=''
cleanup() {
  status=$?
  trap - EXIT
  trap '' INT TERM
  [ -z "$tty_state" ] || stty "$tty_state" </dev/tty 2>/dev/null || :
  if [ -n "$container" ] && ! docker rm -f "$container" >/dev/null 2>&1; then
    printf 'Local agent: could not remove the owned container %s; remove it with Docker.\n' "$container" >&2
    status=1
  fi
  if [ -n "$attach" ]; then
    kill "$attach" 2>/dev/null || :
    wait "$attach" 2>/dev/null || :
  fi
  if ! rm -rf "$directory"; then
    printf 'Local agent: could not remove temporary inputs in %s.\n' "$directory" >&2
    status=1
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
docker compose -f compose.yaml exec -T app cat /run/almanac-secrets/tls.crt > "$directory/ca.pem" || fail 'cannot read the application public certificate'
[ "$(wc -c < "$directory/ca.pem")" -le 65536 ] || fail 'the application public certificate is too large'

if [ -n "${AGENT_TOKEN_FILE:-}" ]; then
  token=$AGENT_TOKEN_FILE
  [ -f "$token" ] && [ ! -L "$token" ] || fail 'AGENT_TOKEN_FILE must be a regular file, not a symlink'
  exec 3< "$token" || fail 'AGENT_TOKEN_FILE is unavailable'
  [ -f /dev/fd/3 ] || fail 'AGENT_TOKEN_FILE must be a regular file'
  case "$(uname -s)" in
    Darwin) details=$(stat -Lf '%u %Lp' /dev/fd/3) ;;
    Linux) details=$(stat -Lc '%u %a' /dev/fd/3) ;;
    *) fail 'protected file validation requires macOS or Linux' ;;
  esac
  set -- $details
  [ "$#" -eq 2 ] && [ "$1" = "$uid" ] || fail 'AGENT_TOKEN_FILE must be owned by you'
  case "$2" in *[!0-7]*|'') fail 'cannot validate AGENT_TOKEN_FILE permissions' ;; esac
  [ $((0$2 & 077)) -eq 0 ] || fail 'AGENT_TOKEN_FILE must have owner-only permissions'
  dd bs=2049 count=1 <&3 > "$directory/token" 2>/dev/null || fail 'cannot read AGENT_TOKEN_FILE'
  exec 3<&-
  [ "$(wc -c < "$directory/token")" -le 2048 ] || fail 'AGENT_TOKEN_FILE is too large'
else
  [ -t 0 ] || fail 'a terminal is required to paste a credential; use AGENT_TOKEN_FILE otherwise'
  tty_state=$(stty -g </dev/tty) || fail 'cannot read terminal settings'
  stty -echo </dev/tty || fail 'cannot hide terminal input'
  printf 'Paste agent credential: ' >&2
  IFS= read -r value </dev/tty || fail 'could not read the credential'
  stty "$tty_state" </dev/tty
  tty_state=''
  printf '\n' >&2
  [ -n "$value" ] || fail 'the supplied credential is empty'
  token="$directory/token"
  printf '%s' "$value" > "$token"
  unset value
fi
[ "$(wc -c < "$directory/token")" -le 2048 ] || fail 'the supplied credential is too large'
{ base64 < "$directory/token" | tr -d '\n'; printf '\n'; base64 < "$directory/ca.pem" | tr -d '\n'; printf '\n'; } > "$directory/input"
name="almanac-agent-$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
container=$(docker create -i --name "$name" --label almanac.local-agent=owned \
  --network "container:$app" --user 10001:10001 --read-only --cap-drop ALL \
  --security-opt no-new-privileges --tmpfs /tmp:rw,noexec,nosuid,size=16m,mode=1777 \
  --entrypoint /bin/sh "$image" -c '
    umask 077
    IFS= read -r token && IFS= read -r ca || { echo "agent input unavailable" >&2; exit 1; }
    printf %s "$token" | base64 -d > /tmp/agent-token || exit 1
    unset token
    printf %s "$ca" | base64 -d > /tmp/agent-ca.pem || exit 1
    unset ca
    exec /usr/local/bin/almanac-agent run --backend-url=https://localhost:8080 \
      --token-file=/tmp/agent-token --ca-file=/tmp/agent-ca.pem
  ') || fail 'could not create the agent container'
printf '%s\n' 'Agent starting. Confirm connection in the Agents page; Ctrl-C stops this agent.'
printf '%s\n' 'A prior owner lease may delay restart; wait for it to expire if ownership is rejected.'
docker start --attach --interactive "$container" < "$directory/input" &
attach=$!
wait "$attach" || fail 'agent stopped; check its credential and any ownership conflict above'
attach=''
