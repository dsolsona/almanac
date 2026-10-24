# SPEC-0015: Datasource providers

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: not started
- Related ADR: [ADR-0013](../adr/0013-provider-capabilities-and-execution.md)

## Purpose and common contract

Datasources identify provider, host, authentication, frequency and assigned agent.
Backend validates schemas and permissions without contacting the source. Agent
executes connection tests, previews, live queries and historical queries. Providers
register explicit supported operations and configuration versions; adding backend
editor support does not imply every installed agent can execute it.

Descriptors include public schema, secret-field paths, supported measurement kinds,
preview limits, history support, query range/step bounds and required capabilities.
Agent registration intersects local implementation support with backend compatibility.
Runtime source limits may be stricter than the static descriptor and are reported.
Unknown provider imports remain inactive with diagnostics rather than discarded.

## First provider and second-source scope

Propose Prometheus-compatible HTTP APIs as the first active provider, then Tempo
TraceQL metrics as the second. Elasticsearch is a declared future provider, not an
implemented capability. Direct ingestion supports other systems immediately without
claiming a native adapter. Provider names in catalogs distinguish available, planned,
and unsupported-on-this-agent states.

Prometheus range queries use explicit start/end/step parameters; the API returns
evaluation points and can return warnings/errors. The adapter must translate these
to Almanac's half-open observation intervals, filtering boundary duplicates without
inventing missing points. [Prometheus HTTP API](https://prometheus.io/docs/prometheus/latest/querying/api/)

Tempo distinguishes trace retrieval/search from metrics queries. Use the metrics
query capability where it can support the measurement, not counts of a truncated
search result. Source retention and historical metrics availability must be checked;
do not promise that any trace query can replay arbitrary history.
[Tempo HTTP API](https://grafana.com/docs/tempo/latest/api_docs/)

## Query semantics

For the initial Prometheus provider, require one output series per SLI component
per evaluation point. Reject multiple series with a diagnostic suggesting explicit
aggregation; never silently sum label combinations. Ratio good/total components
must align in time. A missing component makes the interval unknown, not zero.

Counter definitions use explicit provider conversion over the interval, handling
resets through the selected query semantics. A raw cumulative counter is not itself
an interval count. Rate-to-count estimates require a declared conversion and are
marked estimated. Validation rejects `good > total`, nonfinite data and incompatible
types instead of silently clamping.

For cadence `d`, evaluate at canonical interval end `e`; a result at `e` represents
`[e-d,e)`. Historical queries evaluate endpoints from `start+d` through `end` with
step `d`, then map each point to its preceding interval. Filter unexpected endpoints;
never duplicate a boundary point across chunks. Missing points remain unobserved,
and a provider declaring incomplete query coverage cannot mark absent points empty.

Each count component declares one mode: `interval_count` returns events for the
preceding interval; `counter_increase` uses an explicit reset-aware increase query
over that interval; `rate_estimate` returns events/second and explicitly multiplies
by interval duration. A generic scalar query is not automatically a count. The
provider validates/compiles the mode's query template; query text and conversion
are shown in preview so users can inspect the actual measurement.

For declared estimates, parse the provider's numeric representation and normalize
the resulting decimal count to 12 fractional digits with round-half-even on the agent.
Record the estimated flag, conversion mode and normalization version in definition
and observation provenance. Exact count mode rejects excess precision. Ingestion
accepts only canonical representable decimals and performs no additional rounding.
Validate good/total after normalization and reject inconsistent pairs rather than
clamping. Scalar/ratio values follow the finite binary64 contract.

Default source polling every 60s, minimum 15s; query completed intervals with 60s source
settling delay. Each live execution normally requests one canonical interval. Missing
past intervals go through repair, not overlapping live-window rewrites. Query delay,
cadence and measurement resolution are explicit settings with capability validation.

Connection tests use a bounded authenticated read permitted by the provider; they
do not create source data. Query previews accept unsaved settings, run through the
assigned agent, cap time/series/points, and never write production observations.
Show separately authentication, network/TLS, bad query, unsupported operation,
rate-limit, timeout, empty and incomplete results.

## Configuration and secret handling

Support HTTPS, explicit custom CA bundles, basic authentication and bearer headers
where supported. Certificate verification is on by default; any insecure local-only
override is explicit and visibly marked. Restrict permitted datasource schemes and
disallow embedded URL credentials. Header secrets are encrypted by backend and sent
only to the assigned authorized agent over TLS.

Agent-side outbound access is expected to reach private systems. Operators configure
allowed destination networks/hosts; do not globally ban private addresses that are
the purpose of the agent. Reject metadata-service targets by default, revalidate DNS
resolution/redirect destinations, and disallow forwarding credentials to a changed
origin. Provider redirect and timeout policy is explicit; arbitrary backend proxying
is never offered.

## Backfill and scheduling contract

A provider advertises history capability and a maximum query range, but the backend
owns chunk jobs. Prometheus one-day and Tempo one-hour chunks are conservative
Almanac defaults, not claims of source limits. Subdivide upon resource-limit feedback
down to the canonical interval; return unsupported if no correct bounded execution
exists. Preserve independent historical resource/rate budgets and pause history
when live demand cannot be protected.

## Acceptance criteria

- AC-01: Backend configuration/test requests never query the source directly; only
  an assigned capable agent executes them.
- AC-02: Auth/TLS/rate-limit/query errors are distinct and redacted; no secrets leak
  in previews, logs or public configuration.
- AC-03: Range boundaries, counter resets, missing series, multiple series and
  nonfinite/inconsistent values follow explicit fixtures without fabricated data.
- AC-04: An older agent reports unsupported capability and cannot accept new provider
  work; catalog availability is not inferred from software version alone.
- AC-05: Historical chunks and preview limits bound work while live scheduling keeps
  its capacity; unavailable source history stays visible.

## Review notes and evidence

- **REVIEW-D01:** Prometheus first, Tempo second, Elasticsearch later is a proposed
  delivery order. Confirm desired integrations before provider implementation.
- **REVIEW-D02:** Single-series components, 60s settling delay and declared rate-to-count
  conversion are deliberate correctness restrictions requiring realistic fixtures.
- No providers implemented. Require HTTP fixture tests and acceptance against actual
  supported source versions, including query accuracy and history limits.
