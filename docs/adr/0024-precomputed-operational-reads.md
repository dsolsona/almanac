# ADR-0024: Precompute operational reads with exact derived data

- Status: accepted
- Created: 2026-10-30
- Accepted: 2026-10-30
- Approval: maintainer explicitly replied "approved" after the combined Crit review of this ADR, SPEC-0019 and the stabilization plan
- Related specs: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0007](../specs/0007-storage-and-retention.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: replaces ADR-0007's optional acceleration policy only; other storage and measurement decisions remain in force

## Context

Interactive SLI/SLO reads currently repeat connection setup and, for SLOs, load
canonical observations and evaluate rolling history during the request. A bounded
response does not bound the work needed to produce it. The overview additionally
builds all matching objective projections before returning a page. More history
therefore makes navigation slower even when the visible page has not changed.

[ADR-0007](0007-postgresql-and-storage-boundaries.md) permits revisioned caches
and retains canonical facts to preserve correctness. It does not require raw
history evaluation on every request. This decision makes accelerated reads a
required architecture, without changing the retained source of truth.

## Decision

The following architecture is accepted; implementation and verification remain outstanding.

### Own bounded PostgreSQL pools for the backend lifetime

Reuse pgx pools with separate live, interactive and derived/historical admission.
Start with maximum connection budgets of 4, 8 and 2 respectively, plus the existing
dedicated backend-ownership connection; validate these defaults under SPEC-0019.
Readiness has reserved live admission. Bound acquisition, statements and locks;
an interactive timeout cannot consume live capacity. Shutdown cancels workers and
closes pools within the existing runtime deadline.

Validate schema compatibility at startup and readiness, and gate work if it changes
or is unsupported. Do not open a new connection or reread migration history for each
ordinary storage operation. Preserve per-request authorization, revocation and
transaction semantics. Pooling is not permission to cache authorization decisions.
Apply transaction-local JIT-off configuration to bounded interactive aggregate
queries; do not change the installation-wide PostgreSQL default.

### Build exact, revisioned derived data in the backend

Keep canonical observations and coverage for the accepted 400-day horizon. Add:

- Per-SLI/revision hourly count and coverage aggregates, using unrestricted exact
  numeric sums rather than narrowing sums back into an input-sized numeric column.
- Per-objective current status, 1 h/6 h burn and urgency projections.
- Per-objective history points and bounded display blocks for budget, selected burn,
  attainment and completed UTC-day compliance. Preserve extrema and gap transitions
  from the accepted evaluation grid before display reduction.

Hourly count aggregates are reusable sufficient inputs for aligned occurrence
windows; they are not sufficient evidence for arbitrary slice/scalar/composite
semantics or extrema between hourly endpoints. Read bounded canonical edge intervals
where needed. Preserve exact point calculations for those semantics before caching
or reducing them. Do not silently round a rolling window to whole hours.

Every published result identifies calculation version, source generation,
SLI measurement revision, SLO definition revision, exclusion revision, retention
boundary, actual evaluation bounds and evaluated-through time. Clock-driven expiry,
eligibility and freshness transitions are work even when no new observation arrives.

### Use durable invalidation and generation-fenced publication

Acceptance of new canonical observations atomically advances the affected source
generation and records dirty time ranges. Duplicate accepted deliveries do neither.
Definition, eligibility, exclusion and retention changes invalidate their dependent
derived data. Annotation edits do not invalidate calculations.

A backend-owned worker coalesces dirty ranges, claims bounded batches, rebuilds
aggregates and dependent points, then conditionally publishes against the revisions
it read. Acknowledgment removes only the claimed generation. A newer arrival cannot
be erased by an older worker. Interrupted work survives restart. Repeated failures
back off with a visible error and finite automatic retry budget; manual retry is
audited and does not reset unrelated work.

A late interval affects its containing aggregate and every rolling endpoint whose
window includes it, plus affected completed-day and display reductions. Rebuild that
bounded dependency range, not every retained objective. Build replacement generations
out of sight and expose them atomically. Never publish a partially rebuilt history
as a complete new generation.

### Make interactive work independent of raw retention length

SLI lists read bounded summaries and latest candidates. SLO lists read indexed
published projections; no request evaluates all matching objectives from raw facts.
Apply global filters/group totals/order against a consistent published generation,
then paginate. Existing per-session cursor snapshots may retain those projections,
but must not trigger historical calculation to create them.

Status and history are independently readable and cacheable. A response requiring
several sections pins one coherent generation. Independently loaded sections expose
their generation so clients do not combine incompatible numbers. History reads
retrieve bounded derived blocks and exact-value pages, not a raw-to-chart sweep.

If derived data is absent or invalidated, enqueue/coalesce preparation and return
the explicit preparation state in SPEC-0019. Do not fall back to an unbounded raw
calculation. A previous generation may be shown only with its time and stale state;
it cannot count as a fresh Healthy result or establish alert recovery.

### Cache browser reads without changing the page composition

Share identical in-flight reads, retain previous confirmed data while refreshing,
bound cache lifetime and entries, and cancel obsolete requests. Load definitions,
current results and history independently. Reuse the current client infrastructure
where it provides these properties; select a shared query library only through the
dependency review in the plan. No per-row status/history request fan-out for lists.

## Alternatives considered

- Optimize raw request-time evaluation only: worthwhile immediate repairs, but
  repeated work and retention-dependent latency remain. Not the long-term read path.
- Use float counts, round windows to hours, or discard canonical facts: cheaper,
  but changes accepted precision, boundaries and report capabilities. Rejected here.
- Add a broker or another database: adds deployment and recovery boundaries before
  the PostgreSQL design has been validated. Not required for this stabilization.
- Cache whole HTTP responses without durable invalidation: helps repeated identical
  requests but does not handle late data, restarts, revision races or cold reads.

## Consequences

Reads become predictable and reusable across users. Writes gain small atomic dirty
markers, while background work and derived storage require explicit budgets,
monitoring and recovery. Currentness becomes a disclosed contract rather than an
implicit promise that an expensive calculation happened inside the HTTP handler.
Full-resolution derived point retention may be substantial; measure disk/write cost
alongside latency and use mergeable blocks without weakening extrema/gap evidence.

This does not authorize new measurement methods, external source queries in the
backend, reduced retention, changed urgency thresholds or weaker authorization.
Exact arithmetic and report immutability under ADR-0005 remain unchanged.

## Revisit conditions

Revisit after the SPEC-0019 workload fails despite profiling and bounded processing,
or if measured derived storage exceeds the documented deployment envelope. New
storage engines, lossy semantics and reduced retention require separate decisions.
