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
exactly when the outcome is `observed`, and no stored interval end. The
observation identifier moves to the delivery receipt, which is retained for the
life of its observation; the SLI list, observation history and duplicate
responses read it from there. One partial index on observed rows supports
first/last observation lookups, and an all-outcome index on `(sli_id,
interval_start)` remains for the SLI list's latest-interval lookup across
measurement revisions; drop it only if a query plan on the merged tree shows
the primary key serves that query without sorting the SLI's history. Provenance
columns stay on the row; moving them to receipts would change receipt retention
in SPEC-0007 and is a separate decision.

Scope: cadence-bound observations, the only kind ingestion accepts today.
SPEC-0003's explicitly irregular external producers are not implemented; when
they are, they need their own interval representation, since no cadence can
reconstruct their ends.

The storage function that accepts an observation advances the source generation,
marks the dirty hour and updates every live history pipeline in the acceptance
transaction: extrema cursor widening and legacy series-repair rewinding while
both exist, only the remaining cursor after cutover. The selected F9 delivery
order lands this change after F9-T03 removes the legacy pipeline; another order
must preserve both effects during overlap and adapt F9-T02's invalidation path.
There are no ingestion triggers. Duplicate and conflicting deliveries change no
canonical row and mark no derived work; a duplicate with a new delivery ID still
records its receipt, as today.

## Alternatives considered

- Keep two tables: the join and the second index remain, and a row without its
  values stays representable. Rejected.
- Keep the triggers: they guarantee marking for every write path, but there is one
  write path and the hidden logic already caused one incident. Rejected.

## Consequences

- Two fewer indexes and one fewer table on the largest data; every aggregation
  reads one table. Raw storage per SLI-year falls by roughly a third; F9-T06
  measures it.
- The upgrade runs in stages because the migration runner commits each SQL
  file with its history record in one transaction: one migration creates the
  table and fences legacy writes; the copy then runs partition by partition in
  bounded committed batches that verify counts and values and can resume after
  interruption; a final step drops the old tables and triggers only after
  verification. It must preserve the maintainer's data.
- This is an explicit maintenance upgrade under SPEC-0013: back up, stop and
  drain the backend, run `almanac db upgrade`, then start the compatible backend.
  Ingestion is unavailable during the copy. Interrupted upgrades keep the legacy
  write fence and source tables; rerun the same upgrade to resume before starting
  the backend. Agent buffers and execution leases are finite, so a long outage
  can leave collection gaps; report them as missing and use best-effort repair
  under SPEC-0003. The upgrade does not guarantee continuous collection.
- Retention drops whole partitions as today.

## Revisit conditions

Revisit if a second write path appears that bypasses the storage function, or if
provenance per interval proves unnecessary beyond the replay window.
