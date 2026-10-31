# Operational derived-data implementation

This records the current implementation of [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md)
and [ADR-0024](../adr/0024-precomputed-operational-reads.md). It is not performance
acceptance. The schema and additive wire contracts are reviewable in migration
0022 and `api/openapi.yaml`; real workload evidence remains outstanding.

## Connections and admission

One backend runtime owns live, interactive and derived pools, capped at 4, 8 and 2
connections. The ownership lock and explicit migration commands remain separate.
Backend request contexts carry their storage runtime, so authentication and older
management operations also reuse the owned interactive pool. This carries no
cached permission decision: transactional session, user, installation and role
checks still run. Live agent ownership, heartbeat, configuration and ingestion
select live admission explicitly. A database identity mismatch fails; it never
falls back to opening another connection.

Acquisition waits at most 250 ms. Interactive sessions/statements use 2 s and
derived sessions/statements 5 s, with 250 ms lock waits. Transaction-local JIT-off
applies to bounded operational work. Readiness checks migration compatibility
through reserved live admission and can reopen a repaired schema gate. Standalone
fixtures and startup/migration operations retain explicit standalone connections.

Operational JSON reads negotiate gzip at the HTTP boundary to reduce repeated
sparkline/quality metadata on the specified browser link. Exact decoded bytes are
unchanged. Authentication/credential responses, agent/websocket traffic and strong
resource-ETag representations are excluded. Actual wire size and parse/render time
still require production workload measurement.

Ready first pages of paginated results may reuse an actor/session/role-bound snapshot when every
search/service match still has the same current projection, source generation,
display metadata and history publication/failure state. The comparison includes
all matches before urgency filtering so group totals remain coherent. Reuse
neither renews the two-minute expiry nor changes the saved rows. A cancelled
reader cannot delete a reused snapshot. Ready reuse bypasses the global admission
lock; it does not serialize independent users. Single-page results release their
snapshot immediately, so ordinary filter changes do not spend cursor-retention slots.
New paginated publications require a new snapshot under the existing
four-per-session/64-global admission limits. First-page history
preparation refreshes are capped at three replacements; cursor pages stay pinned.

## Rebuilds and exactness

Canonical acceptance advances the SLI/revision generation and dirties its UTC hour
in the same transaction. A duplicate does neither. The source worker claims at
most 100 dirty keys, calculates unrestricted numeric sums in one bounded batch,
commits those aggregates, then acknowledges only the claimed generations/epochs.
Calculation never holds dirty-row locks. Newer arrivals survive old acknowledgments.
Backlogs drain without a mandatory sleep after every hundred keys; current status
gets a turn each second independently of historical preparation.

An aggregate range uses boundaries common to UTC hours and the source cadence.
For irregular/long cadences, the common grid leaves at most 7,200 canonical source
intervals across both edges; these are read exactly. Windows are not rounded or
prorated. The existing rational evaluator remains the oracle and calculation core.

The second worker prepares history. Reusable blocks retain exact selected points,
first/last/min/max and gap transitions on the original display grid. A late interval
invalidates blocks whose rolling input ranges include its containing hour. The
invalidation evidence is present either in dirty work or in the rebuilt hour's
source generation, including across acknowledgment and restart. Only contiguous
invalid blocks load their canonical input ranges and run a replacement sweep.
Unchanged blocks are reused. Daily results use independently reusable UTC-day blocks.
Retention, definition, measurement, calculation version and freshness-grace changes
participate in validity checks.

Complete response generations publish atomically. Their payloads remain immutable
when reusable blocks are subsequently replaced. Current status, history and exact
table pages identify their generation; a table page never switches generations.
Exact pages contain at most 100 represented values and expire explicitly after
five minutes. Ready interactive reads load derived data rather than running a raw
history sweep. Missing data queues bounded preparation and returns its distinct
state, a bounded retry delay and an authorized diagnostics URL when one objective
is addressed. Waiting for source aggregates does not spend a history failure attempt.
Calculation-version and cadence-aligned retention boundaries also fence queue keys
and complete-response lookups, rather than only checking source generation.
Exhausted required source hours report failure, not indefinite preparation; retrying
the affected source key can restore dependent current work without exhausting an
unrelated current-calculation budget.

## Failure and cache bounds

Cold display-block preparation reads canonical counts through a correlated value
lookup for each coverage interval. Explicit SLI/revision/time bounds prune both
partitioned tables; a lateral `LIMIT 1` preserves the primary key's at-most-one
value contract while keeping timestamp equality in the index probe. Without that
barrier, fresh-partition statistics produced a one-row estimate and a nested loop
that repeatedly scanned the full value range. The retained-history regression
checks the actual SQLC statement's plan for correlated interval lookups; it keeps
the existing query deadlines and missing-value behavior.

