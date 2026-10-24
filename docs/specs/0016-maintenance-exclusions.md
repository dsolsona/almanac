# SPEC-0016: Maintenance exclusions

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `0de0c0e42db6`; approved 2026-10-24 with no changes requested
- Implementation: not started
- Related ADRs: [ADR-0014](../adr/0014-maintenance-exclusion-semantics.md) (accepted)
- Related specs: [SPEC-0006](0006-slo-calculation-and-reporting.md), [SPEC-0011](0011-alerting-and-notifications.md), [SPEC-0017](0017-slo-timeline-annotations.md)
- Supersedes / superseded by: amends [SPEC-0006](0006-slo-calculation-and-reporting.md)'s initial exclusion deferral

## Purpose

Exclude declared maintenance periods from an SLO's measured population and error
budget without deleting observations or pretending the service was healthy.
Operators must be able to explain which periods were excluded and why a report changed.

## Scope and non-goals

Include one-time maintenance windows for explicit SLO IDs, scheduling, cancellation,
audited revisions, UI/API/CLI parity and visible graph overlays. Include
this in F4 before the initial release. Collection, ingestion and repair continue.
Recurring schedules and automatic service/folder selectors are deferred. A separate
notification silence may be created explicitly; maintenance does not imply one.

## Behavior and contracts

### Window identity, scope and time

A maintenance record has a stable ID, title, required reason, UTC start/end, explicit
SLO IDs, creator, timestamps and a revision. Bounds are half-open `[start,end)`;
start must precede end. The UI accepts a timezone and previews exact UTC bounds.
Ambiguous/nonexistent local times require explicit correction. Persist the selected
UTC instants, not a recurring local-time expression.

Resolve scope to explicit SLO IDs at creation. A future folder move or service change
cannot add an SLO to old maintenance. A window affects the selected SLO's calculations,
not other SLOs using the same SLI. A composite window excludes that period from every
child contribution within that composite; independently evaluated child SLOs remain
unchanged. Multiple windows combine by interval union, so overlaps count once.

Require bounds aligned with all applicable observation/slice grids and effective
revisions in scope. Preview alignment errors and suggested common bounds; never
silently expand a window or prorate counts. If no valid common bounds exist, require
separate scoped windows. New definitions cannot make scheduled bounds ambiguous:
validate the combination before activation and require an explicit maintenance edit.

### Calculation

For Occurrences, omit both good and total counts from excluded intervals before
calculating attainment, allowed failures, remaining budget and burn. For Timeslices,
omit the slices from both passing-slice and eligible-slice counts. For RatioTimeslices,
omit them from the average. Apply composite formulas only to remaining eligible
intervals. Do not extend a report or burn window backwards to replace excluded time.

Example: ordinary traffic is 900 good of 1,000 requests. Maintenance contributes
100 good of 1,000 requests. Raw attainment is 1,000/2,000 = 50%; maintenance-adjusted
attainment is 900/1,000 = 90%. Replacing maintenance failures with successes would
produce 1,900/2,000 = 95%, which is not exclusion and is forbidden. At a 99% target,
the adjusted allowed failures are 10, observed failures 100, remaining budget -90,
and burn 10. Show the excluded count separately when observations are available.

Remove excluded intervals from expected *evaluation* coverage. Missing observations
inside them do not make the adjusted SLO incomplete; collection health still records
the missing polls and repair can recover the underlying data. Missing observations
outside maintenance retain ordinary unknown behavior. Do not report unknown excluded
event counts as zero. If every interval is excluded, return `excluded` with null
performance/budget/burn, never 100%. Eligible intervals with zero traffic remain
`zero_traffic`, a different state.

### Revisions, permissions and reports

Viewer reads; Editor schedules or changes wholly future windows; Administrator can
create, change or withdraw exclusions affecting elapsed time. The retrospective
policy was accepted in the recorded review. Evaluate that permission
against server transaction time: a range becoming historical during a race cannot
bypass the administrator requirement. Require a reason for each change, an expected
revision and an atomic audit record. Bound one write to 100 explicit SLO IDs.

A future window can be cancelled. Withdrawing an active or historical exclusion
creates a new revision/tombstone; it never erases the old record. For a future-only
edit to an active window, expose a prospective end operation that preserves elapsed
coverage, snaps only with explicit confirmation, and rejects removing elapsed time.
All other changes to an active window require Administrator.

