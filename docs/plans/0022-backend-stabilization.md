# Backend stabilization implementation plan

> Execution: primary agent implements and reviews changes in this session. Only a
> test-execution worker may be used, as explicitly requested by the maintainer.
> Follow the executing-plans workflow after acceptance. No implementation delegation,
> new command approvals, terminal input, automatic commits or deployment changes.

Goal: make existing SLI/SLO journeys consistently fast and correct before resuming
feature delivery.

Architecture: backend-owned PostgreSQL pools, exact hourly source aggregates,
generation-fenced background status/history preparation and bounded interactive
reads. Retain canonical facts and the existing mathematical and authorization rules.

Tech stack: existing Go, PostgreSQL 18, pgx/v5, sqlc, OpenAPI and React/TypeScript.
No additional database or queue service. A browser query dependency, if selected,
needs a concrete size/behavior review before addition.

Governing contracts: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md)
and [ADR-0024](../adr/0024-precomputed-operational-reads.md). The maintainer approved
both documents and this plan after Crit review on 2026-10-30. Implementation and
verification remain outstanding.

## Global constraints

- Pause remaining feature implementation and resume it only after this stabilization
  passes acceptance and the maintainer confirms the next priority.
- Preserve exact counts, half-open whole intervals, explicit unknown/zero-traffic
  states, immutable observations, 400-day canonical retention and current urgency.
- Preserve operator data/credentials and the available preview. Use isolated
  fixtures for performance, crash, retention and saturation checks.
- Preserve current page composition, extrema/gaps, exact tables and 1,500-point cap.
- Use only verified saved invocations. No new approvals or improvised wrappers.
  If a required bounded benchmark operation lacks an approved entry, document that
  exact gap and continue independent work; never substitute a different fixture.
- Retain existing failures and test-run timing receipts. Do not rerun without a
  diagnosed cause or changed candidate, loosen limits or erase earlier evidence.
- The current uncommitted query/calculation/browser changes are an unaccepted
  candidate. Review and retain useful fixes; do not claim them deployed or verified
  as a whole. Preserve the journey timeout as an unresolved failure.

## Contract change map

SPEC-0019 contains the clause-level amendment table for SPEC-0003/0006/0007/0010/
0012/0013/0014 and ADR-0007/0023. Those amendments are accepted as of 2026-10-30.
Do not rewrite accepted ADR rationale. Record ADR-0024 as a partial supersession of
ADR-0007's optional acceleration policy, retaining the other accepted choices.

The deliberate choices are exact aggregates rather than float conversion; retained
canonical history rather than raw-data deletion; precomputed extrema-preserving
history rather than silently changing graphs to hourly-only samples; globally
consistent backend grouping rather than independent browser row evaluation.

## Reuse map

Reuse complete reviewed algorithms and failure fixtures where they fit. Adapt names,
UUID identities, immutable observations, exact decimal values and current permissions.
Preserve required third-party notices when transplanting implementation. Record code
provenance in the normal license inventory rather than architecture prose.

| Candidate logic | Reuse | Required adaptation |
| --- | --- | --- |
| Bounded pgx pool lifecycle and transactional query configuration | Connection reuse, shutdown, acquisition limits, transaction-local JIT policy | Current single-installation ownership, current schema gate, three admission classes and revocation checks |
| Hourly aggregate reader/rebuilder | Clean-bucket selection, dirty-bucket fallback, raw edge handling and partition-aware SQL | Unrestricted exact sums, immutable measurement revisions, outcome/coverage evidence and no boundary rounding |
| Durable recomputation queue | Coalesced arrivals, bounded scans, generation-specific acknowledgment, restart and failure retention | Dirty ranges and dependency propagation through all affected SLO/burn/day endpoints |
| Rolling series/index algorithms | Ordered sweep, prefix coverage checks, bounded grid and tail handling | Existing rational arithmetic, inclusive target equality and exact original window bounds |
| Revision-checked status publication | Conditional publish, invalidation and stale-source distinction | Existing urgency reasons, exclusions, retention and authorization model |
| Independent browser query caching | In-flight deduplication, previous data, cancellation and mutation invalidation | Bounded cache, generation coherence, sign-out clearing; no per-row request fan-out |
| Database/worker fault fixtures | Dirty versus clean aggregates, late data, publication races, interrupted rebuilds | Current real API/agent fixtures, exact values and isolated resource budgets |
| Existing Almanac evaluator and controls | Keep canonical oracle, deterministic time, exact serialization, shared charts and tables | Move expensive derivation out of interactive handlers; do not replace working contracts |

