# SPEC-0007: Storage and retention

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: in progress
- Related ADRs: [ADR-0007](../adr/0007-postgresql-and-storage-boundaries.md)
- Related specs: [SPEC-0003](0003-ingestion-and-recovery.md), [SPEC-0006](0006-slo-calculation-and-reporting.md)

## Purpose and scope

Provide one PostgreSQL-backed record of configuration, observations, coverage,
work, identity, and audit. The backend owns database access; agents and separately
running UI/alert workers do not receive database credentials.

## Data boundaries

| Records | Invariants |
| --- | --- |
| Users, external identities, sessions and tokens | Identity rules in SPEC-0004; tokens stored as digests; secrets excluded from audit |
| Agents, assignments and execution leases | Monotonic ownership generation; historical lease evidence retained for authorized replay |
| Datasources and SLI/SLO definitions | Mutable presentation metadata; immutable effective measurement revisions |
| Observations and execution coverage | Unique canonical SLI/revision/interval key; typed payload; empty coverage separate from zero traffic |
| Delivery receipts | Unique producer/delivery ID plus normalized content digest and durable result |
| Backfill jobs, chunks and repair cycles | Persisted retry budgets and generation fencing; progress only after durable acceptance |
| Derived status and dirty intervals | Recomputable caches with source/configuration revisions and calculation time |
| Alerts and delivery outbox | State transition plus outbound intent atomic; stable event and destination keys |
| Audit | Actor, request, resource, operation, redacted changes, timestamp; append-only through normal APIs |

Opaque resource IDs use UUIDs; display names and OpenSLO names are separate stable
lookup fields. Use explicit foreign keys and deletion behavior. Resource deletion
is blocked while referenced; disabling collection preserves history. Destructive
history purge is an administrator operation distinct from disabling or renaming.

## Time, values, and definitions

Use UTC timestamps with microsecond precision; reject excess precision rather than
silently moving an interval. Canonical intervals are half-open. Positive integer
second collection resolution avoids fractional scheduling boundaries initially.

Propose exact decimal counts stored as `numeric(38,12)` and encoded as decimal
strings. Reject values exceeding supported precision/scale; do not round silently.
Scalar/ratio inputs use finite binary64 values with negative zero normalized.
Canonical digest generation includes kind, interval, revision and normalized value,
not JSON whitespace or receipt time. Aggregate arithmetic must not overflow a
storage column; sums are computed with sufficient decimal range before formatting.

Only one effective definition applies to an SLI at a given measurement interval.
An edit takes effect at the next common boundary of old/new cadences after the
affected agent acknowledges the replacement configuration/effective bounds or its
old execution authority expires. The common cadence period must be at most one hour;
waiting for lease expiry is additional and the scheduled activation is shown to the
operator. Incompatible cadences require explicitly disabling the old schedule and
creating a new SLI. The old revision remains queryable for its
history. Backdating an edit is not part of ordinary update semantics.

## Transactions and concurrent writes

Ingestion locks authorization/assignment evidence and definition rows in a stable
order, then commits observation writes, coverage, receipt, and dirty-work markers
atomically per submitted item. PostgreSQL constraints are the final uniqueness
guard. No ordinary upsert overwrites differing measurements.

A batch can partly succeed by item as specified in SPEC-0003. Database failure
cannot produce an accepted receipt without the corresponding observations. A late
replay after its receipt expires is rejected, never treated as a fresh delivery.
Use a durable producer replay floor and a bounded replay-age field in the contract;
observation uniqueness remains an independent guard against different IDs.

Configuration/resource writes require an expected resource version. Concurrent
edits fail with conflict; a no-op preserves its version. Audit and mutation share
a transaction. Derived computations read one consistent snapshot and conditionally
publish only if contributing revisions still match; stale calculations retry.

## Retention and partitioning

Provisional defaults: retain canonical observations, empty coverage, effective
definitions and referenced receipts for **400 days**. This deliberately avoids
lossy historical aggregation during the first implementation. Hourly materialized
summaries are optional caches, never substitutes unless they preserve the exact
budgeting semantics and requested boundaries. Mark older requested history as
unavailable; do not fill it with zeros or report complete coverage.

Delivery receipts may be compacted after seven days only when a replay floor makes
older delivery attempts explicitly ineligible. The submission contract restricts
one delivery item's retry age to seven days; manually replaying older observations
uses a new authorized historical operation and still hits canonical uniqueness.
Definitions and job evidence referenced by retained observations cannot be deleted.

