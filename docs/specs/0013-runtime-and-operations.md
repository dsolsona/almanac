# SPEC-0013: Runtime and operations

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: in progress
- Related ADR: [ADR-0008](../adr/0008-runtime-components-and-packaging.md)

## Purpose and executable shape

Provide a convenient combined server and independently runnable components without
requiring distributed deployment for local use. UI-only serving and version/help
exist through [F1-T04](../tasks/F1-T04.md); explicit database upgrades exist through
[F1-T05](../tasks/F1-T05.md). [F1-T14](../tasks/F1-T14.md) adds backend-only local
authentication, protected provisioning, health and single-owner lifecycle.
[F1-T15](../tasks/F1-T15.md) adds noninteractive setup over the HTTPS API.
[F1-T16](../tasks/F1-T16.md) adds the explicit backend/UI pair on the same listener.
The
backend's management, ingestion and agent workloads in the table remain future
work, as do combined alerts and other commands. Unsupported server modes
fail before listening.

| Command | Starts | Credentials |
| --- | --- | --- |
| `almanac serve` | Backend, embedded UI, alert evaluator and notification dispatcher | Database and application secrets |
| `almanac serve --components=backend` | Management/ingestion API, agent connections, storage-owned maintenance/recompute/planning | Database and application secrets |
| `almanac serve --components=ui` | Embedded static UI and restricted same-origin API proxy | Backend origin; no DB credential |
| `almanac serve --components=alerts` | Alert evaluator and delivery worker through private backend API | Scoped worker token; no DB credential |
| `almanac <resource> <action>` | CLI management/API client, exits when operation finishes | User automation token/session profile |
| `almanac db upgrade` | Explicit ordered schema upgrade, then exits | Schema-owner DB credential |
| `almanac setup` | Complete one-time onboarding through API | Initial administrator password and new password via protected files |
| `almanac admin recover` | Offline audited local administrator recovery | Stopped backend, OS/DB administrative access and explicit confirmation |
| `almanac-agent run` | Agent connection and provider execution | Agent token and backend URL |

Component lists may combine `backend,ui,alerts`; reject unknown/duplicate names and
incompatible settings. `serve` never launches a collector. Agent ships as a separate
small executable/image from the same Go module, sharing protocol libraries only.

The UI is a React browser application using the backend HTTP API. “Embedded” means
its compiled HTML/JavaScript/CSS files are shipped inside the Go binary and served
as static files. It does not mean server-side React rendering or direct database
access from UI code. UI-only mode serves the same build with the configured API proxy.

## Internal architecture

`domain` and `slomath` hold deterministic types/functions. Application services
own use cases and consumer-sized interfaces. HTTP, PostgreSQL, secret storage and
provider schemas are adapters; provider execution exists only in the agent. `cmd`
wires dependencies and lifecycle. Package names are a proposed layout, not an
obligation to invent interfaces with no consumer.

Backend exclusively mutates persistent application state. The alert component
claims evaluation/delivery work through a private API and submits fenced results.
Embedded mode uses the same application operations through an in-process adapter;
remote mode uses HTTP. They must have contract parity. The UI proxy forwards only
the configured Almanac API routes to one fixed backend origin, never arbitrary URLs.

## Scale boundaries

Initial deployment supports one active backend instance. UI instances can be
replicated; alert workers use DB-backed claims through the backend and may run
concurrently. PostgreSQL protects duplicate claims with expiring leases/generations.
Agent counts and provider workloads scale by registering separate agent identities.

Multiple active backends require durable agent connection ownership and inter-instance
command routing. Keep a dispatch interface for that extension but do not advertise
backend horizontal scale until it has its own acceptance evidence. Replicating the
backend container blindly is unsupported and must fail duplicate active-owner checks.
Database and API boundaries allow later extraction of recomputation/planning workers.

## Startup, shutdown, and upgrades

Validate configuration before opening listeners. Config comes from a file plus
`ALMANAC_` / `ALMANAC_AGENT_` environment overrides; explicit CLI flags have highest
precedence. Reject unknown keys and redact effective-config output. Secrets support
mounted files so containers need not put them in process arguments.

Backend liveness says its process/event loop is alive; readiness requires compatible
schema and reachable database. Before first startup, provision the initial admin
login and a protected mounted password file; deployment tooling can generate a random
password or accept an operator-provided one. Missing required credentials fail setup
readiness with instructions. Atomically provision the account once, as SPEC-0004
defines. The installation then serves restricted login/onboarding until mandatory
password rotation completes; ordinary APIs and application workers remain unavailable.
Restarts never reset the password or repeat provisioning, even before first rotation. Offline recovery requires stopped server ownership
and the configured encryption key, invalidates affected sessions, personal/automation
tokens and pending authentication challenges, and records an audit.
UI readiness checks its backend route; alert readiness
checks authenticated backend access. Agent liveness is local; operational connection
and applied-config state are separate and do not force process restarts on outages.

Start workers only after readiness prerequisites. Component failure stops the combined
process with a nonzero exit code unless explicitly classified as a retryable remote
outage. On SIGTERM stop new claims/requests, drain bounded in-flight work, close agent
sessions, and join workers within 30 seconds by default. Persisted work can be reclaimed
after its lease; graceful shutdown must not mark unfinished work successful.

