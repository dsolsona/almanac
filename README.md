# Almanac

SLO monitoring and reliability management platform. Track SLIs, error budgets,
burn rates and alerts across services.

## Runtime foundation

`make setup` installs the pinned Go and web toolchains. `make build` builds the
production web assets and embeds them in `bin/almanac`.

The implemented runtime mode is UI-only:

```sh
bin/almanac serve --components=ui --backend-url=http://127.0.0.1:8081
```

It listens on `127.0.0.1:8080`, serves the embedded UI, and proxies `/api/v1/`
requests to the configured backend origin. Backend, alerts, and combined modes
return an explicit unsupported-mode error before binding a listener. The default
component selection remains `backend,ui,alerts` and therefore requires an explicit
`--components=ui` until those components are implemented.

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
