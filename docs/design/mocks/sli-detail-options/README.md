# SLI detail design proposals

Status: E is the accepted SLI detail reference, 2026-11-11, under ADR-0046.
A–D are retained proposals; C was discarded. The HTML uses sample data.

Open [the new E mockup](refined.html) or [the original switcher](index.html) in a browser.
Use the direction links to compare E with the original B; the original four
compositions and desktop screenshots remain available.
Light/dark selection, collapsed intervals, exact-value filters, query disclosure,
support dialog, mobile navigation and the investigation tabs work locally. No
server, dependencies or application credentials are required.

E's graph now has visible points and interval inspection. Hover anywhere along
the plot or collection strip to see the minute, ratio and exact good/total/
unsuccessful counts. Query errors and missing observations show their reasons
and unknown counts, with no fabricated point. Tab to the chart and use Left/Right,
Home/End and Escape; touch devices show a tapped interval's details below the
plot so the panel cannot cover the next selection. Original A–D remain unchanged.

| Direction | First question answered | Tradeoff |
| --- | --- | --- |
| A · At a glance (`#overview`) | What is measured, what is the latest result, and is the data complete? | Most balanced for routine use; still has four summary facts, now in one compact band with consistent density. |
| B · Trend first (`#trend`) | When did the measurement change, and was there traffic? | Gives the chart and aligned volume bars the most room; related objectives sit farther down. |
| C · SLO impact first (`#objectives`) | Which objectives rely on this indicator, and can they be evaluated completely? | Useful to service owners; direct measurement analysis comes after the linked objectives. |
| D · Guided investigation (`#investigate`) | What is measured, what happened, and what can I conclude? | Avoids summary tiles and separates collection from measurements with tabs; more explanatory copy and scrolling. |
| E · Trend + collection (`#refined`, or `refined.html`) | What happened, and which minutes have trustworthy counts? | Builds on B, integrates the latest result into the graph summary, and replaces volume bars with equal-size collection-state squares aligned to the time axis. Request volume stays in the exact counts, rather than a second graph lane. |

All five reuse the existing [mock shell, tokens and font](../README.md), thin
rules, compact controls, single-column composition, and the accepted SLI data-state
vocabulary. The definition in ordinary language is near the title; raw intervals
are closed initially. Queries and collection diagnostics remain available for
investigation. Technical identifiers move to a quiet support action at the end;
there is no misleading “Settings” section containing uneditable identifiers.

The maintainer discarded C and preferred B's compact chart-led hierarchy, asking
to combine it with D's collection-state strip in a new mockup. E is that proposed
refinement, kept separate from the originals. Its green squares mean counts were
received, orange squares mean query failure, and dashed squares mean a missing
observation. The ratio graph retains gaps for the latter two states. E also moves
the latest result into the graph summary and keeps exact rows and diagnostics
collapsed. E is accepted for implementation; the application evidence remains separate.

## Shared fixture and interpretation

The fictional checkout SLI counts successful requests as requests without HTTP
5xx responses. The fixture covers 09:00–10:00 UTC on 11 November 2026:

- 60 expected one-minute intervals; 57 observed, two query errors and one missing.
- 113,402 good events / 114,000 total observed events = **99.48%**, rounded to two
  decimal places; 598 unsuccessful requests. Counts from unobserved intervals
  are unknown and are not included as zeroes or successes.
- The latest interval, 09:59–10:00, is 1,998 / 2,000 = **99.90%**.
- The lowest observed minute, 09:29–09:30, is 1,850 / 2,000 = **92.50%**.
- Query errors occur at 09:43–09:45; the missing interval is 09:52–09:53.
  Charts leave gaps. The assigned agent is currently connected and collection
  running; these facts do not imply the earlier intervals were observed.
- The two fictional SLOs have different rolling windows, both containing the
  missing measurements. Both show Partial data. Their targets do not classify
  the SLI, and no SLO budget is inferred from this one-hour ratio.

Charts, counts and exact rows share this fixture. The chart axis is explicitly
92–100%; the volume lane is event counts, not requests per second. Related-record
links go to existing sample screens, whose examples are independent fixtures.

## Implementation reference

The maintainer accepted E, including exact point inspection, for implementation
under [ADR-0046](../../../adr/0046-sli-measurements-and-collection-detail.md).
The original A–D HTML and desktop images remain available for comparison.

Use production shared controls and the real SLI history inspection bounds. The
fictional checkout explanation is sample copy, not automatic PromQL interpretation;
production uses the supported generic good-events/total-events definition. Existing
refresh, rename and role-based access remain available. Linked SLO status is read
from each objective's current-status endpoint rather than inferred from the SLI.
The current linked projection contains at most five objectives; disclose more.

Implementation, visual comparison, independent arithmetic, real deployment and
review evidence are tracked in [F6-T10](../../../tasks/F6-T10.md). Static mock
rendering alone is not application acceptance. The review controls and fictional
fixture labels are not product controls.
