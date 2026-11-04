# UI mocks: unified SLI, SLO, datasource and agent screens

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
| `slo-list.html` | SLO list with triage band and urgency groups | planned, F6-T02 |
| `slo-detail.html` | Single-column SLO detail: status band, timeline, definition | planned, F6-T02 |
| `slo-create.html` | New SLO: Identity → Indicator → Objective → Alerting | planned, F6-T04 |
| `slo-edit.html` | Edit SLO with revision banner and history | planned, F6-T04 |
| `sli-list.html` | Dense SLI table with health filter and 24 h strip | planned, F6-T03 |
| `sli-detail.html` | Single-column SLI detail: tiles, chart, intervals, definition, objectives | planned, F6-T03 |
| `sli-create.html` | New SLI: Identity → Source → Measurement with query test | planned, F6-T04 |
| `ds-list.html` | Datasource list with health, agent, cadence, SLI count | planned, F6-T05 |
| `ds-detail.html` | Datasource detail: status, tiles, Connection, Agent, Test, delete | planned, F6-T05 |
| `ds-create.html` | New datasource: Identity → Connection → Agent with test result | planned, F6-T05 |
| `agents-list.html` | Agent list with heartbeat strip, datasources, collection | planned, F6-T06 |
| `agent-detail.html` | Agent detail: status, tiles, datasources, credentials, run dialog | planned, F6-T06 |
| `agent-register.html` | Three-step registration page with live first-heartbeat checklist | planned, F6-T06 |

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
