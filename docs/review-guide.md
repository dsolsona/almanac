# Design review guide

This package covers the presently defined Almanac product: **17 specifications and
17 ADRs**. SPEC-0001–0003 and ADR-0001–0003 are accepted from completed reviews.
SPEC-0004–0015 and ADR-0004–0013 were accepted in Crit review `7fd22977b6c0`
on 2026-10-24 after all comments were resolved. The maintainer subsequently accepted
[ADR-0016](adr/0016-authentication-retry-behavior.md) for authentication recovery.
Design acceptance alone makes no implementation or deployment claim; current
verified delivery is recorded in [status](status.md).
Inline REVIEW notes are retained as decision traceability;
wording such as “propose” or “confirm” records the reviewed choice, not a request to
repeat approval. Initial numerical limits still require implementation evidence.
The maintainer accepted [ADR-0017](adr/0017-recoverable-secret-encryption.md) on
2026-10-26, resolving I06 with Tink Go 2.8.0 and protected external keysets.

## Accepted maintenance and annotation additions

Crit review `0de0c0e42db6` accepted SPEC-0016–0017 and ADR-0014–0015 on 2026-10-24
with no changes requested:

- [Maintenance exclusions](specs/0016-maintenance-exclusions.md) and
  [ADR-0014](adr/0014-maintenance-exclusion-semantics.md): remove maintenance from
  good/total populations while retaining raw data; future scheduling by Editors,
  audited historical changes by Administrators. Delivered in F4.
- [Timeline annotations](specs/0017-slo-timeline-annotations.md) and
  [ADR-0015](adr/0015-timeline-annotation-boundary.md): contextual point/duration events
  through UI/API/CLI, independent of measurement and silencing. Delivered in F3.

REVIEW-E01–E03 and REVIEW-N01–N03 retain the accepted review context. ADR-0014
supersedes only ADR-0005's initial exclusion deferral; original measurement rationale
is preserved. Calculation, alerting, UI and delivery specs incorporate the amendment.

## Recommended reading order

1. [Delivery scope](specs/0014-delivery-and-verification.md): first preview, initial
   release, second native provider, and explicit exclusions.
2. [Resource/OpenSLO model](specs/0005-openslo-and-resource-model.md) and
   [calculation/reporting](specs/0006-slo-calculation-and-reporting.md): the meaning
   of measurements, initial history, edits, imports, and reported numbers.
3. [Identity](specs/0004-identity-and-access.md),
   [agent protocol](specs/0009-agent-protocol-and-ownership.md), and
   [backfill/repair](specs/0008-backfill-and-repair.md): who can do what, how outages
   behave, and how history recovers.
4. [Storage](specs/0007-storage-and-retention.md),
   [API/CLI](specs/0010-api-and-cli.md), and
   [runtime](specs/0013-runtime-and-operations.md): persistence and deployment boundaries.
5. [Alerts](specs/0011-alerting-and-notifications.md),
   [UI](specs/0012-user-interface.md), and
   [providers](specs/0015-datasource-providers.md).
6. Review the linked ADRs for alternatives and consequences. The
   [ADR index](adr/README.md) and [spec index](specs/README.md) show every record's status.

## Decisions with the largest product impact

| Review item | Accepted direction | Consequence to validate |
| --- | --- | --- |
| P01/P02 | One-hour offline execution lease; fresh authenticated process starts replace immediately under [ADR-0022](adr/0022-restartable-agent-ownership.md); datasource handoff remains separate | Superseded processes cannot save results, but a partitioned process may query until its lease expires |
| R01/R02 | Combined server modes plus separate agent artifact; one active backend initially | Preserves simple deployment and component separation without promising immediate backend horizontal scaling |
| S01/M07 | Keep canonical observations for 400 days; caches optional | Preserves exact supported reporting semantics but has significant disk/index cost |
| M01/M02 | Import valid unsupported definitions inactive; cross-source ratio components and some alert/calendar/slice cases cannot activate initially | OpenSLO import compatibility is broader than initial executable coverage; this limitation must be explicit |
| M04/U01/U07 | Conservative incomplete-data results; initial burn alerts only regular single-objective non-composite Occurrences | Avoids false recovery but restricts alert availability and can delay detection during data gaps |
| M03 | Separate initial historical validity from live activation; later edits prospective | Allows setup backfill without relabeling old observations or claiming a target existed operationally in the past |
| M05 | Explicit supported composite formulas and restrictions | Some upstream composite semantics require a deliberate interpretation; do not mistake proposal for universal conformance |
| I01/I02 | Admin-owned datasource connectivity, initial-password rotation, local users first and Google later, offline recovery | Controls credential exposure and first-admin ownership; stricter than unrestricted editor setup |
| D01/V01 | Prometheus native first release; Tempo second; Google later; Elasticsearch later; webhook first | Determines how quickly the first useful release can be delivered |

