# SLI and SLO visual reference

The maintainer selected these slo-tracker screenshots on 2026-10-29 as the target
for Almanac's operational screens, and confirmed that the existing left navigation
stays. This is a composition and interaction reference, not just a palette reference.
The maintainer explicitly requested urgency grouping, burn windows, an error-budget
view and every graph shown on the SLO detail page as F3 work.

The retained images establish layout intent; their example values, empty graph
rendering and old calculation behavior are not Almanac calculation contracts.
Use [SPEC-0006](../specs/0006-slo-calculation-and-reporting.md) for those contracts.

| Screen | Reference | Required composition |
| --- | --- | --- |
| SLI list | [Screenshot](references/slo-tracker/sli-list.png) | Dense ruled table; health, name/description, type, datasource, recent measurements, latest sample and linked SLOs; search and compact filters above the table. |
| Measurement editor | [Screenshot](references/slo-tracker/measurement-editor.png) | Prominent explained measurement choices; units/value interpretation; labelled, monospace query editors with line numbers and language context. |
| SLO list | [Screenshot](references/slo-tracker/slo-list.png) | Needs attention, Watch and Healthy sections; compact aligned rows with status, name/context, budget sparkline/window, remaining percentage/meter, 1 h burn with 6 h context, and exhaustion. Search, filters and primary creation action precede results. |
| Objective editor | [Screenshot](references/slo-tracker/objective-presets.png) | Adjacent target and rolling-window inputs with common presets; clear selection and guidance; primary create/save action. |
| SLO detail | [Screenshot](references/slo-tracker/slo-detail.png) | Broad timeline with events above four aligned numerical lanes: SLI attainment, budget remaining, selectable burn rate and daily compliance. Lane labels/current values and exact-table actions sit on the left; status, calculation disclosure and definition occupy the right rail. |

## Adaptation rules

- Keep the existing application shell, left navigation, account controls and mobile drawer.
- Preserve paper/ink surfaces, Bevan headings, compact monospace controls, thin rules,
  restrained corners and dense aligned information. Match hierarchy, proportion,
  spacing and interactions, using shared components rather than copying a second theme.
- At narrow widths reflow the rail and toolbars into reading order; retain labelled
  values and keyboard access. Verify both themes at 360, 1024 and desktop widths.
- The status rail leads with budget remaining as a percentage and meter, then
  attainment, target, 1 h/6 h burn, remaining event quantity and exhaustion.
  Preserve negative exact values even when meter fill is bounded for drawing.
- Show observed-only qualifications beside the affected figures. Missing, zero traffic,
  stale, expired and not-applicable data must remain distinct. Urgency is a separate
  operational classification, not proof of complete-window performance.
- Every graph has real backend data, a meaningful empty/error state and exact values
  from the same response. Never draw a budget curve by treating per-interval success
  as rolling budget, or label full-window burn as 1 h burn.
- Keep the timeline's shared range, bottom time axis, crosshair and controls. Offer
  the reference's 7 d, 90 d and 1 y ranges with disclosed retention/resolution;
  1 h/6 h burn selection; UTC/local presentation; refresh; and per-lane table toggles.
- Events include contextual annotations under SPEC-0017. Alert occurrences, mute
  actions, silences and maintenance overlays attach when their F4 domains exist;
  they are not fabricated as part of F3. Keep all four numerical lanes in F3 scope.
- Measurement modes require real collector support. Reproduce the choice-card
  interaction for supported modes and explain unavailable modes without enabling
  unsupported fraction/threshold execution. This visual correction does not advance F5.
- Folder/team/label controls require their real organization contracts and data;
  do not populate decorative selectors. Search, service and urgency filters are the
  first SLO controls. Record remaining reference control gaps in task acceptance.

## Visual acceptance

For each changed screen, compare the running application side by side with its
reference and record concrete differences in hierarchy, density, typography,
alignment, controls and navigation. Exercise list → create → detail → SLI → back,
including invalid input correction, refresh and direct-link reload. Real Compose
data and independent API arithmetic must support the screenshots. Rendering a page
or passing a theme matrix alone is not acceptance of the reference.

The current F3-T01 list and single success chart are an initial functional
increment, not completion of this visual target. Delivery is tracked in the
[F3 plan](../plans/0021-first-objective.md).
