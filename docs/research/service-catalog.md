# Service catalog research and options for Almanac

Research date: 6 November 2026. Status: research and recommendations for discussion, not an accepted specification or implementation plan.

**Almanac should grow its existing service records into a focused reliability catalog:** a place to find services, identify their owners, see their SLOs, and record dependencies. The starting point is a searchable list, a useful service page, and the same management operations for UI, CLI, and automation.

The current scope is service management through the UI, CLI and API. API callers may be people or automation; the catalog does not distinguish them by how the request arrives. Dedicated integrations, synchronization ownership and source-based editing restrictions are deferred. Environments remain ordinary labels, and dependency visualization is deferred until several UI designs can be compared. Composite SLOs are outside this phase. The field set, API additions, and sequencing below remain proposals.

## What Almanac already has

The current foundation is larger than the navigation suggests. These are source-level findings from `main` at `dfc6b3a`; they do not establish runtime verification of the current deployment.

| Area | Current implementation | Catalog implication |
| --- | --- | --- |
| Service identity | UUID, unique immutable name, mutable display name, version, creation/update timestamps | Keep UUID identity; the proposed editable name requires changing the current name/import-key contract |
| HTTP API | List/create at `/api/v1/services`; read and metadata patch at `/api/v1/services/{service_id}` | Reuse this resource family; there is no need for a second catalog CRUD API |
| CLI | `services list`, `get`, `create`, `update` | Extend existing noninteractive commands alongside the API |
| UI | New SLO has an existing-service picker and inline creation; service links open a basic detail/rename page | The missing journey is an independent services list and creation entry point, plus richer details |
| Relationships | Each SLO definition references one service and one SLI; an SLI can serve multiple SLOs | SLOs already belong to services; service-to-service dependencies do not exist yet |
| Authentication | Service HTTP operations validate signed-in sessions; browser mutations use CSRF protection | Reuse current authorization through UI, CLI and API; additional credential types need separate scope |

Evidence: [OpenAPI](../../api/openapi.yaml), [service HTTP handler](../../internal/slohttp/handler.go), [storage](../../internal/storage/objectives.go), [CLI](../../internal/command/objectives.go), [SLO and service UI](../../web/src/slos/SlosPage.tsx), [application shell](../../web/src/components/ui/ApplicationShell.tsx).

The accepted [resource model](../specs/0005-openslo-and-resource-model.md) already defines service identity, labels and annotations, and archival that preserves history. Referenced resources cannot simply be deleted. The catalog should build on these existing rules.

## How established products approach catalogs

Product behavior in this comparison comes from primary documentation accessed on the research date. The final column is a recommendation for Almanac.

