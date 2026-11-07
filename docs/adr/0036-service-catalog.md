# ADR-0036: Service catalog and dependency exploration

- Status: accepted
- Created: 2026-11-07
- Accepted: 2026-11-07
- Approval: The maintainer approved the service catalog mocks and requested pixel-by-pixel implementation on a new worktree from main.
- Related specs: [SPEC-0005](../specs/0005-openslo-and-resource-model.md), [SPEC-0010](../specs/0010-api-and-cli.md), [SPEC-0012](../specs/0012-user-interface.md)
- Supersedes / superseded by: amends service-name immutability in SPEC-0005 and SPEC-0010; other resource import keys remain unchanged

## Context

Services already have UUID identity and SLO references, but lack independent catalog
management and dependency relationships. The maintainer approved the interactive
[visual reference](../design/mocks/services/README.md), including editable names,
owner/contact text, labels, links, SLO summaries and circular dependency graphs.

## Decision

Implement the reference using existing shared React controls and versioned service
API/CLI operations. UUIDs remain immutable. Service identifiers and display names
are editable; changing either preserves every SLO and dependency reference.
OpenSLO import identity is separate from the editable native identifier: a future
importer must retain explicit immutable source mappings and cannot infer a rename
or create a duplicate solely because the native name changed.

Services have optional description, owner and contact text, multi-value labels,
named HTTP/HTTPS links and explicit outgoing service UUID dependencies. Owner text
has no account, team-directory or permission meaning. No lifecycle, criticality,
environment entity, integration ownership, maintenance or composite SLO behavior
is introduced. Preserve annotations for future OpenSLO interoperability.

Reject self references, duplicate edges, missing/archived targets and duplicate
label pairs. Cycles are allowed and graph traversal must terminate. Used by is
derived, never independently edited. Serialize relationship writes and archival
so a concurrent SLO/dependency create cannot reference an archived service. Delete
archives only an unreferenced service; return blocking SLO/service UUIDs otherwise.
Keep expected versions, current session authorization, CSRF, audit and idempotent
create receipts. Old two-field create and display-name-only update requests remain
compatible. Omitted optional patch fields retain their existing values.

Bound description to 4,000 characters, owner to 200, contact to 320, and labels to
100 key/value pairs (keys use the existing 63-character name grammar, values at
most 256). Allow 50 named links (80-character name, 2,048-character credential-free
HTTP/HTTPS URL), and 100 outgoing dependencies. Management bodies retain the 1 MiB
cap. Lists retain stable, filter-bound cursor pagination, default 50/max 200; the
UI selects 10/20 rows. Search covers name, identifier, UUID, description, owner and
labels. Owner and SLO-coverage filters compose with search.

SLO status summarizes only the service's own objectives and their individual
windows: any current complete violation is Breached; otherwise any missing, stale,
incomplete or unavailable result is Unknown; every result must be complete and met
for On track. No objectives means No SLOs. Preserve all counts in accessible text
and tooltips, including unknown results beside a breach. Use bounded maintained
projection reads; missing/stale projections never become success. Neither graph
selection nor a dependency propagates status or implies live service availability.

Graphs use circular nodes, orbit focus rings, a dotted canvas and curved directed
arrows. Preserve the approved focus, owner-group and list/map arrangements with
deterministic layouts for real records; no force-layout dependency is required.
Narrow screens provide navigable relationship lists. Review controls, comparison
copy, fixture data and record-history disclosures do not enter the product.

## Alternatives considered

- A second catalog CRUD resource would duplicate existing service identity.
- Immutable native service identifiers contradict the approved editable-name design.
- Health inheritance, averaged budgets and integration edit locks add unapproved semantics.
- Hard deletion loses history; unrestricted archival allows dangling active references.

## Consequences

Add a preserving migration, generated service API contracts, API/CLI parity and
connected catalog screens. Verify actual persisted journeys, permissions, conflicts,
cycles and reference geometry in both themes at desktop and mobile widths.

## Revisit conditions

Define explicit source mapping before implementing OpenSLO import. Revisit graph
rendering only when measured catalog scale or interactions require another engine.
