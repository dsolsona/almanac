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

Day to day:

```sh
make run       # Start dependencies and recreate the app; make start is an alias
make stop      # Stop containers, preserving all data and credentials
make logs      # View application logs
```

Run `make bootstrap` again after source changes to rebuild. Repeating it preserves
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
setup; secure UI proxy integration, general CLI profiles, management and metric
ingestion follow separately.

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
