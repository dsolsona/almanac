# Almanac

SLO monitoring and reliability management platform. Track SLIs, error budgets,
burn rates and alerts across services.

## Run locally

Install Docker with its Compose plugin and Make, then run:

```sh
make bootstrap
```

This builds the app inside containers, prepares a separate PostgreSQL 18 database,
generates local credentials and a localhost HTTPS certificate, upgrades the schema,
and starts the app plus local VictoriaMetrics and VictoriaTraces test sources.
No host Go, Node, or OpenSSL installation is needed.
Open **https://localhost:18088** and accept the generated local certificate in your
browser. No host trust settings are changed. Sign in as **admin**; retrieve the
initial password explicitly with `make credentials`, then choose your permanent
password in the onboarding page. The initial password is never printed in startup
or container logs and stops working after rotation.

Startup automatically renews the localhost certificate when it expires within seven
days, retaining its private key and all database/admin credentials. Your browser may
ask you to accept the renewed certificate. Damaged credential state still fails
without resetting it.

The first Compose initialization also provisions the protected source-secret keyset
used for encrypted datasource credentials. It is stored once in the persistent local
secret volume and reused on later starts; Compose does not replace it during normal
startup. Existing v1 local credential volumes are upgraded once without changing
their saved credentials.

Day to day:

```sh
make run       # Start dependencies and recreate the app; make start is an alias
make stop      # Stop containers, preserving all data and credentials
make logs      # View application logs
```

`make run` and its `make start` alias build the current checkout with Docker's cache
before starting the app, including on a fresh checkout. Repeating startup preserves
your users, passwords and data. The app runs in the background; readiness failures
make startup fail visibly. Recreating only the app gives it a fresh network connection,
including after a failed port bind; the database container and volumes are reused.
`ALMANAC_PORT=8082 make bootstrap` selects another local
HTTPS port; use the same setting for later commands. PostgreSQL has no host port.
Named volumes belong to the `almanac-local` Compose project; the test database and
design catalogue are separate. This is a local development stack, not production
deployment or restore qualification.

The current app supports initial administrator onboarding, sign-in and sign-out.
SLI collection, SLO dashboards and alerting are still under development.

### Local data sources

| Source | Host address | Address for an agent on the Compose network |
| --- | --- | --- |
| VictoriaMetrics | `http://localhost:18428` | `http://victoriametrics:8428` |
| VictoriaTraces Tempo API | `http://localhost:19428/select/tempo` | `http://victoriatraces:10428/select/tempo` |

Both services bind host ports to loopback and retain local data in named volumes
across `make stop`/`make run`. Override `ALMANAC_METRICS_PORT` or
`ALMANAC_TRACES_PORT` if those ports are occupied. Both use seven-day retention and
256 MiB cache budgets; these are not total process-memory limits. VictoriaTraces
also has a 2 GiB disk-retention target. These unauthenticated local test sources
are not production deployment examples.

VictoriaMetrics exposes the Prometheus-compatible query API. VictoriaTraces exposes
an experimental Tempo-compatible API using **TraceQL**, including metrics queries;
its trace explorer is at `http://localhost:19428/select/vmui`. Availability of these
containers does not yet provide an Almanac agent or a native trace provider.

After developer tooling setup, `make verify VERIFY_MODE=providers` starts an isolated
provider project, inserts synthetic metrics and spans, checks exact query results
before and after restart, and removes only its test-owned containers and volumes.
It does not populate or reset your normal local sources.

## Runtime foundation

`make setup` installs the pinned Go and web toolchains. `make build` builds the
production web assets and embeds them in `bin/almanac`.

UI-only mode serves the embedded frontend:

```sh
bin/almanac serve --components=ui --backend-url=http://127.0.0.1:8081
```