Do not transplant generated files, migrations, tenant routing, authentication,
floating-point status calculations, mutable sample upserts, changed zero-budget
classification, whole-hour window rounding or assumptions about expired raw history.
Regenerate sqlc/OpenAPI output from the accepted Almanac source contracts.

## Review focus

1. Late data changes both its own hour and future rolling endpoints; every affected
   point and display block must be invalidated without scanning unrelated history.
2. A source generation arriving during publication survives the older worker's
   acknowledgment and never receives the old Healthy result as current.
3. Cadences/windows not aligned to hours, short validity ranges and retention edges
   use exact canonical intervals rather than rounded or prorated aggregates.
4. Freshly published calculations over stale source data remain source-stale;
   clock-driven expiry progresses even when collection stops.
5. Heavy preparation cannot exhaust live database slots, block session revocation,
   overwrite operator state or keep shutdown waiting beyond its deadline.

## Delivery sequence

### 1. Fix the contract and establish the comparison baseline

Files: accepted amendments listed in SPEC-0019; `api/openapi.yaml`,
`internal/storage/query/`,
`internal/command/objectives_journey_integration_test.go` and the existing timing
ledger infrastructure.

Consumes: current source state, retained failing journey/latency evidence and
SPEC-0019 workloads. Produces: accepted schema/API diff, dependency graph and a
bounded benchmark brief with exact approved invocation and output artifacts.

- [x] Obtain acceptance of ADR-0024/SPEC-0019 and reconcile the existing contracts.
- [ ] Identify existing wire fields to reuse and specify additive generation,
  preparation and independent section reads. Keep the current operational response
  compatible or make its version transition explicit before generating clients.
- [ ] Specify tables/indexes, aggregate numeric types, dirty key/range union,
  generation manifest/publication transaction and retention of pinned generations.
- [ ] Define internal ports for aggregate reads, durable claims, conditional publish
  and acknowledgment. Use consumers to justify interfaces; avoid a generic cache
  framework. Include time-driven expiry and current-before-history scheduling.
- [ ] Account for raw/derived row counts, disk cost and rebuild dependencies for
  100 SLOs and 400 days. Set explicit worker and queue bounds from the draft.
- [ ] Reuse retained baseline evidence; run only missing focused reproductions.
  Separate setup, API server duration, transport, parse and chart render timings.

Exit: reviewable accepted contract and reproducible real-data baseline. No broad
browser/Compose matrix is needed for this documentation checkpoint.

### 2. Deliver pooled, bounded storage reads

Files: `internal/storage/initial_owner.go`, `internal/storage/agent_reads.go`,
`internal/storage/storage.go`, backend lifecycle wiring under `internal/command/`,
`internal/storage/query/slis.sql`, generated query output and relevant regressions.
New runtime pool ownership/admission code belongs in `internal/storage/` with one
clear owner, not in each handler.

Consumes: the accepted connection/admission contract. Produces: runtime-owned pools
and bounded SLI reads usable by the existing authenticated UI/API journey.

- [ ] Write focused regressions for connection reuse, cancelled acquisition,
  schema rejection, revocation, shutdown and retained live admission.
- [ ] Introduce the pool owner and route reads through acquired connections without
  changing transactional authority checks. Validate schema at lifecycle boundaries.
- [ ] Keep the latest-candidate query restriction and review indexes/query plans;
  apply transaction-local JIT policy through typed fixed-purpose SQL.
- [ ] Run the focused cases once after implementation; exercise the real SLI list
  with dense history. Inspect query work and connection counts as well as duration.

Exit: authenticated SLI list works through pooled reads with measurable bounded
query work; outage/revocation/shutdown behavior is preserved.

### 3. Deliver durable exact aggregates and current SLO projections

Files: canonical ingestion implementation `internal/storage/sli_ingestion.go`, its
source queries under `internal/storage/query/`, new append-only migration under
`internal/storage/migrations/`, new derived-data storage/worker files, backend
lifecycle wiring, `internal/slo/operational.go` and `internal/storage/slo_operational.go`.
Source SQL lives in `internal/storage/query/`; regenerate typed queries rather than
editing `internal/storage/dbgen/` directly.

Consumes: accepted immutable observations, exact evaluator and generation contract.
Produces: exact hourly aggregates, durable invalidation and published current
status/1 h/6 h/urgency readable without historical sweeps.

- [ ] Write oracle fixtures for fractional/large counts, equality/negative budget,
  missing/known-empty/zero traffic, non-hour bounds, eligibility and stale inputs.
- [ ] Atomically write dirty generation/ranges with canonical acceptance. Ensure
  duplicate receipts do not create contributions or generation churn.
- [ ] Implement bounded claim, coalescing, exact rebuild, conditional publication,
  generation acknowledgment, retries and durable exhausted diagnostics.
