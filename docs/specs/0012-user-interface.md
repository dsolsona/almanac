# SPEC-0012: User interface

> Accepted amendment, 2026-10-30: Operational screens share bounded in-flight/cached reads and load current status independently from history. Previous results retain explicit stale/preparing labels. Existing composition, exact tables, extrema/gaps, point limits and frontend budgets remain required.
> [SPEC-0019](0019-backend-performance-and-derived-data.md) defines the governing
> behavior and additional acceptance criteria AC-01 through AC-09, approved by the
> maintainer after Crit review. Implementation remains outstanding.

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

On 2026-10-27 the maintainer confirmed the established Almanac application layout
and styling, and explicitly selected visible unfinished navigation entries marked
“Not available yet.” The application-shell contract below records that agreement.

On 2026-10-29 the maintainer confirmed that the existing left navigation stays,
and selected the retained [slo-tracker visual references](../design/slo-visual-reference.md)
as the target for SLI/SLO content, hierarchy and interactions. Urgency grouping,
separate burn windows, the error-budget view and all four SLO detail graphs are
required F3 outcomes. The maintainer subsequently authorized implementation of this
plan and [ADR-0023](../adr/0023-slo-operational-urgency.md) in the working conversation.

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

### Application shell

Use persistent left navigation on desktop and a compact header with an accessible
navigation drawer on mobile. Preserve the shared typography, colors, spacing and
controls, including the ALMANAC wordmark. Place account identity, role, theme and
sign-out controls in the navigation footer. Sign-in and restricted first-login
password setup remain separate from the operational shell.

Signed-in administrators initially land on the actual Agents list, with access to
agent details and credential history. Do not place operational screens beneath a
large sign-in confirmation panel. Implemented SLIs and Datasources have working
navigation destinations. Keep planned SLOs and Alerts
entries visible, noninteractive and explicitly marked “Not available yet”; apply
the same treatment to other displayed unfinished destinations. Do not link these
entries to empty dashboards or show illustrative data as product results.

Navigation identifies the current destination. Agent selection and pagination
remain usable through direct links, reload and browser back/forward. Mobile
navigation supports keyboard opening, closing, focus containment and focus
restoration. Permission restrictions and expired sessions must not expose cached
unauthorized records. Navigation must preserve the existing safe-draft and
uncertain-command recovery contracts, without persisting secrets in URLs or
browser storage. Reusable shell components appear in the development catalogue.

Product pages do not display loading messages, progress bars or placeholder
flashes during navigation and reads. Keep the shell and destination heading stable;
retain existing records during a refresh of the same view. Announce pending work
accessibly and disable actions that require an unconfirmed result. Pending reads
must not appear as empty or successful results, and failures must remain actionable.
This presentation requirement was explicitly accepted by the maintainer on
2026-10-27; it does not change request, authorization or recovery behavior.

First-use screens identify the next available action before sending the user to
another page. When SLI creation needs a datasource, expose that prerequisite on
the SLI list and retain the SLI destination through datasource creation. Respect
the user's permissions when presenting setup actions. Use plain-language field
guidance and suggest stable identifiers from display names where supported.

Breadcrumb ancestors with implemented destinations are real links, support browser
history and keyboard navigation, and preserve the current session. Mark the current
page with `aria-current`; category labels such as Product and Administration do not
invent unavailable landing pages. Agent, datasource and SLI details expose their
actual parent list through the same navigation behaviour.

SLI details lead with the latest measurement or an understandable first-result state,
then collection context and observation history. Secondary configuration and technical
identifiers/revisions must not compete with those primary tasks. Show related records
by display name and link to their detail screens when the user has permission.
Unavailable names or restricted agent details have explicit fallback explanations;
do not substitute a raw UUID as the normal label or bypass access control.
Use consistent alignment and spacing at desktop and mobile widths in both themes,
without arranging unrelated content into uneven cards. Explain the first interval
and source settling delay in ordinary language using the actual scheduled times;
do not imply a connected agent has already collected an observation. Under
[ADR-0021](../adr/0021-immediate-initial-collection.md), Create requests an immediate
saved result from existing settled data. Explain this on creation and show the actual
measured interval in the result; do not describe its start as when collection began.
Pending configuration, a disconnected agent and source failures remain
distinguishable from a successful result. Next-query times account for dispatch
spreading. Do not show a future activation countdown for an immediately ready SLI.

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