| Product | Documented approach | Useful lesson for Almanac |
| --- | --- | --- |
| Backstage | Typed entities describe components, resources, APIs, systems and owners. Component descriptors contain type, lifecycle, owner and optional dependencies. Its catalog guidance favors Git as the authority and treats the catalog as a projection of source definitions. | Keep durable identities, ownership and explicit relationships. Git-based catalog management is useful precedent for later, rather than a requirement for the first version. [Entity format](https://backstage.io/docs/features/software-catalog/descriptor-format/), [catalog graph](https://backstage.io/docs/features/software-catalog/creating-the-catalog-graph/) |
| Datadog | The catalog combines dependencies discovered through APM, USM and RUM with manually declared relationships. Users can distinguish declared dependencies from detected ones. A catalog API accepts entity definitions. | Preserve provenance. A declared dependency and an observed call are different evidence, even when they connect the same services. [Dependency management](https://docs.datadoghq.com/internal_developer_portal/use_cases/dependency_management/), [entity API](https://docs.datadoghq.com/api/latest/software-catalog/create-or-update-entities/) |
| Port | Customizable blueprints define entity properties and relations, including cardinality. Relations normally use replacement semantics; optional multi-source ingestion lets writers contribute separate sets of targets. | Document whether an API update replaces the dependency list or edits individual relationships. A configurable schema platform would add substantial scope to Almanac. [Blueprints](https://docs.port.io/context-lake/data-model/setup-blueprint/overview/), [relations and ingestion](https://docs.port.io/context-lake/data-model/setup-blueprint/relate-blueprints/) |
| PagerDuty | Technical and business services form a graph. Focused views show nearby relationships, aggregate additional nodes, and provide a details panel. Dependencies can be provisioned through APIs and Terraform. | Make the map navigable around one service, with visible expansion. Its incident status is a different concept from an SLO result. [Service graph](https://docs.pagerduty.com/incident-management/services/service-graph) |
| Dynatrace | Smartscape separates topology perspectives and supports exploring relationships around a selected entity. | Separate runtime calls, infrastructure containment, and service ownership rather than drawing every relation on one canvas. [Topology views](https://docs.dynatrace.com/docs/analyze-explore-automate/smartscape/smartscape-in-context-views/smartscape-view-topology) |

Three patterns recur: discoverable service pages are useful without a map; catalog metadata and measured runtime state have different lifecycles; and relationships need a clear meaning in both the UI and API.

## Approaches worth considering

| Approach | What it delivers | Tradeoff |
| --- | --- | --- |
| Service management only | Services list, create/edit, links to SLOs using existing records | Smallest immediate fix, but leaves service context and dependencies for later design |
| Focused reliability catalog | Service management, ownership/context, explicit dependencies and API/CLI parity | Recommended. Supports the requested direction while leaving integrations and visualization for later work |
| General developer portal | Arbitrary entity types and schemas, scorecards, scaffolding, many connectors, runtime discovery | Much larger product and operational burden; overlaps tools teams may already use |

The recommended approach can ship in increments. Services remain useful before any graph, external-catalog connector or maintenance policy is added.

## Information a service needs

Treat a service as a stable operational responsibility or capability, not an individual process, pod, datasource connection, or SLO. A service can exist before it has measurements. An external payment provider can be cataloged even when Almanac cannot measure it directly.

### Proposed service fields

| Information | Initial treatment | Reason |
| --- | --- | --- |
| UUID, identifier, display name | Keep the UUID stable; allow identifier and display-name edits; only name/display name required from callers | Preserve SLO and dependency references independently of naming |
| Description | Optional concise explanation | Makes similarly named services understandable |
| Accountable owner | Optional free text team/person name and contact | Helps operators find responsibility; explicitly show Unassigned when absent |
| Links | Optional list of freely chosen names and URLs, such as Runbook, Git, Docs or Support | No fixed list of link types; teams can add what they need |
| Labels | Optional keys with one or more values, such as `team=store` or `env=production` | Search and grouping; annotations are addressed below under OpenSLO compatibility |
| SLOs and coverage | Derived references/counts, with explicit reporting period and data quality | Never accept caller-supplied reliability scores as measured truth |

Ownership describes accountability. It does not grant Editor access or create service-specific authorization. [SPEC-0004](../specs/0004-identity-and-access.md) defers service-specific human membership. Initially, the user types the owner name and contact; there is no team directory, suggested list or account binding behind this field.

Existing clients must still be able to create a service with the current two-field request. Additional metadata stays optional. A catalog may encourage completeness without blocking SLO creation on a runbook or owner.

The maintainer selected editable identifiers with stable UUIDs during visual review
on 2026-11-06. This changes the proposed catalog direction from the currently
accepted immutable native import keys in SPEC-0005. Before product implementation,
revise that contract and define separate OpenSLO import mappings so a name change
does not recreate the service or break its references. The visual mock demonstrates
the intended naming behavior; the current backend contract is unchanged.

Lifecycle and criticality are deferred until their meaning and allowed values have
been defined. Initial ownership is a free text name and contact, without assuming a
team directory or linking that name to account permissions.

Almanac also records who changed the service and when. That is automatic change history, not information a team has to fill in. An internal revision number prevents one edit from silently replacing another: if Alice saves while Bob is editing an older copy, Bob is asked to reload before saving his changes.

### Environments remain labels

Do not introduce an Environment resource, environment-specific relationships, or environment-specific behavior. Teams can use a label such as `env=production` when useful and decide whether their own naming conventions call for separate service records. The catalog does not interpret that label as a special operating mode.

## Relationships and their meaning

There are two relevant relationships at this stage:

| Relationship | Example | Consequence |
| --- | --- | --- |
| Service dependency | Checkout depends on Payments | Potential operational impact and ownership navigation |
| An SLO belongs to a service and uses an SLI | Checkout availability belongs to Checkout and uses the checkout request SLI | Existing definition and calculation behavior |

An SLO **belongs to** a service in the sense that it appears in that service's list of objectives. For example, Checkout availability is one of Checkout's SLOs. This does not grant access or transfer responsibility to another user.

Services and SLOs are separate records. Editing a service's display name, description, owner, labels, links or dependencies does not change an SLO's target, window, SLI or calculated results. SLO pages may display the updated service name because they reference that service. Deleting a service must fail while an SLO references it, with a clear explanation of which SLOs prevent deletion.

An SLO **uses an SLI** to compare measured behavior with a target. For example, the checkout request SLI records successful and total requests; Checkout availability checks those measurements against a 99.9% target over 30 days. The service record groups the objectives; it does not run the queries or perform a separate reliability calculation.

For dependencies, record which service needs which other service, an optional explanation, and where that information came from. If Checkout declares a dependency on Payments, Payments can automatically show Checkout under Used by. Users should not have to enter the same fact twice.

Reject a service depending on itself and repeated copies of the same dependency. Real services can depend on each other in a cycle, so listing or exploring relationships must handle that safely. Also distinguish No dependencies recorded from an explicit statement that a service has none.

Start with relationships declared by teams or their automation. Later discovery from telemetry would need its own source and last-seen information. Sharing an SLI or querying the same monitoring datasource does not establish a service dependency: the monitoring connection is not the application's database.

Queues, datastores, APIs and business capabilities could become additional entity types later. Avoid introducing a generic entity framework until a concrete journey needs them.

## Service screens and later visualization

### Services list and detail

Add a Services destination in the existing shell. The list should support search and useful filters such as owner, labels and whether a service has SLOs, with 10/20-row pagination. A service page should expose description, ownership and links, followed by its SLOs, Depends on and Used by lists. Editors can create a service independently of the SLO form. A Create SLO action can carry the selected service into the existing form.

Keep catalog completeness and SLO performance distinct. The catalog mock proposes an **SLO status** mark: any breached objective makes it Breached; otherwise an incomplete result makes it Unknown; all objectives must be evaluable and on track for On track. A service without objectives reads No SLOs. Keep the counts in its tooltip and accessible description, including unknown results alongside a breach, and link to the individual objectives on the service page. This summarizes each SLO's own target and window; it is not live service availability, an averaged budget or inherited dependency health.

Use Almanac's [shared controls and tokens](../../web/DESIGN_SYSTEM.md), [list grammar](../adr/0031-list-grammar-and-button-roles.md) and [table rules](../adr/0032-centred-list-tables-and-data-state-vocabulary.md). Keep the name left-aligned and other columns centered with one value per cell. Detailed provenance belongs on the service page; list refresh time remains in the refresh control.

### Visualization is a later design decision

Do not select or implement a graph layout in this phase. The maintainer wants to compare several designs in an actual UI before deciding. Useful candidates for that review include a map around one service, a grouped catalog overview, and a service list with a dependency panel. None is selected here.

Keep [PagerDuty's service graph](https://docs.pagerduty.com/incident-management/services/service-graph) and [Dynatrace's topology views](https://docs.dynatrace.com/docs/analyze-explore-automate/smartscape/smartscape-in-context-views/smartscape-view-topology) as references for that later work. Compare the designs using the same service data, including cycles, isolated services, long names and many connections. Choose a rendering library only after deciding which interactions the UI needs. Keyboard access, both themes and a usable mobile alternative remain requirements.

## UI CLI and API management

Extend the existing service API, following [SPEC-0010](../specs/0010-api-and-cli.md) and [ADR-0009](../adr/0009-contracts-and-cli-parity.md). Teams should be able to submit the same service information through the UI, CLI or API. A person using the CLI and a script calling the API follow the same rules.

### Proposed operation families

| Need | Candidate contract direction |
| --- | --- |
| Browse and look up services | Search and filter existing records; let automation find the service it previously created |
| Create and edit metadata | Keep current requests working; accept optional catalog information; reject edits made against an out-of-date copy |
| Declare dependencies | Add/list/remove dependencies; generate the reverse Used by list automatically |
| Submit changes safely | Retrying a create request does not produce duplicate services; reject an update that references a nonexistent dependency |
| Archive | Preserve history and explain which SLOs or other records prevent removal; leaving a service out of an API request does not delete it |

These are proposed capabilities, not new implemented routes. Every management action needs a noninteractive CLI equivalent. The API design must also specify request limits, pagination, authentication and what happens if two callers update the same service.

### The same editing rules for every caller

An API request does not establish whether a person, the CLI or an automated process initiated it. Do not add Managed in Almanac / Managed by an integration states, field ownership, or restrictions based on an assumed source. Any caller with the required permissions can edit the same service fields.

Keep the existing protection against conflicting edits: callers submit the revision they read, and Almanac rejects a save if another change has happened since. This works for UI, CLI and API requests without deciding who should own the service information.

Record the authenticated account or credential that made a change and when it happened. That is enough for this phase; it does not claim to identify the person or process behind a shared credential.

Catalog connectors, scheduled synchronization and rules for combining several sources can be considered later. They are not prerequisites for teams to manage services through the API now.

### Authentication and trust boundaries

Existing service endpoints use signed-in sessions, and the CLI already uses authenticated profiles. Build on those supported paths. Do not make a new integration identity or credential type a prerequisite for service management; if additional automation credentials are needed, define that scope separately. Human session behavior remains governed by [ADR-0020](../adr/0020-session-authorized-management.md).

Existing access controls still apply: a label naming a team cannot grant permission to edit the service. Validate links and text, and keep credentials out of links, metadata and exports. A repository URL is a link, not permission for the backend to fetch it.

### Interoperability

**OpenSLO already has room for this additional context.** A Service has a description, labels and annotations. Labels map a key to one or more values; annotations map a key to a string. Custom annotations can carry links and dependency information, with structured data encoded as a string when necessary. A separate file is not inherently required. [OpenSLO Service](https://openslo.com/schema/v1/service/), [common metadata](https://openslo.com/schema/v1/)

For the user-facing catalog, start with **Labels** for searching/grouping and **Links** with arbitrary names. Do not add a second general-purpose metadata editor called Annotations. Preserve imported annotations for compatibility, and map supported ones to the appropriate service fields.

For example, a possible OpenSLO representation is:

```yaml
apiVersion: openslo/v1
kind: Service
metadata:
  name: checkout
  displayName: Checkout
  labels:
    team: [store]
    env: [production]
  annotations:
    example.com/links.runbook: https://docs.example.com/checkout/runbook
    example.com/links.git: https://git.example.com/store/checkout
    example.com/depends-on: '["payments", "inventory"]'
spec:
  description: Accepts and processes customer orders.
```

The `example.com` annotation keys are illustrative proposed conventions, not existing OpenSLO or Almanac fields. Agree on documented keys and validate their meaning before using them to create links or dependencies. Other OpenSLO tools can preserve these annotations without understanding them. Thus the file can carry the information, but interpreting it as a dependency is an Almanac extension. Existing [import rules](../specs/0005-openslo-and-resource-model.md) still govern validation and preservation.

Backstage integration and format mappings are deferred. This phase can use OpenSLO's metadata capabilities without also building a connector or another import engine.

## Maintenance implications

The accepted [maintenance contract](../specs/0016-maintenance-exclusions.md) saves explicit SLO IDs at creation. Service changes cannot add an SLO to an old window. Automatic service/folder selectors and recurring schedules are deferred; each write is bounded to 100 explicit SLO IDs.

Two future behaviors are possible:

| Behavior | Example | Contract consequence |
| --- | --- | --- |
| Select the current SLOs of a service | Choose Checkout, preview its three SLOs, save those IDs | Closest extension to the accepted semantics; later SLOs are not silently added |
| Maintain a dynamic service policy | Every Checkout SLO belongs to the policy during the window | Needs a new accepted policy for membership timing, effective history, permissions and recomputation |

Recommend the first behavior initially. Preview the resolved SLO list and explain that a newly created SLO needs an explicit maintenance update. If the service has more than the write limit or incompatible measurement grids, expose that before apply rather than silently splitting a supposedly atomic action.

Do not traverse dependency edges to exclude other services' measurements. If Payments is under maintenance and Checkout actually fails, Checkout's user-facing failures still matter unless its SLOs were explicitly included in an authorized exclusion. Shared SLIs retain their observations, and unrelated SLOs remain evaluable.

An exclusion affects calculations; a notification silence affects delivery; an annotation provides context. PagerDuty maintenance disables integrations and new incident triggering, which is useful operational precedent but a different contract from Almanac's mathematical exclusions. [PagerDuty maintenance](https://docs.pagerduty.com/incident-management/services/maintenance-windows)

## Later impact analysis

A dependency map can support statements such as Payments has three potential consumers affected by this incident. It cannot establish that those consumers are failing: retries, caches, fallbacks, asynchronous queues and partial feature use can absorb failures. Label traversal results as potential impact, retain the relationship evidence/context, and show measured SLO results independently.

Historical impact analysis is another distinct capability. A map of today's dependencies cannot explain last month's outage reliably. If that journey is needed, retain effective relationship history and identify the topology snapshot used alongside the measurement window.

## Suggested increments and decisions

| Increment | User outcome | Boundary |
| --- | --- | --- |
| 1. Discover and manage services | Browse, create, inspect and edit services; see their SLOs; reuse inline creation | Extend current records and API/CLI behavior without requiring a graph |
| 2. Reliable catalog data | Ownership/context and explicit dependencies, editable through UI, CLI and API | Preserve current authentication and protect conflicting edits; no integration management |
| 3. Review visualization designs | Compare several UI alternatives using the same service/dependency data | The maintainer chooses the design before graph implementation |
| 4. Operational policies | Select a service's SLOs for maintenance and inspect potential impact | Keep explicit exclusions and measured results distinct |
| Later capabilities | Integrations, historical dependencies, discovery from telemetry and additional entity kinds | Separate designs when needed |

Before implementation, resolve these decisions in a catalog spec and the consequential ADRs:

1. **Dependency updates:** does an API update replace the dependency list, edit individual links, or support both?
2. **OpenSLO conventions:** which annotation keys represent links and dependencies, and how does import handle unknown or missing service references?
3. **Maintenance expectation:** is selecting the current service SLOs sufficient, or is automatic inclusion of future SLOs required?

The existing specs govern compatibility, archival, authorization and maintenance calculations. This research does not supersede them. Environments stay as labels; integrations, source-based editing restrictions and composite SLOs remain outside this phase; visualization needs a separate UI comparison.

### Questions a later implementation should prove

An operator can find and create a service without starting an SLO; see services with no objectives; inspect all associated SLOs; and complete the same mutations through UI, API and CLI. Existing two-field service clients continue working.

For automation, check these concrete cases:

- CI sends Checkout twice after a network timeout: only one Checkout service exists.
- Two people edit the same service: the second save does not silently erase the first person's changes.
- A service created through the API can be edited in the UI or CLI by an authorized user, with the same conflict checks.
- Changing Checkout's links, labels or description leaves its SLO definitions and results unchanged.
- Deleting Checkout while an SLO references it fails and identifies the dependent SLO.
- Checkout names a missing dependency: the response identifies that service so the team can fix the request.
- An invalid update fails without changing the saved service or its SLOs.

The service screens need both themes, mobile and keyboard checks. Choose realistic service counts and page-speed acceptance criteria when writing the implementation spec. The later graph designs should also demonstrate cycles, isolated services, long names and many connections before one is selected.

Maintenance previews identify the exact selected SLOs, respect revision/grid and permission rules, and never silently include dependents or later service members. Catalog edits alone do not change SLO calculations, targets or historical reports.
