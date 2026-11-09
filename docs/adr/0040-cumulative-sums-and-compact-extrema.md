# ADR-0040: Serve SLO history from cumulative sums and compact extrema

- Status: proposed
- Created: 2026-11-09
- Accepted: pending
- Approval: pending
- Related specs: [SPEC-0020](../specs/0020-cumulative-slo-calculation.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0007](../specs/0007-storage-and-retention.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: proposes to supersede the retained point/tile series of [ADR-0025](0025-fast-operational-journeys-and-tracing.md), history jobs and display blocks of [ADR-0024](0024-precomputed-operational-reads.md), while retaining exact hourly aggregates and dirty-hour work under [ADR-0034](0034-self-healing-derived-data.md). Depends on [ADR-0041](0041-one-observation-table-with-cumulative-sums.md).

## Context

The 2026-11-09 review of the SLI/SLO flows found the arithmetic exact and the
derived-data layer expensive. One retained series point measures 3,633 bytes of
JSON (`internal/storage/retained_series.go`, measured with a throwaway test at
`259f7c1`). Per SLO at a 60 s cadence this is about 1.9 GB of points per year,
plus hourly tiles that store every sample of the hour twice (payload and `fine_wire`),
about 3 GB more. The raw observations those values derive from are about 170 MB.
The UI reads eight of the roughly sixty fields in each sample.

Keeping those copies coherent needs five queues (dirty hours, current projections,
series repair, tile work, history jobs), three one-second loops and the recovery
rules of ADR-0034. In steady state every ingested observation rewinds the repair
cursor, republishes the point, rebuilds the open hour's tile (about 340 KB) and
queues a seven-day sparkline job. That is roughly 20 MB of rewritten JSON per SLO
per hour, before any user opens a page. These figures are derived from the measured
point and the schema; the local Postgres was not running and the dev database is
empty, so they were not confirmed on a populated installation.

What the reads need is small: for any cadence-aligned window, its exact sums and
interval counts; for any display bucket, the first, last, minimum and maximum
endpoint and the quality states present. ADR-0024 chose hourly aggregates with
bounded raw edges for the first and tiles for the second. Both are correct; both
are larger and more churn-prone than necessary.

## Decision

Proposal. Replace bulk retained points and tiles with cumulative inputs and compact
extrema, retaining the evidence and fallback required for correctness:

1. **Cumulative sums on observations** (ADR-0041). Trusted window boundaries use
   two prefix lookups. A late observation invalidates the suffix; one cursor per
   SLI revision repairs it in bounded, generation-fenced chunks. Small unprocessed
   windows use exact raw sums, with distinct cases before/across/after the cursor.
   Larger windows use the existing exact hourly aggregates and bounded raw edges.
   Dirty-hour work stays independently current, so old backfill cannot gate an
   unaffected current window behind a revision-wide cumulative repair.
2. **Hourly and daily extrema** per SLO. Each retained mark includes exact counts,
   first/last observation and receipt times, and the original retained-input
   boundary. Both sides of retained quality transitions carry complete marks,
   deduplicated with first/last/min/max; the existing reducer and presence-set
   fallback apply. Offsets use elapsed seconds from the UTC bucket, preserving
   canonical endpoints for cadences that do not divide an hour/day or exceed an
   hour. Daily compliance stores its actual cadence endpoint and UTC day-end.
   One cursor per SLO tracks affected hours and days.
3. **Historical prefix evidence.** Per-SLI cumulative checkpoints retain the
   lookback before the canonical floor needed to rebuild any retained endpoint.
   They are derived evidence, not queryable canonical observations. For inputs
   already expired at upgrade, compact per-endpoint baselines preserve existing
   retained totals and temporal evidence; repair adds only generation-fenced late
   deltas, as `preserveRetainedPrefixes` does today. SPEC-0020 defines capture,
   pruning and migration coverage. Four extrema and one floor cannot replace it.

Reads compose closed hours/days from extrema, partial days from hourly rows and
bounded open edges from exact inputs. Sample values are evaluated from complete
marks and their historical evidence boundary. Nothing is averaged, rounded or
reconstructed from plotted values. F9-T06 measures lookup/fallback cost; there is
no fixed eight-lookup promise during repair.

Remove after verified replacement: `almanac_slo_series_points`,
`almanac_slo_series_tiles`, `almanac_slo_series_tile_work`,
`almanac_slo_series_repair`, `almanac_slo_display_blocks`,
`almanac_slo_history_jobs` and the coverage ingestion triggers, with obsolete code.
During T02/T03 explicit ingestion still marks dirty hours and rewinds legacy
repair in the same acceptance transaction. Do not drop old point tables before
all required historical baselines are verified.

Keep exact hourly aggregates and dirty-hour work, prefix checkpoints/baselines,
current projections/generations/failures, the pinned detail payload and its byte
accounting (five-minute lifetime), and overview snapshots unless ADR-0042 is
accepted. There are two new cursors plus retained dirty-hour work, not a
two-cursor-only pipeline.

Also proposed:

- The open hour is evaluated live. Before this ADR is implemented, defer hourly
  tile work until the hour closes, as daily work already is. T01's equivalence
  tests must prove the retained reader still supplies the same values.
- The seven-day sparkline uses hourly extrema, refreshing its last bucket at each
  cadence tick and rebuilding covered hours after corrections.
- Retained payloads no longer re-hash on read except pinned exact-value payloads.
  Generation fences remain; PostgreSQL checksums do not replace semantic evidence
  or the retained pinned-cache byte budget.
- Workers wake on due time within existing polling/latency bounds.
- Remove verified production-dead fallback/sweep code, display blocks and
  detail-kind history jobs. Keep numerical helpers used by retained/new readers.

## Alternatives considered

- Keep verbose points and tiles: preserves behavior but retains the measured
  duplication and write amplification. Replace only after full equivalence.
- Gate every read on one cumulative cursor and remove hourly aggregates: rejected
  after review. One old insertion invalidates later prefixes even for unaffected
  current windows. Reuse the exact hourly fallback instead of introducing another
  cumulative coordinate model or a correction journal.
- Keep four extrema and one revision floor without historical prefix evidence:
  rejected after review. Once older raw inputs expire they cannot reproduce every
  candidate endpoint needed to reselect extrema after late data.
- Recompute every cadence endpoint on read: a year needs 525,600 evaluations per
  lane at 60 s. Rejected; maintained extrema keep ordinary graph reads bounded.
- Another storage engine: no measured justification; ADR-0025's reasoning holds.

## Consequences

- The original 1 KB/extrema-row and 9 MB/SLO-year estimates omitted temporal
  fields, transitions, prefix evidence, indexes and fallback storage. They are
  withdrawn as total-size claims. F9-T06 must establish actual savings, including
  the temporary migration baseline cache. Prefix size scales with cadence and
  applicable historical lookback; canonical retention remains 365 days.
- Forward cumulative repair remains bounded and resumable, and can be expensive
  for very old data. Independent hourly fallback preserves current reads and
  current-first admission while it catches up. Ordinary navigation still cannot
  wait for queued preparation.
- The proposed last-interval receipt shortcut is withdrawn. Keep the existing
  maximum receipt watermark, including upgrade baselines whose older raw receipt
  inputs have expired. Hourly temporal summaries and bounded edges supply it;
  counts alone cannot establish this metadata.
- SPEC-0019 AC-12 and SPEC-0007's required persisted-point representation become
  extrema/cumulative/prefix contracts. A year of exact history, gap visibility,
  pinned values, restart survival and late correction remain requirements.
- Migration preserves canonical observations and historical evidence. Verify
  values, temporal fields and provenance as well as counts before destructive
  cutover. Provisional migrations follow existing main migration 0044: T02 0045,
  T03 0046, T04 0047, optional T05 0048. Recheck contiguous numbering at each task.

## Review corrections

The 2026-11-09 revision corrects trusted cumulative bounds, complete marks and
transitions, historical prefix preservation, staged legacy invalidation,
unaffected-range availability, arbitrary-cadence offsets and migration numbering.
These decisions remain proposed; authorization to revise the documents is not
acceptance or authorization to implement them.

## Revisit conditions

Revisit if the extrema worker cannot close an hour for every SLO within the
currentness budget at the 1,000-SLO fixture, if late-data recomputation measurably
competes with live ingestion, or if a method other than occurrences needs a
sufficient statistic that cumulative sums cannot provide.
