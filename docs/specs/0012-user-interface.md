# SPEC-0012: User interface

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Implementation: in progress
- Related ADRs: [ADR-0011](../adr/0011-ui-design-and-tooling.md) (accepted),
  [ADR-0002](../adr/0002-agent-communication.md) (accepted)
- Plan: [Visual foundation](../plans/0001-visual-foundation.md)
- Supersedes / superseded by: none

## Accepted amendment

[SPEC-0016](0016-maintenance-exclusions.md) and
[SPEC-0017](0017-slo-timeline-annotations.md), approved in Crit review
`0de0c0e42db6`, govern maintenance and contextual event overlays.

## Purpose

Give operators a clear path from installation and observation setup to trustworthy
SLO results and alert investigation, preserving the Almanac visual design.

## Scope and non-goals

Include onboarding, authentication, services, agents, datasources, SLIs, SLOs,
historical operations, reports, alerting, and authorized administration. Screens
implement their governing domain contracts; this spec does not approve missing
backend behavior. CLI/API management must have equivalent domain capabilities;
visualization and browser session interaction need not have identical commands.
No hosted billing, organization switching, or public marketing site is included.

## Behavior and contracts

### Shared foundations before screens

Create `web/DESIGN_SYSTEM.md`, shared controls in `web/src/components/ui/`, charts
in `web/src/components/charts/`, and a local component catalogue before feature
screens. Catalogue examples use the real exports and local demonstration state.
They cover both themes, keyboard focus, disabled, invalid, loading, empty, error,
long-content, and narrow-screen states. Catalogue assets/routes are excluded from
production builds; enabling a development flag must not expose them in production.

Preserve warm paper, near-white panels, navy ink, vermilion actions, teal series,
thin rules, small rectangular corners and restrained elevation. Dark mode has
explicit semantic palette overrides with identical geometry. Use Bevan Regular
for the uppercase wordmark and headings, system sans for body and measurements,
monospace for technical labels, and tabular numeric figures. Bundle the heading
font and license locally; no external font request is needed. Dense data and
controls must remain legible rather than adopting decorative slab typography.

Centralize color, spacing, typography, radius, layer, focus and motion tokens.
Feature styles own layout, not private controls or status palettes. Operational
status includes text and a shape/icon; brand orange does not imply warning.
Use meaningful headings, labels, inline errors, persistent save feedback, visible
focus, reduced-motion support and predictable dialog focus restoration.

### Operator journeys

| Journey | Required behavior |
| --- | --- |
| First administrator | Sign in with the deployment-provided initial password, then require a different permanent password before showing application screens. Restricted sessions can only rotate or log out. Preserve validation errors and handle another session completing rotation. |
| Sign in | Offer local credentials in the initial release. Add Google only when that later feature is delivered and configured. Show safe actionable failures and session expiry without losing non-secret drafts. |
| Agents | Register, display one-time token securely, provide Docker/Helm setup instructions, inspect capabilities and config acknowledgement, rotate/revoke tokens under permissions. Installation snippets use secret references rather than embedding a token in URLs. |
| Health | Show connection/heartbeat, live collection progress, ingestion progress and observation freshness independently, including timestamps and diagnostic reasons. |
| Datasources | Select compatible assigned agent; edit provider settings; test saved or unsaved settings immediately through that agent without changing active collection. |
| SLIs | Edit definition, test unsaved query with explicit range, inspect result shape, units and redacted errors; save separately. Direct producers receive ingestion instructions without an agent requirement. |
| Historical work | Request supported backfill, inspect range/progress/unavailable history, cancel and retry according to the recovery contract; preserve live health visibility. |
| SLOs and reports | Browse/filter, inspect objective, compliance, budget, burn and coverage with exact values and definition history; distinguish current from historical revisions. |
| Alerts | Manage policies/destinations, test delivery, inspect occurrences and failures, schedule notification silences and retry according to SPEC-0011. |
| Maintenance | Schedule exclusions with scope/bounds preview; show raw and adjusted results and audit history; gate retrospective edits by role under SPEC-0016. |
| Annotations | Add point/duration events, inspect deployment automation events and revision history, and use the accessible event list under SPEC-0017. |
| Administration | Manage authorized users, authentication settings and audit history; explain permission restrictions without exposing secrets. |