It listens on `127.0.0.1:8080`, serves the embedded UI, and proxies `/api/v1/`
requests to the configured backend origin. Modes containing alerts
return an explicit unsupported-mode error before binding a listener. The default
component selection remains `backend,ui,alerts` and therefore requires an explicit
`--components=ui`, `--components=backend`, or `--components=backend,ui`.

`almanac version --output=json` emits one versioned JSON document. `almanac help`
lists the commands and component limitations. UI mode accepts a strict JSON config
file with `listen`, `components`, `backend_url`, and `shutdown_timeout` fields;
environment and explicit flags override file values.

Upgrade PostgreSQL explicitly before starting a compatible backend build:

```sh
bin/almanac db upgrade --database-url-file=/run/secrets/almanac-database-url
```

The file contains one PostgreSQL connection URL, is limited to 16 KiB, and must
not grant group or other permissions. Local automation can use
`ALMANAC_DATABASE_URL`; `--database-url-file` takes precedence over
`ALMANAC_DATABASE_URL_FILE`, which takes precedence over that environment value.
The command uses embedded ordered migrations and does not create users or
application records. `--output=json` emits a single versioned result document.

Backend-only mode exposes local authentication and operational health endpoints:

```sh
bin/almanac serve --components=backend --listen=127.0.0.1:8081 \
  --database-url-file=/run/secrets/almanac-database-url \
  --initial-admin-login=admin \
  --initial-admin-password-file=/run/secrets/almanac-initial-password \
  --public-origin=https://almanac.example.com \
  --tls-cert-file=/run/certs/almanac.pem \
  --tls-key-file=/run/secrets/almanac-key.pem
```

Use `--components=backend,ui` (or `ui,backend`) with the same settings to serve
the embedded frontend and authentication API on one HTTPS listener. This mode
reuses the backend's database ownership and shutdown lifecycle; it does not proxy
API requests to another server. Health responses identify the owning backend.
Backend-only mode does not serve the frontend. Open the configured HTTPS public
origin in combined mode to sign in, complete initial administrator password setup,
inspect your signed-in identity and sign out.

Backend serving requires a protected database URL file. The initial password and
TLS private key files must also deny group and other permissions; the public
certificate file may be readable. Supply a certificate valid for the public origin.
For TLS termination at a proxy, omit both TLS files and explicitly configure
`--trusted-proxy-cidrs`; no proxy is trusted by default. The proxy must supply the
verified client address and HTTPS protocol metadata described in the
[authentication contract](api/openapi.yaml).

For non-Compose deployments, provision a source keyset explicitly and mount the
resulting owner-only file into the backend:

```sh
bin/almanac secrets provision-source-keyset \
  --file=/run/secrets/almanac-source-keyset.json \
  --keyset-id=00000000-0000-4000-8000-000000000001 --output=json
```

Configure both `--source-secret-keyset-file` and `--source-secret-keyset-id` on
`serve`, or set the matching `source_secret_keyset_file` and
`source_secret_keyset_id` JSON properties. The environment equivalents are
`ALMANAC_SOURCE_SECRET_KEYSET_FILE` and `ALMANAC_SOURCE_SECRET_KEYSET_ID`.
The cleartext Tink keyset is deployment key material: protect the file and its
mount. Datasource credential writes remain unavailable when the keyset is not
configured or cannot be loaded.

The first startup provisions the initial administrator, who must change the
password before ordinary access. Later starts ignore the initial credentials once
ownership is persisted. Missing, unsafe or weak initial credentials leave `/livez`
available but `/readyz` and authentication unavailable; correct the configuration
and restart. Schema or database startup failures stop the process. Only one backend
may own a database: duplicate startup fails, SIGTERM drains requests before releasing
ownership, and loss of the ownership connection stops serving with an error exit.

Backend settings have matching snake_case JSON fields, `ALMANAC_` environment
variables and flags; precedence is flags, environment, then file. Proxy CIDRs are a
JSON array or a comma-separated environment/flag value. See the
[runtime task](docs/tasks/F1-T14.md) for the configuration mapping and current bounds.
This increment provides the authentication API, browser onboarding and noninteractive
setup. Agent registration and credential administration are available in the
browser. A separate `almanac-agent` executable connects and applies the current
empty configuration; datasource queries and metric ingestion follow separately.

