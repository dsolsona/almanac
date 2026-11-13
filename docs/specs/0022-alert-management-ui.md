# SPEC-0022: Alert management UI

- Status: draft
- Created: 2026-11-12
- Accepted: pending
- Approval: selected mock direction confirmed by the maintainer; full production behavior pending review
- Implementation: not started
- Related ADRs: [ADR-0051](../adr/0051-durable-alert-policy-state-machine.md) (proposed), [ADR-0028](../adr/0028-unified-operational-ui.md), [ADR-0031](../adr/0031-list-grammar-and-button-roles.md), [ADR-0032](../adr/0032-centred-list-tables-and-data-state-vocabulary.md)
- Plan: none; design review tracked in [F4-T01](../tasks/F4-T01.md)
- Supersedes / superseded by: proposed addition to SPEC-0012's alert management contract; shared foundations remain unchanged

## Purpose

Let operators define burn policies, understand why an alert is open and acknowledge
it for the team. Keep evidence and actions close to the alert without overloading
the list or turning a small creation form into a wizard.

## Scope and non-goals

The selected reference is refined **A** for alert lists/details and the current
single-page creation flow shared by A/C in the [interactive mocks](../design/mocks/alert-options/README.md).
B/D/E remain alternatives, not implementation requirements. Product uses the actual
shared controls/charts and [design system](../../web/DESIGN_SYSTEM.md); mock code
is a visual/interaction reference, not a second production component library.

Implement policy creation/management, active alerts, history, detail and global
acknowledgement with backend semantics from [SPEC-0021](0021-alert-policy-engine.md).
No destinations are required initially. Notes, assignment, acknowledgement expiry,
notification silences and destination configuration are later scope.

## Behavior and contracts

### Alert list and history

Use A's compact table: alert/policy name and SLO are separate, with lifecycle,
severity, time and concise trigger evidence. Show matched conditions and the burn
windows/threshold explaining why the alert is open. For multiple matches, lead
with one and disclose the others without inventing a higher policy severity.
Provide optional grouping by SLO and column filters through compact on-demand
controls. Do not add a permanent second row of dropdowns, summary counters or
a Needs acknowledgement label.

Whole-row navigation follows existing tables: inner links/actions and text
selection remain independent, and the name is a real keyboard-accessible link.
Provide acknowledgement directly on eligible rows. Grouping does not acknowledge
every alert for the SLO. List/filter/group counts come from the backend query,
not just the visible page. Retain 10/20-row pagination.

Firing and Acknowledged remain active; resolved and administrative terminal
occurrences are history. Readiness/processor lag is separate from lifecycle.
A firing alert with stale evidence remains open with that reason visible.
Never call unavailable evidence healthy. Behind-cursor results are explicitly
last evaluated state, not current measurements.

If aggregate preparation remains pending after bounded calculation cannot complete,
show evaluation waiting with its original due time and processing lag. Preserve
the last committed alert state; do not present the delayed boundary as completed
no-data, a new indeterminate decision
or current Healthy evidence. Distinguish aggregate preparation failure from
observation absence. Surface aggregate-wait and total delay separately from the
ready-to-commit latency reported by the processor. Distinguish calculation waiting
(including budget/admission deferral) from unavailable source data. A completed exact
fallback displays ordinary decision evidence; do not add a warning merely because
raw contributions were used. Keep calculation-path and resource details in processor
diagnostics, with source quality and processing delay understandable upfront.

History marks Detected after recovery and shows effective occurrence times and
actual detection time. Distinguish replayed original evidence, reconstructed
evidence and unreconstructed gaps. A gap must not look like a quiet period.

### Detail

Use one alert title, a real SLO link, severity and lifecycle, without repeating
service/SLO/policy names in several headline lines. Separate the current reason,
burn/evidence chart, calculation explanation and lifecycle into the selected cards.

Show which conditions match, which do not and which cannot be evaluated. Explain
why an acknowledged alert stays open and what prevents recovery. A condition
handoff preserves identity and acknowledgement. Show original firing evidence
separately from latest evidence, with bounds and timestamps for each.

Calculation explanation connects bad/total counts, observed error rate, allowed
error rate and burn threshold in readable steps. Exact values remain available;
display rounding never changes a boundary comparison. Charts provide hover,
touch and keyboard inspection through shared components; unavailable data is a
gap with its reason. Keep actor/time, administrative causes and evidence revisions
accessible without putting every field in the headline.

### Creation and editing

Follow the existing New SLI single-page section cards:

1. **Policy:** alert policy name first, searchable SLO picker, Low/Medium/High
   severity segments. Picker selection preserves field height/alignment and
   target context. Severity is informational in this release.
2. **Conditions:** fast and sustained burn initially, with editable values and
   Add condition for presets/custom. Already-present presets cannot be added
   twice through the menu. Keep at least one condition. Name and purpose share
   one line where space permits; wrap gracefully on narrow screens.
3. **Alert destinations:** In Almanac, with external destinations identified as
   later scope. Do not offer a control that cannot save or deliver.

Explain the logic once above the rows: “An alert fires when the burn rate reaches
the threshold in both windows of any one condition.” No boolean operator picker.
Keep severity and eventual destinations at policy level, not inside conditions.

