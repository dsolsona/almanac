# ADR-0052: Compile scoped maintenance populations for the shared calculator

- Status: proposed
- Created: 2026-11-14
- Accepted: pending
- Approval: pending; design candidate only, no production adoption
- Related specs: [SPEC-0016](../specs/0016-maintenance-exclusions.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0020](../specs/0020-cumulative-slo-calculation.md), [SPEC-0021](../specs/0021-alert-policy-engine.md)
- Supersedes / superseded by: proposes extending ADR-0040's shared count path for scoped exclusion populations; no observation-cumulative path or dirty-hour fallback is adopted

## Context

The persisted maintenance authority exists, while production calculations still
use exclusion revision zero. An arbitrary number of declarations can overlap or
fragment a window. Turning every mask boundary into a raw edge can exceed the
existing work budget forever. Rebuilding all retained extrema after each global
exclusion revision would also make unaffected ranges wait.

Alert policy reads must use the same mathematics and exclusion authority as SLO
current/history/report reads. Full source windows, complete quality statistics,
same-facts recovery comparison and preserved original evidence remain required.
This candidate supplies a bounded preparation path rather than another alert-only
formula. It is separate from the failed dirty-interior fallback experiment.

## Decision

Propose two maintained components and one shared totals interface.

1. **Compile immutable exclusion snapshots.** Maintain versioned SLO membership
   intervals with immutable bounds and inclusion-revision lifetimes. A job for
   authority revision R reads raw membership-version pages in indexed start/key
   order, filters validity at R, and unions intervals online with a persisted
   frontier. Later mutations cannot move an R-visible row's bounds or remove it.
   Publish only a complete snapshot. Keep in-progress work unavailable to readers.
   Declarations may be numerous, but every turn visits a bounded raw page and
   finite quiescent input eventually completes; a legitimate fragmented mask is
   not permanently rejected because one request cannot scan it.

2. **Index coverage in that immutable snapshot.** Disjoint union intervals store
   cumulative excluded duration at their endpoints. Two predecessor seeks plus
   clipped edges give excluded duration in an arbitrary window; a predecessor
   also gives the latest eligible endpoint. These are immutable configuration
   summaries, not mutable observation cumulative sums. They do not depend on raw
   observation retention, an implicit baseline or source-generation changes.
   Keep a referenced snapshot and its provenance for as long as evidence needs it.

3. **Prepare exact partial-hour populations.** Fully eligible physical source
   hours reuse canonical SLI aggregates. Fully excluded hours contribute no
   eligible observations; unknown excluded event counts remain unknown. A partly
   masked hour gets a maintained SLO-scoped summary built from canonical interval
   rows, carrying eligible and excluded counts, observed/empty/zero counts,
   first/last observation and receipt metadata. Assign intervals by their start
   hour, as the canonical source does. At one-second minimum cadence a physical
   hour has at most 3,600 canonical starts. Read the bounded intersecting disjoint
   mask fragments, merge in order, and apply existing exclusion arithmetic.
   Coarser/non-hour-dividing grids use their actual aligned interval starts;
   do not split or prorate a crossing interval.

4. **Use sufficient coverage statistics.** Extend the shared totals evaluator to
   consume exact excluded eligible/retained interval counts and latest eligible
   endpoint, alongside the existing excluded numeric population. Keep the raw
   interval-mask evaluator as an independent oracle. Do not pass millions of
   fragments into every alert/current read. Validate aligned integer counts and
   distinguish fully excluded, zero traffic, missing eligible data and unknown
   excluded event totals. All current/history/alert consumers call this shared
   path; no alert-specific subtraction or quality rule is introduced.

## Publication and compatibility

Mask preparation depends on maintenance revision, not observation generation.
An adjusted hour additionally carries its exact source-hour generation. Under
brief existing SLI/SLO/exclusion authority locks, reject a relevant source-hour
or mask change before publication. Unrelated live observations must not restart
an old hour's multi-turn mask compilation. A complete window still reads one
repeatable-read snapshot and receives the global source/exclusion identity that
its normal final publication fence validates.

Persist disjoint conservative changed-input ranges per authority mutation: old/
new coverage differences and scope additions/removals, excluding metadata-only
changes. Keep distant ranges separate; the work queue's broad coalesced envelope
is not a compatibility proof. A stored hour/extrema input may be reused under a
new requested revision only after an indexed absence proof that no intervening
changed-input range intersects its complete source window. Preserve its actual
computed revision and the new proof identity. Never relabel arbitrary old values
as current merely because a revision counter advanced.

The proof requires an indexed interval/revision strategy measured against the
declared history, not an unbounded revision-chain walk or an earlier-start scan.
Prototype PostgreSQL range indexing plus the existing SLO/revision indexes before
choosing additional extensions. If preparation/admission cannot prove compatibility,
keep original evidence labelled with its original identity and enqueue bounded
preparation; do not return invented current values. Future exclusions and narrow
edits must leave distant ranges immediately usable in the acceptance fixtures.

An overlapping declaration can change provenance without changing the effective
union. Metadata-only changes are known directly; other unchanged-coverage cases
need an exact union comparison/certificate, not `coverage_changed` alone. Measure
this case explicitly. Do not invalidate an entire retained year to discover that
its mathematics is unchanged.

On a maintenance-related recovery, prepare both the current snapshot and the
last-breaching snapshot against the same present source facts. Both populations
count against admission. The alert retains its revision and acknowledgement;
only the resulting evidence determines ordinary recovery versus
`resolved_after_maintenance_change`.

## Alternatives considered

- Raw reads for each fragment are simpler initially but cannot finish some valid
  masks within one request budget and multiply repeated policy work.
- One per-SLO cumulative observation sequence would add another mutable numeric
  authority and repeat the retention/late-data problems rejected by ADR-0040.
- Rebuilding every retained mark for each global exclusion revision loses
  unaffected-range availability. Revision equality alone is not compatibility.
- Alert-only masks leave current status/history and alert decisions disagreeing.

## Consequences

Preparation is additional persisted work and may cause explicit processing lag;
normal navigation must still return usable retained rows quickly with honest
readiness. Raw work per adjusted hour and membership-compile turn is bounded.
Read work is independent of declaration overlap after compilation, while long
windows read bounded hourly summaries rather than full raw populations.

The candidate needs new schema/query/worker code and an accepted SPEC-0020 scope
amendment before production adoption. Numerical admission/compile batch defaults,
interval-index choice and retained physical size remain experiment outputs.
Existing pool sizes, source failure rules and performance targets remain in force.
No public maintenance-management endpoint or unsupported SLO method is implied.

## Revisit conditions

Reject or change this candidate if fragmented/overlapping cases cannot finish with
bounded work, compatibility checks scan growing revision history, membership
compilation restarts under unrelated writes, exact quality/provenance diverges
from the raw oracle, or the 100/200-policy workload harms ingestion/navigation.
