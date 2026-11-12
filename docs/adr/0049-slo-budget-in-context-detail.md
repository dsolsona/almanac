# ADR-0049: SLO budget in context detail

- Status: accepted
- Created: 2026-11-12
- Accepted: 2026-11-12
- Approval: maintainer selected and refined B in the working conversation, then requested pixel-perfect implementation, PR publication and CodeRabbit monitoring.
- Related specs: [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md)
- Supersedes / superseded by: supersedes the SLO detail composition in [ADR-0028](0028-unified-operational-ui.md); calculation, urgency and other screens remain unchanged.

## Context

The previous status band dominated the page with duplicated facts and calculation
internals. The maintainer chose [B](../design/mocks/slo-detail-options/index.html#b),
then approved point inspection, less instructional copy, distinct day-legend
markers and structured objective/data-quality and calculation disclosures.

## Decision

Lead with an operational verdict, then integrate budget, attainment and recent
burn into the rolling-budget chart. Follow with independently selectable 1 h/6 h
burn, interval attainment and completed-day compliance blocks. Use the current
application shell, shared tokens and controls. Preserve refresh, rename, history
ranges, timezone, routing, permissions and actionable failed/partial reads.

Keep objective/data quality and calculation disclosures collapsed. Present exact
server counts and arithmetic operands without recalculating budget policy in the
browser. Preserve eligibility start, measurement cutoff, missing/known-empty/
zero-traffic/expired intervals and separate observation freshness.

Remove the exact-values table section and its actions as explicitly requested.
Point inspection supplies exact response values via hover, keyboard and touch;
unknown states never become zero. This is the SLO-specific exception to the earlier
mandatory table pairing in SPEC-0012. Completed days retain status, date and
attainment inspection, including unknown and not-applicable days. No API or
calculation changes, dependencies or fabricated descriptions are authorized.

Use a bounded, resize-aware shared SVG chart matching the accepted geometry,
with true timestamps, independently computed series, gaps and partial markers.
Summary and disclosures use one current snapshot; history retains its own
snapshot and explicitly discloses a mismatch or failed refresh.

## Alternatives considered

A retained four lanes with a smaller status band; C paired graphs; D provided a
narrative briefing; E led with an allowance ledger; F used a context rail. The
maintainer chose B for its compact chart-led hierarchy matching the SLI redesign.

## Consequences

B is the SLO detail reference. Compare deterministic real components against the
independent HTML in both themes at 360/1024/1440 px, and verify real container
journeys and page speed. Sample review controls stay outside the product.

## Revisit conditions

Revisit when new budgeting methods or persisted descriptions are supported, or
operators request a bulk exact-value export rather than point inspection.
