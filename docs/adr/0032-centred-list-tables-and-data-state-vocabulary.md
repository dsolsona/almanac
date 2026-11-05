# ADR-0032: Centred list tables, one value per cell and SLI data-state vocabulary

- Status: accepted
- Created: 2026-11-05
- Accepted: 2026-11-05
- Approval: the maintainer reviewed [tables-v2](../design/mocks/proposals/tables-v2.html) on 2026-11-05 ("Everything else looks pretty good"), asked for centred columns ("doesn't that look better? … Isn't that what is expected normally when working with tables?") and answered "yep, I like this much more thanks. apply it everywhere"
- Related specs: [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: amends decision 3 of [ADR-0028](0028-unified-operational-ui.md) for SLIs (Healthy, Failing, Not reporting, Waiting) and its "right-aligned numerics" row rule; amends the table details of decision 3 of [ADR-0031](0031-list-grammar-and-button-roles.md) (status reason line in SLO rows, "UTC" in the SLI time header); the rest of both records stands

## Context

Reviewing the running SLI and SLO lists after ADR-0031, the maintainer found
them hard to read:

- Columns were sized by fixed widths unrelated to their content. Spare width
  landed as uneven gaps, so content touched in one place and floated in another.
- Text columns were left-aligned and numbers right-aligned, so no column had a
  single line to read along.
- Cells carried several facts. The SLI list showed exact counts, the last sample
  with its UTC time, and the first linked SLO plus "+N more". The SLO list showed
  a status reason, "Burn 1 h · 6 h" and an exhaustion caption.
- SLO rows could not be text-selected because the name link covered the row.
- SLIs were labelled Healthy or Failing. An SLI is a measurement, and only an SLO
  judges it against a target.
- Focused fields showed two rings: the field border plus an outline offset
  around it.

## Decision

1. **Column layout.** In list tables, the name column is left-aligned and takes
   the spare width. Every other column, headers included, is as wide as its
   widest item, centred, and has equal side padding. Neighbouring columns are
   therefore evenly spaced: 32 px apart, or 20 px between 861 and 1,200 px.
   Cells align to the name's first line (24 px). Narrow screens keep each page's
   stacked layout. The shared `ui-list-table` class applies this to the SLI,
   datasource and agent lists. The SLO urgency groups apply it through one grid
   shared by subgrid rows.
2. **One value per cell.** Exact counts, exact times, reasons and secondary rates
   move to the cell's tooltip and to text for screen readers.
   - **SLI list:**
     - The name with only its datasource.
     - Health.
     - The recent strip, without its exact-values disclosure; exact counts stay
       on the SLI page.
     - Last sample as one value and its age ("99% · 2 h ago"); the header loses
       "UTC".
     - SLOs as a count ("6 SLOs") with the names in the tooltip. The count opens
       the SLO when there is one and the SLI's objectives otherwise. With none,
       the cell offers "Create SLO".
   - **SLO list:**
     - Status as the chip only.
     - Objective ("99.9% · 30 d").
     - Budget · 7 d, the trend.
     - Budget left, a number.
     - Burn rate, the 1 h rate, with 6 h in the tooltip.
     - Budget runs out ("in ≈ 2 h", "exhausted" or "—"), with the reason in the
       tooltip.
3. **Selectable rows.** SLO rows have no link overlay. A click anywhere opens the
   SLO, unless it selected text, used a modifier key or hit a link inside the
   row. The name stays a real link for keyboard and middle-click.
4. **SLI health names the data, not a verdict.** The labels are Up to date, Stale,
   Waiting for data, Query error, Partial data and Zero traffic. They replace
   Healthy, Not reporting, Waiting and Failing. The shapes and tones are
   unchanged. SLO, datasource and agent vocabularies are unchanged.
5. **One focus border on fields.** Text inputs, selects, the search field, filter
   pills and query editors show a single accent border on the field itself.
   Buttons, links and other controls keep the 3 px outline.

## Alternatives considered

- **Left-align every column.** Rendered and rejected: each header starts on a
  line, but chips and short values float unevenly inside wide columns.
- **Text left and numbers right**, the earlier rule. Rejected: these values are
  read one row at a time, not added up. They also mix words ("exhausted",
  "in ≈ 2 h") with numbers.
- **Link the SLO count to an SLO list filtered by SLI.** Deferred: the overview
  search matches SLO names only, so it needs a backend filter. The SLI page
  already lists the SLI's objectives.

## Consequences

- `SliRecentStrip` gains `exactValues` (default true).
- The budget sparkline is 24 px high, matching the first line.
- The SLO list's budget meter is dropped: the trend and the number show the same
  value. The detail page keeps its meter.
- The catalogue shows a list-table example.
- The SLI, datasource and agent verifiers assert the new cells. The datasource and
  agent verifiers now also measure centring on the real lists.
- The agent list still needs a sideways scroll at 360 px, because of an earlier
  fixed table width. Stacking it like the datasource list is follow-up work.

## Revisit conditions

- Revisit the column layout if a list gains a column of long free text, which
  should be left-aligned.
- Revisit it if operators need to compare figures digit by digit down a column.
- Revisit the SLOs cell when the overview gains an SLI filter, so the count can
  open the filtered SLO list.
