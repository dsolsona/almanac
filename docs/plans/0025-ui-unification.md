# UI unification: implement the approved mocks

- State: accepted 2026-11-04 (Crit session `9a46588aaaa4`, no comments); F6-T01 in review, F6-T02 next
- Created: 2026-11-04
- Scope: UI-only; every element needing backend or contract changes is deferred (see the [mocks README](../design/mocks/README.md))
- Base: merged `main` at `2f059e7` (PR #53); all earlier PRs merged
- Owner: primary agent; delegation limited to frozen-candidate verification lanes and, if useful, nothing else in this round
- Governing records: [ADR-0028](../adr/0028-unified-operational-ui.md), [SPEC-0012](../specs/0012-user-interface.md), [SPEC-0018](../specs/0018-agent-management-usability.md), [SPEC-0019](../specs/0019-backend-performance-and-derived-data.md), [ADR-0025](../adr/0025-fast-operational-journeys-and-tracing.md), [design guide](../../web/DESIGN_SYSTEM.md), [visual reference](../design/slo-visual-reference.md)

## Goal

Bring the SLI, SLO, datasource and agent screens to the approved mocks so the
whole application reads as one product, using only data the current API already
returns. Accuracy, exact values, keyboard access, both themes, mobile layouts and
the accepted performance budgets are unchanged requirements.

## Sequence

Each task is a usable outcome with its own record, acceptance checklist and
side-by-side comparison against its mock. Tasks are sequential; each branches from
the integrated result of the previous one.

- [ ] **F6-T01 Design-system foundation.** `Combobox`, `SelectionSummary`, choice
  chips (`ChoiceChips`), segmented control, unified `StatusMark` (replacing
  `StatusBadge` and the SLO `ReferenceStatus`), list header/toolbar/pager
  composition, card inset token, 13 px hint rule, textarea field. Every state in
  the catalogue, both themes, 360/1024/1440. No feature screen changes. Design
  guide updated.
- [ ] **F6-T02 SLO list and detail** ([F6-T02](../tasks/F6-T02.md), in review). Triage band, grouped rows with status
  vocabulary and Target · window column, header refresh; single-column detail with
  status band, timeline, definition. Edit SLO page limited to display name (the
  only editable field today). Re-run traced performance acceptance for list and
  detail; Events lane, Mute and revision history deferred.
- [ ] **F6-T03 SLI list and detail** ([F6-T03](../tasks/F6-T03.md), in review). Dense table with health filter, last-24 h
  strip reusing `SliRecentStrip`, last sample as ratio, compact SLO links,
  inline "Create SLO"; single-column detail with tiles, measurements chart from
  the observation history, intervals table with outcome filter, definition,
  objectives. Description deferred.
- [ ] **F6-T04 SLI and SLO forms.** New SLI with Identity → Source (type-ahead)
  → Measurement (segmented kind, side-by-side editors); New SLO with Identity
  (service type-ahead, existing services only) → Indicator (SLI type-ahead) →
  Objective (chips) → Alerting placeholder. Query test, units toggle, inline
  service creation, budget preview and descriptions deferred.
- [ ] **F6-T05 Datasources.** List with health derived from agent reachability
  and reassignment state, agent, cadence, auth and SLI count (last query deferred);
  detail with status line, banner, tiles, Connection (auth block, cadence and TLS
  segmented, secret summary card), Agent (summary card, move), Test (connection
  test and preview as today), delete section; create page with provider cards and
  agent type-ahead.
- [ ] **F6-T06 Agents.** List with status, name, datasources by name, collection
  state (heartbeat strip and query ratio deferred); detail with status line,
  banner, tiles, datasources table, credentials table, "How to run" dialog,
  collapsed runtime details, quiet disable/delete line; registration page with
  three steps and the live checklist polling existing runtime status.

## Acceptance for every task

- Side-by-side comparison of the running page with its mock in both themes at
  360, 1024 and 1440 px, recorded in the task with concrete differences.
- Keyboard and focus through every new control; errors and empty states; exact
  values unchanged; continuous navigation and browser history.
- Focused unit and component checks while editing; one real journey early; the
  broad gates (catalogue matrix, Compose journey, integration selection) on the
  frozen candidate. Performance traces where SPEC-0019 applies.
- Deferred mock elements listed explicitly in the task as out of scope.
- Commits authored as `Daniel Solsona <dsolsona@gmail.com>`; publish only when
  authorized; no merge without authorization.

## Out of scope

Backend or contract changes of any kind, new providers, alerting, annotations,
SLO revisions, descriptions, query tests for unsaved SLIs, list projections for
datasource and agent health history. These remain in the mocks for a later round.