## Full assumption register

Inline `REVIEW-*` notes in the linked documents retain the detailed review context.
This table makes hidden comments and related choices visible in one place.

| IDs | Subject | Document |
| --- | --- | --- |
| I01 | Roles, datasource administration and automation permissions | [Identity](specs/0004-identity-and-access.md) |
| I02 | Deployment-provided initial password and mandatory rotation, local recovery, later Google sign-in | [Identity](specs/0004-identity-and-access.md) |
| I03 | Password/session/token/reset/rotation lifetime defaults | [Identity](specs/0004-identity-and-access.md) |
| I04 | Offline authority and revocation delay | [Identity](specs/0004-identity-and-access.md) |
| I05 | Encryption-key operations and delayed grants | [Identity](specs/0004-identity-and-access.md) |
| I06 | Resolved: Tink, key loading and ciphertext binding | [ADR-0017](adr/0017-recoverable-secret-encryption.md) |
| M01–M03 | OpenSLO coverage, cadence constraints, initial history and revisions | [Resource model](specs/0005-openslo-and-resource-model.md) |
| M04–M07 | Missingness, composites, calendar/exclusion limits, precision/retention | [Calculations](specs/0006-slo-calculation-and-reporting.md) |
| S01–S03 | Retention, decimal/replay limits, IDs and effective boundaries | [Storage](specs/0007-storage-and-retention.md) |
| B01/B02 | Backend chunk planning, finite scan/retry budgets, historical pause offline | [Backfill](specs/0008-backfill-and-repair.md) |
| P01–P03 | Execution expiry, takeover, wire/heartbeat/memory defaults | [Agent protocol](specs/0009-agent-protocol-and-ownership.md) |
| A01/A02 | OpenAPI format and CLI/HTTP compatibility defaults | [API/CLI](specs/0010-api-and-cli.md) |
| U01/U02/U07 | Alert evaluability, webhook delivery, silences and method scope | [Alerts](specs/0011-alerting-and-notifications.md) |
| U03/U04/U08 | Browser/catalogue support, UI order and folder organization | [UI](specs/0012-user-interface.md) |
| U05 | Backend-owned alert state with private worker operations | [Alert ADR](adr/0010-alert-evaluation-and-delivery.md) |
| U09 | UI bundle, load and interaction budgets with bounded chart rendering | [UI](specs/0012-user-interface.md) |
| U06 | React/TypeScript/Vite/ECharts tooling | [UI ADR](adr/0011-ui-design-and-tooling.md) |
| R01/R02 | Binary split and initial scaling boundary | [Runtime](specs/0013-runtime-and-operations.md) |
| V01/V02 | Release split and workload validation target | [Delivery](specs/0014-delivery-and-verification.md) |
| D01/D02 | Provider order, single-series restriction and normalization | [Providers](specs/0015-datasource-providers.md) |

## Reconciled design details

- Live, historical and diagnostic operations have distinct budgets. Shared provider
  and database resources require admission limits too; separate workers alone are
  not evidence that live scheduling is protected.
- Source estimates may use an explicitly versioned normalization before ingestion;
  ingestion itself rejects invalid precision and inconsistent counts.
- Reports retain exact measurement meaning; remote alert workers receive exact
  comparison inputs, not only display-rounded burn values.
- Notification silences do not alter SLO math. Maintenance exclusions have
  their own versioned calculation contract, never an accidental side effect of muting alerts.
- Setup-time history is an explicit retrospective measurement request. Future query
  edits wait for acknowledged effective bounds or old authority expiry.
- Backend restore invalidates restored bearer credentials before exposure. It can
  repeat external notifications with stable IDs; no exactly-once promise is made.
- Scoped administrator automation exists for unattended management, without allowing
  limited user-admin permissions to mint unrestricted administrator authority.

## What remains before implementation

Produce implementation tasks using the accepted records. Follow ADR-0017 for the
selected encryption dependency and key/ciphertext format. Detailed
OpenAPI/agent schemas, SQL changes, provider fixtures, UI catalogue assets and commands
are implementation deliverables governed by these specs. Record measured UI and binary
sizes as required, and retain the release sequence: local users first, composites
after the first single-indicator flow, Google SSO later. Do not reopen accepted
requirements simply because their implementation has not started.
