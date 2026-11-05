# ADR-0031: Name-first lists, quiet freshness and button roles

- Status: accepted
- Created: 2026-11-05
- Accepted: 2026-11-05
- Approval: the maintainer reviewed the proposal mocks in [docs/design/mocks/proposals](../design/mocks/proposals/README.md) on 2026-11-05 and answered "Yes, I like this, please apply it everywhere", accepting the four recommendations: health as a chip column, no count subline or "Updated" line, no datasource Agent filter or Auth column, and the button roles
- Related specs: [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: supersedes the list header and row order in decision 1 of [ADR-0028](0028-unified-operational-ui.md) (mono count subline, "Updated hh:mm:ss UTC" beside the actions, status as the first column); the rest of ADR-0028 stands. Table details of decision 3 amended by [ADR-0032](0032-centred-list-tables-and-data-state-vocabulary.md)

## Context

Reviewing the merged screens, the maintainer found the list grammar of
ADR-0028 noisy and inconsistent: a count subline ("1 datasource · 1 needs
attention · all times UTC") repeating the filter counts, an "Updated
hh:mm:ss" line at the top of every list and detail page, health as the first
column so rows did not start with what they are, a datasource Agent filter
and label misaligned in the toolbar, actions placed differently per page
(the agents title sat apart from its Register and Refresh buttons), an agent
credential history using a card per token with a full-size revoke button, and
every detail action rendered as the same outline button. The design guide
listed button variants without saying when to use each.

## Decision

1. **List head:** the title and the page's one primary action share a line.
   There is no count subline and no "Updated" line on lists or detail pages.
2. **Toolbar:** one aligned row: search, the segmented health filter, any
   secondary filter as a filter pill at control height, then a quiet icon
   refresh at the far end whose tooltip carries the last refresh time. A failed
   refresh is reported by the page's existing notice. The datasource Agent
   filter and the list's Auth column are removed (search matches agent names;
   auth stays on the detail page).
3. **Rows:** the name leads every row and is the link. Health is a chip in the
   second column using the existing shape vocabulary in a tinted pill; its
   reason line appears only when the state needs attention. "UTC" sits in the
   header of a column that shows times. The SLO list keeps its urgency groups
   and triage tiles.
4. **Detail pages:** a breadcrumb line above the title, the actions on the
   title line, a quiet refresh, at most one primary action, and destructive
   actions as danger-outline buttons in a danger zone at the end.
5. **Agent credentials:** one row per token: state chip, identifier, the dates
   that matter for that state (readable, with the exact recorded value in the
   tooltip) and a text-style Revoke that confirms in a dialog.
6. **Button roles:** primary (one main action per view; dialog confirm),
   secondary/outline, quiet/ghost, inline/link, danger-outline (starts a
   destructive action), filled danger (only confirms one in its dialog) and
   selected (pressed toggle state). Recorded in the design guide.

## Consequences

The shared `PageHeader` loses its `updatedAt` prop; `RefreshButton`,
`FilterSelect`, `ToolbarSpacer` and `StatusChip` join the shared controls and
the catalogue. Register agent is a list action only. Verifiers and tests that
named the generic "Refresh" button, the count subline or the credential cards
follow the new markup. Folding revoked and expired credentials under "Show
earlier credentials", shown in the proposal mock, is not implemented.

## Revisit conditions

If operators need to see freshness at a glance (for example, a wall display),
add an explicit freshness indicator as a reviewed design change rather than
restoring the per-page "Updated" line.