Upgrade schema separately before compatible new server binaries; pin image digests
for reproducibility. Document minimum/maximum schema versions per release. Back up
before irreversible schema changes; restore is the recovery procedure, not blind
downgrade. Agent rolling upgrades follow protocol-major compatibility checks.

Restore activation follows SPEC-0007: isolate restored state, rotate credential epoch,
recover administrator access offline, reissue machine credentials, fence work, and
ensure old execution authority has expired or explicitly accept forced query overlap.
Never reopen a database snapshot with its restored bearer tokens still valid.

## Delivery and observability

### Local Compose workflow

The maintainer requested a container-only local workflow on 2026-10-26.
`make bootstrap` builds and starts the application and PostgreSQL with Docker
Compose, automatically preparing protected local credentials, a localhost TLS
certificate and the schema. Only Docker with Compose and Make are host prerequisites.
`make run` and `make start` perform a cached container build of the current checkout
before starting the stack, including when the local image is absent. `make bootstrap`
uses the same path without a duplicate build. Invalid ports fail before build/start
side effects; build failure preserves a running app and never reports readiness.
These commands do not install host tools. `make stop`
stops it without deleting data. Repeating bootstrap preserves users, passwords and
database contents. This explicitly brings local Compose packaging forward from F5;
production images, Helm and restore qualification remain separate release work.

The maintainer additionally requested VictoriaMetrics and VictoriaTraces as local
test sources for F2 on 2026-10-26. Start both through the same Compose workflow,
with persistent data volumes and pinned images. Their host HTTP ports bind only
to loopback (18428 and 19428 by default); PostgreSQL stays on the Compose network.
Verify actual Prometheus-compatible and Tempo-compatible TraceQL metrics queries
against synthetic data. These test services do not imply that native trace collection
is delivered before its accepted F6 slice.

Use named volumes and a dedicated Compose project, separate from integration
fixtures and the design catalogue. Expose the app on loopback (HTTPS port 18088
by default). The application runs without
root and reads protected secret files. Initialization and migrations must complete
before serving. Readiness failures return an unsuccessful command result rather
than announcing a usable app. Startup prints its URL and how to obtain the initial
admin password; secret disclosure is an explicit terminal action, never container
logs. A generated local certificate may need a browser exception; bootstrap never
changes host trust or disables application authentication/TLS checks.
After validating saved credential state and certificate/key correspondence, startup
renews expired or soon-expiring local certificates using the existing private key.
Only the public certificate is atomically replaced; database/admin credentials and
data are preserved. Invalid saved credentials must not trigger regeneration.

Stop/restart and repeated bootstrap must preserve completed onboarding. Occupied
ports, missing prerequisites, invalid saved state, and schema/init failures must
fail visibly without deleting or adopting unrelated resources. No reset occurs as
part of bootstrap, run, or stop.

Provide Docker images for server and agent, a Compose example with PostgreSQL and
a demo metrics source, and Helm charts for server/agent. Keep demo credentials and
synthetic data explicitly local; production requires initial-password onboarding.
Non-root containers, read-only filesystems where feasible, and mounted secret files
are defaults. No Node runtime is required to serve the embedded production UI.

Expose structured logs and metrics for component health, ingest latency/rejections,
buffer eviction, live schedule lateness, historical queue age, repair exhaustion,
notification delivery, database pools and storage growth. Avoid unbounded label
cardinality and secrets in logs. Metrics/debug endpoints bind to an internal listener;
profiling is disabled by default. Correlate request/job IDs without logging query bodies.

Initial toolchain proposal: Go 1.27.3, verified in the official release history on
2026-10-24; use the Modern Go Guidelines CLI for the pinned target before Go edits.
[Go release history](https://go.dev/doc/devel/release)
Pin a supported PostgreSQL 18 patch and build-tool versions at scaffolding. Upgrade
patches through ordinary tested dependency updates; do not require a new ADR per patch.

## Acceptance criteria

- AC-01: Combined mode and separate backend/UI/alert modes produce equivalent behavior;
  UI/alert/agent containers have no database credentials.
- AC-02: No server mode initiates collection against external datasources.
- AC-03: Two alert workers cannot publish the same state transition or claim one
  delivery concurrently; expired claims recover with fencing.
- AC-04: Incompatible schema/configuration fails readiness explicitly; initialization
  remains reachable without exposing ordinary APIs before setup.
- AC-05: Shutdown/restart preserves durable work and bounded time; agents reconnect
  without losing identity or implicitly applying unknown configuration.
- AC-06: Docker and Helm instructions launch the documented artifacts; backups restore
  data and secrets into an isolated environment successfully.
- AC-07: Record executable size with and without UI assets, frontend build size,
  and compressed artifact/image size using the same target/build flags. Review the
  packaging cost before release; embedded assets are not assumed to be negligible.

## Review notes and evidence

- **REVIEW-R01:** Propose two artifacts (`almanac`, `almanac-agent`), with CLI in the
  server artifact. This keeps remote collectors small; a single universal binary
  remains an alternative if distribution simplicity outweighs that benefit.
- **REVIEW-R02:** Backend horizontal replication is deferred, not implied by component
  flags. Confirm one active backend is acceptable for the first release.
- UI-only embedded serving, proxy/outage handling and bounded shutdown have real
  HTTP/process checks. The combined backend/UI pair has real TLS, setup CLI,
  PostgreSQL and lifecycle coverage under F1-T16. Secure UI-only authentication,
  combined/separate alert parity, containers, restore and scalability remain
  unverified and unimplemented.
