# Selected alert list presentation evidence

[F4-T10](../../tasks/F4-T10.md) implements the selected
[A list](../../design/mocks/alert-options/README.md) using shared controls. These
are actual catalogue components with fictional data. They do not prove an
API-backed alert journey, server pagination, persisted acknowledgement or latency.

- [Desktop light](alert-list-desktop-light.png) and
  [desktop dark](alert-list-desktop-dark.png): separate policy/SLO identity,
  supplied paired-window evidence, multiple matches, unavailable recovery and
  delayed evaluation with preserved lifecycle.
- [Grouped desktop dark](alert-list-grouped-desktop-dark.png): supplied global
  group totals and separate accessible row groups.
- [Mobile light](alert-list-mobile-light.png) and
  [mobile dark](alert-list-mobile-dark.png): compact heading/toolbar and table
  scrolling confined to its labelled region. The visible outline records keyboard
  focus on that scroll region.
- [Touch filters light](alert-list-filters-touch-light.png) and
  [dark](alert-list-filters-touch-dark.png): fields scroll while dialog heading and
  Apply/Cancel remain visible. The complete field set is not shown simultaneously.
- [Touch pending action](alert-list-touch-dark.png): the table is scrolled to the
  action; pending keeps the supplied Firing state rather than fabricating success.

The focused checks pass 14 component cases (3.60 s), typecheck and ten browser
cases (36.2 s). Added dialog-heading geometry/capture checks pass in light (10.2 s)
and dark (9.6 s). They exercise keyboard filter apply/cancel/clear, expected-version
acknowledgement/conflicts, action focus, 10/20-row pagination, history and gaps.
The screenshot helper expands capture height only after interaction checks while
preserving viewport width and theme. Touch cases use a real touch-enabled browser
context at 360×800. A later two-case pagination selection passes in 12.7 s, including acknowledging
all remaining rows of a filtered last page. Manual screen-reader and non-Chromium
testing remain unrun.

Primary review compared desktop/mobile renders with the retained A reference in
both themes. Production integration, performance, real SLO links and global
multi-user races remain required by plan 0035; this catalogue has no backend calls.
