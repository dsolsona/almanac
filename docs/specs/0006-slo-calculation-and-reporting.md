# SPEC-0006: SLO calculation and reporting

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: not started
- Related ADRs: [ADR-0005](../adr/0005-measurement-semantics.md), [ADR-0006](../adr/0006-openslo-compatibility.md)
- Plan: none
- Supersedes / superseded by: none

## Accepted amendment

[SPEC-0016](0016-maintenance-exclusions.md) adds initial-release maintenance
exclusions and audited revisions, approved in Crit review `0de0c0e42db6`.
The calculation and acceptance sections below include that amendment.

On 2026-10-29 the maintainer authorized the operational SLO view plan under
[ADR-0023](../adr/0023-slo-operational-urgency.md). It adds backend-owned urgency
independent of performance/quality, distinct 1 h/6 h burn windows, historical rolling
budget evaluations, UTC day-end rolling-objective compliance and an explicitly
qualified frozen-budget exhaustion estimate. The stationarity assumptions required
below remain mandatory; this is not a forecast of actual rolling-window breach.
The [F3 plan](../plans/0021-first-objective.md) tracks delivery; none of these new
views is established as implemented by acceptance of the decision.

The compliance graph requests the ordinary rolling-objective evaluation at each
completed UTC day end. It exposes both that requested midnight and the existing
cadence-aligned effective evaluation boundary at or before it; local display does
not change day membership or calculation bounds. Plot the actual rolling attainment
percentage against the objective target; report the day's classification separately.
Met/violated days require complete,
unshortened, evaluable windows. Unknown and not-applicable days remain explicit;
the evaluable-day denominator contains met plus violated days. This graph is
distinct from the local-date daily report/export grouping below.

## Purpose

Give operators reproducible attainment, error budgets, burn rates, and daily reports
whose uncertainty is visible. Calculations consume PostgreSQL observations, never
query external sources, and take evaluation time explicitly as an input.

## Scope and non-goals

Include occurrences, timeslices, ratio timeslices, multi-objective SLOs, supported
composites, rolling/calendar windows, CSV/JSON export, and maintenance exclusions
under [SPEC-0016](0016-maintenance-exclusions.md). Notification silences never exclude
observations from calculations. Retrospective maintenance edits use the audited
administrator workflow; retrospective observation/query corrections remain excluded.
Exclude prediction of future traffic, automatic target
selection, arbitrary composite expressions, and PDF/email report delivery initially.
These are proposed contracts; no implementation is implied.

## Common measurement contract

Use half-open intervals `[start,end)`, UTC instants at microsecond precision, and
canonical cadence/alignment from effective SLI definitions. A report includes only
whole intervals fully contained in its bounds. Explicit custom bounds must align
with all participating cadences or fail with nearest valid bounds; never prorate
counts or scalar samples. Rolling evaluations round their end down to the common
interval boundary and disclose requested time and effective end. Calendar boundaries
must also align; incompatible definitions stay inactive.

Count input follows [SPEC-0007](0007-storage-and-retention.md): exact decimal strings
representable in PostgreSQL `numeric(38,12)` (at most 26 integer and 12 fractional digits);
ingestion rejects excess precision or range instead of rounding. Provider adapters
may perform explicitly declared source normalization before submission: fractional
count estimates are rounded half-even to 12 fractional digits and carry estimated
provenance and the normalization rule. This is part of the measurement definition,
not a storage-side correction. Validate finite values, range, and `0 <= good <= total`
after conversion; inconsistent results remain errors and are never clamped. Exact
count mode cannot silently become estimated mode. Count sums retain exact decimal
arithmetic beyond individual-field width or fail explicitly on implementation limits.
Targets and weights use decimal input within the same precision/scale limits. Scalar
threshold/ratio values use finite IEEE binary64, normalize negative zero to zero,
and preserve their round-trip representation. No NaN/infinity is valid. Threshold
comparisons use the stored binary64 values directly, with no hidden epsilon.

Use exact rational intermediate arithmetic for divisions, means, normalized burns,
and weighted sums; do not round intermediate results. In Go, arbitrary-precision
integer/rational arithmetic is an implementation option without a decimal dependency.
For scalar calculations, convert each stored finite binary64 value to its exact
rational value first, preserving the represented input rather than inventing extra
measurement precision. Serialize derived values as decimal strings with 12 fractional
digits, rounded half-even only at the output boundary. Status comparisons use exact
unrounded quantities (cross multiplication for decimal fractions).
Display rounding never changes status. Missing numerical values are JSON null and
empty CSV fields accompanied by a reason, never zero or NaN. A failure raw ratio
becomes success `1-r` for evaluation; preserve original input and interpretation.

## Objective formulas