Accepted observations remain immutable. Reports include the exclusion revision
snapshot, effective excluded intervals/duration, reasons and calculation version.
A new report uses the current maintenance revision; previously downloaded reports
and recorded alert evidence do not change. A comparison view can show raw and adjusted
results from the same observation snapshot, with unambiguous labels. Historical
changes invalidate affected cached reports/statuses and enqueue bounded recomputation;
never publish a mixed old/new snapshot or describe stale cached results as current.
The revision and dirty-work record commit atomically with the change.

Maintenance records needed to interpret retained measurements/report evidence are
retained with that history, at least 400 days under SPEC-0007. Rotation of queries
or credentials does not erase exclusion provenance. Changing an exclusion changes
which observations participate, not the query/target attached to those observations.

### Alerts and graph display

Burn evaluation uses the same adjusted counts and exclusion snapshot as reports.
A fully excluded short or long window is unevaluable; it cannot resolve an incident
as recovered or fire a no-data alert solely because it was excluded. During fully
excluded evaluation, keep incident history and mark its current evaluation excluded;
reset the continuous no-data timer. After maintenance, that timer starts anew if
eligible expected observations are missing. Collection/agent health remains visible.
Partly excluded windows evaluate only with complete, fresh eligible observations.
Judge freshness against the latest eligible expected interval; do not declare SLO
observations stale solely because all newer expected intervals are excluded.
Independent agent/collection health can still report a real collection outage.

Maintenance changes fence older in-flight evaluations. Historical edits may change
future evaluations but never retract sent notifications or replay historical pages.
If a current incident resolves after an exclusion change, record
`resolved_after_maintenance_change` and the exclusion revision; do not claim that the
underlying service recovered. Separate silences retain SPEC-0011 delivery behavior.

Graphs shade excluded intervals while retaining raw observations and gaps. Label
maintenance independently from notification silences and informational annotations.
Provide keyboard-accessible details and an event table with scope, bounds, reason,
author and revisions. Do not draw the excluded span as measured 100% success.

## Failure and recovery

Validation rejects invalid bounds, unsupported grids, missing/deleted SLOs and
unauthorized retrospective changes before applying anything. Version conflicts
return the current revision without overwriting it. Idempotent API retries cannot
create duplicate windows. Database or recomputation failure exposes a pending/stale
state, not a fabricated adjusted result. Secrets and raw queries are not window data.

## Acceptance criteria

- AC-01: The numerical example produces 90% adjusted attainment, -90 remaining events
  and burn 10; raw measurements and the 50% raw result remain available.
- AC-02: Union, adjacent bounds, effective revisions, DST input and alignment tests
  exclude exactly whole intended intervals, never double subtract or prorate.
- AC-03: Missing inside/outside, all-excluded, zero-traffic and composite-scope fixtures
  preserve distinct results without changing unrelated SLOs or collection health.
- AC-04: Scheduled and retrospective permissions are enforced through API/UI/CLI,
  including concurrent time/revision changes and withdrawal of a past exclusion.
- AC-05: Report and alert snapshots include exclusion revisions; edits invalidate
  caches atomically and cannot rewrite downloaded exports or sent notifications.
- AC-06: Fully excluded evaluations do not masquerade as recovery/no-data; later
  calculations and exclusion-driven resolutions record their actual reason.
- AC-07: Graph overlays, exact tables and raw/adjusted views agree in both themes,
  with keyboard access; collection and historical repair remain active.

## Dependencies and open questions

- REVIEW-E01 (accepted): Scheduled exclusions are required. Permit retrospective
  changes only for administrators, with immutable audit/version history.
- REVIEW-E02 (accepted): Initial explicit-SLO scope, aligned whole intervals, no recurrence,
  and separate opt-in notification silences. Their effects must never be conflated.
- REVIEW-E03 (accepted): F4 delivery and the alert behavior when all evaluable time is
  excluded or an exclusion edit changes a current incident's calculation.

## Verification evidence

Documentation only. Arithmetic examples are checkable independently; acceptance
requires calculation, transaction/concurrency, API/CLI and browser evidence.

## Change notes

- 2026-10-24: Drafted the requested maintenance-calculation feature after the initial
  design review.
- 2026-10-24: Accepted in Crit review `0de0c0e42db6`; linked contracts reconciled.