Editors may test queries against existing sources; source connection/credential
changes remain administrator operations. Diagnostic requests use the backend's command API, coordinated to the assigned
agent over the accepted WebSocket protocol. Show queued/running/completed/failed,
deadline and cancellation state correlated to the request. If inputs change,
mark previous results as belonging to an older draft. Never show a late result as
validation of new inputs or silently save settings after a successful test. An
offline/incompatible agent produces an actionable error, never a backend query.

### Organization

Services organize ownership; folders and labels organize SLO navigation without
changing measurement identity. Propose a cycle-free hierarchical folder tree with
sibling-scoped unique names and one parent per folder. Each SLO has at most one
folder and any number of validated labels; unfiled SLOs remain directly reachable.
Moving a folder or SLO preserves resource IDs, history and alert bindings. Reject
moves beneath a descendant and deletion of nonempty folders; explicit reassignment
precedes deletion. Label changes affect filtering only, never historical identity.
Viewers browse/search/filter; Editors create, rename, move and remove organization
resources through matching API and CLI operations. Show permission and version
conflicts without silently losing a move or resetting filters.

### Charts, lists and interaction

SLO timelines share aligned time grids, a single bottom axis and a common crosshair;
short windows retain time-of-day labels. Mark real gaps, notification silences
and definition changes distinctly. Add maintenance shading and informational event
markers under SPEC-0016/0017, with distinct meanings and bounded event-layer queries.
Charts never fill missing data with zero or
join across unknown coverage. Every chart has an accessible summary and exact
values in a semantic table using the same response and range. Downsampling must
be labelled and preserve access to the underlying exact values through bounded
pagination. Units, timezone, coverage and range are explicit; formatted display
must not hide meaningful precision or substitute binary attainment for a ratio.

Lists support bounded pagination, stable sort, useful empty/error states and
shareable URL filters without secrets. A failed refresh marks retained values
stale and shows their timestamp. A failed save retains safe draft fields and
focuses an error summary; conflicting edits require reconciliation. Never store
passwords, tokens or datasource credentials in local storage or URL state.

Reflow toolbars and metadata before reducing text; avoid page-wide horizontal
scrolling. Wide tables use named keyboard-accessible scroll regions. DOM and
visual reading order agree. All workflows work without pointer hover. Proposed
support is the current and previous major stable Chrome, Firefox and Safari at
release; record the tested browser versions with each release.

### Performance and bounded rendering

Fast startup, responsive interactions and lightweight SLO/error-budget charts are
required product behavior. Apply these rules from the first screen:

- Split route bundles and lazy-load charts; sign-in and ordinary administration
  must not download charting code. Import only used chart types and components.
- Paginate lists and exact-value tables; never mount an unbounded history table.
  Bound charts to at most 1,500 plotted points per series by default and request
  a display resolution suited to the viewport. More detail requires zoom/range
  requests rather than loading all retained observations into browser memory.
- Keep display reduction separate from calculation. Preserve missing regions,
  explicitly report resolution, and retain extrema for numeric lines so visual
  averaging does not hide spikes. Exact values remain available through bounded
  pages, and budget/burn summaries come from canonical backend calculations.
- Cancel obsolete requests and discard stale responses after filter/range changes.
  Reuse unchanged chart state; updating a form must not rerender every graph.
  Refresh visible data only and bound retained response caches. Large export work
  executes on the backend, not as a browser-wide synchronous loop.

Accepted initial budgets; implementation measurement remains required:

| Check | Proposed target |
| --- | --- |
| JavaScript needed for sign-in/app shell | At most 300 KiB gzip, excluding lazy chart routes |
| Additional JavaScript for first chart route | At most 500 KiB gzip, including chart library; shared modules counted once |
| Cold SLO-detail load to primary content/chart | At most 2.5 seconds at p75 in the controlled fixture below |
| Filter/click feedback | At most 200 ms at p95; show pending state while an API request runs |
| Four visible chart updates after response parsing | At most 200 ms at p95; loading must not freeze navigation |