Let target `T` satisfy `0 <= T < 1`, and permitted error fraction `e = 1-T`.
A zero target is valid: `e=1`, all evaluable nonnegative attainment meets the target,
and budget/burn arithmetic remains defined. A target of 100% is rejected; it would
make normalized burn undefined. This follows
the published target domain. [OpenSLO SLO reference](https://openslo.com/schema/v1/slo/)

| Method | Eligible input | Attainment `A` and denominator `D` |
| --- | --- | --- |
| Occurrences | Counts | `G=sum(good)`, `D=sum(total)`, `A=G/D` |
| Timeslices | Counts, raw ratios, threshold values | `D=number of evaluable slices`, `A=goodSlices/D` |
| RatioTimeslices | Counts, raw ratios, threshold values | `D=number of evaluable slices`, `A=sum(sliceSuccess)/D` |

For counts, each slice success is `good/total`; a zero denominator is zero traffic,
not an evaluable ratio. For raw ratios use the normalized success value. For a
threshold sample, success is 1 if its declared `lt/lte/gt/gte` comparison passes,
otherwise 0. Initial slice size equals one canonical measurement interval; do not
average percentile measurements or invent subinterval samples. Timeslices compares
slice success to `timeSliceTarget` with inclusive `>=`; threshold predicates are
applied first. Raw ratios and threshold values cannot use occurrence budgeting.

For an evaluable objective: bad quantity `B=D*(1-A)`, allowed budget `E=e*D`,
remaining budget `R=E-B`, consumed fraction `C=B/E`, remaining fraction `1-C`, and
normalized burn `b=(1-A)/e`. Counts use event units; Timeslices uses slices;
RatioTimeslices uses equivalent bad slices, which may be fractional. Negative
remaining budget and consumption over 100% are retained. Do not clamp them.
For any shorter burn window, recompute that method's `A` over that window with the
same target. Burn is dimensionless; no traffic or unknown input yields null burn.
The normalization matches the established error-budget burn definition.
[Google SRE workbook: alerting on SLOs](https://sre.google/workbook/alerting-on-slos/)

Budget in an unfinished calendar period is the budget for observed elapsed data,
not an estimate of the eventual full-period event count. Do not claim projected
exhaustion time from event burn without an explicit traffic-stationarity model.
A standard SLO with several objectives exposes separate results and an overall
`met` only if every objective is met with complete data; it has no invented shared
budget. Overall violation is visible alongside any independently unknown objectives.

## Missingness and eligibility

Return independent data-quality and performance fields. Quality includes complete,
partial, no-data, zero-traffic, excluded, and expired-history; freshness is separate.
A stale latest interval is not made current by a healthy agent heartbeat.

Report expected eligible intervals, accepted observation intervals, known-empty
intervals, missing intervals, excluded intervals, zero-traffic intervals, first/last
observation time, and receipt watermark. Coverage is accepted-or-known-empty
intervals divided by expected eligible intervals; it measures collection coverage,
not traffic or success. A known-empty query avoids repair but contributes no ratio.
Distinguish counts of known-empty versus zero-traffic even though neither supplies
a slice success. Report expired intervals separately from never-received intervals.

An occurrence report may show provisional arithmetic over observed positive traffic
while marking incomplete coverage; it cannot declare the full window met. Complete
observations containing some 0/0 intervals are legitimate complete coverage; their
zero counts add nothing. All-zero counts give zero-traffic and null attainment/burn.
For slice methods, known-empty, missing, and zero-traffic count intervals make the
full-window result unknown; expose an observed-only provisional result separately.
Raw ratio 0 is observed total failure, and raw ratio 1 is observed success; neither
implies an event count. An irregular occurrence SLI reports coverage as unknown,
so attainment is explicitly observed-only and cannot prove complete-window success.

For complete evaluable data, performance is met when `A >= T`, violated otherwise.
For incomplete data, report performance unknown plus provisional comparison. This
avoids claiming a window violation or success from a biased missing denominator.
Alerts must explicitly consume quality and freshness, rather than treating a null
value or absence of a firing result as recovery.

## Composite formulas and boundaries

The upstream prose describes additive weighted burn for occurrences, logical union
of failed slices for Timeslices, and additive weighted deficits for RatioTimeslices.
Almanac proposes the following explicit interpretation, including limits where the
source leaves implementation choices. [OpenSLO specification](https://openslo.com/specification/)

All children use one budgeting method and the same report window. Weights are finite
positive decimals, default 1. Every required child must be evaluable with complete
coverage for a definitive composite result; never drop a missing child or renormalize
remaining weights. Return each child's result regardless of composite availability.

- Occurrences: child `b_i=(1-A_i)/(1-T_i)`; composite consumption/burn
  `C=sum(w_i*b_i)`, remaining fraction `1-C`, met if `C<=1`. Different child targets,
  traffic volumes, sources, and agents are supported. This is normalized budget
  accounting, not a probability of end-to-end request success. Composite event counts,
  attainment, and event-unit budget are null because denominators are not comparable.
- Timeslices: require common cadence/alignment, a common outer target `T`, and unit
  weights. Child slice predicates may differ. A composite slice is good iff every
  child's slice is good. Apply single-objective Timeslices formulas to those slices.
  Nonunit weights or different outer targets import inactive with explicit reasons;
  no probabilistic interpretation of a weighted binary failure is invented.
- RatioTimeslices: require common cadence/alignment and common target `T`. For slice
  `j`, deficit `d_j=sum(w_i*(1-r_ij))`. Composite deficit `d=mean(d_j)`; budget
  consumption/burn `C=d/(1-T)`, remaining fraction `1-C`, met if `C<=1`.
  Equivalent good ratio `1-d` may be negative; label it a composite score, never a
  probability or ordinary attainment percentage. Preserve unbounded deficit rather
  than cap it and hide overspend. Differing targets import inactive initially.

These restrictions and formulas are review assumptions, not universal OpenSLO
conformance claims. In particular, pooled event counts, arithmetic averaging of
child burn, and maximum child burn are different products and are not substituted.

## Windows, SLO exclusions, and revisions

Rolling windows accept positive fixed minutes/hours/days/weeks; days mean 24 hours.
Rolling `M/Q/Y` durations import inactive rather than guessing variable duration.
Calendar windows use an IANA zone and explicit local anchor. Accept whole-day or
whole-week steps and month/quarter/year steps anchored to day 1 at midnight.
Other valid anchors import inactive initially. Weeks advance seven civil dates;
months advance calendar months; quarters three months; years twelve months. Daily
windows span local midnight to midnight and therefore may be 23 or 25 hours.
For a nonexistent civil midnight use the first valid instant of that date; for an
ambiguous midnight use its earlier occurrence. Persist resolved UTC boundaries and
time-zone-data version in report metadata. No silent fixed-30-day month conversion.

Current calendar reporting uses `[periodStart,effectiveNow)` and identifies the full
period end separately. Future intervals are not missing. The first SLI revision may
have an explicitly authorized historical `measurement-valid-from` earlier than live
activation, and an initial SLO may have a separately requested `evaluation-valid-from`
under its chosen target, as specified in SPEC-0005. Evaluate only their overlapping
validity range. Reports identify that retrospective initial-definition application,
its requesting actor/range, and operational activation separately; it is not evidence
of a past operational reliability commitment. Before the selected validity bounds,
report not-applicable rather than missing or invented history. A report
crossing SLI/SLO effective revisions returns chronological segments with their own
targets, methods, and coverage; the initial release does not combine segment budgets
into one allegedly comparable total. Short burn windows crossing a semantic revision
are unavailable until a full window under the new revision exists. Scheduled
measurement activation follows SPEC-0005 acknowledgement/lease-expiry fencing;
calculations use the recorded effective boundary, not the edit request timestamp.

Maintenance exclusions follow [SPEC-0016](0016-maintenance-exclusions.md). Remove
both good and total counts (or eligible slices) for the union of excluded whole
intervals. Continue collection and retain original observations. An exclusion applies
to its selected SLO only; for a composite it masks every child contribution within
that evaluation without changing independent child SLOs. Entirely excluded windows
return excluded/null, not 100%. Missing eligible intervals remain unknown; excluded
missing intervals still appear in collection health but not evaluation coverage.

Reports, caches and alert inputs identify the exclusion revision snapshot. Editors
manage future windows; administrators may make audited retrospective changes.
Downloaded reports and sent notifications remain unchanged. Notification silences
and informational graph annotations do not alter calculations. Retrospective query
reinterpretation and observation replacement remain outside the accepted contract.

OpenSLO exclusion extensions still require an explicit supported import mapping;
native maintenance support does not silently make arbitrary extensions executable.

## Reports and export

UI and CLI request the same calculation contract by SLO IDs, interval, time zone,
and optional daily grouping. Daily rows use actual local-date bounds and include
objective/revision identity, target, method, numerators/denominators, budget units,
attainment or composite score, burn, coverage, exclusions, freshness, and provisional
reasons. Include a request snapshot ID, evaluation time, observation commit watermark,
calculation version, and resolved boundaries. Recompute from immutable observations;
late accepted data may improve a later snapshot but cannot mutate a downloaded one.

CSV has one row per objective/revision/day (and explicitly typed composite rows),
UTF-8 headers and decimal dot with no localized percent signs. JSON preserves exact
numeric strings and null reasons. Escape text cells beginning spreadsheet formula
characters; numerical fields remain typed decimal representations. Pagination and
exports share one database snapshot or retained generation watermark; missing pages
cannot silently mix generations. Offer bounded asynchronous export for large ranges
with authorization checked on creation and download. Expired source history returns
explicit unavailable regions; caches cannot supply unsupported scalar semantics.

## Numerical acceptance examples

| Example | Required result |
| --- | --- |
| Counts 99/100 and 900/1000 | Occurrence `A=999/1100=0.908181818182`, not mean 0.945 |
| Same two intervals, RatioTimeslices | `A=(0.99+0.9)/2=0.945` |
| Same intervals, Timeslices, slice target 0.95 | One good of two, `A=0.5` |
| `T=0.99`, good 9850/10000 | Allowed 100, bad 150, remaining -50 events, consumption/burn 1.5 |
| `T=0.999`, observed error ratio 0.002 | Burn 2, independent of presentation rounding |
| Threshold `lte 200 ms`, values 200 and 201 | Success values 1 and 0; Timeslices attainment 0.5 |
| Raw failure ratio 0.02 | Success ratio 0.98; no occurrence totals |
| Two occurrence children, burns 0.5 and 0.25, weights 1 and 2 | Composite burn 1, remaining 0, exactly met |
| Three slices: child A bad only first, B bad only second | Composite Timeslices good 1/3, not average child attainment 2/3 |
| RatioTimeslices children each 0.95, weights 1, common T=0.99 | Composite score 0.90, burn 10 |
| Missing required composite child | Composite unknown; available child remains visible |
| Local day Europe/Madrid 2026-03-29, minute cadence | 1,380 expected minutes before exclusions, not 1,440 |

## Failure, acceptance criteria, and verification

Database errors fail the request; they do not generate empty successful reports.
Cache results include calculation/version/watermark keys; invalidated or stale cache
entries are disclosed or recomputed. No cache may turn a conflicting ingestion item
into an accepted observation. Resource limits return actionable errors without a
partially labelled complete export.

- AC-01: Unit fixtures reproduce every numerical example and distinguish the three
  methods; randomized chunk/ordering tests preserve exact occurrence sums.
- AC-02: Boundary, leap-year, DST, and revision fixtures never
  double count or prorate an interval, and disclose rejected alignments. Maintenance
  union, revision and permission fixtures follow SPEC-0016; notification silences
  and annotations leave calculated budgets unchanged.
- AC-03: Missing, known-empty, 0/0, raw zero, stale, excluded, and expired cases have
  distinct machine-readable output and cannot silently become successful attainment.
- AC-04: Duplicate ingestion leaves reports unchanged; late accepted missing data
  changes only later snapshots. Conflicting measurements never replace accepted ones.
- AC-05: Mixed-agent composites apply the stated method and missing-child policy;
  unsupported weights/targets/cadences block activation with stable reasons.
- AC-06: UI, CLI, daily CSV, and JSON agree on exact input quantities, effective
  bounds, statuses, and units. Rounding at a threshold cannot change pass/fail.
- AC-07: Ingestion rejects excess count precision; declared provider estimation
  normalizes half-even before submission and retains provenance. Post-conversion
  inconsistent counts fail without clamping. Historical initial-definition reports
  expose requested validity and operational activation as separate facts.

Not implemented. Research checked official OpenSLO and Google SRE sources on
2026-10-24. Independent decimal arithmetic confirmed the occurrence, ratio, and
overspend examples; a time-zone calculation confirmed the 1,380-minute DST day.
Relative documentation links were checked. These are document checks, not passing
application tests; the full criteria remain unimplemented.

## Dependencies and review assumptions

Depends on [SPEC-0003](0003-ingestion-and-recovery.md) for typed immutable ingestion
and [SPEC-0005](0005-openslo-and-resource-model.md) for revisions and compatibility.
[SPEC-0007](0007-storage-and-retention.md) governs precision and retained source facts.

- REVIEW-M04: Approve conservative unknown results for incomplete coverage, with
  separately labelled observed-only arithmetic; this may make alerts unknown sooner.
- REVIEW-M05: Approve additive occurrence composites, common-target slice composites,
  unit-weight Timeslices, and unbounded RatioTimeslices deficit. These choices resolve
  ambiguity with explicit execution exclusions instead of claiming universal support.
- REVIEW-M06: Approve aligned whole-interval queries, restricted calendar anchors,
  and maintenance under SPEC-0016 (initial deferral superseded). More flexible boundaries need finer
  retained facts; notification silences remain independent of this decision.
- REVIEW-M07: Approve exact decimal counts and proposed 400-day raw retention as the
  initial correctness baseline; aggregates may accelerate but not replace facts needed
  for supported reports. Capacity testing must validate deployment defaults.

## Change notes

- 2026-10-24: Proposed deterministic measurement, composite, and reporting semantics.
