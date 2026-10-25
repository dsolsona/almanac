# ADR-0007: PostgreSQL and storage boundaries

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related spec: [SPEC-0007](../specs/0007-storage-and-retention.md)

## Context and decision

PostgreSQL is an established product requirement. Propose using it for metadata,
canonical measurements, durable jobs, alert state, outbox and audit, accessed only
through backend application services. Use plain SQL, `pgx/v5`, generated `sqlc`
queries, and ordered SQL schema changes applied by an explicit administrative
command. Domain math has no SQL or network dependencies.

Retain canonical measurements initially instead of depending on lossy rollups.
Use partitioned tables for bounded retention and separate database admission budgets
for live, interactive and historical work. Cache results only with revision evidence
and an invalidation/recomputation path. Partition uniqueness constraints must include
the time partition key as described by [PostgreSQL](https://www.postgresql.org/docs/current/ddl-partitioning.html).

Schema changes are append-only after release, serialized by a database lock and
recorded in a version table. Serving processes never race to alter schemas at boot.
Use expand/contract changes when compatibility with running processes is required;
readiness fails for unsupported schema versions. No automatic destructive rollback.

## Alternatives and consequences

- A separate queue or time-series database adds failure and backup boundaries before
  workload evidence requires it; defer those dependencies.
- An ORM could reduce CRUD typing but obscures the explicit observation, conflict,
  lease, and partition operations; generated typed SQL is the proposed fit.
- Raw-plus-hourly-only long retention reduces storage but cannot preserve arbitrary
  threshold and timeslice semantics. Canonical retention avoids that correctness risk.

The operational price is PostgreSQL capacity planning, index maintenance and restore
practice. `sqlc` and `pgx` versions will be pinned at scaffolding, not floated. Generated
files are regenerated, never edited. Numeric types, horizons and row keys follow
SPEC-0007 after its review.

## Revisit and review

Revisit after measured throughput, retention cost or recovery needs exceed the
validated single-database envelope. **REVIEW-S01–S03** in SPEC-0007 capture the
provisional storage choices. No workload benchmark has been run.
