# SLO detail B implementation evidence

Task: [F6-T11](../../tasks/F6-T11.md), governed by
[ADR-0049](../../adr/0049-slo-budget-in-context-detail.md).
Reference: [refined B](../../design/mocks/slo-detail-options/index.html#b).

## Visual comparison and interactions

The browser fixture passes through the normal authenticated route and validated
current-status/history clients. Its independent fictional snapshot is 10 million
requests, 8,200 unsuccessful, target 99.9%, 10,000 allowed errors, 1,800 remaining
and 18% budget. Interval attainment, rolling budget and independent burn windows
are separate inputs. Backend integration evidence is recorded in the task.

All three numerical SVG plots match the independent HTML reference with **zero
differing pixels** at all six viewport/theme combinations (18 comparisons).
Crop origins are aligned to prevent unrelated shell/header heights changing SVG
subpixel rasterization; comparison tolerance remains zero. Reference assets are
served independently from the repository, not generated from the React component.

| Width | Light | Dark |
| --- | --- | --- |
| 1440 px | [Page](actual-1440-light.png) | [Page](actual-1440-dark.png) |
| 1024 px | [Page](actual-1024-light.png) | [Page](actual-1024-dark.png) |
| 360 px | [Page](actual-360-light.png) | [Page](actual-360-dark.png) |
| Structured disclosures | [Expanded](disclosures-1440-light.png) | [Expanded](disclosures-1440-dark.png) |

The acceptance browser check also covers exact current figures, collapsed
panels, definition navigation/focus, keyboard point selection and Escape,
completed-day touch persistence/dismissal, and absence of table actions. A real
mobile-touch context checks exact decimal counts beyond safe JavaScript integers,
wrapping within calculation rows, and the tooltip below its graph. The mobile
axis uses three nonoverlapping date ticks; this corrects the reference's crowded
phone labels without changing plot geometry. Both reference and product include
that readability correction.

## Production adaptations

- The current application shell, navigation, account controls, quiet refresh and
  authorized rename action stay. Mock review/theme/scenario selectors are absent.
- Real range/timezone controls remain in the budget header; selected historical
  burn retains its confirmed identity while a different window loads or fails.
- Names, values, units, status reasons and data-quality facts come from the API.
  The fixture's leading reason is Low budget under the backend's precedence;
  the prototype's invented prose and HTTP-5xx definition are not inferred from queries.
- The objective links to the real SLI and datasource. The structured disclosure
  retains actual eligibility start, due interval counts and measurement cutoff.
  Source queries live on the linked SLI, rather than being duplicated here.
- Completed-day blocks use exactly the days returned for the selected history
  range (six completed days in this 7-day fixture). The prototype's independent
  30-day block example is not fabricated into the response.
- Current summary and both disclosures share a current snapshot. History keeps
  its own generation; mismatches, retained reads, errors, preparation gaps and
  observed-only qualifications remain explicit. Numeric reduced/partial values
  use qualified markers rather than a successful continuous line.

These are component/reference artifacts, not substitutes for the real container
journey or page-speed checks. Physical devices, non-Chromium browsers, a screen
reader, a year-long workload and larger fleets are not claimed.
