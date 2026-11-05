# ADR-0030: Browser tracing overhead is not an acceptance check

- Status: accepted
- Created: 2026-11-05
- Accepted: 2026-11-05
- Approval: the maintainer, in the 2026-11-05 working conversation: the current browser tracing exists to measure the application against its speed goals, its own overhead is not a product requirement, the final product will not ship this incarnation of browser tracing, and tests of that overhead make no sense
- Related specs: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md) (page-speed budgets and trace evidence remain)
- Supersedes / superseded by: decision 2 superseded by [ADR-0033](0033-simple-test-suite.md); supersedes the "enabled/disabled tracing" measurement clause in the consequences of [ADR-0025](0025-fast-operational-journeys-and-tracing.md); the rest of ADR-0025 stands

## Context

The Compose verifier's telemetry stage (F3 telemetry work under ADR-0025)
loaded the SLI list, SLO list and SLO detail pages ten times with browser
tracing enabled and ten times disabled, in two network cohorts, and failed
when the enabled p95 exceeded the disabled p95 by more than 5 %. On the host
the pages took 181 to 396 ms and the gate passed. Inside the development
container (ADR-0029) the same pages load in 100 to 160 ms, so the roughly
constant 10 ms cost of tracing reads as 6 to 10 % and the gate fails, while
every page stays far below the 500 ms preferred and 1,000 ms hard budgets
and every trace is complete (F7-T03, attempts 3 and 5).

The maintainer's position is that browser tracing was added as the
instrument for measuring the application against the speed goals of
SPEC-0019 and ADR-0025, not as a product feature with its own budget.

## Decision

1. The enabled-versus-disabled tracing comparison and its 5 % allowance are
   removed from the Compose verifier. Tracing stays enabled, the product's
   default, for every measured sample.
2. What remains in that stage is what measures the product: the page-speed
   samples under the SPEC-0019 budgets in both network cohorts, the server
   minute-boundary visit, the canonical-dataset check, and the connected
   VictoriaTraces captures for representative pages and the slowest SLI
   sample, which stay the performance evidence required by ADR-0025.
3. Whether browser tracing remains in the product, and in what form, is a
   separate decision; nothing here removes the instrumentation.

## Consequences

The Compose stage runs about half the samples and no longer toggles the
application's tracing configuration mid-run. Reports keep per-sample
timings and process samples; the `overhead` section disappears. Historical
receipts and records that cite the 5 % gate stay as written.

## Revisit conditions

If browser tracing becomes a product feature with a cost budget, define that
budget in a spec with its measurement method and an absolute floor, rather
than reinstating a relative gate on ten samples.
