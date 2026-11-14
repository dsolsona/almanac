# Alert presentation review corrections

Verified on 2026-11-14 above `88c4461`, in `alert-ui-review-fixes`. The selected
[form](../../plans/0040-alert-policy-form.md),
[list](../../plans/0041-alert-occurrence-list.md) and
[detail](../../plans/0043-alert-detail-presentation.md) compositions remain the
reference. These are fictional catalogue/mock interactions, not a production
alert API, engine or performance acceptance claim. [Source/capture hashes](manifest.json)
identify the candidate and retained screenshots.

## Corrections

- [PR94 mock width](https://github.com/dsolsona/almanac/pull/94#discussion_r4568016270):
  align the alert mock shell with the shared 860 px sidebar breakpoint. The real
  failure was a hidden sidebar leaving the form in a 210 px grid column. Keep
  the condition inputs and existing row composition; do not shrink them to
  compensate for the broken shell.
- [PR101 inline editing](https://github.com/dsolsona/almanac/pull/101#discussion_r4568017577):
  retain committed normalized/cancelled text locally before closing, so reopening
  selects the displayed value and typing replaces it.
- [PR104 unavailable evidence](https://github.com/dsolsona/almanac/pull/104#discussion_r4568002581):
  show its last-evaluated timestamp even when processing is current.
- [PR104 closure time](https://github.com/dsolsona/almanac/pull/104#discussion_r4568002599):
  show effective closure separately from detection. A compact native disclosure
  exposes the full timestamp to keyboard and touch without navigating the row.
  Visual review rejected an always-expanded date that made rows unnecessarily tall.
- [PR106 repeated failure](https://github.com/dsolsona/almanac/pull/106#discussion_r4568817080):
  focus feedback after each acknowledgement result, including the same error
  twice, while unrelated rerenders preserve the operator's focus.
- [PR106 lane identity](https://github.com/dsolsona/almanac/pull/106#discussion_r4568817087):
  visible and spoken multi-lane inspection includes lane and series names.
  Single-lane presentation stays compact.
- [PR106 resize](https://github.com/dsolsona/almanac/pull/106#discussion_r4568817095):
  reposition existing inspection after real chart resizing, using the latest
  selection without requiring another React render or user action.

## Verification and retained failures

All application commands ran through Docker with `make shell CMD='…'`.
Private logs and failure traces are in `.local/alert-ui-review-fixes/` in the
dedicated worktree. No E2E stack, performance workload or operator fixture was
started or reset. The explicit assignment selected focused frontend checks;
broad application CI remains the integration owner's gate.

| Check | Result |
| --- | --- |
| `cd web && npm test -- tests/alert-occurrence-list.test.tsx tests/alert-occurrence-detail.test.tsx tests/timeline-inspection.test.tsx` before fixes | Four intended failures, 24 existing cases passing; 5.50 s |
| New browser regressions on `desktop-light` before fixes | Normalized reopening selected no text; 2.2 s case, 12.5 s run |
| Tightened geometry regressions before fixes | Cursor remained 100 px behind resized series (6.1 s case); mock control exceeded card right edge, 317 vs 187 px (0.59 s case) |
| `cd web && npm test -- tests/alert-occurrence-list.test.tsx tests/alert-occurrence-detail.test.tsx tests/timeline-inspection.test.tsx tests/alert-policy-form.test.tsx tests/timeline-render.test.tsx && npm run typecheck` | 38 cases pass, 7.51 s; TypeScript passes |
| Four affected browser files: new regressions, policy form, occurrence list and detail | 42 pass; two new mobile geometry assertions fail; 1.3 min |
| Corrected geometry oracle plus affected list/detail/reopening/touch selections | 22 pass, 41.1 s; TypeScript passes |
| Final compact closure disclosure: list unit file, TypeScript, six-theme/size list browser selection | 16 unit cases pass, 1.53 s; TypeScript passes; six browser cases pass, 19.3 s |
| Final touch shrink/expansion and closure disclosure, both themes | Two browser cases pass, 10.8 s; TypeScript passes |

The first geometry probes were insufficient: comparing an overflowing grid with
itself did not detect clipping, and a viewport change could leave a capped chart
unchanged. Before source fixes, the probes were changed to compare controls with
the actual card and resize the actual chart host. Both then failed as described.

The later mobile failure was in the new assertion, not hidden by a tolerance
increase: ECharts adjusted its grid inset when labels changed at narrow widths.
Retained SVG paths showed endpoint x=272.8834 before resize and x=178 afterwards,
matching the corrected cursor. The final oracle compares the cursor directly
with the rendered series endpoint. Keyboard cases cover both themes at
360/1024/1440 px; actual touch cases verify shrink and expansion. Mock cases
cover 761, 800, 820, 860 and 861 px in both themes. Default timeline rendering,
lazy failure, pending states, original evidence and existing form interactions
retain their focused passing checks.

## Visual inspection

The rendered captures were inspected against the retained selected composition.
History stays compact; the full closure timestamp opens deliberately. Narrow
tables retain their existing labelled horizontal-scroll region.

- Corrected intermediate mock: [light](mock-light-761.png), [dark](mock-dark-761.png).
- History desktop: [light](history-desktop-light.png), [dark](history-desktop-dark.png).
- History mobile, scrolled to times/actions: [light](history-mobile-light.png), [dark](history-mobile-dark.png).
- [Expanded mobile closure](history-expanded-mobile-dark.png) and [touch chart inspection](touch-resize-light.png).

These corrections do not establish authenticated alert journeys, global
acknowledgement semantics, engine correctness or performance capacity.
