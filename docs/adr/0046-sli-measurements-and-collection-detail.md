# ADR-0046: SLI measurements and collection detail

- Status: accepted
- Created: 2026-11-11
- Accepted: 2026-11-11
- Approval: maintainer approved mockup E, including point inspection, and explicitly requested implementation, pixel parity, a PR and CodeRabbit monitoring in the working conversation.
- Related specs: [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: supersedes the SLI detail summary-tile composition of [ADR-0028](0028-unified-operational-ui.md); other screens and its shared visual grammar are unchanged.

## Context

The previous SLI detail placed its definition below an expanded interval table.
Four summary cards mixed sparse values with dense collection internals. The
maintainer selected the [E reference](../design/mocks/sli-detail-options/refined.html)
after comparing four alternatives and adding exact point inspection.

## Decision

Use the approved single-column E composition: definition near the title; one
measurement panel with latest result, observed-period ratio and interval coverage;
a ratio graph with point inspection and a time-aligned collection-state strip;
linked SLOs; collapsed exact intervals, measurement definition and collection
diagnostics. Technical identifiers are available through a quiet support dialog.
Preserve refresh, rename, permissions, navigation and actionable failure states.

Observed squares mean received counts, never achievement of an SLO target.
Failed and missing intervals remain gaps. Keep zero traffic, known empty and
partial observations distinct. Hover, keyboard and touch expose the same exact
counts; ratios never invent counts for an unavailable interval.

Use the existing consecutive-interval history API and its inspected bounds. The
initial range requests approximately one hour at the configured cadence (up to
two bounded pages at the minimum 15-second cadence), shortened by actual retention
and activation bounds. Older exact rows may be loaded without changing the initial
graph range. Labels disclose the actual inspected period; no aggregate is computed
from an arbitrary count of received observations. Sum decimal event counts exactly.

Read linked objectives through the existing current-status endpoint, independently
of measurement rendering. Failed objective reads remain visible and cannot be
substituted with guessed status. The current projection provides at most five names;
disclose any additional objectives. No new API, schema, collector or dependency is
introduced. Use an honest generic ratio definition rather than infer prose from
PromQL or add an unapproved description field.

The specialized bounded SLI SVG chart lives with shared charts and is demonstrated
in the catalogue. It implements the approved reference geometry directly without
changing the established SLO timeline renderer or eagerly loading ECharts.

## Alternatives considered

- A's summary cards: rejected in favor of B's more compact graph-led composition.
- B's separate latest-result card and volume lane: replaced by the integrated
  summary and D's collection-state strip.
- C's SLO-first hierarchy: discarded by the maintainer.
- Guessing a description or classifying SLI values using an SLO target: misleading.

## Consequences

The E HTML is the SLI detail composition reference. Browser evidence compares the
rendered measurement panel with that reference in both themes and at mobile widths,
using the same deterministic fixture. Integrated journeys verify real backend and
database behavior independently of visual fixtures. The existing shell remains.

## Revisit conditions

Revisit the generic definition when SLI descriptions have an accepted persistence
contract, or the bounded chart when longer interactive ranges are requested.
