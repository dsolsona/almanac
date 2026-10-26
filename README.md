# Almanac

SLO monitoring and reliability management platform. Track SLIs, error budgets,
burn rates and alerts across services.

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
Backend-only mode does not serve the frontend. Browser onboarding screens remain
separate work; the current frontend content is unchanged.

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
This increment provides the authentication API and noninteractive setup; secure UI
proxy integration, onboarding screens, general CLI profiles, management and metric
ingestion follow separately.

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
through a deliberate sign-in using the authentication API; the new password may
already be active. Do not automatically retry setup after an uncertain outcome.

For isolated local storage checks, `make setup SETUP_MODE=database` provisions the
pinned PostgreSQL 18.6 fixture on loopback port 15487. Run:

```sh
make verify VERIFY_MODE=integration GO_PACKAGE=./internal/storage ALMANAC_TEST_DATABASE_URL='postgres://almanac:almanac_f1_t05_local_only@127.0.0.1:15487/almanac_f1_storage?sslmode=disable'
```

Run `make verify VERIFY_MODE=go` for focused Go checks, `make verify VERIFY_MODE=go-race`
for race checks, or `make verify` for web and Go verification. Browser checks remain
separate with `make verify VERIFY_MODE=browser`.

See [the web design system guide](web/DESIGN_SYSTEM.md) for the local UI setup,
catalogue and verification commands.