- [ ] Schedule current recomputation on cadence/freshness boundaries independently
  of new ingestion; prioritize it over historical rebuilding.
- [ ] Prove the five review-focus cases with real transaction/concurrency fixtures,
  including restart at each durable boundary and a newer arrival during publish.
- [ ] Switch current-summary reads to valid published results; expose preparation
  distinctly. Run the existing ordinary SLO journey with a prepared current result.

Exit: ingestion → background derivation → authenticated current result works, and
restart/late-arrival tests establish correctness. No generic raw fallback remains.

### 4. Deliver cached history and coherent operational lists

Files: `internal/storage/slo_operational.go`, new history/projection queries and
storage files, `internal/slo/operational.go`, new migration if required, API handlers
and the accepted OpenAPI source; generated clients follow source generation.

Consumes: exact aggregates, current projections and dirty dependency ranges.
Produces: bounded four-lane histories, exact-value pages and globally grouped lists
from pinned derived generations.

- [ ] Reuse the existing canonical sweep as the differential oracle; produce exact
  cached endpoints and mergeable first/last/min/max/gap display blocks in workers.
- [ ] Rebuild affected future rolling endpoints and UTC-day results for late data;
  preserve independent definition/exclusion/retention boundaries and no-data states.
- [ ] Publish complete generations atomically. Pin list counts/order/pages and
  summary/history/exact tables; reject invalid/expired generation cursors explicitly.
- [ ] Replace overview raw computation with indexed derived reads before pagination.
  No per-objective full detail calculation may occur on the list request path.
- [ ] Compare all numerical lanes/tables against the canonical oracle, including
  narrow spikes and gaps hidden inside an hour. Exercise invalidation mid-read.
- [ ] Verify ready 7/28/90/365-day reads fetch bounded derived data and meet focused
  budgets before running any broad end-to-end gate.

Exit: actual API list/detail/exact-value journeys return coherent bounded results;
preparation and error responses are honest and currentness is measured.

### 5. Deliver independent, shared browser loading

Files: `web/src/slos/client.ts`, `web/src/slos/OperationalViews.tsx`, affected SLI
clients/views under `web/src/slis/`, shared request-cache owner, their focused tests
and generated API types. Reuse existing primitives before selecting a dependency.

Consumes: generation-aware independent APIs. Produces: unchanged page composition
with shared reads and status visible before history is ready.

- [ ] Specify a small shared query-cache interface or review a pinned query-library
  dependency for bundle impact, cancellation and mutation invalidation.
- [ ] Implement in-flight sharing, bounded retention and protected-cache clearing;
  invalidate relevant data after mutations and reject obsolete generation responses.
- [ ] Keep preparation/history errors local to their section. Retain labelled
  confirmed data during refresh; do not make row request fan-out the list strategy.
- [ ] Verify back navigation, rapid range/filter changes, delayed history, revoked
  sessions and bounded request counts in focused checks, then one real journey.
- [ ] Inspect unchanged layout and exact values in both themes and a mobile width.

Exit: the ordinary list → detail → range/table → SLI → back journey works without
duplicate historical evaluations or unrelated sections waiting on history.

### 6. Prove the integrated candidate and resume decision

Files: existing fixed verification catalog in `Makefile` and
`scripts/verification-controls.py` only if a reviewed fixed workload selector is
needed; real storage/API/browser/Compose fixtures, `docs/status.md` and timing ledger.

Consumes: one integrated source candidate from steps 2–5. Produces: published
correctness/performance evidence with failures and remaining gaps intact.

- [ ] Run relevant focused regressions after the final changes. Use the test-only
  worker for execution and report exact commands, timings and first failures.
- [ ] Run the 56-day integrated workload and concurrency/fault/isolation scenarios.
  Record p50/p95/p99, query plans/row counts, queue lag, pools, memory and disk cost.
- [ ] Run the 400-day retention fixture once, including expiry and cold restart with
  persisted derived data. Measure empty-derived preparation separately.
- [ ] Run the production browser samples with the prescribed network/CPU settings.
  Then run the required full candidate and isolated normal Compose acceptance once.
  Reuse evidence for unchanged components; do not repeat a catalogue matrix on each
  backend edit. New code/failures justify only the checks affected by that change.
- [ ] Independently exercise the real journey, including pending/failed preparation,
  restart, filters and exact tables. Verify the built revision and preview readiness.
- [ ] Reconcile docs and report every SPEC-0019 criterion as verified or unresolved.
  Feature work remains paused if any acceptance failure is open. Return the actual
  evidence to the maintainer before resuming the feature roadmap.

Exit: all required correctness, latency/currentness, isolation and lifecycle gates
pass on the same candidate. A small successful fixture is not sufficient acceptance.

