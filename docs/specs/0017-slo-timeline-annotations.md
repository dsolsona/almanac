# SPEC-0017: SLO timeline annotations

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `0de0c0e42db6`; approved 2026-10-24 with no changes requested
- Implementation: not started
- Related ADRs: [ADR-0015](../adr/0015-timeline-annotation-boundary.md) (accepted)
- Related specs: [SPEC-0012](0012-user-interface.md), [SPEC-0010](0010-api-and-cli.md), [SPEC-0016](0016-maintenance-exclusions.md)
- Supersedes / superseded by: none

## Purpose

Explain changes in an SLO graph with events such as deployments, incidents, load
tests and configuration changes. Operators can connect an observed change with its
context without implying causation or changing the measured reliability.

## Scope and non-goals

Include manual UI notes and API/CLI events for automation in the first version.
Include point events and duration events, explicit SLO scope, filters and an accessible
event list. No automatic deployment integration, comment threads, attachments,
external URL fetching or recurring events initially. An annotation never excludes
measurements, changes budget or suppresses an alert.

## Behavior and contracts

An annotation has a stable ID, revision, title (1–160 characters), optional plain-text
body (up to 8 KiB), kind (`deployment`, `incident`, `change`, `note`), UTC event time,
optional later end time, explicit SLO IDs, creator and created/updated timestamps.
An interval is `[start,end)`; a point has no end. Separate event time from received
and edited times so a delayed deployment job can annotate the actual deployment.
Allow historical events within retained measurement history and clearly label future
ones as planned. Future event timing is contextual only; it schedules no operation.

Preview and resolve service/folder selections into explicit SLO IDs, limited to 100
per annotation. Later navigation changes cannot attach old events to unrelated SLOs.
A composite annotation belongs to the selected SLO; it does not automatically appear
on independent child SLOs. Do not infer annotation scope from agent assignments.

Allow up to five optional HTTP(S) links with a 2 KiB bound per URL. Treat them as
untrusted text, never fetch them on the backend or render remote previews, and use
safe external-link behavior. Reject other URL schemes; escape title/body and forbid
raw HTML. A source label is descriptive metadata, not proof of origin. The API derives
authenticated actor/credential identity from the request and cannot accept a forged
creator. Never include tokens or datasource credentials in notes or URLs.

### Permissions, automation and persistence

Viewer reads. Editor/Admin creates and revises annotations in permitted scope through
the same API/UI/CLI operations. Restrict automated credentials to `annotations:write`
and explicit SLO IDs; annotation authority grants neither datasource access nor
maintenance-exclusion permission. Agent and Producer credentials cannot create notes
unless separately issued annotation authority. A revision requires expected-version
concurrency checks and a recorded actor. Deletion is an audited tombstone; preserve
prior versions for at least the 400-day audit/history window.

Use command idempotency for retries under SPEC-0010. Additionally support an optional
external event ID unique per automation principal, independent of the short request
retry horizon. Repeating the same event and scope returns its original ID; conflicting
content returns a conflict requiring an explicit update. Bound IDs to 200 characters;
retain uniqueness/tombstones while annotation history is retained. This allows a
pipeline retry to avoid duplicate deployment markers without guessing by title/time.

### Rendering and querying

Use point markers and shaded duration bands with distinct symbols and text labels.
Show them across aligned SLO/SLI, budget and burn panels under one timeline, preserving
shared crosshair and timezone. Maintenance and notification-silence overlays are
separate typed records: do not create editable annotation copies of them. Their details
link to their own authorized workflows and explain their different effects.

Fetch annotations by explicit SLO IDs and visible UTC range, with bounded cursor
pagination (50 default, 200 maximum). Interval events match overlap, including a
window beginning before the viewport; point events match `start <= time < end`.
Filter by kind/source without hiding that filters are active. Scope checks apply to
reads, counts, pagination and links, not just to mutation buttons.

Cap visible markers at 200 per panel. Cluster dense events with an accurate count
and an explicit zoom/list action; never silently drop events. Load metadata separately
from metric series so a note edit cannot invalidate or recompute SLO calculations.
An annotation request failure leaves the measurement graph usable and marks the event
layer unavailable. Fit rendering into SPEC-0012 performance budgets.

Provide keyboard navigation and an event table, including title, kind, event/received
time, duration, author, links and revision history. Both themes and mobile widths
must support adding, finding and inspecting notes without hover. API/CLI can list,
create, update and delete annotations; CSV measurement reports do not acquire hidden
annotation effects. A separate JSON annotation export includes event revisions.

## Failure and recovery

Validation and authorization fail atomically without partially applying SLO scope.
Late retries cannot recreate tombstoned events. Revision conflicts preserve the user's
safe draft. Database failure returns an error rather than an empty successful list.
Missing/deleted SLO references produce explicit unavailable scope; they cannot be
silently rebound by a new resource with the same display name.

## Acceptance criteria

- AC-01: UI/API/CLI create and inspect point and interval annotations with equivalent
  scope, actor identity and permissions; automated deployments can use bounded keys.
- AC-02: Adding/editing/deleting a note leaves measurement, budget, alert state and
  maintenance unchanged; annotation-layer failure leaves metric rendering functional.
- AC-03: Range overlap, timezone, pagination and dense-event clustering preserve every
  event through explicit navigation; no unbounded chart or DOM rendering occurs.
- AC-04: Retry IDs, tombstones, conflicting payloads and revision races do not create
  duplicate events or overwrite another edit silently.
- AC-05: HTML/URL injection and forged actors are rejected or safely rendered; scope
  checks apply to every read/write and do not grant secret access.
- AC-06: Both themes, mobile, keyboard and exact event-table checks cover notes,
  maintenance and silence overlays with clearly different meanings.

## Dependencies and open questions

- REVIEW-N01 (accepted): UI and API/CLI from the first annotation release, enabling
  deployment automation without building vendor-specific integrations.
- REVIEW-N02 (accepted): Point/duration events, explicit SLO scope, Editor management,
  scoped automation, immutable revision audit and 400-day history retention.
- REVIEW-N03 (accepted): F3 alongside the first graphs, independently of F4 maintenance.

## Verification evidence

Documentation only. Requires API/persistence tests, unchanged-calculation evidence,
security fixtures, browser accessibility and dense-event performance checks.

## Change notes

- 2026-10-24: Initial draft for contextual SLO graph events.
