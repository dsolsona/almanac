# Parallel operational detail reads

- State: accepted; implementation in progress
- Accepted: 2026-11-01, explicit maintainer approval in the working conversation
- Created: 2026-11-01
- Task: [F3-T08](../tasks/F3-T08.md)
- Governing contracts: [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [ADR-0025](../adr/0025-fast-operational-journeys-and-tracing.md), [API contract](../../api/openapi.yaml)

## Problem and outcome

An authenticated detail visit currently waits for the SLO definition before
requesting status and history. The definition supplies cadence, initial display
period and a server clock. Those facts already belong to the backend serving
history. The normal-container trace recorded in F3-T08 shows a 107.5 ms browser
definition request followed by parallel status/history requests. Browser startup
and chart rendering are additional costs; removing this dependency alone does
not establish the required <500 ms complete-page latency.

Start definition, current status and the requested history together after session
confirmation. Preserve the layout, initial period, exact results, disclosed bounds,
authorization, independent status loading and all existing performance limits.

## Accepted API amendment

Add one optional query parameter to
`GET /api/v1/slos/{slo_id}/operational`:

| Parameter | Values | Meaning |
| --- | --- | --- |
| `period` | `window`, `7d`, `90d`, `1y` | Select a server-aligned display period without first fetching the definition or server clock |

This is additive. Requests without `period` retain their current behavior,
including the seven-day default and explicitly paired `start`/`end` bounds.

- `period` cannot be combined with `start` or `end`; return the existing
  `400 invalid_request` response for ambiguity, repeated or unknown values.
- `at` remains optional and retains its existing validation. Its default remains
  server now. `burn_window_seconds` remains 3600 or 21600.
- Resolve `window` using the existing UI's display period: round the rolling
  window to the nearest whole number of days, with half days rounding up, then
  clamp to 1–365 days. This changes neither the calculation window nor target.
- `7d`, `90d` and `1y` mean exactly 7, 90 and 365 times 86,400 seconds before
  cadence alignment. Align end down to a completed cadence boundary and start
  up to a cadence boundary. Return those actual bounds in the existing range.
- Keep one server-selected evaluation boundary for the response. Reuse the same
  retained composition, generation fencing, density/size caps, immutable exact
  pages, error semantics and response schema. A period read cannot enqueue a
  graph preparation job.

Do not add another endpoint, response wrapper, cache, worker, database migration
or dependency. API and CLI clients may use the same selector. Existing explicit
timestamp callers keep their behavior.

## Implementation after approval

1. Amend the accepted API source and affected specifications, then regenerate
   types using the existing generation tooling.
2. Resolve the selector in the shared operational-bound calculation so retained
   and canonical paths agree. Leave exact calculation and reduction unchanged.
3. Start the definition read, independent status read and period-selected history
   together. Render the existing detail component from the confirmed history's
   objective and keep settings' existing ETag-based update behavior. Preserve
   cancellation, identity changes and late-response fencing. Do not duplicate
   the initial history read when its definition arrives.
4. Preserve the initial `window` display and existing range/burn/zone URL state.
   Range and burn changes use the selector directly; local display changes do
   not trigger a network read. Keep status independent of history failures.

## Verification and remaining limits

Focused cases cover invalid combinations, default compatibility, server clock
skew, 365-day limits, a cadence that does not divide a day, window rounding,
equivalence with explicit bounds and navigation cancellation. A component case
holds the definition response pending and requires status/history requests to
have started; this detects the current waterfall directly.

Use the existing final production-container and VictoriaTraces gates only on the
completed candidate. The trace must show sibling definition/status/history
requests, with no definition-to-history dependency, and the same requested
period and exact values. Preserve every failed sample. This proposal does not
authorize another broad run or the currently unapproved capacity command.

The full cold-start cost and tracing overhead remain unresolved. Removing this
request dependency is necessary foreground work, not a prediction that the page
will meet <500 ms. Scale, year-history accuracy and usable-content acceptance
remain required under F3-T08.

## Alternatives

- Keep the public contract and optimize the serial reads: avoids an amendment
  but leaves an unnecessary network round trip on every cold detail load.
- Start with seven days and fetch the selected window afterward: displays the
  wrong initial period or waits for a second history request; reject it.
- Embed protected data in the HTML: removes more startup round trips, but changes
  static serving, authentication/cache boundaries and rendering architecture.
  This is a larger decision and is not part of this proposal.
