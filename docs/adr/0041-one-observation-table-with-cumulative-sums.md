# ADR-0041: One observation table with explicit marking and cumulative sums

- Status: proposed
- Created: 2026-11-09
- Accepted: pending
- Approval: pending
- Related specs: [SPEC-0020](../specs/0020-cumulative-slo-calculation.md), [SPEC-0007](../specs/0007-storage-and-retention.md), [SPEC-0003](../specs/0003-ingestion-and-recovery.md)
- Supersedes / superseded by: resolves open questions 1, 2 and 3 of [ADR-0034](0034-self-healing-derived-data.md); amends the two-table layout of [SPEC-0007](../specs/0007-storage-and-retention.md)

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

ADR-0040 needs a place for cumulative sums. They belong on the observation row,
which makes this the moment to settle the layout.

## Decision

Proposal. One partitioned table `almanac_sli_observations` with primary key
`(sli_id, measurement_revision, interval_start)`, monthly range partitions on
`interval_start`, nullable `good_count`/`total_count` constrained to be present
exactly when the outcome is `observed`, no stored interval end and no separate
observation identifier. Cumulative columns `cum_good`, `cum_total`, `cum_observed`,
`cum_known_empty` and `cum_zero_traffic` hold running totals since the revision
start and are all null until the cumulative worker has processed the row. One
partial index on observed rows supports first/last-observation lookups.

The storage function that accepts an observation advances the source generation,
lowers the revision's `cumulative_through` to the new interval start and marks the
affected SLOs' extrema cursors, in the acceptance transaction. There are no
triggers. Duplicate and conflicting deliveries change nothing, as today.

Provenance columns (`execution_id`, `delivery_id`, `agent_id`, `session_generation`,
`configuration_revision`, `lease_id`, `agent_created_at`, `result_digest`) stay on
the row for now; moving them to receipts is a separate decision because it changes
the receipt retention in SPEC-0007.

## Alternatives considered

- Keep two tables and add cumulative columns to the values table: observed-only
  rows cannot carry counts of known-empty intervals, so cumulative counts would
  need the join anyway. Rejected.
- Keep the triggers: they guarantee marking for every write path, but there is one
  write path and the hidden logic already caused one incident. Rejected.
- A separate cumulative table keyed like the observations: doubles the row count
  for no isolation benefit. Rejected.

## Consequences

- Two fewer indexes and one fewer table on the largest data; every window read is
  one table. Raw storage per SLI-year falls by roughly a third after the cumulative
  columns are added.
- The migration copies existing coverage and values into the new table partition
  by partition, computes initial cumulative sums, then drops the old tables and
  triggers. It must run on the maintainer's preview without losing observations
  and must be resumable.
- Retention drops whole partitions as today. Before a partition is dropped the
  cumulative values at its upper boundary are stored on the revision row as the
  retained floor so later windows keep their exact totals.

## Revisit conditions

Revisit if a second write path appears that bypasses the storage function, or if
provenance per interval proves unnecessary beyond the replay window.