Automatic failure budgets are five attempts. Failed jobs remain diagnosable;
ingestion alone does not reset an exhausted budget. A last-attempt worker crash
becomes a visible exhausted state after lease expiry. Preparation diagnostics return
at most 100 records, and a manual retry addresses one current/history/aggregate
job, respects a one-minute retry limit and commits its audit record atomically.

History admission counts retained failures as well as active jobs: at most 64
retained history jobs globally and eight per objective. Whole history payloads
are capped at 20 MiB. Reusable and complete history payloads share a 2 GiB logical
JSON byte budget, enforced transactionally. Current status does not take that
budget lock, so historical cache pressure cannot block current publication there.
The budget is a bound on encoded live payloads; indexes, WAL and dead tuples are
separate measured storage costs. Five-minute expiry is collected in bounded batches,
including while the history queue is idle. Exhaustion returns an explicit failure,
not an unbounded raw calculation.

Payload digests detect corruption of current, complete-history and reusable-block
caches. Invalid current payloads become due for recomputation. Invalid history
blocks are rebuilt from retained canonical inputs. Mutable display metadata is
read separately from retained calculation values.

Agent-session drain sends its existing shutdown notice, then bounds the peer close
grace by half the caller's remaining shutdown budget, capped by the existing
five-second write bound. After grace it expires and closes the captured transport,
interrupting an already-running WebSocket/TLS close handshake, and joins session
handlers. A non-reading peer cannot consume the whole runtime deadline before HTTP
drain starts. No configured timeout was increased. The ordinary reassignment worker
also receives the owned storage context instead of creating a standalone connection
on every polling turn.

## Internal metrics

The backend optionally serves `GET /metrics` on `metrics_listen`, configured through
the JSON file, `ALMANAC_METRICS_LISTEN` or `--metrics-listen`. It is disabled by
default. Configure a separate internal numeric-IP address, for example
`127.0.0.1:9091`; deployment networking must keep that port internal. The listener
has no API, authentication, health or profiling routes, and closes with the backend.
An occupied configured address fails startup. Public HTTP routes do not gain a
metrics endpoint.

Fixed labels identify the three traffic classes, two preparation stages and three
ingestion results: accepted, duplicate and unsuccessful.
Counters/histograms measure pool admission, query duration/errors, rebuilt source
keys, processed history jobs, preparation failures and complete-history cache hits.
An ingestion histogram measures the complete observation HTTP handler, including
credential checks, canonical acceptance and response serialization. An HTTP 200
batch containing rejected/retryable items is unsuccessful; a duplicate-only batch
cannot substitute for new canonical acceptance. Its fixed geometric duration
buckets support conservative before/after percentile bounds, with coherent
snapshots and no per-request retention. The workload compares a loaded upper bound
to the baseline lower bound rather than interpolating a passing ratio.
No query text, parameters, error text, user or objective identifier is retained.
Heap bytes/objects are sampled at scrape time. The existing source worker samples
queue counts/age, current lag, pending objectives, logical cache usage and actual
canonical/derived relation bytes every 30 seconds with derived admission and a
five-second deadline. Scrapes perform no database queries. Before the first
successful sample, readiness is zero and lag/storage values are absent; subsequent
samples carry a timestamp so a failed sampler cannot masquerade as fresh evidence.
These metrics are implemented but have not yet supplied workload acceptance evidence.

## Resource estimates and evidence still required

The retention fixture has 57.6 million canonical intervals and up to 960,000 hourly
keys across 100 SLIs. At a measured combined heap/index cost of B bytes per interval,
its canonical footprint is `57,600,000 × B`, before WAL and temporary rebuild space.
For example, 500 bytes would mean 28.8 GB; this is arithmetic, not a measured size.

A maximally reduced detail view has up to 1,500 budget points, 1,500 burn points,
375 attainment buckets and 365 daily results. At a measured P bytes per represented
value, 100 such views use at most `374,000 × P` bytes before duplicate complete
response storage and indexes. Cache admission and the 2 GiB live-payload bound can
reject additional combinations; no unmeasured capacity promise is made.

There is one historical calculation at a time, with the existing 2.5-million-row
canonical input cap per admitted calculation. Record actual peak RSS/heap, CPU,
encoded payload sizes, relation sizes, WAL, queue lag/drain rates and query plans
under the specified 56/400-day workloads. Unit equivalence and bounded selectors
do not prove the latency, currentness, live-isolation or deployment acceptance gates.
