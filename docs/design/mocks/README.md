# UI mocks: unified SLI, SLO, datasource and agent screens

Alerting exploration, 2026-11-12: [five interactive directions](alert-options/README.md)
cover active alerts, evidence, global acknowledgement, history and new-alert-policy
flows. Refined A and the current single-page creation flow are the selected visual
baseline. [SPEC-0022](../../specs/0022-alert-management-ui.md) records the draft
production contract; the static mocks do not implement the alert engine.

SLO detail exploration, 2026-11-12: [six interactive proposals](slo-detail-options/README.md)
compare three evolutions and three radical compositions. Refined B is accepted
for SLO detail under [ADR-0049](../../adr/0049-slo-budget-in-context-detail.md);
the other directions remain proposals.

Approved SLI detail update, 2026-11-11: [mockup E](sli-detail-options/refined.html)
governs the SLI detail under [ADR-0046](../../adr/0046-sli-measurements-and-collection-detail.md).
It replaces the four tiles with an integrated graph summary, exact interval
inspection and aligned collection squares. Original A–D remain for comparison.

Approved design, 2026-11-06: [Settings and user administration](settings/README.md)
has clickable Settings, account, user list/add/edit and password journeys. It is
approved for implementation with pixel parity, including its settings-specific table sizing.

The maintainer reviewed and approved these mocks on 2026-11-04, and accepted
[ADR-0028](../../adr/0028-unified-operational-ui.md) and [plan 0025](../../plans/0025-ui-unification.md)
in Crit the same day, making them the visual and interaction reference for Almanac's operational and administration screens. They
replace the earlier slo-tracker screenshots as the acceptance reference; see
[ADR-0028](../../adr/0028-unified-operational-ui.md) and the
[visual reference](../slo-visual-reference.md).

Open any `*.html` file in a browser. Append `?theme=dark` for the dark theme and
narrow the window for the mobile layout. `mock.css` copies the production tokens;
`mock.js` draws the shared shell. Pickers and chips are interactive. The `png/`
folder holds desktop light renders for quick comparison; the HTML is the reference.

| Mock | Screen | Implemented by |
| --- | --- | --- |
| `slo-list.html` | SLO list with triage band and urgency groups | [F6-T02](../../tasks/F6-T02.md), in review |
| `slo-detail.html` | Single-column SLO detail: status band, timeline, definition | [F6-T02](../../tasks/F6-T02.md), in review (Events lane, Mute, edit page deferred) |
| `slo-create.html` | New SLO: Identity → Indicator → Objective → Alerting | [F6-T04](../../tasks/F6-T04.md), in review (description, budget preview deferred) |
| `slo-edit.html` | Edit SLO with revision banner and history | deferred (revision contract); display-name dialog from F6-T02 |
| `sli-list.html` | Dense SLI table with health filter and 24 h strip | [F6-T03](../../tasks/F6-T03.md), in review (health filter deferred) |
| `sli-detail-options/refined.html` | Approved SLI detail: integrated measurement summary, interval states and exact inspection | [F6-T10](../../tasks/F6-T10.md) |
| `sli-detail.html` | Historical SLI detail with four tiles | Superseded by ADR-0046 |
| `sli-create.html` | New SLI: Identity → Source → Measurement with query test | [F6-T04](../../tasks/F6-T04.md), in review (description, units toggle, query test deferred) |
| `ds-list.html` | Datasource list with health, agent, cadence, SLI count | [F6-T05](../../tasks/F6-T05.md), in review (last query deferred) |
| `ds-detail.html` | Datasource detail: status, tiles, Connection, Agent, Test, delete | [F6-T05](../../tasks/F6-T05.md), in review (last test summary, delete deferred) |
| `ds-create.html` | New datasource: Identity → Connection → Agent with test result | [F6-T05](../../tasks/F6-T05.md), in review |
| `agents-list.html` | Agent list with heartbeat strip, datasources, collection | [F6-T06](../../tasks/F6-T06.md), in review (heartbeat strip, row datasources deferred) |
| `agent-detail.html` | Agent detail: status, tiles, datasources, credentials, run dialog | [F6-T06](../../tasks/F6-T06.md), in review |
| `agent-register.html` | Three-step registration page with live first-heartbeat checklist | deferred by [F6-T06](../../tasks/F6-T06.md): the token and uncertain-intent flows are built on the registration dialog; needs its own task |

Proposals from the 2026-11-05 review (list grammar, health chips, button roles) are in [proposals](proposals/README.md); approved the same day and recorded in [ADR-0031](../../adr/0031-list-grammar-and-button-roles.md). Where they differ from the mocks above, the proposals win.

## Deferred elements

The first implementation round (plan [0025](../../plans/0025-ui-unification.md))
is UI-only. The following mock elements need backend or contract changes and are
deferred to a later round; implement the screen without them and keep the mock as
the target.

| Element | Mock | Needs |
| --- | --- | --- |
| Description field | SLI and SLO forms, lists, details | `description` on SLI and SLO resources |
| "Queries return: per interval / per second" toggle | `sli-create.html` | SLI measurement unit and collector conversion |
| Query test panel before saving | `sli-create.html` | Unsaved-query diagnostic endpoint reusing the datasource preview path |
| SLO edit as a definition revision with reason and history | `slo-edit.html` | Revision write and history read |
| Inline service creation from the service picker | `slo-create.html` | Create request accepting a new service, or transactional two-step |
| Events lane with annotations, revisions and maintenance | `slo-detail.html` | SPEC-0017 annotations and later F4 domains |
| Budget preview ("what this target means") | `slo-create.html` | Recent-traffic estimate read |
| Datasource health, last query time and duration, last successful test | `ds-list.html`, `ds-detail.html` | List projection and persisted diagnostic summary |
| Heartbeat strip, query success ratio, credential expiry on rows | `agents-list.html`, `agent-detail.html` | Heartbeat history and list projection |
| Live first-heartbeat checklist | `agent-register.html` | Polling read of runtime status during registration (runtime status exists; wiring is UI work and is in scope) |
| Provider cards for Tempo and Loki | `ds-create.html` | Later providers; shown disabled in scope |
| "Mute" action on SLO detail | `slo-detail.html` | F4 alerting; shown disabled in scope |

Where a column's value cannot be computed from today's API, omit the column rather
than show a placeholder. Record each omission in the implementing task.

## Service catalog reference

The approved [service catalog reference](services/README.md) governs F8-T01 under ADR-0036.
