# ADR-0028: One visual grammar for operational and administration screens

- Status: accepted
- Created: 2026-11-04
- Accepted: 2026-11-04
- Approval: Crit session `9a46588aaaa4` approved with no comments, 2026-11-04; the mocks it references were approved in the same day's working conversation
- Related specs: [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0018](../specs/0018-agent-management-usability.md)
- Supersedes / superseded by: supersedes the right-rail SLO detail composition and the slo-tracker screenshots as acceptance reference in [the visual reference](../design/slo-visual-reference.md); amends the native-select rule in [the design guide](../../web/DESIGN_SYSTEM.md); SLI vocabulary in decision 3 and right-aligned numerics in decision 1 amended by [ADR-0032](0032-centred-list-tables-and-data-state-vocabulary.md)

## Context

On 2026-11-04 the maintainer reviewed the SLI, SLO, datasource and agent screens
against the retained slo-tracker reference and rejected the current state: SLI and
SLO pages use two different visual grammars, status labels contradict urgency
groups, detail pages use a two-column layout that leaves unstructured space,
forms expose spec language and native selects that clip, and the datasource and
agent screens are record tables with no operational state.

The maintainer then approved a set of HTML mocks, iterated in that conversation,
as the target for all four areas: [docs/design/mocks](../design/mocks/README.md).
Several of those decisions change accepted contracts or guidance, which is why
this record exists. The approved mocks are the authority for composition; this
record captures the decisions that other documents must follow. Accepted; the
amendments to SPEC-0012, the visual reference and the design guide apply.

## Decision

1. **One grammar.** (List header and row order superseded by
   [ADR-0031](0031-list-grammar-and-button-roles.md).) Every list uses the same header (Bevan title, mono count
   subline, "Updated hh:mm:ss UTC", Refresh, one primary action), toolbar (search
   with `/`, segmented health filter, compact selects), row (status shape plus
   label plus one-line mono reason, name with context subline, right-aligned
   numerics) and pager (position, per-page, previous/next). Every detail page uses
   a status line under the title, a banner when action is needed, summary tiles,
   then full-width cards. Every form uses numbered sections with a guiding
   question in the header, hints only where mistakes are likely, and no spec
   vocabulary.
2. **Single-column detail pages.** SLI and SLO detail pages are one column. The
   SLO detail leads with a full-width status band (budget hero, reasons in words,
   facts), then the timeline, then the definition. This supersedes the right-rail
   composition in SPEC-0012 and the visual reference. The rail's content is kept;
   only its placement changes.
3. **Status vocabulary.** (SLI labels amended by [ADR-0032](0032-centred-list-tables-and-data-state-vocabulary.md): Up to date, Stale, Waiting for data, Query error, Partial data, Zero traffic.) Status marks are a shape plus a label that names the
   reason: Breached, Burning fast, Low budget, Not reporting, No data yet, Healthy
   for SLOs; Healthy, Failing, Not reporting, Zero traffic, Waiting for SLIs;
   Healthy, Not collecting, Moving for datasources; Connected, Unreachable, Never
   connected, Disabled for agents. Filled circle is healthy, open circle is stale
   or absent data, triangle is warning, square is breached. A label never
   contradicts the urgency group that contains it. Marks never sit inside a
   heading; they go on a body-font status line under it.
4. **Pickers are type-ahead comboboxes.** Datasource (SLI form), SLI and service
   (SLO form) and agent (datasource form) pickers are a text input with a filtered
   listbox whose options carry a status mark and a secondary line, keyboard
   navigation, a no-match action and disabled options; the selection collapses to
   a summary card with a Change action. This amends the design guide's "select
   options are native options" rule for pickers that need state context. Fixed
   small choices stay native or segmented.
5. **Presets as chips with a self-applying custom chip.** Target, rolling window,
   cadence and credential expiry use choice chips; a dashed "Custom…" chip
   reveals an input, applies as you type, and relabels itself. Windows are whole
   days; bounds appear as validation, not hints.
6. **Measurement section.** Measurement kind is a segmented control in the
   section header with unavailable kinds disabled; good and total query editors
   sit side by side; the counts-versus-rate choice is a small toggle in the
   editors' footer.
7. **Type and spacing.** Hints, errors and sublines are 13 px minimum; 12 px only
   for labels, ticks and captions. One card inset token (24 px desktop, 14 px
   mobile) governs card headers, bodies, first and last table cells, pagers and
   lane labels.
8. **Agent registration is a page**, not a dialog: Name → Token → Start, with the
   start step showing the agent summary, environment-specific commands and a live
   checklist that fills as the agent connects.

The approved mocks replace the slo-tracker screenshots as the side-by-side
acceptance reference under SPEC-0012 AC-11. The left navigation and application
shell are unchanged.

## Alternatives considered

- Keep the slo-tracker reference and fix only the inconsistencies. Rejected: the
  reference itself has the two-column detail and native selects the maintainer
  found wanting, and it does not cover datasources or agents.
- Keep the right rail and tighten its alignment. Rejected after rendering: with
  two short cards the rail ends a third of the way down and the queries, which are
  the definition, are squeezed into 320 px.
- Selectable cards instead of type-ahead for pickers. Mocked both; the maintainer
  chose type-ahead for its scaling and compactness, with health shown in options.

## Consequences

- Design-system additions: `Combobox`, `SelectionSummary`, choice chips, segmented
  control, unified `StatusMark`, list header/toolbar/pager composition, card inset
  token and the 13 px hint rule, each with catalogue states in both themes and at
  mobile width before feature screens consume them.
- SPEC-0012, the design guide and the visual reference are amended in the same
  change that accepts this record. Earlier F3-T07 visual acceptance against the
  slo-tracker reference no longer establishes acceptance; each screen is accepted
  against its mock.
- The SLO detail composition change requires re-running the traced performance
  acceptance from F3-T08; budgets are unchanged.
- Mock elements that need backend or contract changes are deferred and listed in
  the mocks README; the first round is UI-only under plan 0025.

## Revisit conditions

Revisit if real use shows the single-column detail hides the status below the
fold on common screens, if the type-ahead proves slower than a select for
installations with one datasource, or when deferred backend work (descriptions,
revisions, annotations, list projections) lands and the mocks need updating.
