# Charts and exact tables implementation plan

Goal: reusable synchronized timelines and bounded exact-value tables in the catalogue.
Architecture: modular ECharts behind shared React wrappers; exact decimal strings
are preserved separately from plot-number conversion. No client-side SLO math.
Spec: [SPEC-0012](../specs/0012-user-interface.md); task [F1-T03](../tasks/F1-T03.md).

## Global constraints

Shared Almanac tokens and local Bevan; both themes and keyboard access; explicit
missingness, bounded rendering, exact values and production catalogue exclusion.
One Luna implementer in isolation, fixed approved commands, independent review.

## Review focus

Missing intervals cannot be joined or filled with zeros. Decimal text survives
plot conversion. Pagination cannot lose rows. Crosshair cannot reuse a stale value.
Theme/resize must reuse and clean up chart resources. No hidden chart import in
non-chart routes. Fail visibly for unsupported large input instead of truncating.

## Task 1: Timeline and exact table foundation

Read [F1-T03](../tasks/F1-T03.md) for contracts and acceptance checks.

- [ ] Implement shared chart/table components and lazy catalogue examples.
- [ ] Verify units/browser/bundle exclusion and inspect screenshots.
- [ ] Review, integrate and record actual evidence and remaining scope.
