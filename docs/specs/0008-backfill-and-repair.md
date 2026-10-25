# SPEC-0008: Backfill and repair

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: not started
- Related: [SPEC-0003](0003-ingestion-and-recovery.md), [SPEC-0009](0009-agent-protocol-and-ownership.md), [ADR-0003](../adr/0003-bounded-buffering-and-historical-isolation.md)

## Purpose and responsibilities

The backend owns historical job planning and persisted progress. The assigned agent
executes provider queries. Manual backfill and automatic healing use the same bounded
historical path, separate from live collection. They are best effort and never promise
full historical recovery. Externally submitted SLIs expose gaps but require the user
to submit history with an authorized producer key.

## Job creation and planning

An Editor can request backfill for an SLI and UTC half-open range; datasource setup
can apply the request to selected SLIs, returning a group of per-SLI jobs. Preview
the effective range, retention limits, provider support and estimated chunks first.
Require explicit confirmation of clamping to supported history; never silently change
the requested interval. Split across effective definition and assignment boundaries.
Validate start/end against each effective definition's canonical grid; reject
misaligned bounds with suggested aligned values unless the user confirms a preview
adjustment. Never prorate an observation. Initial setup can explicitly authorize a
first revision's historical measurement-valid-from before live activation, as in
SPEC-0005; ordinary later revisions cannot backdate their meaning.
Historical definitions needing revoked secrets or unavailable queries are unreplayable.

The backend plans chunks using provider descriptors and reported agent capabilities.
Provisional chunk maximum is one day for Prometheus and one hour for Tempo; providers
may advertise smaller limits. These are Almanac workload defaults, not vendor limits.
The canonical observation interval remains unchanged when query chunks change.
Adaptive subdivision may split oversized/time-limited chunks down to one observation
interval; if even that cannot fit, mark it failed rather than loop indefinitely.

Job identity includes SLI, revision, requested range and initiating actor/cycle.
Active-job range exclusion prevents two repair/backfill jobs from independently
owning the same work. Repeated requests with the same command idempotency key return
the original job. Overlap returns the existing job plus uncovered ranges for explicit
admission; never create hidden duplicate historical work.

## Lifecycle and durable progress

Job states: queued, running, paused, completed, completed-with-gaps, cancelled.
Chunks: queued, leased, committed, retry-wait, unavailable, failed, cancelled.
Persist retry attempts, next eligible time, lease generation, last redacted error,
committed coverage, and explicit missing intervals. Completed query execution alone
does not advance coverage. Observation acceptance and chunk commit must be atomic or
reconciled by durable idempotent receipts before publishing progress.

Historical chunk leases last two minutes, renewed every 30s while connected. They are
distinct from the agent's one-hour offline live-execution grant. Cancellation or
assignment change increments generation; later stale chunk commits are rejected.
If connection or delivery is unavailable, stop admitting historical queries and let
claims expire. Bounded in-flight results may retry only while their grant remains valid.
Only live collection continues under the offline ring-buffer policy.

Pause stops new claims; active queries finish within their deadline unless an operator
requests cancel. Cancellation is best effort for remote execution but immediate for
backend result authorization. Resume preserves completed chunks and attempt budgets.
An administrator's explicit retry-exhausted action creates a new bounded cycle and
audit record; ordinary resume cannot reset failed attempts invisibly.

## Periodic healing

Every 60s the backend scans a bounded page of overdue expected intervals, beginning
after a five-minute delivery allowance. Check at most 1,000 intervals per pass by
default and persist the scan cursor. Look back at most 24h per routine scan; a separate
bounded historical sweep traverses older retained gaps over time so long outages do
not leave unexamined holes. Both scans share admission and active-range exclusion.

A repair cycle attempts each chunk at most 3 times, including the initial attempt,
with 30s then 2m minimum retry delay plus jitter. Honor longer provider Retry-After,
within a 24h cycle deadline. Authentication failures, unsupported replay and expired
history terminate immediately until an explicit relevant configuration change or
administrator retry creates a new cycle. A configuration change may make a gap
eligible but must not reset an unrelated exhausted budget repeatedly.

After exhaustion, keep the interval missing with reason, attempt history and next
available operator action. An administrator can retrigger selected ranges through
UI/CLI. Never retry known successful-empty coverage as a missing execution. Duplicate
or conflicting late/live/backfill values follow SPEC-0003; a repair fills holes and
does not overwrite accepted observations.

## Isolation and limits

Provisional agent historical pool: 2 concurrent queries overall, 1 per datasource,
8 MiB total result/in-flight memory, 1 simultaneous historical submission. Reserve
live slots independently. Provider token/rate budgets allocate remaining headroom
to history only after live needs. If live queries run late or provider throttles,
stop historical admission for at least one live interval and reassess.

A new historical chunk cannot preempt an already scheduled live query. Historical
timeouts default 20s; live timeout derives from its cadence and provider contract.
If an upstream cannot safely execute both, disable concurrent history for that source
and show waiting-for-live-capacity. A permanently saturated source may never finish
backfill; surface this honestly. Backend historical DB pool/transaction limits apply
independently of agent limits.

## Acceptance criteria

- AC-01: A month-long request produces provider-appropriate chunks without changing
  canonical observation resolution or double-counting boundary points.
- AC-02: Crash/restart, expired leases and cancellation do not duplicate committed
  progress or accept fenced results.
- AC-03: Automatic retry budgets survive scans/restarts; exhaustion stays visible
  until explicit retry or a documented relevant change creates a new cycle.
- AC-04: Live queries keep their schedule under a saturated historical queue, slow
  provider and failed historical submissions; historical work pauses first.
- AC-05: Manual retrigger is authorized, auditable, bounded and deduplicated against
  active jobs; successful-empty and externally submitted gaps are handled correctly.
- AC-06: Requested/committed/unavailable/failed intervals are distinguishable; a
  partially recovered job never reports full completion.

## Review notes and evidence

- **REVIEW-B01:** Backend chunk planning, scan timing, retry counts and concurrency
  are proposed defaults. They keep state inspectable and survive agent restarts.
- **REVIEW-B02:** Historical work pauses on disconnect; only live work continues.
  This avoids spending source capacity on history that cannot be delivered.
- Not implemented. Validate with controllable clocks, crash injection, overlap,
  cancellation, partial provider history, and simultaneous live/historical load.