Durations and thresholds appear as text by default. Click, tap or keyboard
activation opens one editor without changing row geometry. Duration editing
uses one padded field such as `5 min`, `1 h` or `2 d`, with no unit dropdown.
Accept case-insensitive minute/hour/day abbreviations or full names; a bare number
retains the current unit. Reject unknown units, non-finite values and durations
that fail the backend's whole-minute/cadence requirements. Normalize valid input
on blur/Enter; Escape restores the previous value. Thresholds use a numeric editor
with a spaced multiplication suffix.

Values matching a preset retain its label; other values show Custom condition.
Preserve invalid text visibly and reopen the relevant field on failed submission.
Associate errors programmatically. Client validation does not replace server
validation. Restore focus after add/remove, menus and cancelled edits. Hover
affordances supplement labelled keyboard/touch targets; they are not the only
way to discover or activate editing.

Saving an enabled policy does not fabricate a firing occurrence. Show saved policy
state and per-condition readiness from the backend. Cancel returns without saving.
Editing evaluation settings explains that an open occurrence will be superseded;
disabling has a distinct administrative outcome.

There is no standalone Close alert action. Policy disablement, definition
supersession and confirmed SLO archive are the administrative terminal routes.
The SLO archive confirmation identifies affected policies, open alerts and queued
ranges and requires the backend's expected-version/impact checks. Afterwards show
the policy as inactive because its SLO is archived, and its open occurrence as
Disabled with cause SLO archived. Preserve historical SLO links and original
configuration; never describe archival as recovery. An ordinary blocked archive
lists dependent policy IDs with a route to the confirmed operation.

### Acknowledgement, concurrency and permissions

Editors/Administrators acknowledge an identified occurrence globally. Show its
committed actor/time after success, including on other clients after refresh.
Local mock state does not prove real-time cross-client synchronization. Viewers
have read-only behavior. Retain rows while a request is pending and prevent
duplicate activation.

If someone acknowledges first, refresh their acknowledgement rather than replacing
the actor. If recovery wins, show the terminal state. Reconcile uncertain mutation
responses through request/occurrence reads without blindly replaying the mutation.
Session expiry, denied access, stale versions and errors retain navigation and
actionable feedback. Domain behavior matches API/CLI; browsers do not own lifecycle.

## Failure and recovery

Empty lists, no matches, unreadable data, processor lag, unavailable condition
evidence and permission denial remain distinct. Creation errors preserve values.
Policy disable/removal cannot imply SLO recovery.

Both themes, 360/1024/1440 px, long names, keyboard/focus and touch require real-page
acceptance. Alert list/detail and acknowledgement-to-usable-state journeys retain
<1,000 ms with <=500 ms preferred on the declared 100/200-policy fixtures. Measure
authenticated first visits/cache misses and concurrent evaluation/catch-up, not
only warm API responses. Existing SLO performance contracts are unchanged.

## Acceptance criteria

- AC-01: List/detail/creation match selected references using shared components in both themes and all three viewport sizes.
- AC-02: Rows, links, acknowledgement, grouping, filters and 10/20-row pagination work by pointer and keyboard without accidental navigation.
- AC-03: Name-first creation, stable SLO picker, severity segments and inline duration/threshold editing preserve values, geometry and accessible focus/errors.
- AC-04: Multi-condition firing, handoff, unavailable evidence and recovery are explained from the same backend evidence as transitions.
- AC-05: Global acknowledgement, permissions, concurrent resolution and uncertain responses preserve actor/state and provide correction paths.
- AC-06: Catch-up history distinguishes effective/detection times, reconstruction and gaps without showing recovered history as current firing.
- AC-07: Policy edits/disable and unsupported/new-SLO readiness have explicit outcomes; saving alone never creates a synthetic alert.
- AC-08: Exact counts, thresholds, chart inspection and SLO navigation agree with real API data; missing data stays explicit.
- AC-09: Authenticated alert navigation meets the reviewed performance contract on both fixtures during engine work, with browser/tracing evidence.
- AC-10: Domain operations have matching API/CLI semantics; destinations and deferred response features cannot appear functional prematurely.
- AC-11: Calculation-waiting views preserve last committed lifecycle and distinguish preparation/budget lag from completed unavailable source evidence; original due time and total delay remain visible. Successful exact fallback uses ordinary evidence without a false data-quality warning.
- AC-12: Archive impact/version conflicts, inactive Archived SLO policies and Disabled occurrence history follow the backend contract; no standalone manual-close control appears.

## Dependencies and open questions

SPEC-0021/ADR-0051 must be accepted before production implementation. The mock
baseline is selected but does not prove engine behavior. Before UI acceptance,
extend review coverage for multiple matching conditions on one occurrence,
non-30-day preset confirmation, per-condition cold-start readiness, administrative
closure, evaluation gaps and real concurrent acknowledgement/errors. Preserve
the chosen composition while adding these states.
Include aggregate preparation waiting/failure and the confirmed SLO archive flow
in that remaining review coverage. These corrections change the draft contract;
the currently published static mocks do not yet demonstrate those states.

## Verification evidence

Static checks and visual inspection are recorded in the mock README and F4-T01.
No production alert UI, API integration or performance result is claimed.

## Change notes

- 2026-11-12: Selected A/current creation reference recorded; proposed production state coverage remains under review.
- 2026-11-12: Review adds pending-aggregate lag, named administrative closure routes and confirmed SLO archive states; static mock coverage remains unchanged.
- 2026-11-13: Clarify waiting after bounded-calculation deferral versus source unavailability; successful fallback does not change the selected layouts or imply degraded data quality.
