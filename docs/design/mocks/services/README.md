# Service catalog visual proposal

Status: approved visual reference for implementation under [ADR-0036](../../../adr/0036-service-catalog.md). The HTML itself is a sample-data prototype.
Scope follows the reviewed [catalog research](../../../research/service-catalog.md).

Open [index.html](index.html) directly in a browser. The prototype reuses the existing
mock shell, tokens, Bevan font, form sections, buttons and list patterns. Its HTML
represents proposed shared-control composition; it does not import production React
components or call application APIs. All examples are fictional and changes stay
in memory until the page reloads.

## Screens

Use the prototype navigation or the following fragment identifiers:

| Fragment | Review |
| --- | --- |
| `#list` | See service SLO status; search by name, owner or label; filter by SLO coverage/owner; paginate 10/20 rows |
| `#service/checkout` | Service context, individual SLO states, dependencies, labels, arbitrary links and deletion safeguards |
| `#new` | Create a service with optional ownership, context, multi-value labels, arbitrary named links and dependencies |
| `#edit/checkout` | Edit metadata without changing SLO definitions; validation, save errors, conflicts and discard confirmation |
| `#dependencies/checkout` | Open the dependency section within service details; edit relationships through Edit service |
| `#compare` | Compare two dependency visualization approaches using the same seven services |
| `#map/a` | Service focus with direct relationships and a selected-service summary |
| `#map/b` | The same services grouped by owner |
| `#components` | Individual controls/states, blocked and permitted deletion, service selection and inline creation within an SLO form |
| `#slo-component/checkout` | Start an SLO from a service, change the selected service or create one inline while keeping the objective draft |

The review controls above the mock select Normal, Empty, Read error, Refresh error,
Save conflict or Save error, and Editor/Viewer access. Use the shell theme button,
or `?theme=dark`, for dark mode. Narrow layouts stack tables and replace the graph
with accessible relationship lists. The mobile menu and native dialogs are usable
with the keyboard. On service details, the Dependencies review selector compares
a connected relationship view with a simpler card layout. It is a mock review
control, not a proposed product setting. Both presentations link to related services;
narrow screens use cards grouped under Used by and Depends on.

## Decisions still represented as proposals

- Details open with one information card containing the description, owner and
  available contact, labels and useful links. Bold field headings and the owner
  name establish hierarchy; email contacts link to a mail client. SLO results
  ordered by urgency and dependencies follow.
  SLO result counts do not invent an overall service availability.
- Catalog rows open details on click, retaining native name links, text selection
  and the separate dependency links within each row.
- Table / Graph tabs expose the full dependency map from Services. The graph
  colors each service using its own SLO summary and explains the states in a
  legend. Unknown includes stale, incomplete and unavailable results; No SLOs
  stays neutral. Selection keeps the status color and uses an outer orbit ring.
  Empty owners do not add a placeholder or leading separator to graph captions.
- The catalog's SLO status summarizes each service's own objectives: any breached
  SLO makes it Breached; otherwise an incomplete result makes it Unknown; all
  objectives must be evaluable and on track for On track. A service without
  objectives shows No SLOs. Counts appear in the tooltip and accessible label,
  including unknown results alongside a breach. This summarizes compliance across
  each SLO's own window, not live service availability or dependency health.
- Services have stable UUIDs. The display name and human-readable identifier are
  editable; changing either preserves SLO and dependency references. Readable
  fixture routes such as `#service/checkout` are mock aliases for those UUIDs.
  The maintainer selected this direction on 2026-11-06. Before product
  implementation, update the accepted immutable import-key contract in
  [SPEC-0005](../../../specs/0005-openslo-and-resource-model.md) and define the
  separate OpenSLO import mapping behavior.
- Owner is optional free text plus contact, without suggestions, a team directory
  or an account binding.
- Lifecycle and criticality are deferred until their meanings and allowed values are defined.
- Labels use a token input: type `key:value` and press Enter, then remove with ×.
  Read-only tags and editable chips share a rounded shape, subtle tint, muted key
  and emphasized value. Long labels wrap. Repeated keys can have different values;
  exact duplicates are rejected.
- Links use one Name/URL entry area. Enter adds a compact list item with a remove
  action; saved links do not remain editable input rows.
- Dependencies extend the user-role type-ahead pattern with removable chips for
  multiple selected services. Form selection stays in the draft until Save;
  inverse relationships are derived.
- Graphs use circular service nodes with orbit rings, a dotted canvas and curved
  directional connections. Selecting a node emphasizes its direct relationships;
  the selected ring denotes focus, not health. The connected detail view uses the
  same circular visual language and curved arrows, aligned to the nodes as the
  layout resizes. Graphs show declared dependencies without
  propagating SLO status or impact.
- The graph uses a fixed-height canvas with drag panning, pointer-centered wheel
  zoom and explicit zoom controls. Reset view clears filters/search and restores
  the loaded catalog. Status chips filter service summaries and can be combined;
  the type-ahead finder shows owner and status and centers the chosen service.
  Arrow keys
  pan, +/− zoom and 0 fits all. The duplicate Relationships table is removed;
  mobile keeps the selected service's navigable relationships.

Maintenance, integrations, source-based editing restrictions, an Environment resource
and composite SLOs are outside these mocks. UI/CLI/API parity is a product contract;
this prototype illustrates UI flows only. Backend and authentication behavior, browser
history/draft recovery, and production shared-control wiring need implementation
verification later. Sample SLO values are illustrative fixtures, not calculations.


The 50-service refinement replaces fixed columns with deterministic positions
computed from dependency links and label separation. Positions do not change on
selection. Owner groups use compact, separately packed regions, and group headings
stay readable when zooming. Nodes show service names and status; owner/count details
remain in hover text, the service list and selected-service summary. Explicit focus
dims unrelated context; Reset view restores the full overview. The reference retains a
snapshot of the same layout math; rendered geometry and independent topology/layout
acceptance checks cover the result.

The 2026-11-08 refinement retains Service focus and Owner groups, removes List &
map, and replaces framing actions with Reset view. The shared type-ahead picker
shows service context before selection. On track, Breached, Unknown and No SLOs
chips filter each service’s own aggregate status; no selection means all statuses,
and multiple selections combine as alternatives. Reset clears filters and search.
Circle tint and stroke use semantic status colors without inheriting dependency
health; missing or stale results remain Unknown.

Status filters retain each matching service's direct dependencies and dependents
as muted context, including their connecting arrows. Context remains selectable,
keeps its own SLO color and becomes fully readable on hover, keyboard focus or
selection. Context does not expand recursively or count as a filter match. The
finder, Reset view, zoom and help share one desktop toolbar row; narrow screens
wrap the finder. Direction and gesture explanations live in the compact help
control and remain available to assistive technology.
