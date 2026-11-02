# ADR-0027: Keep dense history graphs available at the displayed resolution

- Status: accepted
- Created: 2026-11-02
- Accepted: 2026-11-02
- Approval: after discussing grouped gaps and alternatives, the maintainer selected Grafana and Nobl9 as the main references: "I think probably what grafana or nobl9 do would be best, those are my main references"
- Related specs: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: partially supersedes [ADR-0025](0025-fast-operational-journeys-and-tracing.md)'s requirement to enumerate every fine-grained gap in a wide graph; exact calculations, retained data and latency requirements remain unchanged

## Context

Long graphs currently fail with HTTP 422 `range_too_dense` when every quality
transition cannot fit within 1,500 plotted samples. The controlled ten-objective
fixture reproduces this with frequent missing, empty and zero-traffic intervals.
Rejecting the whole graph prevents viewing otherwise available history.

Grafana adjusts query resolution to the selected range and graph width, and
separately controls whether null values break lines. Nobl9 separates missing-data
status from historical charts, which can remain populated. These documented
behaviors guide the user outcome; they do not establish either product's internal
encoding or calculations.

## Decision

Keep supported graphs available with bounded detail appropriate to their selected
range. Preserve exact arithmetic and selected point values. Missing data must be
visible and must never look like continuous successful coverage.

1. Keep the 1,500-sample bound. Preserve actual first, last, minimum and maximum
   evaluations per display bucket, comparing exact source counts rather than
   rounded percentages. Preserve individual quality transitions when they fit.
2. When transitions exceed the bound, retain those numerical extrema and attach
   `display_coverage` to selected samples. It lists the distinct sample states,
   quality states and freshness states present within their existing
   `bucket_start`/`bucket_end` bounds. These are presence sets, not interval
   counts or averages. They do not alter the selected sample's own quality,
   timestamp, source counts or calculated value.
3. Render a bucket containing missing, unavailable, empty, zero-traffic, expired,
   partial or pre-validity evaluations as incomplete coverage. Break the normal
   line through it; show valid selected values as qualified markers. Use the
   existing gap bands and coverage styling, with a plain explanation that a
   shorter range shows finer detail. Freshness remains distinct from coverage.
4. Narrower ordinary range requests select finer retained records. They reveal
   individual transitions when those fit their display bound. Do not promise that
   an arbitrarily dense range can expose every event without zooming further.
5. Exact tables retain the actual selected evaluations and immutable generation.
   Coarse display coverage is additional context, never a synthetic point quality.
   Current SLO calculations, urgency, burn windows and daily compliance do not change.
6. A supported graph cannot fail solely because quality transitions exceed the
   plotted-point limit. Keep existing byte, memory, authorization and expiry
   limits and all browser/API latency and accuracy gates.

Retained tiles already preserve the information needed for presence sets.
Compose sets by union across disjoint contributing tiles and exact bounded edges;
do not infer counts from reduced samples. Stable tiles that themselves exceed the
point bound use the same qualified reduction. Existing tiles remain readable:
their retained quality transitions establish presence without a rebuild. No new
database, retained store, renderer or preparation queue is required.

Preserve chart layout, navigation and controls. This decision does not activate
[ADR-0026](0026-bounded-cold-operational-rendering.md).

## Alternatives considered

- Reject dense ranges: prevents the required viewing of retained history.
- Increase point limits or transmit every minute/gap separately: grows transfer
  and rendering cost with retention; precise locations still cannot all occupy
  distinct pixels on a wide graph.
- A separate lossless gap mask: preserves finer locations in the wide response,
  but adds an encoding and rendering path that is unnecessary for the chosen
  resolution-based presentation.
- Silently drop gaps or connect through them: misrepresents coverage.
- Add stored per-bucket counts: unnecessary here; reduced retained samples cannot
  establish exact endpoint counts. Presence sets reuse the existing evidence.

## Consequences

A coarse incomplete bucket may hide valid line segments conservatively. Selected
numeric values remain visible as qualified markers, and zooming provides finer
detail. The UI must describe this limitation plainly. The same retained source
fences and late-data invalidation apply to the additional display context.

Acceptance includes a year of hourly minute-long gaps, alternating empty/zero
traffic, brief peaks, near-equal unrounded values, non-hour boundaries and late
corrections. Independent expected extrema and presence sets must match. Both
normal and added-100-ms rendered journeys, ten concurrent users, and the
10/100/1,000-SLO fleet matrix remain mandatory. No capacity claim follows from
this decision alone.

## References

- [Grafana query resolution](https://grafana.com/docs/grafana/latest/visualizations/dashboards/variables/built-in-variables/)
- [Grafana null-value display](https://grafana.com/docs/grafana/latest/visualizations/panels-visualizations/visualizations/time-series/)
- [Nobl9 missing-data detection](https://docs.nobl9.com/slo-oversight/data-anomalies/)
- [Nobl9 chart and current-status behavior](https://docs.nobl9.com/guides/troubleshooting/slos/)

## Revisit conditions

Revisit if measured payload, query or rendering costs miss the accepted budgets,
or operators cannot distinguish coarse incomplete coverage from exact gaps.
Do not weaken calculations or latency requirements to preserve this representation.