#### Operational SLI and SLO composition

Use the [visual reference](../design/slo-visual-reference.md) for the dense SLI
table, explained measurement choices/query editors, grouped SLO list and objective
presets. A generic list of objective names or a single success chart is not visual
completion. The reference adapts to the accepted shell rather than replacing it.

The SLI list is a dense ruled table with health, name/description, measurement
type, datasource, recent measurements, latest sample and linked objectives. Search
and supported filters run on the server before cursor pagination. List rows may
include bounded recent data for the latest 12 completed cadence intervals of the
current measurement revision, from interval-time coverage retained by the backend;
missing, zero, empty, partial and failed results stay explicit, with cadence and
the eligible interval range disclosed. A compact plot does not classify samples
against an SLO target; exact counts, timestamps and coverage states remain available
from the same bounded projection. Linked objectives show at most five names with
the exact total when more exist. Datasource and
objective links use the signed-in user's normal permissions. This compact list
projection does not replace full observation history or alter collection.

The editor presents two-query event counts as the supported, event-weighted
measurement. Fraction and threshold choices explain that their collector modes
are unavailable and remain disabled. Query inputs keep native text editing and
show PromQL context and line numbers. Query values are counts for each completed
source-cadence interval; the editor does not imply that a connected agent has
already produced an observation.

The SLO list defaults to Needs attention, Watch and Healthy groups with reason text,
status, linked objective/SLI/service context, budget history sparkline, remaining
percentage and meter, 1 h burn with 6 h context, and a qualified exhaustion estimate.
Search, supported filters and bounded pagination operate over the actual result set.
Urgency does not override performance, data quality, freshness or alert state.

SLO detail uses a broad timeline and a narrower status/definition rail. Events sit
above four aligned numerical lanes: SLI attainment, rolling budget remaining,
selectable 1 h/6 h burn, and daily compliance. Keep lane labels, current values,
reference lines, exact-table actions, common range/timezone controls and shared
crosshair. The right rail leads with remaining budget, then attainment, target,
burn windows, remaining event quantity and exhaustion; calculation detail is
expandable and related definitions link by name. All numerical data is calculated
by the backend under SPEC-0006, with explicit qualifications for partial coverage.

All four numerical lanes and contextual annotation events are F3 work. Alert
occurrences, mute/silence actions and maintenance overlays arrive with their F4
domains; F3 must not invent records or enable unsupported commands. Unsupported
measurement choices and organization controls similarly cannot imply delivered
capabilities. Their absence does not justify replacing the agreed composition.

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
- AC-10: The operational shell provides desktop and mobile navigation, account
  controls and a working Agents destination. Unfinished destinations are visibly
  marked “Not available yet” and cannot navigate. Direct links, browser history,
  keyboard navigation, authorization and recovery retain their specified behavior.
- AC-11: SLI list/editor and SLO list/editor/detail receive side-by-side acceptance
  against the retained visual references for hierarchy, density, typography,
  controls and alignment, in both themes and at mobile/intermediate/desktop widths.
- AC-12: The operational SLO list shows backend-owned urgency with reasons, real
  budget sparklines/meters and separately computed 1 h/6 h burn. Global filtering,
  grouping and pagination do not classify only the currently loaded browser page.
- AC-13: SLO detail shows attainment, rolling-budget, selectable burn and daily
  compliance graphs on one timeline with exact tables, target/1x reference lines,
  common range/timezone controls and a status/definition rail. Annotations use
  SPEC-0017; unavailable F4 event domains cannot fabricate overlays.
- AC-14: Incomplete, stale, no-data, zero-traffic, expired and pre-validity states
  stay explicit in list groups, figures, estimates, graph gaps and tables. Rounded
  values never decide urgency or performance; partial data cannot prove success.

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
