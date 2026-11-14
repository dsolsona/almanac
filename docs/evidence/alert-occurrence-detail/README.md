# Selected alert detail presentation

F4-T12, 2026-11-13. These are actual development-catalogue component captures
with fictional supplied snapshots, not production alert responses. The selected
[A detail references](../../design/mocks/alert-options/README.md) govern the
composition. Source and capture hashes are recorded in [source-sha256.json](source-sha256.json).

## Reviewed renders

| Viewport | Default light / dark | Original evidence and calculation light / dark |
| --- | --- | --- |
| 1440 px | [Light](desktop-light.png) · [Dark](desktop-dark.png) | [Light](calculation-desktop-light.png) · [Dark](calculation-desktop-dark.png) |
| 1024 px | [Light](intermediate-light.png) · [Dark](intermediate-dark.png) | [Light](calculation-intermediate-light.png) · [Dark](calculation-intermediate-dark.png) |
| 360 px | [Light](mobile-light.png) · [Dark](mobile-dark.png) | [Light](calculation-mobile-light.png) · [Dark](calculation-mobile-dark.png) |

[Touch light](touch-inspection-light.png), [touch dark](touch-inspection-dark.png)
and [keyboard focus](keyboard-focus-dark.png) retain inspection/focus evidence.
Default comparison captures are unfocused. Capture height expands only to include
the component; viewport width and touch behavior are preserved. Touch inspection
captures keep the actual 360 × 800 viewport.

The one title, SLO link/severity, reason banner, paired evidence, slab chart heading,
red/teal series, calculation disclosure and lifecycle card were compared with A.
Original/latest evidence and exact provenance remain separately accessible. Values,
condition outcomes, detection precision and lifecycle come from supplied data.
The shared timeline renderer provides hover, persistent touch and keyboard inspection.
There is no copied mock chart or new chart library.

## Checks

- Focused components: 44 passed in 11.83 s, including default timeline rendering,
  model behavior and existing SLO inspection. A test-only palette type narrowing
  correction then passed its 27-case file in 1.70 s.
- Final TypeScript: passed; 5.004 s observed wall time including container startup/poll.
- New detail browser selection: 11 passed; Playwright reported 1.0 m. Cases include
  coherent concurrent resolution, unchanged original evidence, condition handoff,
  unavailable evidence, waiting, administrative cause, permissions, errors/focus,
  long names, both themes, all three widths, touch and lazy-import recovery.
- After the selected heading/capture correction, all eight affected matrix/touch
  cases passed in 47.4 s. These produced the final images above.
- Existing SLO tooltip checks: five cases reported passing before a wrapper
  termination; the remaining eight passed in a persistent session in 48.3 s.
- Existing lazy catalogue loading/failure checks: two passed in 20.7 s.

Two non-TTY `make shell` runs ended with unexplained exit 143. No assertion failure
preceded either termination. The later run's Playwright child briefly outlived its
wrapper and left no final report. Partial case logs/captures and bounded diagnostic
metadata remain private. Completing missing cases with a persistent session does
not establish that the wrapper issue is fixed. Tests, assertions and timeouts were
not weakened. The [task](../../tasks/F4-T12.md) records exact commands and limits.

No production route/API, backend persistence, multi-client acknowledgement,
engine correctness, authenticated navigation speed or workload capacity is accepted
from these catalogue checks. Backend/integration/E2E/performance suites and a
production build were not part of this presentation slice.
