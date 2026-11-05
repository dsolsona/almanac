# Proposals from the 2026-11-05 review

Status: approved 2026-11-05 ("Yes, I like this, please apply it everywhere"); recorded in [ADR-0031](../../../adr/0031-list-grammar-and-button-roles.md), health as the chip column (variant A). These revise parts of
[ADR-0028](../../../adr/0028-unified-operational-ui.md) decision 1 (the list
header with a count subline and "Updated hh:mm:ss UTC", and health as the first
column) and add button roles the design guide does not define yet. Once chosen,
a new ADR supersedes the affected part of ADR-0028 and the screens follow.

Open the HTML files in a browser (append `?theme=dark` for dark). The `png/`
folder holds desktop light renders made in the Linux dev container, so monospace
text looks narrower than in a Mac browser.

| Mock | What it proposes |
| --- | --- |
| `ds-list.html` | List grammar: title and one primary action on one line; no count subline or "Updated"; one aligned toolbar with refresh as a quiet icon; name-first rows; health as a chip (`?variant=a`) or a status rail (`?variant=b`); "UTC" in the time column header; a freshness notice only when a refresh fails. |
| `agents-list.html` | The same grammar for agents; no breadcrumb on a top-level page; Register agent in the same place as every primary action. |
| `agent-detail.html` | Breadcrumb line above the title on every detail page; one primary action; compact one-row-per-token credentials with a text Revoke and older tokens folded; a danger zone at the end. |
| `sli-list.html` | Same grammar; secondary filters as filter chips in the toolbar row; type and datasource folded into the name subline. |
| `slo-list.html` | Same head and toolbar; urgency groups and triage tiles kept; name-first rows with status chips. |
| `buttons.html` | Button roles: primary, secondary, quiet, inline, destructive, confirm destructive, selected; when to use each. |

## Decisions for the maintainer

1. Health as a chip column (A) or a status rail (B).
2. Remove "Updated hh:mm:ss" and the count subline everywhere, with freshness shown only on a failed refresh.
3. Remove the datasource list's Agent filter (search covers agent names) and its Auth column.
4. Adopt the button roles for the design guide.