The first browser sign-in uses the deployment-provided initial password and opens
restricted setup. Enter that current password and a different permanent password;
successful setup opens the signed-in identity screen. Reloading during setup asks
for the current password again. If a login or password-change response is lost,
the page checks session state and keeps the unknown outcome visible. It never
automatically resubmits the action. The new password may already be active, so use
a deliberate sign-in when prompted.

Complete initial administrator setup through HTTPS:

```sh
bin/almanac setup --backend-url=https://almanac.example.com \
  --initial-admin-login=admin \
  --initial-admin-password-file=/run/secrets/almanac-initial-password \
  --new-password-file=/run/secrets/almanac-permanent-password \
  --output=json
```

For a private certificate authority, add `--ca-file=/run/certs/issuer-ca.pem`.
Certificate verification is always enabled. Password files contain the exact UTF-8
password text: a trailing newline is part of the password. The command validates
local inputs before network access, never prompts, and does not save or print session
secrets. `--timeout` bounds the whole operation (default30s, maximum2m).

Successful JSON output reports `completed:true` and `session_cleanup:"complete"`.
If rotation succeeds but logout cannot be confirmed, completion remains true and
cleanup is `"unknown"`, with a diagnostic on stderr. Running setup with an ordinary
administrator session refuses another password rotation.

A lost or ambiguous login/password response reports `outcome_unknown:true`; setup
checks session context once when its deadline permits and never repeats the mutation.
An authenticated context alone does not establish that rotation succeeded. Recover
through a deliberate sign-in using the browser or authentication API; the new password may
already be active. Do not automatically retry setup after an uncertain outcome.

For reusable CLI authentication on Linux, choose a profile explicitly:

```sh
bin/almanac auth login --profile-file=/home/operator/.almanac/profile.json \
  --backend-url=https://almanac.example.com --login=admin \
  --password-file=/run/secrets/almanac-permanent-password --output=json
bin/almanac auth context --profile-file=/home/operator/.almanac/profile.json --output=json
bin/almanac auth logout --profile-file=/home/operator/.almanac/profile.json --output=json
```

Create the parent directory beforehand, owned by the current user and without
group/other write permission. Profile and companion lock files must be regular0600
files; profile names ending in `.lock` or starting with `.almanac-profile-tmp-`
are reserved. The profile stores a session credential, exact backend origin and
optional CA bundle. Protect it as a secret. It stores no password, cached role or
CSRF token. Other platforms currently fail explicitly rather than weaken protection.

Login accepts the same optional `--ca-file` and whole-operation `--timeout` bounds
as setup. Existing profiles retain their origin and CA binding; select a different
profile to change either. An active session requires explicit logout before another
login. Context fetches the current identity, including restricted status; it does
not grant management access to an initial account. Use `setup` for initial rotation.

Each command returns one versioned JSON document with safe identity/session state
or a stable error. An uncertain login is reconciled once and never retried; any
recovered credential is saved while the outcome remains unknown. Uncertain logout
retains the original credential for a later explicit context check. A failed save
or output write returns a nonzero exit even when the server accepted the action.
Agent management CLI operations follow this shared profile boundary.

Agent administration uses the same HTTPS management API and requires an
unrestricted administrator session in the selected profile. Reads request one
bounded page; provide the returned cursor explicitly to continue. Registration
uses that current session without asking for the password again. Credential
rotation, revocation and disable require the administrator's current password
in a protected file. Mutations use an explicit idempotency key; versioned
operations also require the exact current ETag:

```sh
bin/almanac agents list --profile-file=/home/operator/.almanac/profile.json --output=json
bin/almanac agents get --profile-file=/home/operator/.almanac/profile.json --agent-id=AGENT_UUID --output=json
bin/almanac agents credentials list --profile-file=/home/operator/.almanac/profile.json --agent-id=AGENT_UUID --output=json
bin/almanac agents create --profile-file=/home/operator/.almanac/profile.json --name=collector-east --display-name='East collector' \
  --idempotency-key=registration-2026-10-27 \
  --token-file=/home/operator/.almanac/collector-east.token --output=json
```

Credential rotation, revocation and disable also require `--confirm` and an
exact `--if-match` value returned by `agents get`. Creation and rotation require
exactly one explicit token destination: a new protected `--token-file` or the
deliberate `--show-token` option. A fresh token is emitted once; replay returns
metadata only. Commands never retry a mutation after an uncertain response. A
lost or rejected token-file write reports an unknown outcome because the issued
secret cannot be recovered; inspect the agent and deliberately rotate if needed.
Text mode uses JSON escaping for remote names so control characters do not alter
terminal output. `--timeout` bounds profile-lock wait and the full API operation.

Build both executables, then run the separate outbound-only agent process on the
host that should own the registered agent:

```sh
make build
bin/almanac-agent run --backend-url=https://almanac.example.com \
  --token-file=/run/secrets/almanac-agent-token
```

Create the credential with `agents create` and its `--token-file` option as shown
above, then deliver that protected file to the agent host through your deployment
secret mechanism. The file must be a regular file with no group or other
permissions; the agent rejects broader permissions and does not print the token.
Keep one process running for this credential. A second process using a copied token
cannot take over the active owner. Stop the process with SIGINT or SIGTERM; it
reconnects after temporary backend loss and exits with a distinct safe message for
credential rejection, revocation, active-owner conflict or protocol incompatibility.
The resume proof exists only in process memory. After a process restart, a new
process cannot resume the prior owner; wait for its ownership lease to expire
(up to one hour, or sooner if the credential expires) before starting the
replacement process. There is no force-takeover command in this increment.

The backend URL must be an HTTPS origin without a path, query or fragment. System
certificate roots are used by default. To add a private CA while retaining normal
certificate verification, pass its PEM certificate bundle with `--ca-file`:

```sh
bin/almanac-agent run --backend-url=https://almanac.example.com \
  --token-file=/run/secrets/almanac-agent-token \
  --ca-file=/run/certs/almanac-private-ca.pem
```

This increment applies versioned configuration and reports connection and heartbeat
state through `agents get`. Its configuration has no datasource assignments, so it
does not execute queries or report collection success. Empty configuration grants
no query authority. A rejected configuration reports its bounded failure code and
revision in agent status while leaving the last successfully applied revision intact.

An authenticated agent that offers only unsupported protocol versions is reported
with reachability `incompatible`. If no compatible runtime has been observed, its
protocol is shown as `incompatible` and software version as `unknown`; these are
safe status markers, not peer-supplied version values. If a compatible runtime
already exists, its observed fields remain intact while the newer incompatible
attempt changes reachability. A successful compatible connection clears that state.

For isolated local storage checks, `make setup SETUP_MODE=database` provisions the
pinned PostgreSQL 18.6 fixture on loopback port 15487. Run:

```sh
make verify VERIFY_MODE=integration GO_PACKAGE=./internal/storage ALMANAC_TEST_DATABASE_URL='postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable'
```

Run `make verify VERIFY_MODE=go` for focused Go checks, `make verify VERIFY_MODE=go-race`
for race checks, or `make verify` for web and Go verification. Catalogue browser
checks use `make verify VERIFY_MODE=browser`. The full PostgreSQL integration lane
also exercises browser onboarding against the built combined HTTPS executable;
it requires the Chromium installation from `make setup`. Ordinary unit checks do
not start PostgreSQL or run that browser journey.

See [the web design system guide](web/DESIGN_SYSTEM.md) for the local UI setup,
catalogue and verification commands.
