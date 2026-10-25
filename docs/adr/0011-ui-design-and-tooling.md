# ADR-0011: UI design and tooling

- Status: accepted
- Created: 2026-10-24
- Accepted: 2026-10-24
- Approval: Crit review `7fd22977b6c0`; approved 2026-10-24 after all comments resolved
- Related specs: [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: none

## Context

Almanac needs interactive diagnostics, dense time-series inspection and shared
management workflows while preserving its paper/ink visual design. No application
frontend or component catalogue exists in this repository. Tool selection must
not imply delivered screens or an accepted backend API. Fast startup and responsive
SLO graphs, error-budget views and navigation are product requirements. The design
must remain lightweight as history and SLO counts grow; choosing React or a chart
library alone does not establish that performance.

## Decision

Propose a React and TypeScript client built with Vite, shared semantic CSS tokens,
and Apache ECharts behind Almanac chart wrappers. Serve compiled static assets
alongside the backend API under one origin; Node is build/development tooling,
not a required production application server. Pin supported dependency/toolchain
versions in the implementation and lockfile after compatibility/security review;
this proposal does not select an unverified version range.

React renders in the browser and reads/writes the backend through the same HTTP API
as the CLI. Packaging its compiled files inside the Go binary only provides a
convenient static-file server; it does not couple screen rendering to Go.

Split routes and load chart code only on screens that need it. Import only required
ECharts features, bound chart points and table rows, cancel obsolete requests, and
avoid rebuilding every chart on unrelated state changes. The backend provides
bounded display series; full-resolution calculations remain authoritative and exact
values remain available through paginated data. Do not make the browser download
months of raw observations to draw a dashboard.

Treat bundle size, cold-load time, interaction latency and chart-update time as
release checks with documented fixtures and hardware, as specified in SPEC-0012.
If ECharts cannot meet those budgets after targeted imports and profiling, reconsider
it before adding heavier wrappers or removing accessibility. Visual downsampling
must preserve gaps and clearly disclose its resolution; it cannot change SLO math.

Vite supports React/TypeScript and production static assets in its
[official guide](https://vite.dev/guide/). Its role is building the frontend;
authentication, authorization and calculations remain backend contracts.
ECharts supports descriptions and visual patterns, but accessibility must be
explicitly configured and checked; a chart library is not an accessibility
implementation. See the [ECharts accessibility guide](https://echarts.apache.org/handbook/en/best-practices/aria/).
Provide semantic exact-value tables and concise summaries alongside every chart.

Before feature screens, establish the design guide, token foundations, shared
controls, chart wrappers and a local-only catalogue of their actual exports.
Use native HTML semantics where suitable, with deliberate Almanac styling and
keyboard behavior. Bundle Bevan and its license locally. Keep operational status
separate from brand/series color. Feature-specific CSS owns composition only.
No generic component theme may replace the Almanac appearance.

The UI calls the same domain APIs as CLI management; it does not implement a
parallel calculation or permission system. Keep request handling and domain
views separate from reusable controls. Generated types must come from the
accepted API schema rather than a hand-maintained second contract. Exclude the
catalogue from production output instead of relying on a hidden navigation link.

## Alternatives considered

- Server-rendered HTML with progressive enhancement reduces client infrastructure
  but still needs substantial interactive state for diagnostics and coordinated
  timelines; it remains credible if the interaction scope becomes much smaller.
- A full-stack JavaScript framework provides rendering/server features but adds
  a second server boundary unnecessary for this authenticated Go application.
- Hand-built SVG/canvas charts reduce dependency surface but require more work
  for aligned axes, interactions, resizing and accessibility; wrappers contain
  ECharts coupling while exact tables protect access to data.

## Consequences

A build toolchain and chart dependency add upgrade, licensing and bundle-size
responsibilities. Native semantics and reusable controls still need real-browser
verification. Shared wrappers make rendering replaceable without changing SLO
mathematics. Tokens and catalogue-first work create initial delivery cost but
prevent each feature from establishing incompatible controls and status meaning.
No implementation commands or passing test claims are established by this ADR.

<!-- REVIEW-U06: Accept React/TypeScript/Vite and ECharts as the proposed frontend stack? Preserve the design and accessibility contract even if the tooling choice changes. -->

## Revisit conditions

Revisit measured bundle/performance problems, browser support conflicts,
accessibility limitations not repairable in wrappers, or a materially different
interaction/rendering requirement.