Partition observation and coverage data by UTC month. Prune only complete partitions
older than the configured cutoff, exposing the actual earliest available interval.
The query API enforces the published cutoff even if a partially retained partition
contains older rows. An eligible observation must start at or after that cutoff;
expose the next canonical interval start per revision as the earliest queryable
boundary. Never slice or prorate an observation crossing the retention cutoff.
Precreate partitions and prepare admitted historical ranges
before dispatch. Missing partitions fail ingestion visibly; agents do not create them.

Partition-aware uniqueness includes the partition time key. PostgreSQL requires
partition keys in parent unique constraints; use keys that include interval start.
[PostgreSQL partitioning](https://www.postgresql.org/docs/current/ddl-partitioning.html)

Retention changes are previewed for impact and audited. Increasing retention does
not recreate deleted data. Decreasing it never deletes pending unexpired notification
work or active job state without an explicit cleanup outcome. Audit defaults to
400 days; operational completed jobs default to 30 days with compact repair
exhaustion summaries retained across the observation horizon.

Keep active incidents and undelivered outbox entries regardless of ordinary history
age. Retain resolved alert/event history and terminal delivery outcomes for 400 days;
maintain event/destination deduplication tombstones through that horizon. Expired
events cannot be manually redelivered without an explicit new notification operation.

## Capacity and operations

Estimate rows before enablement: 100 SLIs at 60-second resolution produce 144,000
observations/day, or 57.6 million over 400 days, before indexes and metadata.
This is arithmetic, not a benchmark. Surface configured retention and storage growth.
Warn before admitting a configuration beyond tested capacity; do not promise a
specific supported scale without load evidence.

Budget database connections by traffic class: live writes/readiness first,
interactive reads next, historical writes and maintenance last. Short historical
transactions use statement/lock timeouts; maintenance does not hold long locks over
the live write path. Backup includes database and separately escrowed encryption
key material. Restore into an isolated installation and verify data plus secret
decryption before declaring a backup usable.

Before restored state is exposed, rotate its installation credential epoch to
invalidate restored sessions, tokens and pending challenges. Reconcile administrator
access through offline recovery and issue replacement machine credentials. Stop the
old backend before activation; fence all restored work generations and wait the full
maximum offline execution lifetime from confirmed old-backend shutdown before normal
agent takeover. A backup's lease timestamps alone may be stale. An explicit forced
activation may acknowledge possible old-agent query overlap, but cannot accept their
old writes. Restore never silently revives credentials revoked after the snapshot.

## Acceptance criteria

- AC-01: Concurrent duplicate writes contribute once; different values conflict.
- AC-02: Crash between transaction commit and response preserves replay safety.
- AC-03: Definition edits, stale calculations and concurrent metadata updates do
  not rewrite or relabel accepted historical observations.
- AC-04: Retention expiry returns explicit unavailable coverage and cannot permit
  expired retries to reinsert purged history silently.
- AC-05: Historical saturation and partition maintenance preserve reserved live
  connection capacity and bounded transaction waits.
- AC-06: Backup/restore recovers identities, definitions, observations, outstanding
  work and decryptable credentials; stale worker claims are fenced. Restore may
  repeat an external notification already sent after the backup snapshot, with the
  same event/delivery identity. Do not claim exactly-once external side effects.

## Review notes and evidence

- **REVIEW-S01:** 400-day canonical retention favors correctness over disk cost.
  Confirm the desired history and deployment scale; reduce the default if needed,
  without claiming arbitrary long reports from lossy rollups.
- **REVIEW-S02:** Decimal count precision and seven-day delivery replay horizon are
  proposed wire/storage limits. Confirm before schema/code generation.
- **REVIEW-S03:** UUID identity and next-common-boundary definition edits are proposed
  implementation choices, not inherited requirements.
- [F1-T05](../tasks/F1-T05.md) implements explicit schema upgrades, immutable
  migration history and installation metadata, with real PostgreSQL transaction,
  concurrency and cancellation checks. Observation persistence, precision,
  partition boundaries, retention races, restore and live/historical isolation
  remain unimplemented and unverified.

## Maintenance and annotation records

Store the versioned exclusions, annotation revisions/tombstones and external event-ID
uniqueness required by [SPEC-0016](0016-maintenance-exclusions.md) and
[SPEC-0017](0017-slo-timeline-annotations.md). Retain their audit/history for at least
400 days and preserve exclusion provenance needed by retained measurement/report
evidence. Exclusion revision changes atomically invalidate dependent calculations;
annotation changes never invalidate measurement math. Apply the same snapshot,
transaction and bounded retention discipline as other persistent records.
