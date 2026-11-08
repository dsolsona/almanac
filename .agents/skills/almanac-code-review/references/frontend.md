# Frontend accuracy and usability

Read [UI contracts](../../../../docs/specs/0012-user-interface.md),
[design system](../../../../web/DESIGN_SYSTEM.md), and applicable UI ADRs.
SLI/SLO changes also require the
[retained visual reference](../../../../docs/design/slo-visual-reference.md).

- Trace React/TypeScript request state through navigation, forms and charts.
  Check stale responses, abandoned requests, failed lazy imports, refreshes,
  authentication expiry and correction after a failed submission.
- Compare actual SLI/SLO pages with the retained composition and chart geometry.
  Inspect both themes, mobile and real interaction when render evidence is available;
  static source analysis alone cannot establish visual or usability acceptance.
- Reuse components in `web/src/components/ui/` and charts in
  `web/src/components/charts/`. Check actual APIs and catalogue states before
  recommending a replacement. Raw markup inside a component's implementation is expected.
- Check keyboard reachability, visible focus, control names/labels, dialog focus
  behavior, busy/disabled state and programmatically associated errors. Preserve
  reduced-motion behavior and readable theme tokens.
- Verify exact values, units, time bounds, quality and freshness across rows,
  tooltips, graphs and accessible alternatives. Null, loading, empty and failed
  states must remain distinct rather than appearing as successful zero values.
- Assess navigation through usable rows and painted graphs, including first visits,
  range changes and cache misses. Use [Performance](performance.md) for changed
  rendering/request costs and measured page-speed acceptance.

Report the concrete user barrier or wrong displayed result. State when themes,
reference comparison, browser interactions or timings could not be inspected.
