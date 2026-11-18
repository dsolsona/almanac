# Maintenance review prototype

[Open the revised prototype](http://127.0.0.1:5199/maintenance-preview.html).
The original address redirects here. This version imports actual Almanac React
controls, production CSS, the application shell and SLO chart renderer. It uses
50 fictional services and 147 objectives; changes stay in memory.

## Review journeys

- [Schedule](http://127.0.0.1:5199/maintenance-preview.html#new): Context →
  Scope → Schedule. Use the shared multi-value combobox to search services or
  owners, add/remove selected tokens and inspect affected SLOs. Search results
  are bounded to eight; typing searches the whole fixture, including service 50.
  The impact dialog searches and paginates ten objectives at a time. Search stays first at a constant width, with selected chips wrapping in a bounded area underneath. The outer search control carries one focus border; the embedded input has no extra red rectangle.
- [Maintenance](http://127.0.0.1:5199/maintenance-preview.html#list): existing
  resource-list grammar, search, Upcoming/Active/History, 10/20 rows and refresh.
  Open a row for schedule, reason, affected services and SLOs, then audit history.
- [SLO detail](http://127.0.0.1:5199/maintenance-preview.html#slo): accepted
  budget-in-context composition with the actual chart renderer, aligned shaded
  bands, accessible window inspection, Without maintenance / Including maintenance views and excluded states.
- [Service entry](http://127.0.0.1:5199/maintenance-preview.html#service):
  schedule with Checkout preselected; inspect active maintenance and objectives.
- [Alerts](http://127.0.0.1:5199/maintenance-preview.html#alerts): preserve
  pre-existing incident state and explain scoped notification suppression.

The quiet **Review prototype · demo** button opens screen navigation, theme,
role and failure/overlap/excluded/pending scenarios. Review controls are outside
the proposed product flow. The shell includes a prototype-only Maintenance item.
The shared Combobox provides search-first selection. Maintenance routes in this preview use fictional data.

## Design evidence

Screenshots from the running prototype use fictional data.

| Journey | Evidence |
| --- | --- |
| Find and open a maintenance window | [Maintenance list](screenshots/maintenance-list.png) |
| Schedule maintenance across services | [Desktop form with twelve selections](screenshots/schedule-desktop.png) · [Dark mobile form](screenshots/schedule-mobile-dark.png) |
| Inspect scope | [Affected SLOs dialog](screenshots/affected-slos.png) |
| Manage an active window | [Active window detail](screenshots/active-window.png) |
| Explain a shaded interval and the rolling result | [SLO graph annotation](screenshots/slo-annotation.png) |
| Inspect an incident during maintenance | [Alerts](screenshots/alerts.png) |

## Design choices

The form starts with title and reason, followed by searchable service/SLO selection and an affected count. All services is one explicit choice with a compact summary, not a huge
selected catalogue. An on-demand impact dialog keeps the form short at fleet
scale. DatePicker and ChoiceChips preserve the existing calendar and preset
patterns. The custom duration editor replaces the Custom chip inline; narrow layouts wrap the complete editor together. One short UTC preview shows when the window ends. Context supplies the
title and reason that operators will later inspect on the graph.

Detail leads with current effect and end time, then schedule/reason and navigable
scope. Audit history and historical correction stay secondary. Notification
suppression is shown where operators inspect alerts. SLO charts use the same
production renderer and point inspection. Each chart labels its maintenance band; hovering the band gives the name, bounds, reason and sample context, while click, tap or the keyboard-accessible legend opens the owning window. Numeric tooltips can paint above adjacent cards; maintenance context uses a viewport-bounded portal. These behaviors are scoped to this prototype.

## Run and share

Run both processes from the task worktree:

```sh
make shell SOCKET=1 CMD='make preview'
make shell SOCKET=1 CMD='python3 docs/design/mocks/maintenance/serve.py'
```

Vite listens on guest loopback 5198; the restricted review proxy serves port 5199.
From a laptop that can reach the guest, use the guest address and port 5199.
For SSH forwarding, use the configured guest identity:

```sh
ssh -N -L 5199:127.0.0.1:5199 dev@YOUR_VM_ADDRESS
```

Then open `http://localhost:5199/maintenance-preview.html`.
See [microVM preview guidance](../../../firecracker.md#preview-and-retrieve-work).

## Focused verification

```sh
make shell CMD='cd web && ./node_modules/.bin/tsc -p review-maintenance/tsconfig.json'
make shell SOCKET=1 CMD='node web/review-maintenance/verify.cjs'
make shell SOCKET=1 CMD='node web/review-maintenance/feedback.cjs'
make shell SOCKET=1 CMD='node web/review-maintenance/picker-layout.cjs'
```

Browser checks use the network preview, both themes and 1440/1024/360 px widths.
Screenshots are in `.local/maintenance-review-v2/`, including selected services,
searching the fiftieth service, the affected-SLO dialog and annotation inspection.

The historical action is labelled **Include maintenance events…** under **Change historical SLO calculations**. It restores both successful and failed events to new calculations, with a reason and explicit consequence confirmation. **Without maintenance** is the compliance view; **Including maintenance** is comparison only.

## Limits

This is a design prototype, not application acceptance. It performs no API calls,
server authorization, maintenance writes, recalculation, membership updates or
collection-grid validation. Save failure/conflict scenarios retain local drafts.
Short-range plotted samples, 30-day summary counts and completed-day examples are
separate fictional fixtures. Full service/SLO CRUD and the complete existing alert
screen are outside this mock; their entry surfaces demonstrate maintenance context.

Governing proposals: [SPEC-0023](../../../specs/0023-maintenance-management.md) and
[ADR-0053](../../../adr/0053-service-maintenance-workflow.md). Mock review and
backend implementation approval remain separate.
