# Frontend accuracy and usability

Read [UI contracts](../../../../docs/specs/0012-user-interface.md),
[design system](../../../../web/DESIGN_SYSTEM.md), and applicable UI ADRs.
SLI/SLO changes also require the
[retained visual reference](../../../../docs/design/slo-visual-reference.md).

- Trace request state through navigation, forms and charts.
  Check stale responses, abandoned requests, failed lazy imports, refreshes,
  authentication expiry and correction after a failed submission.
- Compare affected screens with their current approved references. Inspect the
  themes, viewports and interactions required by the design guidance;
  static source analysis alone cannot establish visual or usability acceptance.
- Discover the current shared controls and charts through the design guidance and
  code. Check their APIs and demonstrated states before recommending a replacement.
  Raw markup inside a component's implementation is expected.
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