For these checks use a production build, a deterministic fixture of 100 SLOs, a
50-row list page, and an SLO-detail view with four charts of two series each, capped
at 1,500 points per series. Record the machine, browser, viewport and build; use
Chrome with 4x CPU slowdown, 10 Mbit/s download and 100 ms RTT. Fixture API handler
time is capped at 500 ms and reported separately from transport/rendering. Collect
at least 20 cold loads and 50 interaction/update samples with a repeatable script.
Also inspect mobile and Firefox/Safari manually for responsiveness and correctness.
This is a reproducible frontend gate, not a claim about arbitrary network or backend
latency. Integrated API latency is measured separately in SPEC-0014.

Inspect bundle composition and browser traces before increasing a budget. A limit
change requires an explicit reason and maintainer review; do not silently weaken
it or remove exact-value access/accessibility to make a benchmark pass. If the chart
library cannot meet the workload after reasonable optimization, revisit ADR-0011.

## Failure and recovery

API authorization governs every action; hidden controls are not an access boundary.
Explain unavailable actions where useful. Handle expired sessions and forbidden
operations without exposing cached unauthorized details. Empty state, transport
failure, stale data and permission denial must never share a misleading “healthy”
view. Error pages preserve navigation and a safe retry path.

## Acceptance criteria

- AC-01: Catalogue precedes screens, uses actual exports and is absent from production.
- AC-02: All journeys use shared tokens/components in light and dark themes.
- AC-03: Restricted first-login rotation, local sign-in and permission/session failures
  work end to end. Google sign-in has the same checks in its later delivery increment.
- AC-04: Unsaved diagnostics execute remotely, correlate to the draft and never mutate
  live configuration; offline/cancellation/late-result paths are visible.
- AC-05: Agent connection, progress and freshness are independently represented.
- AC-06: Charts and exact tables agree, including gaps, zero traffic and definition changes.
- AC-07: Browser checks cover keyboard, focus, dialogs, errors, long content, 360 px mobile,
  1024 px intermediate and desktop in both themes; contrast and screen-reader checks
  cover the catalogue and core setup/SLO/alert journeys.
- AC-08: Domain management operations have matching API/CLI behavior and no UI-only rules.
- AC-09: Production bundle and browser evidence meet the agreed performance budgets
  on the documented fixture. Non-chart routes exclude chart code; long ranges keep
  rendered points, table rows and caches bounded without hiding gaps or exact data.

## Dependencies and open questions

Domain specs govern data and permissions. The shared control catalogue and
[design guide](../../web/DESIGN_SYSTEM.md) exist. Shared timeline charts and exact
tables are implemented in [F1-T03](../tasks/F1-T03.md) using illustrative catalogue
data. Feature journeys, event overlays and the full SLO workload benchmark remain
unimplemented.

<!-- REVIEW-U09: Confirm the proposed bundle, load and interaction budgets and benchmark fixture. Fast, lightweight SLO/chart interaction is required; these initial numeric thresholds need implementation evidence. -->
<!-- REVIEW-U08: Accept hierarchical folders, one folder per SLO, stable-ID moves and nonempty deletion protection? This is navigation organization, not a separate authorization hierarchy. -->
<!-- REVIEW-U03: Accept the proposed browser support window and catalogue-first delivery gate? -->
<!-- REVIEW-U04: Confirm first UI delivery order: catalogue, onboarding, agent/datasource/SLI setup, SLO detail, then alerting and reports. Domain contracts remain prerequisites. -->

## Verification evidence

Shared control catalogue verification is recorded in [delivery status](../status.md).
Component/browser tests, contrast checks, visual inspection and production-bundle/
routing checks cover this initial slice. Manual screen-reader checks, supported
browser coverage and domain journeys/API/CLI comparisons remain outstanding.
The whole spec is not verified; screenshots alone are insufficient.

[F1-T18](../tasks/F1-T18.md), under the
[browser onboarding plan](../plans/0017-browser-onboarding.md), adds real combined-
runtime sign-in, restricted password setup and identity/logout journeys in Chromium,
with both themes and360/1024/1440px layouts. Lost login/password responses are tested
after real backend commits and remain uncertain without replay. Shared controls and
catalogue exclusion remain intact. This does not establish the later SLO workload
budgets, manual screen-reader checks or supported-browser release matrix.

## Change notes

- 2026-10-24: Initial draft preserving the Almanac design and remote setup workflows.