## Current state

Execution started on 2026-10-30 after the maintainer requested completing PR47
review corrections first. Those review threads have replies; the final urgency
disclosure correction is published in `98779e1`. Its focused suite passed 269 cases
in 8.103s. This does not establish the stabilization workload or visual acceptance.

Step 1 is in progress. The implementation boundary below is the first concrete
design checkpoint; schema/API generation and runtime changes remain outstanding.
Earlier uncommitted performance work is retained for review against these contracts.

## Step 1 implementation boundary

### Connection ownership

Create `internal/storage/runtime.go` for one runtime-owned `Runtime` containing the
three pgx pools. `OpenRuntime(ctx, databaseURL)` validates schema and initializes
bounded pools; `Close(ctx)` cancels workers and drains within the runtime deadline.
The backend command owns it and passes it through application storage adapters.
Do not install a process-global pool registry keyed by database URL, or hide pooled
connections behind a function that still closes each connection on return.

An internal acquired handle exposes the pgx transaction/query operations actually
used by storage and an idempotent release. Dedicated ownership and explicit migration
connections stay distinct. Replace `connectCurrentSchema`/`closeInitialOwnerConnection`
at runtime call sites through this handle; keep standalone administrative upgrades
independent. Current session/user/installation checks remain inside each operation.
The 76 existing setup/cleanup references require explicit ownership review rather
than a mechanical Close-to-Release substitution.

### Derived schema responsibilities

Use append-only migrations after 0021 and source SQL under `internal/storage/query/`.
The following table responsibilities define the schema diff to implement:

| Table | Key and responsibility |
| --- | --- |
| `almanac_sli_source_generations` | `(sli_id, measurement_revision)`; monotonically advancing source generation, updated in canonical acceptance transaction |
| `almanac_derived_dirty_ranges` | SLI/revision, half-open affected range and generation; coalesced bounded work with lease epoch, attempt budget and failure diagnostics |
| `almanac_sli_hourly_aggregates` | SLI/revision/hour; unrestricted exact numeric sums, coverage evidence, source generation and calculation version |
| `almanac_slo_derived_generations` | Immutable generation UUID, objective/revisions/source generation, evaluated bounds, retention/exclusion/calculation identity and publication state |
| `almanac_slo_current_projections` | Objective and published generation; indexed service/name/urgency, current values and exact burn summaries |
| `almanac_slo_history_points` | Derived generation/lane/evaluation time; exact values/counts/quality, partitioned by time |
| `almanac_slo_history_blocks` | Generation/lane/time block; mergeable extrema and gap evidence for bounded read reduction |

Reuse existing `almanac_sli_dirty_intervals` only if its generation and acknowledgment
semantics prove sufficient; do not treat its existence as proof of those properties.
Existing overview snapshots retain immutable projection references/payloads for
pagination and must not trigger raw computation. Publication updates the visible
generation reference atomically after all necessary blocks are complete. Current
and historical preparation have separate priorities and disclose different ready
boundaries; combining them requires a pinned compatible manifest.

### API compatibility

Keep `/api/v1/slos/overview` and `/api/v1/slos/{slo_id}/operational` compatible for
ready results. Add generation/currentness metadata through an optional additive
schema first, then require it for the new independent reads:

- `GET /api/v1/slos/{slo_id}/status`: current projection with generation identity.
- `GET /api/v1/slos/{slo_id}/history`: requested aligned range, burn window and
  optional pinned generation; returns numerical series and compliance for that generation.
- `GET /api/v1/slos/{slo_id}/history/values`: bounded lane-specific exact-value
  pagination requiring generation identity, with the same authorization checks.

Use `DerivedGeneration` metadata with `generation_id`, `source_generation`,
`measurement_revision`, `definition_revision`, `exclusion_revision`,
`calculation_version`, `evaluated_through`, `published_at` and disclosed calculation
lag. Source-observation freshness remains in existing quality fields. Decimal strings
and revision strings retain current wire conventions. Never overload evaluation_id
or snapshot_id with a different meaning.

Pending preparation uses the accepted 503 problem and Retry-After contract. A ready
legacy composite response is assembled from one compatible manifest; it must not
fall back to raw calculation or combine independently changing generations.
Authorization errors retain existing response behavior. Exact OpenAPI parameter,
problem and pagination schemas are the next source-contract edit in this checkpoint.

### Evidence boundary

The known dense-fixture journey timeout remains open; no retry is warranted solely
to change its result. Reuse its retained timings as baseline and reproduce only
missing diagnostic evidence. The next runtime increment must first prove bounded
pool ownership and SLI read behavior, before adding workers or large workload gates.
