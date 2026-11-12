# SLO detail design exploration

Status: refined B accepted for implementation, 2026-11-12, under
[ADR-0049](../../../adr/0049-slo-budget-in-context-detail.md). Other directions remain proposals. The maintainer requested evolutionary and radical
redesign exploration; no application behavior or calculation policy changes here.

Open [the interactive comparison](index.html). Choose A–F, light/dark, and Healthy,
Burning fast, Breached or Partial data. All assets are local; no server or login is
required. Arrow keys inspect charts; hover values and definition disclosures work.

| Direction | Journey | Tradeoff |
| --- | --- | --- |
| A · Essential | Verdict → three facts → four aligned evidence lanes | Safest evolution; still requires scrolling across four charts. |
| B · Budget in context | Verdict → summary integrated into budget plot → recent burn | Recommended evolution; closest to the accepted SLI redesign. |
| C · Signal & detail | Verdict → parallel budget and burn → supporting evidence | Fast comparison on wide screens; narrower chart axes and more eye movement. |
| D · Reliability briefing | Read the answer → understand recent spending → inspect evidence | Boldest change in reading order; helpful for occasional users, wordier for experts. |
| E · Budget ledger | Understand the allowance → see its balance → explain the trend | Makes percentages tangible; the block grid is approximate and needs exact numbers alongside it. |
| F · Investigation desk | Keep current context visible while investigating history | Best for sustained analysis; returns to a side rail on wide screens. |

## What matters first

The first visit should answer whether the objective is met, how much error budget
remains, whether recent burn is above its allowance, and whether measurements can
support that conclusion. Urgency, rolling performance and data quality remain
separate. Breached with improving recent burn is deliberately included: a low
current burn does not restore an overspent rolling budget.

Repeated target, collection and evaluation metadata move to a disclosure. Exact
counts, boundaries and calculation context stay available. Missing-data warnings
remain visible next to the leading numbers. No forecast is supplied without an
explicit traffic model. Definition navigation remains easy; it is not the primary
operational content.

Charts use thin rules, restrained fills, shared UTC bounds, explicit units and
reference lines. Gaps remain gaps. Budget is a rolling evaluation, attainment is
an interval ratio, burn uses independently selected 1 h/6 h windows, and daily
compliance means the rolling evaluation at a completed UTC day's end. All four
views remain reachable in every proposal. Chart inspection supports pointer,
touch and keyboard, with values from the same fictional arrays.

## Fixture and review boundaries

Current snapshot: 12 November 2026, 10:00 UTC; rolling 30 days; target 99.9%;
10 million observed requests; 10,000 allowed unsuccessful requests.

- Burning: 8,200 unsuccessful, 1,800 remaining, 18% budget, 99.918% attainment;
  1 h burn 2.4× and 6 h burn 1.2×.
- Healthy: 2,800 unsuccessful, 7,200 remaining, 72% budget, 99.972% attainment.
- Breached: 12,000 unsuccessful, −2,000 remaining, −20% budget, 99.880% attainment.
- Partial: same observed arithmetic as Burning, but 1,440 of 43,200 due intervals
  are missing. Full-window performance is unknown and short burn is unavailable.

Historical arrays are illustrative snapshots, independently specified rather than
calculated from current summary counts. The 7-day history and 30-day current
objective window are distinct. Completed-day examples cover 30 days and omit today.
Related SLI links open an independent sample fixture, not synchronized data.

Review acceptance: six distinct compositions, the existing shell/palette/type,
responsive reading order, both themes, inspectable charts, accessible disclosures,
negative budgets and an explicit incomplete-data scenario. This is design evidence,
not integrated application acceptance. Runtime validation is recorded after checks.

The [accepted SLI reference](../sli-detail-options/refined.html),
[current SLO reference](../slo-detail.html),
[design system](../../../../web/DESIGN_SYSTEM.md),
[urgency contract](../../../adr/0023-slo-operational-urgency.md) and
[calculation contract](../../../specs/0006-slo-calculation-and-reporting.md)
informed the proposals. Production implementation would need an approved design
and real snapshot, missingness, exact-value and range interactions.

## Verification

Checked in Docker via `make shell` on 2026-11-12:

- JavaScript syntax: `node --check docs/design/mocks/slo-detail-options/options.js`, passed.
- Focused Chromium rendering/interaction check: 54 cases, 3.635 seconds. All six
  directions at 1440/1024/360 px in light/dark had no horizontal page overflow;
  each direction also exercised Healthy, Breached and Partial data. Burn switching,
  exact disclosure/12 rows, keyboard inspection, missing-series gaps and theme
  switching passed without JavaScript errors.
- Documentation check: `python3 scripts/check-docs.py`, passed (219 files, 1,608 local links; 0.58 seconds including container launch).
- Initial shell launch failed on a stale Docker development network. Catalogued
  `make stop-dev` removed the stopped fixture; the next launch succeeded.

Desktop renders: [A](png/a--desktop--light.png), [B](png/b--desktop--light.png),
[C](png/c--desktop--light.png), [D](png/d--desktop--light.png),
[E](png/e--desktop--light.png), [F](png/f--desktop--light.png).
Dark desktop renders are alongside them. These are fixed mock screenshots;
application suites, integrated API/database journeys and performance benchmarks
were not run because the change is limited to design proposals.

## B refinements

The maintainer selected B, then requested point hover details, less explanatory
copy, clear completed-day legend colors, removal of the empty exact-values
section and a more structured calculation disclosure. Hover/touch/keyboard
inspection remains. Table actions and the exact-values section are removed.
Calculation details now show inputs followed by three numbered arithmetic rows:
error allowance, remaining allowance and budget percentage. Negative balances and
observed-only qualifications remain explicit. The disclosure starts collapsed.

The phone axis now uses three date ticks to avoid overlapping labels; plot geometry is unchanged. Implementation and current verification are tracked in [F6-T11](../../../tasks/F6-T11.md).
