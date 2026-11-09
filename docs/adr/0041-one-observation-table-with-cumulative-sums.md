# ADR-0041: One observation table with explicit marking

- Status: proposed
- Created: 2026-11-09
- Accepted: pending
- Approval: pending
- Related specs: [SPEC-0020](../specs/0020-cumulative-slo-calculation.md), [SPEC-0007](../specs/0007-storage-and-retention.md), [SPEC-0003](../specs/0003-ingestion-and-recovery.md)
- Supersedes / superseded by: resolves open questions 1, 2 and 3 of [ADR-0034](0034-self-healing-derived-data.md); amends the two-table layout of [SPEC-0007](../specs/0007-storage-and-retention.md). Independent of [ADR-0040](0040-cumulative-sums-and-compact-extrema.md); the file name keeps the first draft's title for link stability.

## Context

Observations are stored as an interval record in `almanac_sli_coverage` and, for
observed intervals, a values row in `almanac_sli_observation_values`. Two triggers
on the coverage table advance the source generation, mark the dirty hour and rewind
the series repair cursor. ADR-0034 documents the incident this layout caused and
lists merging the tables, explicit marking and dropping the stored interval end as
open questions. The 2026-11-09 review added: the primary key `(interval_start,
sli_id, measurement_revision)` is never the access path, so each table carries a
second index on `(sli_id, interval_start)`; every aggregation joins the two tables;
and the per-row provenance is about half of each coverage row.

The first draft of this record also added cumulative sums to the row. ADR-0040 no
longer needs them, so they are withdrawn; this record is now a storage tidy-up
that can land independently, before or after ADR-0040.

## Decision

Proposal. One partitioned table `almanac_sli_observations` with primary key
`(sli_id, measurement_revision, interval_start)`, monthly range partitions on
`interval_start`, nullable `good_count`/`total_count` constrained to be present
exactly when the outcome is `observed`, no stored interval end and no separate
observation identifier. One partial index on observed rows supports first/last
observation lookups. Provenance columns stay on the row; moving them to receipts
would change receipt retention in SPEC-0007 and is a separate decision.

The storage function that accepts an observation advances the source generation,
marks the dirty hour and widens the affected SLOs' extrema cursors (or, before
ADR-0040 lands, rewinds the series repair cursor) in the acceptance transaction.
There are no ingestion triggers. Duplicate and conflicting deliveries change
nothing, as today.

## Alternatives considered

- Keep two tables: the join and the second index remain, and a row without its
  values stays representable. Rejected.
- Keep the triggers: they guarantee marking for every write path, but there is one
  write path and the hidden logic already caused one incident. Rejected.

## Consequences

- Two fewer indexes and one fewer table on the largest data; every aggregation
  reads one table. Raw storage per SLI-year falls by roughly a third; F9-T06
  measures it.
- The migration copies coverage and values into the new table partition by
  partition, verifies counts and values, then drops the old tables and triggers.
  It must be resumable and preserve the maintainer's data.
- Retention drops whole partitions as today.

## Revisit conditions

Revisit if a second write path appears that bypasses the storage function, or if
provenance per interval proves unnecessary beyond the replay window.
