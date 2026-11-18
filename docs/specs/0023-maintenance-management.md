# SPEC-0023: Service maintenance management

- Status: accepted
- Created: 2026-11-17
- Accepted: 2026-11-18
- Approval: Maintainer approved [PR #137](https://github.com/dsolsona/almanac/pull/137) on 2026-11-18.
- Implementation: in progress
- Related ADRs: [ADR-0053](../adr/0053-service-maintenance-workflow.md), [ADR-0014](../adr/0014-maintenance-exclusion-semantics.md), [ADR-0052](../adr/0052-compiled-maintenance-populations.md)
- Plan: [0046](../plans/0046-maintenance-management.md)
- Supersedes / superseded by: amends SPEC-0016 scope and automatic suppression for this workflow. Retains the calculation, alignment, permissions, audit and provenance rules

## Purpose

Let operators schedule expected downtime around a service, selected objectives or
the whole installation, without spending the SLO budget or receiving maintenance
notifications. Preserve the observations and explain exclusions on graphs.

## Scope and non-goals

One-time windows with title, required reason, start date/time, timezone and duration.
List/search/filter windows; inspect affected objectives and audit history; cancel
future windows, end active windows prospectively, and withdraw elapsed exclusions
with administrator authority. Support UI/API/CLI parity in the eventual feature.
Recurring calendars and external maintenance integrations remain deferred.

## Behavior and contracts

### Scope and scheduling

Each window uses one scope mode:

- **Selected services** covers the SLOs of one or more services.
- **Selected SLOs** covers one or more individually selected SLOs.
- **All services** covers the entire installation, including eligible joins during maintenance.

Use a searchable shared picker for each selected mode. All services needs no picker.
Show the selected scope separately from the affected SLO count and preview.

Resolve service membership at activation and include later joins
prospectively. For calculation exclusion, coverage consists of complete
observation intervals contained in both the maintenance window and the recorded
membership interval. An unaligned join takes effect for calculation at the next
applicable observation boundary. An unaligned departure leaves the interval
containing the departure eligible. For an hourly SLO joining at 02:30 in a
02:00–04:00 window, only 03:00–04:00 is excluded. If no whole interval remains,
the join has no calculation effect for that window. Departures retain already
covered intervals. All-service
scope includes services created while active. Revisions retain effective membership
and changes, so moving resources cannot rewrite history. Ordinary membership changes
must not require administrative retrospective editing. Composite child
propagation follows SPEC-0016's selected-SLO boundary.

First-version scope details

- Use stable service identity. Renaming a service does not interrupt coverage.
  Archived services cannot be newly selected. Historical coverage survives
  archival and retains the original identifiers.
- A service with no objectives can be scheduled. Its preview reports zero
  objectives and its window covers subsequently eligible joins. Missing inventory is an error,
  distinct from a successfully resolved empty service.
- During a window, an eligibility or service assignment change records its
  effective time. Members joining after the exclusive end receive no coverage.
  Explicit SLO scope does not follow service membership. A composite selected by
  service covers its own observations and does not automatically cover child
  SLOs on other services.
- The selected scope appears separately from the resolved, paginated impact
  view. A preview identifies the inventory time used for its counts. Effective
  history links each objective to its recorded participation interval.
- Keep the accepted explicit-only resource contract available to existing
  clients. The extended contract needs a discriminated selector for selected
  services, all services and explicit SLOs. Never overload the existing
  `slo_ids` field with an empty sentinel for all services or truncate an
  effective population into that field.

Store exact UTC `[start,end)` bounds. Show the chosen timezone and exact UTC preview.
Reject nonexistent/ambiguous local input unless an unambiguous instant is selected.
Alignment follows SPEC-0016: no silent expansion or invented fractional counts.
Reason, duration, scope, roles and expected revisions are validated atomically.

### UI presentation

Creation follows Context (title and reason), Scope, then Schedule. Custom duration edits inline with presets. Fleet-scale selection and impact inspection follow AC-08.

Use **Without maintenance** for the eligible compliance population and **Including maintenance** for the comparison that restores maintenance events. Explain both choices beside the control. Historical withdrawal is presented as **Include maintenance events…**, with successful and failed events, recalculation and notification consequences explicit in confirmation. Keep this advanced correction separate from the ordinary End early operation.

### Calculations and graphs

Retain SPEC-0016 arithmetic: omit both good and total events, union overlaps once,
and retain immutable raw data. Never extend evaluation backwards to replace time.
Missing eligible observations, zero traffic and fully excluded windows stay distinct.

Independent example at target 99.9%:

| Population | Good | Total | Failures |
| --- | ---: | ---: | ---: |
| Raw | 1,097,822 | 1,100,000 | 2,178 |
| Maintenance | 98,002 | 100,000 | 1,998 |
| Eligible | 999,820 | 1,000,000 | 180 |

Adjusted attainment is 99.982%; allowance 1,000; remaining 820 / 82%.
Raw attainment is 99.802%; allowance 1,100; remaining -1,078 / -98%.
Raw comparison never becomes alert evidence or the adjusted compliance result.

Shade maintenance on aligned raw, budget and burn graphs using the same time bounds.
Retain raw failures and collection gaps. Calculated points with no eligible
population are gaps labelled `Excluded — maintenance`, not 100% or numeric zero.
Rolling results with eligible population still render. Label the shaded band independently from the evaluation state: hover anywhere inside it explains its owning window, even when that point retains an evaluable rolling result. Click/tap and keyboard access open maintenance details. Numeric and maintenance inspection panels must remain visible beyond chart-card boundaries and inside the viewport. Entirely excluded completed
days are distinct from met, violated or unknown days. Provide keyboard/touch
inspection, a non-hover event list and links to the owning maintenance record.

### Alerts

Automatically suppress SLO notification delivery within active scope. Exclusion
alone does not suppress thresholds caused by pre-maintenance eligible failures.
Preserve existing incidents and evidence; excluded evaluation is not recovery.

For future external delivery, suppress opening, reminder and recovery notifications. After the
last covering maintenance ends, resume ordinary evaluation and notification
eligibility without replaying a suppressed backlog. Independent manual silences
continue to apply. Collection/agent health remains visible. Maintenance does not
change unrelated scopes or silence installation-wide health notifications.

In the first stage, Almanac surfaces occurrences in
the application, API and CLI. A maintenance-aware evaluation still runs and
retains its evidence. While covered, a matching result does not open a new
current occurrence. An occurrence already open keeps its identity,
acknowledgement and evidence. A valid clear result from complete eligible
data may resolve it. Fully excluded evaluation remains unavailable and cannot
manufacture recovery. At the first evaluable boundary after the last applicable
window ends, a continuing match can open an occurrence with that boundary
as its effective start. Suppressed historical transitions remain auditable and
do not open a firing occurrence during catch-up. Future external delivery
follows the suppression and resume policy when that surface exists.

Cancellation or prospective early end changes both effects together. Retrospective
correction changes new calculations but cannot undo sent notifications or previous
exports. Stale calculations are marked outdated/pending until consistent results
are available; a stale snapshot cannot override current suppression authority.

### Permissions and audit

Retain Viewer read, Editor wholly future writes and Administrator historical writes.
A prospective early-end operation preserves elapsed coverage under SPEC-0016.
Require revision checks and change reasons. Preserve original revisions, actor,
bounds and scope membership for retained evidence. Conflicts preserve safe drafts.

## Failure and recovery

Reject empty scope, invalid bounds, DST ambiguity, unaligned scope and unauthorized
history changes with actionable feedback. Scope read failures must not look empty.
Recalculation failure must not fabricate current adjusted results. Event-layer read
failure leaves measurements usable and marks annotations unavailable. Bulk scope
activation must remain bounded, atomic in observable authority and restartable;
its persistence/API design is a prerequisite rather than an assumed implementation.

## Acceptance criteria

- AC-01: Schedule one service, several services, explicit SLOs and all services;
  preview affected objectives and exact UTC bounds before saving.
- AC-02: Verify the independent counts above, overlap union, fully excluded,
  missing eligible and zero-traffic cases without changing raw measurements.
- AC-03: Inspect maintenance bands, reasons and exact bounds across accepted SLO
  detail composition in both themes, keyboard and mobile layouts.
- AC-04: Suppress new current occurrences within effective scope, keep
  pre-existing acknowledgements and evidence, and resume at an evaluable
  boundary after overlapping windows end. Verify future notification delivery
  independently once it exists. In both cases, catch-up creates no suppressed
  backlog.
- AC-05: Cancel future, end active and audit administrator historical changes;
  conflict and failed-save paths retain drafts and do not partially apply scope.
- AC-06: Record joins and departures prospectively;
  historical scope and report provenance remain stable.
- AC-07: Bound large/all-service writes and activation, including membership/send
  races, restart, invalidation and ordinary page performance acceptance.

- AC-08: Reuse shared Almanac pickers, calendar, duration presets, form, list and chart patterns. Search service/owner or objective context across the full catalogue; bound suggestions and paginate affected-objective inspection. A 50-service fixture must not render 50 checkbox choices or expand the entire affected population into the form.

## Implementation dependencies

The next slice needs an exact API and CLI contract, a bounded bulk publication
protocol, and application verification. Composite child behavior follows
SPEC-0016's explicit selection boundary. Exact cooldown and delivery
deduplication belong to the future external delivery adapter.
See [plan 0046](../plans/0046-maintenance-management.md).

## Change notes

- 2026-11-17: Drafted the initial direction.
- 2026-11-18: Accepted the scope, prospective membership and in-Almanac suppression contract.
