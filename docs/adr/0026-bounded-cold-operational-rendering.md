# ADR-0026: Bound cold operational rendering and start reads before React

- Status: proposed
- Review: on hold; on 2026-11-01 the maintainer accepted sub-second journeys and requested smaller improvements with the existing renderer
- Created: 2026-11-01
- Accepted: pending
- Approval: pending
- Related specs: [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md)
- Supersedes / superseded by: proposes partially superseding [ADR-0011](0011-ui-design-and-tooling.md)'s ECharts implementation choice for operational timeline charts and budget sparklines; retains React, TypeScript, Vite and static serving

## Context

Updated evidence on normal CPU/no bandwidth cap: first enabled details measured
248.108 ms on the normal network and 660.749/606.910 ms with 100 ms added latency.
The latter cohort's repeat p95 is 612.336 ms. The maintainer accepted results under
one second and retained 500 ms as an optimization target, explicitly preferring
smaller improvements before this proposal. Keep the existing renderer; this ADR
does not authorize implementation. The list-readiness failure and the unexecuted
year/scale workload still prevent final acceptance. The discussion below records
the earlier proposal and its original, stricter budget.

The detail-page budget is <500 ms for every measured cold or repeat visit. The
earlier benchmark added 4x CPU slowdown; the maintainer clarified on 2026-11-01
that users have reasonably capable devices and that artificial slowdown was not
requested. Re-measure without CPU throttling before deciding whether this
rendering proposal is necessary. The maintainer also requested separate normal
network and added-100-ms-latency measurements without an artificial bandwidth cap.
The earlier benchmark also used 10 Mbit/s
download and 100 ms RTT. The completion requirement includes
authentication, usable current values and drawn graphs. Raising that budget,
warming the page before timing or excluding JavaScript loading is not an option.

The production-container run at `75515e3` confirms HTTP/2 resource delivery and
connected VictoriaTraces. Tracing overhead passes, but cold details take about
1,235 ms and repeat p95 is 968 ms. The captured cold diagnostic transfers
328,088 bytes of JavaScript; the ECharts chunk alone is 171,502 bytes and finishes
at 503.7 ms. The main entry finishes at 447 ms. In the captured acceptance trace,
authentication begins at 610.2 ms, the history handler takes 21.786 ms and chart
option construction/rendering takes 164.3 ms. The separate CPU profile is
diagnostic, not an acceptance sample.

These earlier throttled measurements identify a browser startup cost, but do not
establish a rendering-architecture problem on the clarified hardware baseline
or at 100–1,000 SLOs. Asset preloads, compression, parallel detail reads and standard
HTTP/2 are already implemented. ADR-0011 explicitly calls for reconsidering the
chart implementation when targeted imports and profiling do not meet the budget.

## Decision

Propose two coordinated changes within F3-T08. Keep the public APIs, exact backend
calculations, source retention, current/history generations and latency limits.
Keep the existing page markup, layout, typography, colors, chart geometry and
left navigation. No new dependency or production runtime is introduced.

### Render operational charts with bounded native SVG

Replace the ECharts implementation inside the shared operational timeline and
budget-sparkline wrappers with native SVG. Reuse the validated timeline model,
option/layout rules, existing controls, exact tables and theme tokens. Existing
callers continue using the wrappers; there is no second user-selectable renderer.
Do not load an ECharts enhancement after declaring the initial graph usable.

Preserve aligned lane heights/insets, scales, labels, reference lines, gap bands,
partial and isolated marks, compliance bars, the shared crosshair and exact-value
tooltips. Preserve resizing, UTC/local display, keyboard access, both themes and
mobile geometry. Native SVG performs display transforms only; it never calculates
attainment, budget, burn or compliance. Keep all point and exact-table limits.
Retain upstream license notices for any reused implementation code.

### Start the existing initial requests before the main React bundle

For a direct SLO detail document, ship a small fingerprinted, same-origin classic
startup script before the main bundle, with the existing module preloads ahead
of it. It starts the existing authentication-context POST without waiting for
React. Keep this script independent of React, the chart engine and the OTEL SDK.
Only after a successful, bounded, validated unrestricted session response does
it start the same definition, status and period-selected history GETs in parallel.
The main clients consume those in-flight responses once through their normal
validation and cancellation paths, instead of issuing duplicates.

Use only the four fixed same-origin API paths for the current validated UUID and
existing period/burn selectors. Keep credentials, origin/mode, redirect rejection,
body limits and finite deadlines. Do not persist protected responses or expose
their contents in logs, HTML, URLs or trace attributes. Sign-out, identity changes,
mutation invalidation and cancelled navigation must discard obsolete startup
responses. Anonymous/restricted/error cases retain the existing authentication
flow. A failed startup script falls back to the normal clients.

Preserve a single real document journey. The startup script reserves bounded
W3C root/request identities and records actual start/header times. The installed
OTEL SDK adopts those identities and exports the original request parentage and
timestamps; it must not fabricate later requests or omit startup from timing.
Sampling and explicit disabled mode retain their existing semantics. Validate
all adopted identities, paths, bounds and record counts before SDK admission.
No transport or telemetry operation may delay visible content for export.

The startup script is an external asset containing build-owned static code only,
without inline JavaScript or interpolated user data. Do not weaken an existing
deployment CSP to permit it. It does not
add a backend HTML renderer, bootstrap endpoint, response schema, cache service
or background preparation job.

## Alternatives considered

- Continue tuning the existing startup chain: useful for bounded secondary costs,
  but the measured required chart asset itself arrives after the entire deadline.
- Render a simplified initial graph and replace it with ECharts later: duplicates
  rendering logic and risks declaring incomplete interactions usable. Reject it.
- Embed protected page data or server-render charts in HTML: could remove round
  trips, but changes serving/authentication/cache boundaries and duplicates API
  composition. The proposed client change keeps the existing backend contracts.

## Consequences

Almanac owns bounded SVG geometry and interactions instead of delegating them to
ECharts. This costs implementation and visual-parity verification. Startup
response ownership and trace adoption add sensitive lifecycle code; cancellation,
identity fencing, failed-response recovery and connected trace parentage require
focused checks and the real production-container journey.

Implement and verify the two changes as one cold-path outcome. First check
reference geometry, gaps/marks, interactions and startup ownership at focused
layers, then measure the completed candidate through real browser/VictoriaTraces
journeys. Retain every failed sample. This decision does not establish that
500 ms has been achieved, authorize unapproved commands or waive year/scale,
accuracy, recovery and normal-deployment acceptance.

## Revisit conditions

Reconsider if the completed candidate still misses cold latency, cannot preserve
reference geometry/accessibility, or cannot retain truthful connected startup
traces. Do not accept a smaller history/scale workload, weaker calculations or
a relaxed completion predicate to hide such a failure.
